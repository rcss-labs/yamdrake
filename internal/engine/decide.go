package engine

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/rcss-labs/yumdrake/internal/spec"
	"github.com/rcss-labs/yumdrake/internal/store"
)

// View is everything Decide may look at: no database, no clock.
type View struct {
	Run   store.Run
	Steps []store.RunStep
	Legs  []store.Leg // every leg of the run
	// LocksHeld reports whether the run currently holds its locks.
	LocksHeld bool
	// IntendedLegs holds the ids of legs that have an operation in the intended
	// state: a change may have been made and its outcome is not recorded.
	IntendedLegs map[int64]bool
	// Frozen is a non-empty reason when a freeze blocks the run's label or
	// environment. Only filled for queued runs.
	Frozen string
	// RequesterProblem is non-empty when a queued run's requester may no
	// longer run it (permission removed, or disabled): the reason to end it with.
	RequesterProblem string
	// RunningInEnv and RunningInAccount count legs of OTHER runs that are
	// queued, claimed or running.
	RunningInEnv, RunningInAccount int
	Limits                         Limits
}

// Limits caps the legs in flight. A value of 0 or less means no limit.
//
// The limits are soft across orchestrators: two orchestrators advancing two
// runs of one environment or account at the same moment each see the other's
// legs as they were before the transaction, so the total may overshoot
// transiently. There is deliberately no extra advisory lock; the overshoot is
// bounded by the number of concurrently advancing runs and corrects itself as
// legs finish.
type Limits struct{ PerEnvironment, PerAccount int }

// Decision is what Advance should apply to a run in one transaction.
type Decision struct {
	QueueLegs, SkipLegs, CancelLegs []int64
	MarkCancelRequested             []int64 // in-flight legs to flag
	CreateRevalidateLeg             bool
	TryAcquireLocks                 bool
	// NewState and WaitReason: "" means unchanged. For a queued run, Advance
	// applies NewState and CreateRevalidateLeg only if the locks were acquired.
	// Advance clears the run's WaitReason whenever NewState is set (unless
	// WaitReason is also set), so a stale reason such as a freeze never
	// outlives the state it explained.
	NewState, WaitReason string
	// ClearWaitReason empties the run's wait reason without a state change (a
	// reason Decide set earlier, such as waiting for capacity, no longer holds).
	ClearWaitReason bool
	// Reason says in plain words why NewState was chosen when it is a final
	// state that needs explaining (for example a resolve that ended unknown).
	Reason       string
	ReleaseLocks bool
	// FailUnauthorized asks Advance to audit the failure as run.fail_unauthorized.
	FailUnauthorized bool
}

const (
	legPending   = "pending"
	legQueued    = "queued"
	legClaimed   = "claimed"
	legRunning   = "running"
	legSucceeded = "succeeded"
	legFailed    = "failed"
	legSkipped   = "skipped"
	legCancelled = "cancelled"
	legUnknown   = "unknown"
)

func legSettled(state string) bool {
	switch state {
	case legSucceeded, legFailed, legSkipped, legCancelled, legUnknown:
		return true
	}
	return false
}

func legActive(state string) bool {
	return state == legQueued || state == legClaimed || state == legRunning
}

func isFinalRunState(state string) bool {
	switch state {
	case "succeeded", "partially_succeeded", "failed", "cancelled", "rejected", "expired", "invalidated":
		return true
	}
	return false
}

