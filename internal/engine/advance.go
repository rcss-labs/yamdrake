package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/yamdrake/internal/audit"
	"github.com/rcss-labs/yamdrake/internal/auth"
	"github.com/rcss-labs/yamdrake/internal/spec"
	"github.com/rcss-labs/yamdrake/internal/store"
	"github.com/rcss-labs/yamdrake/internal/wake"
)

var (
	// ErrForbidden means the person may not do this to this run.
	ErrForbidden = errors.New("not allowed")
	// ErrNotRunnable means the job has problems or no inventory is active.
	ErrNotRunnable = errors.New("job is not runnable")
	// ErrWrongState means the run (or leg) is not in a state that allows the action.
	ErrWrongState = errors.New("run is not in a state that allows this")
)

// SelectionError reports every problem with a selection (Build's problems).
type SelectionError struct{ Problems spec.Problems }

func (e *SelectionError) Error() string { return e.Problems.Error() }

const (
	// plannedRunLifetime is how long a planned run waits for a person to start it.
	plannedRunLifetime = time.Hour
	// defaultApprovalExpiry applies when the job's approval block sets none.
	defaultApprovalExpiry = 24 * time.Hour

	defaultLimitPerEnvironment = 10
	defaultLimitPerAccount     = 20
)

// Publisher announces that something changed, so sleepers need not wait for
// their next poll. The id is the run the news is about.
type Publisher interface {
	Publish(ctx context.Context, topic string, id int64)
}

// Deps is what an Engine needs.
type Deps struct {
	DB     store.DBTX // a pool; every action opens its own transaction
	Limits Limits     // zero value means the defaults, 10 per environment and 20 per account
	Now    func() time.Time
	Log    *slog.Logger
	// Publisher is optional: nil means no wake-ups.
	Publisher Publisher
}

// Engine creates runs, advances them under a run row lock and carries out
// people's actions on them.
type Engine struct {
	db     store.DBTX
	limits Limits
	now    func() time.Time
	log    *slog.Logger
	pub    Publisher
}

// New builds an Engine, filling defaults for the clock, logger and limits.
func New(deps Deps) *Engine {
	e := &Engine{db: deps.DB, limits: deps.Limits, now: deps.Now, log: deps.Log, pub: deps.Publisher}
	if e.limits == (Limits{}) {
		e.limits = Limits{PerEnvironment: defaultLimitPerEnvironment, PerAccount: defaultLimitPerAccount}
	}
	if e.now == nil {
		e.now = time.Now
	}
	if e.log == nil {
		e.log = slog.New(slog.DiscardHandler)
	}
	return e
}

// ApprovalExpiry is how long a run may wait for approval: the job snapshot's
// approval.expires, 24 hours when the job does not say.
func ApprovalExpiry(r store.Run) time.Duration {
	var job spec.Job
	if err := json.Unmarshal(r.JobSnapshot, &job); err == nil && job.Approval != nil && job.Approval.Expires > 0 {
		return job.Approval.Expires
	}
	return defaultApprovalExpiry
}

// outcome collects what to announce once a transaction has committed.
type outcome struct {
	runChanged, legsQueued bool
}

func (e *Engine) publish(ctx context.Context, runID int64, o outcome) {
	if e.pub == nil {
		return
	}
	if o.legsQueued {
		e.pub.Publish(ctx, wake.TopicLegs, runID)
	}
	if o.runChanged {
		e.pub.Publish(ctx, wake.TopicRuns, runID)
	}
}

// inTx runs fn in a transaction and publishes what it reports after commit.
func (e *Engine) inTx(ctx context.Context, runID int64, fn func(tx pgx.Tx, o *outcome) error) error {
	tx, err := e.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var o outcome
	if err := fn(tx, &o); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	e.publish(ctx, runID, o)
	return nil
}

func targetOf(r store.Run) auth.Target {
	return auth.Target{Label: r.Label, Environment: r.Environment, FolderID: r.FolderID}
}

