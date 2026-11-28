//go:build integration

package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/yamdrake/internal/store"
)

// freeze stores a freeze that starts now and ends after lasts.
func (h *harness) freeze(label, environment, reason string, lasts time.Duration) store.Freeze {
	h.t.Helper()
	f, err := store.CreateFreeze(context.Background(), h.pool, store.Freeze{
		Label: label, Environment: environment, Reason: reason,
		CreatedBy: h.admin.UserID, CreatedAt: h.clock.Now(), ExpiresAt: h.clock.Now().Add(lasts),
	})
	if err != nil {
		h.t.Fatalf("CreateFreeze: %v", err)
	}
	return f
}

// queuedRun starts a planned run that needs no approval and returns it queued.
func (h *harness) queuedRun(services ...string) store.Run {
	h.t.Helper()
	run := h.planned(h.starter, services...)
	if err := h.eng.Start(context.Background(), h.starter, run.ID); err != nil {
		h.t.Fatalf("Start: %v", err)
	}
	return run
}

// expectFrozen checks that the run is queued with the freeze's wait reason,
// holds no locks and has started no leg.
func (h *harness) expectFrozen(runID int64, f store.Freeze) {
	h.t.Helper()
	got := h.expectState(runID, "queued")
	want := "frozen: " + f.Reason + ", until " + f.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC")
	if got.WaitReason != want {
		h.t.Fatalf("wait reason %q, want %q", got.WaitReason, want)
	}
	if locks := h.locks(runID); len(locks) != 0 {
		h.t.Fatalf("a frozen run holds locks %v", locks)
	}
	for _, l := range h.legs(runID) {
		if l.Kind == "revalidate" || l.State == "queued" {
			h.t.Fatalf("a frozen run has a started leg: %+v", l)
		}
	}
}

func TestFrozenLabelBlocksQueuedRunWithReason(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	f := h.freeze(testLabel, "", "quarter-end change freeze", 2*time.Hour)
	run := h.queuedRun("s1")
	h.advance(run.ID)
	h.expectFrozen(run.ID, f)
}

func TestFrozenEnvironmentOnlyBlocksThatEnvironment(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.freeze("", "production", "other environment", time.Hour)
	h.freeze("other-label", "", "other label", time.Hour)
	run := h.queuedRun("s1")
	h.advance(run.ID)
	h.expectState(run.ID, "running")

	h2 := newHarness(t, harnessOptions{})
	f := h2.freeze("", testEnvironment, "staging is frozen", time.Hour)
	blocked := h2.queuedRun("s1")
	h2.advance(blocked.ID)
	h2.expectFrozen(blocked.ID, f)
}

func TestRunningRunContinuesUnderFreeze(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	run := h.running("s1")
	h.freeze(testLabel, "", "freeze after start", time.Hour)
	h.pastRevalidation(run.ID)
	h.expectState(run.ID, "running")
	if len(h.locks(run.ID)) == 0 {
		t.Fatalf("a running run lost its locks to a freeze")
	}
	queued := 0
	for _, l := range h.legs(run.ID) {
		if l.Kind == "step" && l.State == "queued" {
			queued++
		}
	}
	if queued == 0 {
		t.Fatalf("no step leg was queued: a freeze stopped a running run")
	}
}

func TestRunStartsWhenFreezeLifted(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	f := h.freeze(testLabel, "", "hold", time.Hour)
	run := h.queuedRun("s1")
	h.advance(run.ID)
	h.expectFrozen(run.ID, f)

	if err := store.LiftFreeze(context.Background(), h.pool, f.ID, h.admin.UserID, h.clock.Now()); err != nil {
		t.Fatalf("LiftFreeze: %v", err)
	}
	h.advance(run.ID)
	got := h.expectState(run.ID, "running")
	if got.WaitReason != "" || len(h.locks(run.ID)) == 0 {
		t.Fatalf("run is running with wait reason %q and locks %v", got.WaitReason, h.locks(run.ID))
	}
}

func TestRunStartsWhenFreezeExpires(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	f := h.freeze(testLabel, "", "short hold", 30*time.Minute)
	run := h.queuedRun("s1")
	h.advance(run.ID)
	h.expectFrozen(run.ID, f)

	h.clock.Advance(29 * time.Minute)
	h.advance(run.ID)
	h.expectFrozen(run.ID, f)

	h.clock.Advance(2 * time.Minute)
	h.advance(run.ID)
	h.expectState(run.ID, "running")
}