// Decide computes a run's next transitions. It is pure and never changes v.
func Decide(v View) Decision {
	cancelRequested := v.Run.CancelRequestedAt != nil || v.Run.CancelRequestedBy != nil

	switch state := v.Run.State; {
	case isFinalRunState(state):
		return Decision{}

	case state == "resolving":
		if cancelRequested {
			return cancelBeforeStart(v)
		}
		return decideResolving(v)

	case state == "planned", state == "waiting_approval":
		if cancelRequested {
			return cancelBeforeStart(v)
		}
		return Decision{}

	case state == "queued":
		if cancelRequested {
			return cancelBeforeStart(v)
		}
		if v.RequesterProblem != "" {
			return Decision{NewState: "failed", Reason: v.RequesterProblem, FailUnauthorized: true, ReleaseLocks: v.LocksHeld}
		}
		if v.Frozen != "" {
			var d Decision
			if reason := "frozen: " + v.Frozen; reason != v.Run.WaitReason {
				d.WaitReason = reason
			}
			return d
		}
		return Decision{TryAcquireLocks: true, NewState: "running", CreateRevalidateLeg: true}

	case state == "running":
		if cancelRequested {
			if allSettled(v.Legs) {
				return finish(v, true)
			}
			d := stopLegs(v)
			d.NewState = "cancelling"
			return d
		}
		return decideRunning(v)

	case state == "cancelling":
		if allSettled(v.Legs) {
			return finish(v, true)
		}
		return stopLegs(v)

	case state == "needs_reconciliation":
		return decideReconciling(v, cancelRequested)
	}
	return Decision{}
}

// latestLeg returns the leg of the given kind with the highest id.
func latestLeg(legs []store.Leg, kind string) (store.Leg, bool) {
	var found store.Leg
	ok := false
	for _, l := range legs {
		if l.Kind == kind && (!ok || l.ID > found.ID) {
			found, ok = l, true
		}
	}
	return found, ok
}

func allSettled(legs []store.Leg) bool {
	for _, l := range legs {
		if !legSettled(l.State) {
			return false
		}
	}
	return true
}

func anyUnknown(legs []store.Leg) bool {
	for _, l := range legs {
		if l.State == legUnknown {
			return true
		}
	}
	return false
}

// stopLegs cancels every pending leg and every queued leg without an intended
// operation, and flags every claimed or running leg, and every queued leg with
// an intended operation, that is not flagged yet. Nothing is queued.
//
// A queued leg with an intended operation may have changed something whose
// outcome nobody knows (the runner died and the sweep requeued it). Settling it
// as cancelled would end the run and release its locks with the outcome still
// uncertain, so it stays queued, flagged: a runner claims it, reconciles and
// settles it as succeeded, failed or unknown. A queued leg without one (never
// claimed, or claimed and released or lost before its first operation) has
// changed nothing, whatever its attempt count, and is cancelled directly so no
// executor runs for it.
func stopLegs(v View) Decision {
	return stopLegsWith(v, true)
}

// stopLegsWith is stopLegs; reconcileAttempted false cancels every queued leg
// whatever its operations, for a run that has not started running and so has
// made no external change (its only leg is the read-only resolve).
func stopLegsWith(v View, reconcileAttempted bool) Decision {
	var d Decision
	for _, l := range v.Legs {
		switch {
		case l.State == legPending, l.State == legQueued && (!v.IntendedLegs[l.ID] || !reconcileAttempted):
			d.CancelLegs = append(d.CancelLegs, l.ID)
		case l.State == legQueued, l.State == legClaimed, l.State == legRunning:
			if !l.CancelRequested {
				d.MarkCancelRequested = append(d.MarkCancelRequested, l.ID)
			}
		}
	}
	return d
}

// cancelBeforeStart handles a cancel request in a state before running: the
// run is cancelled and its legs are stopped. No locks are held yet, but the
// release is requested if they somehow are.
func cancelBeforeStart(v View) Decision {
	d := stopLegsWith(v, false)
	d.NewState = "cancelled"
	d.ReleaseLocks = v.LocksHeld
	return d
}

func decideResolving(v View) Decision {
	resolve, ok := latestLeg(v.Legs, "resolve")
	if !ok {
		return Decision{}
	}
	switch resolve.State {
	case legPending:
		return Decision{QueueLegs: []int64{resolve.ID}}
	case legSucceeded:
		switch {
		case !v.Run.AutoStart:
			return Decision{NewState: "planned"}
		case v.Run.ApprovalRequired:
			return Decision{NewState: "waiting_approval"}
		default:
			return Decision{NewState: "queued"}
		}
	case legUnknown:
		return Decision{NewState: "failed", Reason: "the resolve step's outcome could not be determined; nothing was changed"}
	case legFailed, legSkipped, legCancelled:
		return Decision{NewState: "failed"}
	}
	return Decision{} // claimed or running: wait
}