func displayName(p auth.Principal) string {
	if p.Name != "" {
		return p.Name
	}
	return p.Email
}

// Create plans a new run: it checks the person may run the job against the
// target, builds the legs and locks from the selection, and stores the run in
// resolving with a snapshot of the job. The resolve leg is queued; the run
// becomes planned (or, with autoStart, moves on) when it succeeds.
func (e *Engine) Create(ctx context.Context, p auth.Principal, jobSourceID int64, envName string, sel Selection, via string, autoStart bool) (store.Run, error) {
	source, err := store.GetJobSource(ctx, e.db, jobSourceID)
	if err != nil {
		return store.Run{}, fmt.Errorf("create run: %w", err)
	}
	if !source.Runnable || len(source.LastGood) == 0 {
		return store.Run{}, fmt.Errorf("create run: %w: the job has problems or was never read", ErrNotRunnable)
	}
	snapshot, ok, err := store.ActiveSnapshot(ctx, e.db)
	if err != nil {
		return store.Run{}, fmt.Errorf("create run: %w", err)
	}
	if !ok {
		return store.Run{}, fmt.Errorf("create run: %w: no inventory is active", ErrNotRunnable)
	}
	var job spec.Job
	if err := json.Unmarshal(source.LastGood, &job); err != nil {
		return store.Run{}, fmt.Errorf("create run: decode job: %w", err)
	}
	var inventory spec.Inventory
	if err := json.Unmarshal(snapshot.Inventory, &inventory); err != nil {
		return store.Run{}, fmt.Errorf("create run: decode inventory: %w", err)
	}

	environment, known := inventory.Environment(envName)
	if known && !p.Can(auth.Run, auth.Target{Label: environment.Label, Environment: envName, FolderID: source.FolderID}) {
		return store.Run{}, fmt.Errorf("create run: %w", ErrForbidden)
	}
	built, problems := Build(job, inventory, envName, sel)
	if len(problems) > 0 { // includes an unknown environment, which has no label to check
		return store.Run{}, &SelectionError{Problems: problems}
	}
	selectionJSON, err := json.Marshal(sel)
	if err != nil {
		return store.Run{}, fmt.Errorf("create run: encode selection: %w", err)
	}

	now := e.now()
	legs := built.Legs
	for i := range legs {
		if legs[i].State == legQueued {
			queuedAt := now
			legs[i].QueuedAt = &queuedAt
		}
	}
	run := store.Run{
		Kind: "deploy", JobSourceID: source.ID, JobID: job.ID, JobSnapshot: source.LastGood,
		InventoryCommit: snapshot.Commit, JobCommit: source.LastGoodCommit, InventorySnapshotID: snapshot.ID,
		Environment: envName, Label: environment.Label, AWSAccount: environment.AWSAccount, FolderID: source.FolderID,
		Selection: selectionJSON, LockResources: built.Resources,
		ApprovalRequired: needsApproval(job, environment.Label), AutoStart: autoStart,
		State: "resolving", RequestedBy: p.UserID, Via: via, CreatedAt: now,
	}

	var created store.Run
	err = e.inTx(ctx, 0, func(tx pgx.Tx, o *outcome) error {
		created, err = store.InsertRun(ctx, tx, run, built.Steps, legs)
		if err != nil {
			return fmt.Errorf("create run: %w", err)
		}
		if err := store.AppendRunLine(ctx, tx, created.ID, "info",
			fmt.Sprintf("Run created by %s for %s in %s; working out the plan.", displayName(p), job.ID, envName), now); err != nil {
			return err
		}
		return audit.Record(ctx, tx, &p, now, audit.Event{
			Action: "run.create", TargetType: "run", TargetID: fmt.Sprint(created.ID),
			Detail: map[string]any{"job_id": job.ID, "environment": envName, "via": via, "auto_start": autoStart, "approval_required": created.ApprovalRequired},
		})
	})
	if err != nil {
		return store.Run{}, err
	}
	e.publish(ctx, created.ID, outcome{runChanged: true, legsQueued: true})
	return created, nil
}

