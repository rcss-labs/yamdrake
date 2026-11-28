package engine

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/rcss-labs/yamdrake/internal/spec"
	"github.com/rcss-labs/yamdrake/internal/store"
)

var cancelAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func needs(t *testing.T, ns ...spec.Need) []byte {
	t.Helper()
	b, err := json.Marshal(ns)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func ptr(i int64) *int64 { return &i }

func run(state string) store.Run { return store.Run{ID: 1, State: state} }

func cancelled(r store.Run) store.Run {
	r.CancelRequestedAt = &cancelAt
	return r
}

func leg(id int64, kind, state string) store.Leg {
	return store.Leg{ID: id, RunID: 1, Kind: kind, State: state}
}

func stepLeg(id, stepID int64, service, state string) store.Leg {
	return store.Leg{ID: id, RunID: 1, StepID: ptr(stepID), Kind: "step", Service: service, State: state}
}

// withRevalidated returns the legs of a run whose revalidation succeeded.
func withRevalidated(legs ...store.Leg) []store.Leg {
	return append([]store.Leg{leg(90, "resolve", "succeeded"), leg(91, "revalidate", "succeeded")}, legs...)
}

func ids(x []int64) []int64 {
	out := append([]int64{}, x...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func wantIDs(t *testing.T, what string, got []int64, want ...int64) {
	t.Helper()
	if want == nil {
		want = []int64{}
	}
	if !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

func TestResolving(t *testing.T) {
	cases := []struct {
		name      string
		run       store.Run
		legState  string
		wantState string
	}{
		{"succeeded to planned", run("resolving"), "succeeded", "planned"},
		{"auto start without approval queues", store.Run{State: "resolving", AutoStart: true}, "succeeded", "queued"},
		{"auto start with approval waits", store.Run{State: "resolving", AutoStart: true, ApprovalRequired: true}, "succeeded", "waiting_approval"},
		{"approval without auto start still planned", store.Run{State: "resolving", ApprovalRequired: true}, "succeeded", "planned"},
		{"failed", run("resolving"), "failed", "failed"},
		{"unknown fails", run("resolving"), "unknown", "failed"},
		{"running leg waits", run("resolving"), "running", ""},
		{"queued leg waits", run("resolving"), "queued", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Decide(View{Run: c.run, Legs: []store.Leg{leg(1, "resolve", c.legState)}})
			if d.NewState != c.wantState {
				t.Fatalf("NewState = %q, want %q", d.NewState, c.wantState)
			}
			if c.legState == "unknown" && d.Reason == "" {
				t.Fatal("unknown resolve must give a reason")
			}
			if d.WaitReason != "" {
				t.Fatalf("a final state must not carry a wait reason: %q", d.WaitReason)
			}
			if d.TryAcquireLocks || d.CreateRevalidateLeg {
				t.Fatalf("unexpected lock work: %+v", d)
			}
		})
	}
}

func TestResolvingCancelCancelsUnclaimedLegAndFlagsClaimedOne(t *testing.T) {
	d := Decide(View{Run: cancelled(run("resolving")), Legs: []store.Leg{leg(1, "resolve", "queued")}})
	if d.NewState != "cancelled" {
		t.Fatalf("NewState = %q", d.NewState)
	}
	wantIDs(t, "CancelLegs", d.CancelLegs, 1)
	wantIDs(t, "MarkCancelRequested", d.MarkCancelRequested)

	d = Decide(View{Run: cancelled(run("resolving")), Legs: []store.Leg{leg(1, "resolve", "claimed")}})
	if d.NewState != "cancelled" {
		t.Fatalf("NewState = %q", d.NewState)
	}
	wantIDs(t, "CancelLegs", d.CancelLegs)
	wantIDs(t, "MarkCancelRequested", d.MarkCancelRequested, 1)
}

func TestPlannedAndWaitingApprovalOnlyMoveOnCancel(t *testing.T) {
	for _, st := range []string{"planned", "waiting_approval"} {
		d := Decide(View{Run: run(st), Legs: []store.Leg{leg(1, "resolve", "succeeded")}})
		if !reflect.DeepEqual(d, Decision{}) {
			t.Fatalf("%s without cancel: %+v", st, d)
		}
	}
}

func TestQueued(t *testing.T) {
	legs := []store.Leg{leg(1, "resolve", "succeeded"), stepLeg(2, 10, "a", "pending")}
	d := Decide(View{Run: run("queued"), Legs: legs})
	if !d.TryAcquireLocks || d.NewState != "running" || !d.CreateRevalidateLeg {
		t.Fatalf("queued run should try locks and start: %+v", d)
	}

	d = Decide(View{Run: run("queued"), Legs: legs, Frozen: "change freeze"})
	if d.TryAcquireLocks || d.NewState != "" || d.CreateRevalidateLeg || d.WaitReason == "" {
		t.Fatalf("frozen run should only set a wait reason: %+v", d)
	}

	// The reason is not repeated once the run already shows it.
	r := run("queued")
	r.WaitReason = d.WaitReason
	d2 := Decide(View{Run: r, Legs: legs, Frozen: "change freeze"})
	if d2.WaitReason != "" {
		t.Fatalf("repeated wait reason: %+v", d2)
	}
}

func TestRunningWaitsForRevalidation(t *testing.T) {
	for _, st := range []string{"queued", "claimed", "running"} {
		d := Decide(View{Run: run("running"), LocksHeld: true, Legs: []store.Leg{
			leg(1, "resolve", "succeeded"), leg(2, "revalidate", st), stepLeg(3, 10, "a", "pending")}})
		if !reflect.DeepEqual(d, Decision{}) {
			t.Fatalf("revalidate %s: %+v", st, d)
		}
	}
}

func TestRevalidateFailedInvalidatesAndReleases(t *testing.T) {
	drifted := leg(2, "revalidate", "failed")
	drifted.FailureShort = RevalidateDriftReason
	d := Decide(View{Run: run("running"), LocksHeld: true, Legs: []store.Leg{
		leg(1, "resolve", "succeeded"), drifted, stepLeg(3, 10, "a", "pending")}})
	if d.NewState != "invalidated" || !d.ReleaseLocks {
		t.Fatalf("got %+v", d)
	}
	wantIDs(t, "CancelLegs", d.CancelLegs, 3)
	wantIDs(t, "QueueLegs", d.QueueLegs)
}

func TestRevalidateThatCannotReachAWSFailsTheRunInsteadOfInvalidatingIt(t *testing.T) {
	unreachable := leg(2, "revalidate", "failed")
	unreachable.FailureShort = RevalidateCouldNotCheckReason + ": api: ecs:DescribeServices failed: ThrottlingException"
	d := Decide(View{Run: run("running"), LocksHeld: true, Legs: []store.Leg{
		leg(1, "resolve", "succeeded"), unreachable, stepLeg(3, 10, "a", "pending")}})
	if d.NewState != "failed" || !d.ReleaseLocks {
		t.Fatalf("got %+v, want failed with the locks released", d)
	}
	if d.Reason != unreachable.FailureShort {
		t.Errorf("reason %q, want the leg's short reason %q", d.Reason, unreachable.FailureShort)
	}
	wantIDs(t, "CancelLegs", d.CancelLegs, 3)

	// A leg that failed for another reason is also not drift.
	other := leg(2, "revalidate", "failed")
	other.FailureShort = "the run has no manifest"
	d = Decide(View{Run: run("running"), LocksHeld: true, Legs: []store.Leg{leg(1, "resolve", "succeeded"), other}})
	if d.NewState != "failed" || d.Reason != "could not re-check the targets: the run has no manifest" {
		t.Errorf("got %+v", d)
	}
}

func TestRevalidateUnknownNeedsReconciliationKeepsLocks(t *testing.T) {
	legs := []store.Leg{
		leg(1, "resolve", "succeeded"), leg(2, "revalidate", "unknown"), stepLeg(3, 10, "a", "pending")}
	d := Decide(View{Run: run("running"), LocksHeld: true, Legs: legs})
	if d.NewState != "needs_reconciliation" || d.ReleaseLocks {
		t.Fatalf("got %+v", d)
	}
	wantIDs(t, "CancelLegs", d.CancelLegs, 3)

	// still unknown: the run waits
	after := apply(legs, d)
	if d := Decide(View{Run: run("needs_reconciliation"), LocksHeld: true, Legs: after}); !reflect.DeepEqual(d, Decision{}) {
		t.Fatalf("still unknown: %+v", d)
	}
	// the check-again leg settles: the run reaches a final state and releases locks
	for state, want := range map[string]string{"succeeded": "failed", "failed": "failed"} {
		settled := append([]store.Leg{}, after...)
		settled[1].State = state
		d := Decide(View{Run: run("needs_reconciliation"), LocksHeld: true, Legs: settled})
		if d.NewState != want || !d.ReleaseLocks {
			t.Fatalf("revalidate %s: %+v", state, d)
		}
	}
}

// fourServices: step "build" (position 0) and step "deploy" (position 1) over
// four services; the service "s1" build failed.
func fourServices(t *testing.T, scope string) View {
	steps := []store.RunStep{
		{ID: 10, Name: "build", Position: 0},
		{ID: 11, Name: "deploy", Position: 1, Needs: needs(t, spec.Need{Step: "build", Scope: scope})},
	}
	legs := withRevalidated(
		stepLeg(1, 10, "s1", "failed"), stepLeg(2, 10, "s2", "succeeded"),
		stepLeg(3, 10, "s3", "succeeded"), stepLeg(4, 10, "s4", "succeeded"),
		stepLeg(5, 11, "s1", "pending"), stepLeg(6, 11, "s2", "pending"),
		stepLeg(7, 11, "s3", "pending"), stepLeg(8, 11, "s4", "pending"))
	return View{Run: run("running"), LocksHeld: true, Steps: steps, Legs: legs}
}

func TestStepScopeSkipsWholeStepWhenOneNeededLegFails(t *testing.T) {
	d := Decide(fourServices(t, "step"))
	wantIDs(t, "SkipLegs", d.SkipLegs, 5, 6, 7, 8)
	wantIDs(t, "QueueLegs", d.QueueLegs)
}

func TestServiceScopeSkipsOnlyThatService(t *testing.T) {
	d := Decide(fourServices(t, "service"))
	wantIDs(t, "SkipLegs", d.SkipLegs, 5)
	wantIDs(t, "QueueLegs", d.QueueLegs, 6, 7, 8)
}

func TestUnknownNeededLegSkipsDependentsInBothScopes(t *testing.T) {
	for scope, want := range map[string][]int64{"step": {5, 6, 7, 8}, "service": {5}} {
		v := fourServices(t, scope)
		v.Legs[2] = stepLeg(1, 10, "s1", "unknown")
		d := Decide(v)
		wantIDs(t, scope+" SkipLegs", d.SkipLegs, want...)
	}
}

func TestUnknownInChainSettlesIntoNeedsReconciliation(t *testing.T) {
	steps := []store.RunStep{
		{ID: 10, Name: "a", Position: 0},
		{ID: 11, Name: "b", Position: 1, Needs: needs(t, spec.Need{Step: "a", Scope: "step"})},
		{ID: 12, Name: "c", Position: 2, Needs: needs(t, spec.Need{Step: "b", Scope: "service"})},
	}
	v := View{Run: run("running"), Steps: steps, LocksHeld: true,
		Legs: withRevalidated(stepLeg(1, 10, "x", "unknown"), stepLeg(2, 11, "x", "pending"), stepLeg(3, 12, "x", "pending"))}
	d := Decide(v)
	wantIDs(t, "first SkipLegs", d.SkipLegs, 2)
	v.Legs = apply(v.Legs, d)
	d = Decide(v)
	wantIDs(t, "second SkipLegs", d.SkipLegs, 3)
	v.Legs = apply(v.Legs, d)
	d = Decide(v)
	if d.NewState != "needs_reconciliation" || d.ReleaseLocks {
		t.Fatalf("after cascade: %+v", d)
	}
}

func TestStepScopeWaitsUntilEveryNeededLegSucceeded(t *testing.T) {
	v := fourServices(t, "step")
	v.Legs[2] = stepLeg(1, 10, "s1", "running")
	d := Decide(v)
	wantIDs(t, "SkipLegs", d.SkipLegs)
	wantIDs(t, "QueueLegs", d.QueueLegs)

	v.Legs[2] = stepLeg(1, 10, "s1", "succeeded")
	d = Decide(v)
	wantIDs(t, "QueueLegs", d.QueueLegs, 5, 6, 7, 8)
}

func TestNeedsOnUnknownStepIsIgnoredAndNoNeedsIsReady(t *testing.T) {
	steps := []store.RunStep{
		{ID: 10, Name: "a", Position: 0},
		{ID: 11, Name: "b", Position: 1, Needs: needs(t, spec.Need{Step: "dropped", Scope: "step"})},
	}
	d := Decide(View{Run: run("running"), Steps: steps, LocksHeld: true,
		Legs: withRevalidated(stepLeg(1, 10, "x", "pending"), stepLeg(2, 11, "x", "pending"))})
	wantIDs(t, "QueueLegs", d.QueueLegs, 1, 2)
}

func TestSkipCascadesThroughChain(t *testing.T) {
	steps := []store.RunStep{
		{ID: 10, Name: "a", Position: 0},
		{ID: 11, Name: "b", Position: 1, Needs: needs(t, spec.Need{Step: "a", Scope: "step"})},
		{ID: 12, Name: "c", Position: 2, Needs: needs(t, spec.Need{Step: "b", Scope: "service"})},
	}
	legs := withRevalidated(stepLeg(1, 10, "x", "failed"), stepLeg(2, 11, "x", "pending"), stepLeg(3, 12, "x", "pending"))
	v := View{Run: run("running"), Steps: steps, LocksHeld: true, Legs: legs}

	d := Decide(v)
	wantIDs(t, "first SkipLegs", d.SkipLegs, 2)
	v.Legs = apply(v.Legs, d)
	d = Decide(v)
	wantIDs(t, "second SkipLegs", d.SkipLegs, 3)
	v.Legs = apply(v.Legs, d)
	d = Decide(v)
	if d.NewState != "failed" || !d.ReleaseLocks {
		t.Fatalf("after cascade: %+v", d)
	}
}

// apply returns legs after the leg changes in d, as Advance would make them.
func apply(legs []store.Leg, d Decision) []store.Leg {
	out := append([]store.Leg{}, legs...)
	set := func(ids []int64, state string) {
		for _, id := range ids {
			for i := range out {
				if out[i].ID == id {
					out[i].State = state
				}
			}
		}
	}
	set(d.QueueLegs, "queued")
	set(d.SkipLegs, "skipped")
	set(d.CancelLegs, "cancelled")
	for _, id := range d.MarkCancelRequested {
		for i := range out {
			if out[i].ID == id {
				out[i].CancelRequested = true
			}
		}
	}
	return out
}

func TestMaxParallelAndLimitsCapQueueing(t *testing.T) {
	build := func(maxParallel int, running ...store.Leg) View {
		legs := withRevalidated(running...)
		for i := int64(0); i < 5; i++ {
			legs = append(legs, stepLeg(100+i, 10, "s", "pending"))
		}
		return View{Run: run("running"), LocksHeld: true,
			Steps: []store.RunStep{{ID: 10, Name: "a", MaxParallel: maxParallel}}, Legs: legs}
	}

	d := Decide(build(2))
	wantIDs(t, "step limit 2", d.QueueLegs, 100, 101)

	d = Decide(build(0))
	wantIDs(t, "no step limit", d.QueueLegs, 100, 101, 102, 103, 104)

	// own running legs count toward the step limit
	d = Decide(build(2, stepLeg(1, 10, "s", "running")))
	wantIDs(t, "step limit with one running", d.QueueLegs, 100)

	v := build(0)
	v.Limits = Limits{PerEnvironment: 10}
	v.RunningInEnv = 9
	wantIDs(t, "environment limit", Decide(v).QueueLegs, 100)

	v = build(0)
	v.Limits = Limits{PerAccount: 20}
	v.RunningInAccount = 19
	wantIDs(t, "account limit", Decide(v).QueueLegs, 100)

	// this run's own active legs count too
	v = build(0, stepLeg(1, 10, "s", "claimed"))
	v.Limits = Limits{PerEnvironment: 10}
	v.RunningInEnv = 8
	wantIDs(t, "environment limit with own leg", Decide(v).QueueLegs, 100)

	v = build(0)
	v.Limits = Limits{PerEnvironment: 10, PerAccount: 20}
	v.RunningInEnv, v.RunningInAccount = 10, 0
	wantIDs(t, "environment full", Decide(v).QueueLegs)
}

func TestQueueOrderIsStepPositionThenLegID(t *testing.T) {
	steps := []store.RunStep{{ID: 10, Name: "late", Position: 1}, {ID: 11, Name: "early", Position: 0}}
	v := View{Run: run("running"), LocksHeld: true, Steps: steps, Limits: Limits{PerEnvironment: 2},
		Legs: withRevalidated(stepLeg(1, 10, "x", "pending"), stepLeg(2, 11, "y", "pending"), stepLeg(3, 11, "z", "pending"))}
	wantIDs(t, "QueueLegs", Decide(v).QueueLegs, 2, 3)
}

func TestRunningCancelMovesToCancelling(t *testing.T) {
	d := Decide(View{Run: cancelled(run("running")), LocksHeld: true, Legs: withRevalidated(
		stepLeg(1, 10, "a", "pending"), stepLeg(2, 10, "b", "running"))})
	if d.NewState != "cancelling" || d.ReleaseLocks {
		t.Fatalf("got %+v", d)
	}
	wantIDs(t, "QueueLegs", d.QueueLegs)
}

func TestCancellingCancelsAndFlagsAndQueuesNothing(t *testing.T) {
	steps := []store.RunStep{{ID: 10, Name: "a"}}
	v := View{Run: cancelled(run("cancelling")), LocksHeld: true, Steps: steps, Legs: withRevalidated(
		stepLeg(1, 10, "a", "pending"), stepLeg(2, 10, "b", "queued"),
		stepLeg(3, 10, "c", "claimed"), stepLeg(4, 10, "d", "running"),
		stepLeg(5, 10, "e", "succeeded"))}
	d := Decide(v)
	wantIDs(t, "CancelLegs", d.CancelLegs, 1, 2)
	wantIDs(t, "MarkCancelRequested", d.MarkCancelRequested, 3, 4)
	wantIDs(t, "QueueLegs", d.QueueLegs)
	if d.NewState != "" || d.ReleaseLocks {
		t.Fatalf("got %+v", d)
	}
}

func TestRunningWithAllLegsSettledAppliesPrecedence(t *testing.T) {
	d := Decide(View{Run: run("running"), LocksHeld: true, Legs: withRevalidated(stepLeg(1, 10, "a", "succeeded"))})
	if d.NewState != "succeeded" || !d.ReleaseLocks {
		t.Fatalf("got %+v", d)
	}
}

func TestPrecedence(t *testing.T) {
	cases := []struct {
		name      string
		cancel    bool
		states    []string
		wantState string
		release   bool
	}{
		{"all succeeded", false, []string{"succeeded", "succeeded"}, "succeeded", true},
		{"some failed", false, []string{"succeeded", "failed"}, "partially_succeeded", true},
		{"all failed", false, []string{"failed", "failed"}, "failed", true},
		{"some skipped only", false, []string{"skipped", "skipped"}, "failed", true},
		{"succeeded and skipped", false, []string{"succeeded", "skipped"}, "partially_succeeded", true},
		{"one unknown among successes", false, []string{"succeeded", "unknown", "succeeded"}, "needs_reconciliation", false},
		{"cancel with mixed settled legs", true, []string{"succeeded", "cancelled", "failed"}, "cancelled", true},
		{"cancel with one unknown", true, []string{"succeeded", "unknown", "cancelled"}, "needs_reconciliation", false},
	}
	for _, c := range cases {
		for _, st := range []string{"running", "cancelling"} {
			t.Run(c.name+"/"+st, func(t *testing.T) {
				legs := withRevalidated()
				for i, s := range c.states {
					legs = append(legs, stepLeg(int64(i+1), 10, "svc", s))
				}
				r := run(st)
				if c.cancel || st == "cancelling" {
					r = cancelled(r)
				}
				d := Decide(View{Run: r, LocksHeld: true, Legs: legs})
				want := c.wantState
				if st == "cancelling" && !c.cancel && want != "needs_reconciliation" {
					want = "cancelled"
				}
				if d.NewState != want {
					t.Fatalf("NewState = %q, want %q", d.NewState, want)
				}
				if want != c.wantState {
					return
				}
				if d.ReleaseLocks != c.release {
					t.Fatalf("ReleaseLocks = %v, want %v", d.ReleaseLocks, c.release)
				}
			})
		}
	}
}

func TestUnknownRevalidateOrResolveCountsInPrecedence(t *testing.T) {
	d := Decide(View{Run: run("running"), LocksHeld: true, Legs: []store.Leg{
		leg(1, "resolve", "unknown"), leg(2, "revalidate", "succeeded"), stepLeg(3, 10, "a", "succeeded")}})
	if d.NewState != "needs_reconciliation" {
		t.Fatalf("got %+v", d)
	}
}

func TestNeedsReconciliation(t *testing.T) {
	r := run("needs_reconciliation")
	still := Decide(View{Run: r, LocksHeld: true, Legs: withRevalidated(stepLeg(1, 10, "a", "unknown"), stepLeg(2, 10, "b", "succeeded"))})
	if !reflect.DeepEqual(still, Decision{}) {
		t.Fatalf("unknown leg remains: %+v", still)
	}

	d := Decide(View{Run: r, LocksHeld: true, Legs: withRevalidated(stepLeg(1, 10, "a", "failed"), stepLeg(2, 10, "b", "succeeded"))})
	if d.NewState != "partially_succeeded" || !d.ReleaseLocks {
		t.Fatalf("resolved: %+v", d)
	}

	d = Decide(View{Run: cancelled(r), LocksHeld: true, Legs: withRevalidated(stepLeg(1, 10, "a", "succeeded"))})
	if d.NewState != "cancelled" || !d.ReleaseLocks {
		t.Fatalf("cancel requested: %+v", d)
	}

	// a check-again leg was requeued: the run stays put, never resumes
	d = Decide(View{Run: r, LocksHeld: true, Legs: withRevalidated(stepLeg(1, 10, "a", "queued"))})
	if !reflect.DeepEqual(d, Decision{}) {
		t.Fatalf("retried: %+v", d)
	}
	d = Decide(View{Run: cancelled(r), LocksHeld: true, Legs: withRevalidated(stepLeg(1, 10, "a", "running"))})
	if !reflect.DeepEqual(d, Decision{}) {
		t.Fatalf("retried with cancel requested: %+v", d)
	}
}

func TestFinalStatesDecideNothing(t *testing.T) {
	for _, st := range []string{"succeeded", "partially_succeeded", "failed", "cancelled", "rejected", "expired", "invalidated"} {
		d := Decide(View{Run: cancelled(run(st)), Legs: withRevalidated(stepLeg(1, 10, "a", "pending"))})
		if !reflect.DeepEqual(d, Decision{}) {
			t.Fatalf("%s: %+v", st, d)
		}
	}
}

func TestCancelInEveryRunState(t *testing.T) {
	legsFor := map[string][]store.Leg{
		"resolving":        {leg(1, "resolve", "running")},
		"planned":          {leg(1, "resolve", "succeeded"), stepLeg(2, 10, "a", "pending")},
		"waiting_approval": {leg(1, "resolve", "succeeded"), stepLeg(2, 10, "a", "pending")},
		"queued":           {leg(1, "resolve", "succeeded"), stepLeg(2, 10, "a", "pending")},
		"running":          withRevalidated(stepLeg(2, 10, "a", "running")),
		"cancelling":       withRevalidated(stepLeg(2, 10, "a", "running")),
	}
	want := map[string]string{
		"resolving": "cancelled", "planned": "cancelled", "waiting_approval": "cancelled",
		"queued": "cancelled", "running": "cancelling", "cancelling": "",
	}
	for st, legs := range legsFor {
		t.Run(st, func(t *testing.T) {
			d := Decide(View{Run: cancelled(run(st)), Legs: legs, LocksHeld: st == "running" || st == "cancelling"})
			if d.NewState != want[st] {
				t.Fatalf("NewState = %q, want %q", d.NewState, want[st])
			}
			if len(d.QueueLegs) != 0 || d.TryAcquireLocks || d.CreateRevalidateLeg {
				t.Fatalf("a cancelled run must not start work: %+v", d)
			}
			if st == "planned" || st == "waiting_approval" || st == "queued" {
				wantIDs(t, "CancelLegs", d.CancelLegs, 2)
			}
			// In-flight legs are flagged, never silently left.
			for _, l := range legs {
				if l.State == "running" {
					wantIDs(t, "MarkCancelRequested", d.MarkCancelRequested, l.ID)
				}
			}
		})
	}
	t.Run("needs_reconciliation with an unknown leg", func(t *testing.T) {
		d := Decide(View{Run: cancelled(run("needs_reconciliation")), LocksHeld: true,
			Legs: withRevalidated(stepLeg(2, 10, "a", "unknown"))})
		if !reflect.DeepEqual(d, Decision{}) {
			t.Fatalf("got %+v", d)
		}
	})
	t.Run("needs_reconciliation settled", func(t *testing.T) {
		d := Decide(View{Run: cancelled(run("needs_reconciliation")), LocksHeld: true,
			Legs: withRevalidated(stepLeg(2, 10, "a", "failed"))})
		if d.NewState != "cancelled" || !d.ReleaseLocks {
			t.Fatalf("got %+v", d)
		}
	})
}

func TestDecideIsIdempotent(t *testing.T) {
	for _, scope := range []string{"step", "service"} {
		v := fourServices(t, scope)
		d := Decide(v)
		v.Legs = apply(v.Legs, d)
		d2 := Decide(v)
		if len(d2.QueueLegs) != 0 || len(d2.SkipLegs) != 0 {
			t.Fatalf("%s: second call repeats work: %+v", scope, d2)
		}
	}
	v := View{Run: cancelled(run("cancelling")), LocksHeld: true,
		Legs: withRevalidated(stepLeg(1, 10, "a", "pending"), stepLeg(2, 10, "b", "running"))}
	v.Legs = apply(v.Legs, Decide(v))
	d := Decide(v)
	if len(d.CancelLegs) != 0 || len(d.MarkCancelRequested) != 0 {
		t.Fatalf("cancelling repeats work: %+v", d)
	}
}

func TestDecideDoesNotMutateItsInput(t *testing.T) {
	v := fourServices(t, "service")
	v.Legs = append(v.Legs, stepLeg(9, 11, "s5", "pending"))
	// reverse the legs so sorting inside Decide would show
	for i, j := 0, len(v.Legs)-1; i < j; i, j = i+1, j-1 {
		v.Legs[i], v.Legs[j] = v.Legs[j], v.Legs[i]
	}
	before := append([]store.Leg{}, v.Legs...)
	stepsBefore := append([]store.RunStep{}, v.Steps...)
	Decide(v)
	if !reflect.DeepEqual(before, v.Legs) || !reflect.DeepEqual(stepsBefore, v.Steps) {
		t.Fatal("Decide changed its input")
	}
}

func TestCancelFlagsAQueuedLegWithAnIntendedOperationInsteadOfCancellingIt(t *testing.T) {
	intended := stepLeg(10, 1, "s1", "queued")
	intended.Attempt = 1
	// Released after a claim, no operation: attempted, but nothing changed.
	released := stepLeg(11, 1, "s2", "queued")
	released.Attempt = 1
	legs := withRevalidated(intended, released, stepLeg(12, 2, "s1", "pending"))

	for _, state := range []string{"running", "cancelling"} {
		d := Decide(View{Run: cancelled(run(state)), Legs: legs, IntendedLegs: map[int64]bool{10: true}})
		wantIDs(t, state+" CancelLegs", d.CancelLegs, 11, 12)
		wantIDs(t, state+" MarkCancelRequested", d.MarkCancelRequested, 10)
	}

	// Before the run starts nothing external was changed: the legs are cancelled whatever their attempts.
	resolve := leg(1, "resolve", "queued")
	resolve.Attempt = 2
	d := Decide(View{Run: cancelled(run("resolving")), Legs: []store.Leg{resolve}})
	wantIDs(t, "resolving CancelLegs", d.CancelLegs, 1)
}

func TestRunHeldBackByCapacityShowsAWaitReasonAndClearsIt(t *testing.T) {
	full := func(reason string) View {
		r := run("running")
		r.Environment = "prod-eu"
		r.WaitReason = reason
		legs := withRevalidated(stepLeg(100, 10, "s", "pending"))
		return View{Run: r, LocksHeld: true, Steps: []store.RunStep{{ID: 10, Name: "a"}}, Legs: legs,
			Limits: Limits{PerEnvironment: 10}, RunningInEnv: 10}
	}
	const reason = "waiting for capacity in prod-eu"

	d := Decide(full(""))
	wantIDs(t, "QueueLegs", d.QueueLegs)
	if d.WaitReason != reason {
		t.Fatalf("WaitReason = %q, want %q", d.WaitReason, reason)
	}
	if d := Decide(full(reason)); d.WaitReason != "" || d.ClearWaitReason {
		t.Fatalf("an unchanged reason was set again or cleared: %+v", d)
	}

	// Room appears: the leg is queued and the reason goes.
	v := full(reason)
	v.RunningInEnv = 3
	d = Decide(v)
	wantIDs(t, "QueueLegs", d.QueueLegs, 100)
	if !d.ClearWaitReason || d.WaitReason != "" {
		t.Fatalf("got %+v, want the capacity reason cleared once a leg is queued", d)
	}

	// Another kind of reason is not ours to clear.
	v = full("waiting for run #4")
	v.RunningInEnv = 3
	if d := Decide(v); d.ClearWaitReason {
		t.Fatalf("cleared a reason it did not set: %+v", d)
	}
}