// RevalidateDriftReason is the short failure reason of a revalidate leg that
// found the targets changed since planning. Any other failed revalidate leg
// could not re-check the targets, which is not drift.
const RevalidateDriftReason = "changed since planning"

// RevalidateCouldNotCheckReason starts the short failure reason of a
// revalidate leg that could not tell whether the targets changed.
const RevalidateCouldNotCheckReason = "could not re-check the targets"

func couldNotCheckReason(short string) string {
	if strings.HasPrefix(short, RevalidateCouldNotCheckReason) {
		return short
	}
	if short == "" {
		return RevalidateCouldNotCheckReason
	}
	return RevalidateCouldNotCheckReason + ": " + short
}

func decideRunning(v View) Decision {
	revalidate, ok := latestLeg(v.Legs, "revalidate")
	if !ok {
		return Decision{} // Advance creates it in the same transaction that starts the run
	}
	switch revalidate.State {
	case legPending:
		return Decision{QueueLegs: []int64{revalidate.ID}}
	case legQueued, legClaimed, legRunning:
		return Decision{}
	case legUnknown:
		// Keep the locks; the pending step legs will never run.
		d := stopLegs(v)
		d.NewState = "needs_reconciliation"
		return d
	case legFailed, legSkipped, legCancelled:
		// Nothing ran. Pending legs will never run.
		d := stopLegs(v)
		d.ReleaseLocks = true
		if revalidate.State == legFailed && revalidate.FailureShort != RevalidateDriftReason {
			// The targets could not be re-checked: that is not drift, so the
			// plan may still be valid, but this run cannot go on.
			d.NewState = "failed"
			d.Reason = couldNotCheckReason(revalidate.FailureShort)
			return d
		}
		d.NewState = "invalidated"
		return d
	}
	if allSettled(v.Legs) {
		return finish(v, false)
	}
	return scheduleStepLegs(v)
}

// finish applies the result precedence to a run whose legs are all settled.
func finish(v View, cancelRequested bool) Decision {
	switch {
	case anyUnknown(v.Legs):
		return Decision{NewState: "needs_reconciliation"} // keeps its locks
	case cancelRequested:
		return Decision{NewState: "cancelled", ReleaseLocks: true}
	}
	succeeded, total := 0, 0
	for _, l := range v.Legs {
		if l.Kind != "step" {
			continue
		}
		total++
		if l.State == legSucceeded {
			succeeded++
		}
	}
	switch {
	case succeeded == total: // also a run with no step legs
		return Decision{NewState: "succeeded", ReleaseLocks: true}
	case succeeded > 0:
		return Decision{NewState: "partially_succeeded", ReleaseLocks: true}
	}
	return Decision{NewState: "failed", ReleaseLocks: true}
}

// decideReconciling leaves needs_reconciliation only for a final state. While
// any leg is unknown or unsettled (a check-again leg was requeued) the run
// stays put: it never resumes running and never re-acquires locks.
func decideReconciling(v View, cancelRequested bool) Decision {
	if !allSettled(v.Legs) || anyUnknown(v.Legs) {
		return Decision{}
	}
	return finish(v, cancelRequested)
}

// needVerdict is how a step leg stands against its needs.
type needVerdict int

const (
	needsWait needVerdict = iota
	needsReady
	needsSkip
)