func needsApproval(job spec.Job, label string) bool {
	if job.Approval == nil {
		return false
	}
	for _, l := range job.Approval.Labels {
		if l == label {
			return true
		}
	}
	return false
}

// Advance moves a run as far as one decision takes it: in one transaction it
// locks the run row, looks at its legs, decides and applies. Any number of
// orchestrators may call it for one run at once; the row lock makes them take
// turns, and each sees the result of the one before. Calling it on a final run
// does nothing.
func (e *Engine) Advance(ctx context.Context, runID int64) error {
	return e.inTx(ctx, runID, func(tx pgx.Tx, o *outcome) error {
		run, err := store.LockRun(ctx, tx, runID)
		if err != nil {
			return fmt.Errorf("advance run %d: %w", runID, err)
		}
		return e.advanceLocked(ctx, tx, run, o)
	})
}

// advanceLocked decides and applies for a run whose row the caller has locked.
func (e *Engine) advanceLocked(ctx context.Context, tx pgx.Tx, run store.Run, o *outcome) error {
	if isFinalRunState(run.State) {
		return nil
	}
	steps, err := store.StepsOfRun(ctx, tx, run.ID)
	if err != nil {
		return err
	}
	legs, err := store.LegsOfRun(ctx, tx, run.ID)
	if err != nil {
		return err
	}
	locks, err := store.LocksOfRun(ctx, tx, run.ID)
	if err != nil {
		return err
	}
	view := View{Run: run, Steps: steps, Legs: legs, LocksHeld: len(locks) > 0, Limits: e.limits}
	if run.State == "running" {
		view.RunningInEnv, view.RunningInAccount, err = store.ActiveLegsOfOtherRuns(ctx, tx, run.ID, run.Environment, run.AWSAccount)
		if err != nil {
			return err
		}
	}
	return e.apply(ctx, tx, run, legs, Decide(view), o)
}

// apply carries out a decision. The caller holds the run row lock.
func (e *Engine) apply(ctx context.Context, tx pgx.Tx, run store.Run, legs []store.Leg, d Decision, o *outcome) error {
	now := e.now()

	if d.TryAcquireLocks {
		held, err := store.AcquireLocks(ctx, tx, run.ID, run.LockResources, now)
		if err != nil {
			return err
		}
		if len(held) > 0 {
			// Locks are all or nothing: nothing was taken. Stay queued and say who we wait for.
			reason := fmt.Sprintf("waiting for run #%d", lowestHolder(held))
			if reason == run.WaitReason {
				return nil
			}
			if err := store.UpdateRunState(ctx, tx, run.ID, run.State, reason, now); err != nil {
				return ignoreFinal(err)
			}
			o.runChanged = true
			return store.AppendRunLine(ctx, tx, run.ID, "info",
				fmt.Sprintf("Waiting: %s holds locks this run needs.", reason[len("waiting for "):]), now)
		}
		if err := store.AppendRunLine(ctx, tx, run.ID, "info",
			fmt.Sprintf("Took all %d locks this run needs.", len(run.LockResources)), now); err != nil {
			return err
		}
	}

	for _, id := range d.QueueLegs {
		if err := store.SetLegState(ctx, tx, id, legQueued, now); err != nil {
			if errors.Is(err, store.ErrLegMoved) {
				continue
			}
			return err
		}
		o.legsQueued = true
	}
	for _, id := range d.SkipLegs {
		if err := store.SetLegState(ctx, tx, id, legSkipped, now); err != nil && !errors.Is(err, store.ErrLegMoved) {
			return err
		}
	}
	for _, id := range d.CancelLegs {
		err := store.SetLegState(ctx, tx, id, legCancelled, now)
		if errors.Is(err, store.ErrLegMoved) {
			// A runner claimed the leg since we read it: ask it to stop instead.
			err = store.FlagLegCancel(ctx, tx, id)
		}
		if err != nil {
			return err
		}
	}
	for _, id := range d.MarkCancelRequested {
		if err := store.FlagLegCancel(ctx, tx, id); err != nil {
			return err
		}
	}
	if d.CreateRevalidateLeg {
		if _, err := store.InsertLeg(ctx, tx, store.Leg{RunID: run.ID, Kind: "revalidate", State: legQueued, QueuedAt: &now}); err != nil {
			return err
		}
		o.legsQueued = true
	}

	if d.NewState != "" || d.WaitReason != "" {
		newState, wait := run.State, d.WaitReason
		if d.NewState != "" {
			newState = d.NewState
			if isFinalRunState(newState) && wait == "" {
				wait = d.Reason // for a final run the column holds why it ended
			}
		}
		if run.State == "resolving" && d.NewState != "" {
			if err := copyManifest(ctx, tx, run.ID, legs); err != nil {
				return err
			}
		}
		if err := store.UpdateRunState(ctx, tx, run.ID, newState, wait, now); err != nil {
			return ignoreFinal(err)
		}
		o.runChanged = true
		if newState != run.State {
			level, text := transitionLine(run.State, newState, d.Reason)
			if err := store.AppendRunLine(ctx, tx, run.ID, level, text, now); err != nil {
				return err
			}
		}
	}

	if d.ReleaseLocks || (d.NewState != "" && isFinalRunState(d.NewState)) {
		if err := store.ReleaseLocks(ctx, tx, run.ID); err != nil {
			return err
		}
	}
	return nil
}