func TestLongestFreezeGivesTheReason(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.freeze(testLabel, "", "short", time.Hour)
	long := h.freeze("", testEnvironment, "long", 5*time.Hour)
	run := h.queuedRun("s1")
	h.advance(run.ID)
	h.expectFrozen(run.ID, long)
}

// Review Focus 3: the run waits behind another run's locks, a freeze appears,
// the locks free up. The run must still not take them.
func TestFreezeCreatedWhileRunWaitsForLocksStillBlocksIt(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	a := h.running("s1")
	b := h.queuedRun("s1")
	h.advance(b.ID)
	if got := h.expectState(b.ID, "queued"); !strings.HasPrefix(got.WaitReason, "waiting for run #") {
		t.Fatalf("B waits with %q, want it to wait for A's locks", got.WaitReason)
	}

	f := h.freeze(testLabel, "", "freeze while waiting", time.Hour)
	h.setLeg(h.legByKind(a.ID, "revalidate").ID, "failed")
	h.advance(a.ID)
	h.expectState(a.ID, "invalidated")
	if len(h.locks(a.ID)) != 0 {
		t.Fatalf("A still holds locks")
	}

	h.advance(b.ID)
	h.expectFrozen(b.ID, f)
	if err := store.LiftFreeze(context.Background(), h.pool, f.ID, h.admin.UserID, h.clock.Now()); err != nil {
		t.Fatalf("LiftFreeze: %v", err)
	}
	h.advance(b.ID)
	h.expectState(b.ID, "running")
}

func TestPlanningAndApprovingAreNotBlocked(t *testing.T) {
	h := newHarness(t, harnessOptions{approvalFor: []string{testLabel}})
	ctx := context.Background()
	f := h.freeze(testLabel, "", "hold", time.Hour)

	run := h.create(h.starter, false, "s1")
	h.advance(run.ID)
	h.resolved(run.ID)
	h.advance(run.ID)
	h.expectState(run.ID, "planned")

	if err := h.eng.Start(ctx, h.starter, run.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.expectState(run.ID, "waiting_approval")
	if err := h.eng.Approve(ctx, h.approver, run.ID, h.run(run.ID).ManifestID, ""); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	h.advance(run.ID)
	h.expectFrozen(run.ID, f)
}

func TestQueuedRunFailsWhenRequesterLostRunPermission(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	ctx := context.Background()
	h.running("s1")
	b := h.queuedRun("s1")
	h.advance(b.ID)
	h.expectState(b.ID, "queued")

	grants, err := store.GrantsOfUser(ctx, h.pool, h.starter.UserID)
	if err != nil || len(grants) == 0 {
		t.Fatalf("GrantsOfUser: %v, %v", grants, err)
	}
	for _, g := range grants {
		if err := store.DeleteGrant(ctx, h.pool, g.ID); err != nil {
			t.Fatalf("DeleteGrant: %v", err)
		}
	}
	h.advance(b.ID)
	h.expectUnauthorizedEnd(b.ID, "the person who started the run no longer has permission to run it")
}

func TestQueuedRunFailsWhenRequesterDisabled(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.running("s1")
	b := h.queuedRun("s1")
	h.advance(b.ID)
	h.expectState(b.ID, "queued")

	if err := store.SetDisabled(context.Background(), h.pool, h.starter.UserID, true); err != nil {
		t.Fatalf("SetDisabled: %v", err)
	}
	h.advance(b.ID)
	h.expectUnauthorizedEnd(b.ID, "the person who started the run is disabled")
}

func (h *harness) expectUnauthorizedEnd(runID int64, reason string) {
	h.t.Helper()
	got := h.expectState(runID, "failed")
	if got.EndReason != reason {
		h.t.Fatalf("end reason %q, want %q", got.EndReason, reason)
	}
	if len(h.locks(runID)) != 0 {
		h.t.Fatalf("the run holds locks %v", h.locks(runID))
	}
	for _, l := range h.legs(runID) {
		if l.Kind == "revalidate" || l.State == "queued" {
			h.t.Fatalf("a leg was started for an unauthorised run: %+v", l)
		}
	}
	entries := h.audits("run.fail_unauthorized")
	if len(entries) != 1 {
		h.t.Fatalf("audit entries %+v, want exactly one run.fail_unauthorized", entries)
	}
	if e := entries[0]; e.ActorID != nil || e.TargetType != "run" || e.TargetID != fmt.Sprint(runID) {
		h.t.Fatalf("audit entry %+v: want no principal, target run %d", e, runID)
	}
}