func scheduleStepLegs(v View) Decision {
	stepsByName := make(map[string]store.RunStep, len(v.Steps))
	stepsByID := make(map[int64]store.RunStep, len(v.Steps))
	for _, s := range v.Steps {
		stepsByName[s.Name] = s
		stepsByID[s.ID] = s
	}
	legsByStep := map[int64][]store.Leg{}
	activeByStep := map[int64]int{}
	activeOwn := 0
	for _, l := range v.Legs {
		if l.StepID != nil {
			legsByStep[*l.StepID] = append(legsByStep[*l.StepID], l)
			if legActive(l.State) {
				activeByStep[*l.StepID]++
			}
		}
		if legActive(l.State) {
			activeOwn++
		}
	}

	var d Decision
	var ready []store.Leg
	for _, l := range v.Legs {
		if l.Kind != "step" || l.State != legPending || l.StepID == nil {
			continue
		}
		step, ok := stepsByID[*l.StepID]
		if !ok {
			continue
		}
		switch judgeLeg(l, step, stepsByName, legsByStep) {
		case needsSkip:
			d.SkipLegs = append(d.SkipLegs, l.ID)
		case needsReady:
			ready = append(ready, l)
		}
	}

	sort.Slice(ready, func(i, j int) bool {
		pi, pj := stepsByID[*ready[i].StepID].Position, stepsByID[*ready[j].StepID].Position
		if pi != pj {
			return pi < pj
		}
		return ready[i].ID < ready[j].ID
	})

	envRoom := room(v.Limits.PerEnvironment, v.RunningInEnv+activeOwn)
	accountRoom := room(v.Limits.PerAccount, v.RunningInAccount+activeOwn)
	heldBack := false
	for _, l := range ready {
		if envRoom == 0 || accountRoom == 0 {
			heldBack = true
			break
		}
		step := stepsByID[*l.StepID]
		if step.MaxParallel > 0 && activeByStep[step.ID] >= step.MaxParallel {
			continue
		}
		d.QueueLegs = append(d.QueueLegs, l.ID)
		activeByStep[step.ID]++
		if envRoom > 0 {
			envRoom--
		}
		if accountRoom > 0 {
			accountRoom--
		}
	}

	// Say why the run is idle when a limit alone holds its ready legs back, and
	// take the reason away as soon as a leg is queued or none is held back.
	capacityReason := "waiting for capacity in " + v.Run.Environment
	switch {
	case heldBack && len(d.QueueLegs) == 0:
		if v.Run.WaitReason != capacityReason {
			d.WaitReason = capacityReason
		}
	case strings.HasPrefix(v.Run.WaitReason, "waiting for capacity in "):
		d.ClearWaitReason = true
	}
	return d
}

// room is how many more legs fit under limit, or -1 for no limit.
func room(limit, inUse int) int {
	if limit <= 0 {
		return -1
	}
	if inUse >= limit {
		return 0
	}
	return limit - inUse
}

// judgeLeg checks every need of the leg's step. Any need that can no longer
// be met skips the leg; the leg is ready only when every need is met. A need
// naming a step with no row (dropped when the plan was built) is ignored, as
// is a service-scoped need whose service has no leg in the needed step.
// Unparseable needs never become ready.
func judgeLeg(l store.Leg, step store.RunStep, stepsByName map[string]store.RunStep, legsByStep map[int64][]store.Leg) needVerdict {
	var needs []spec.Need
	if len(step.Needs) > 0 {
		if err := json.Unmarshal(step.Needs, &needs); err != nil {
			return needsWait
		}
	}
	verdict := needsReady
	for _, need := range needs {
		needed, ok := stepsByName[need.Step]
		if !ok {
			continue
		}
		var considered []store.Leg
		for _, nl := range legsByStep[needed.ID] {
			if need.Scope == "service" && nl.Service != l.Service {
				continue
			}
			considered = append(considered, nl)
		}
		for _, nl := range considered {
			switch nl.State {
			case legFailed, legSkipped, legCancelled, legUnknown:
				// An unknown needed leg skips its dependents like a failed
				// one, so the run settles into needs_reconciliation.
				return needsSkip
			case legSucceeded:
			default:
				verdict = needsWait
			}
		}
	}
	return verdict
}