// copyManifest stores the resolve leg's result as the run's manifest, with the
// hex SHA-256 of the result as its id.
func copyManifest(ctx context.Context, tx pgx.Tx, runID int64, legs []store.Leg) error {
	resolve, ok := latestLeg(legs, "resolve")
	if !ok || resolve.State != legSucceeded || len(resolve.Result) == 0 {
		return nil
	}
	sum := sha256.Sum256(resolve.Result)
	return store.SetManifest(ctx, tx, runID, resolve.Result, hex.EncodeToString(sum[:]))
}

func lowestHolder(held map[string]int64) int64 {
	var lowest int64
	for _, id := range held {
		if lowest == 0 || id < lowest {
			lowest = id
		}
	}
	return lowest
}

// ignoreFinal treats "the run is already final" as success: nothing to change.
func ignoreFinal(err error) error {
	if errors.Is(err, store.ErrRunFinal) {
		return nil
	}
	return err
}

// transitionLine words a state change for the run's log.
func transitionLine(from, to, reason string) (level, text string) {
	text = fmt.Sprintf("Run moved from %s to %s: ", from, to)
	level = "info"
	switch to {
	case "planned":
		text += "the plan is ready and waits for a person to start it."
	case "waiting_approval":
		text += "the plan is ready and needs an approval before it can run. No locks are held."
	case "queued":
		text += "waiting for its turn to take locks."
	case "running":
		text += "checking that the plan still holds, then the steps run."
	case "cancelling":
		text += "cancel requested; waiting for the legs in flight to stop."
		level = "warn"
	case "needs_reconciliation":
		text += "at least one leg's outcome is unknown. Locks stay held until a person reconciles it."
		level = "warn"
	case "succeeded":
		text += "every step succeeded. Locks released."
		level = "ok"
	case "partially_succeeded":
		text += "some steps succeeded and some did not. Locks released."
		level = "warn"
	case "failed":
		text += "the run failed. Locks released."
		level = "error"
	case "cancelled":
		text += "the run was cancelled. Locks released."
		level = "warn"
	case "rejected":
		text += "an approver rejected the plan."
		level = "warn"
	case "expired":
		text += "nobody acted in time."
		level = "warn"
	case "invalidated":
		text += "the targets changed since the plan was made, so nothing was run. Locks released. Plan again."
		level = "error"
	default:
		text += "state changed."
	}
	if reason != "" {
		text += " " + reason + "."
	}
	return level, text
}

// lockedRunFor locks the run and checks the person may do action to it.
func (e *Engine) lockedRunFor(ctx context.Context, tx pgx.Tx, p auth.Principal, runID int64, action auth.Action) (store.Run, error) {
	run, err := store.LockRun(ctx, tx, runID)
	if err != nil {
		return store.Run{}, err
	}
	if !p.Can(action, targetOf(run)) {
		return store.Run{}, ErrForbidden
	}
	return run, nil
}

// moveTo changes a locked run's state for an action, writing the run-level line.
// For a final state the reason is kept on the run and said in the line.
func (e *Engine) moveTo(ctx context.Context, tx pgx.Tx, run store.Run, state, reason, actor string, o *outcome) error {
	now := e.now()
	if err := store.UpdateRunState(ctx, tx, run.ID, state, reason, now); err != nil {
		return err
	}
	level, text := transitionLine(run.State, state, reason)
	if actor != "" {
		text = actor + ": " + text
	}
	if err := store.AppendRunLine(ctx, tx, run.ID, level, text, now); err != nil {
		return err
	}
	if isFinalRunState(state) {
		if err := store.ReleaseLocks(ctx, tx, run.ID); err != nil {
			return err
		}
	}
	o.runChanged = true
	return nil
}

func (e *Engine) record(ctx context.Context, tx pgx.Tx, p *auth.Principal, action string, runID int64, detail map[string]any) error {
	return audit.Record(ctx, tx, p, e.now(), audit.Event{Action: action, TargetType: "run", TargetID: fmt.Sprint(runID), Detail: detail})
}

// Start lets a person start a planned run: it waits for approval if the job
// needs one, otherwise it queues and tries to take its locks at once.
func (e *Engine) Start(ctx context.Context, p auth.Principal, runID int64) error {
	return e.inTx(ctx, runID, func(tx pgx.Tx, o *outcome) error {
		run, err := e.lockedRunFor(ctx, tx, p, runID, auth.Run)
		if err != nil {
			return fmt.Errorf("start run %d: %w", runID, err)
		}
		if run.State != "planned" {
			return fmt.Errorf("start run %d: %w: it is %s", runID, ErrWrongState, run.State)
		}
		next := "queued"
		if run.ApprovalRequired {
			next = "waiting_approval"
		}
		if err := e.moveTo(ctx, tx, run, next, "", "Started by "+displayName(p), o); err != nil {
			return err
		}
		if err := e.record(ctx, tx, &p, "run.start", runID, map[string]any{"manifest_id": run.ManifestID, "next_state": next}); err != nil {
			return err
		}
		return e.advanceAfterAction(ctx, tx, runID, o)
	})
}

// advanceAfterAction re-reads the locked run and advances it in the same
// transaction as the action, so a person's action and its first consequence
// commit together.
func (e *Engine) advanceAfterAction(ctx context.Context, tx pgx.Tx, runID int64, o *outcome) error {
	run, err := store.GetRun(ctx, tx, runID)
	if err != nil {
		return err
	}
	return e.advanceLocked(ctx, tx, run, o)
}

// Cancel records the request and advances: runs that have not started end at
// once, running ones stop their legs and settle as cancelled.
func (e *Engine) Cancel(ctx context.Context, p auth.Principal, runID int64) error {
	return e.inTx(ctx, runID, func(tx pgx.Tx, o *outcome) error {
		run, err := e.lockedRunFor(ctx, tx, p, runID, auth.Cancel)
		if err != nil {
			return fmt.Errorf("cancel run %d: %w", runID, err)
		}
		if isFinalRunState(run.State) {
			return fmt.Errorf("cancel run %d: %w: it is %s", runID, ErrWrongState, run.State)
		}
		now := e.now()
		if err := store.RequestCancel(ctx, tx, runID, p.UserID, now); err != nil {
			return err
		}
		if err := store.AppendRunLine(ctx, tx, runID, "warn", fmt.Sprintf("Cancel requested by %s while %s.", displayName(p), run.State), now); err != nil {
			return err
		}
		if err := e.record(ctx, tx, &p, "run.cancel", runID, map[string]any{"state": run.State}); err != nil {
			return err
		}
		o.runChanged = true
		return e.advanceAfterAction(ctx, tx, runID, o)
	})
}

// Approve lets an approver (not the person who started the run, unless an
// admin) approve exactly the manifest the run holds; the run is then queued.
func (e *Engine) Approve(ctx context.Context, p auth.Principal, runID int64) error {
	return e.decideApproval(ctx, p, runID, "run.approve", "queued")
}

// Reject ends a run waiting for approval as rejected.
func (e *Engine) Reject(ctx context.Context, p auth.Principal, runID int64) error {
	return e.decideApproval(ctx, p, runID, "run.reject", "rejected")
}

func (e *Engine) decideApproval(ctx context.Context, p auth.Principal, runID int64, action, next string) error {
	return e.inTx(ctx, runID, func(tx pgx.Tx, o *outcome) error {
		run, err := store.LockRun(ctx, tx, runID)
		if err != nil {
			return fmt.Errorf("%s %d: %w", action, runID, err)
		}
		if !p.MayApproveRunStartedBy(run.RequestedBy, targetOf(run)) {
			return fmt.Errorf("%s %d: %w", action, runID, ErrForbidden)
		}
		if run.State != "waiting_approval" {
			return fmt.Errorf("%s %d: %w: it is %s", action, runID, ErrWrongState, run.State)
		}
		if err := e.moveTo(ctx, tx, run, next, "", displayName(p), o); err != nil {
			return err
		}
		return e.record(ctx, tx, &p, action, runID, map[string]any{"manifest_id": run.ManifestID})
	})
}

// Expire ends a run nobody acted on: a planned run after an hour, a run
// waiting for approval after the job's approval expiry. Any other run is left
// alone. It is the system's doing, not a person's.
func (e *Engine) Expire(ctx context.Context, runID int64) error {
	return e.inTx(ctx, runID, func(tx pgx.Tx, o *outcome) error {
		run, err := store.LockRun(ctx, tx, runID)
		if err != nil {
			return fmt.Errorf("expire run %d: %w", runID, err)
		}
		waited := e.now().Sub(run.StateChangedAt)
		switch {
		case run.State == "planned" && waited > plannedRunLifetime:
		case run.State == "waiting_approval" && waited > ApprovalExpiry(run):
		default:
			return nil
		}
		if err := e.moveTo(ctx, tx, run, "expired", "", "", o); err != nil {
			return err
		}
		return e.record(ctx, tx, nil, "run.expire", runID, map[string]any{"was": run.State})
	})
}

// Reconcile records the real outcome of an unknown leg, as found by a person.
// It never resumes the run: dependents of the leg stay skipped.
func (e *Engine) Reconcile(ctx context.Context, p auth.Principal, runID, legID int64, outcomeChoice string) error {
	if outcomeChoice != legSucceeded && outcomeChoice != legFailed {
		return fmt.Errorf("reconcile run %d: %w: outcome must be succeeded or failed, not %q", runID, store.ErrInvalid, outcomeChoice)
	}
	return e.inTx(ctx, runID, func(tx pgx.Tx, o *outcome) error {
		run, leg, err := e.unknownLegOf(ctx, tx, p, runID, legID)
		if err != nil {
			return fmt.Errorf("reconcile run %d: %w", runID, err)
		}
		now := e.now()
		if err := store.SetLegState(ctx, tx, legID, outcomeChoice, now); err != nil {
			return legMovedAsWrongState(err)
		}
		if err := store.AppendRunLine(ctx, tx, runID, "info",
			fmt.Sprintf("%s recorded the unknown %s leg for %q as %s.", displayName(p), leg.Kind, leg.Service, outcomeChoice), now); err != nil {
			return err
		}
		if err := e.record(ctx, tx, &p, "run.reconcile", runID, map[string]any{"leg_id": legID, "service": leg.Service, "outcome": outcomeChoice}); err != nil {
			return err
		}
		o.runChanged = true
		return e.advanceLocked(ctx, tx, run, o)
	})
}

// CheckAgain gives an unknown leg one more reconciliation attempt: it is
// queued again with its resume data and operations. The run stays in
// needs_reconciliation until the leg settles.
func (e *Engine) CheckAgain(ctx context.Context, p auth.Principal, runID, legID int64) error {
	return e.inTx(ctx, runID, func(tx pgx.Tx, o *outcome) error {
		run, leg, err := e.unknownLegOf(ctx, tx, p, runID, legID)
		if err != nil {
			return fmt.Errorf("check again run %d: %w", runID, err)
		}
		now := e.now()
		if err := store.SetLegState(ctx, tx, legID, legQueued, now); err != nil {
			return legMovedAsWrongState(err)
		}
		if err := store.AppendRunLine(ctx, tx, runID, "info",
			fmt.Sprintf("%s asked to check the unknown %s leg for %q again.", displayName(p), leg.Kind, leg.Service), now); err != nil {
			return err
		}
		if err := e.record(ctx, tx, &p, "run.check_again", runID, map[string]any{"leg_id": legID, "service": leg.Service}); err != nil {
			return err
		}
		o.runChanged, o.legsQueued = true, true
		return e.advanceLocked(ctx, tx, run, o)
	})
}

// unknownLegOf locks the run and returns it with the named leg, after checking
// the person may restore, the run is in needs_reconciliation and the leg is
// one of its unknown legs.
func (e *Engine) unknownLegOf(ctx context.Context, tx pgx.Tx, p auth.Principal, runID, legID int64) (store.Run, store.Leg, error) {
	run, err := e.lockedRunFor(ctx, tx, p, runID, auth.Restore)
	if err != nil {
		return store.Run{}, store.Leg{}, err
	}
	if run.State != "needs_reconciliation" {
		return store.Run{}, store.Leg{}, fmt.Errorf("%w: it is %s", ErrWrongState, run.State)
	}
	legs, err := store.LegsOfRun(ctx, tx, runID)
	if err != nil {
		return store.Run{}, store.Leg{}, err
	}
	for _, l := range legs {
		if l.ID != legID {
			continue
		}
		if l.State != legUnknown {
			return store.Run{}, store.Leg{}, fmt.Errorf("%w: the leg is %s, not unknown", ErrWrongState, l.State)
		}
		return run, l, nil
	}
	return store.Run{}, store.Leg{}, fmt.Errorf("leg %d is not a leg of this run: %w", legID, store.ErrNotFound)
}

func legMovedAsWrongState(err error) error {
	if errors.Is(err, store.ErrLegMoved) {
		return fmt.Errorf("%w: the leg is no longer unknown", ErrWrongState)
	}
	return err
}

// ReleaseLocks lets an admin free the locks of a run stuck in
// needs_reconciliation without reconciling it. The run fails.
func (e *Engine) ReleaseLocks(ctx context.Context, p auth.Principal, runID int64) error {
	if !p.Admin {
		return fmt.Errorf("release locks of run %d: %w", runID, ErrForbidden)
	}
	return e.inTx(ctx, runID, func(tx pgx.Tx, o *outcome) error {
		run, err := store.LockRun(ctx, tx, runID)
		if err != nil {
			return fmt.Errorf("release locks of run %d: %w", runID, err)
		}
		if run.State != "needs_reconciliation" {
			return fmt.Errorf("release locks of run %d: %w: it is %s", runID, ErrWrongState, run.State)
		}
		reason := fmt.Sprintf("locks released by %s without reconciliation", displayName(p))
		held, err := store.LocksOfRun(ctx, tx, runID)
		if err != nil {
			return err
		}
		if err := e.moveTo(ctx, tx, run, "failed", reason, "", o); err != nil {
			return err
		}
		return e.record(ctx, tx, &p, "run.release_locks", runID, map[string]any{"released": held})
	})
}
