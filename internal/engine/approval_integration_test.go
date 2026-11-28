//go:build integration

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/yamdrake/internal/auth"
	"github.com/rcss-labs/yamdrake/internal/store"
)

// waitingRun makes a planned run for p and starts it into waiting_approval.
func (h *harness) waitingRun(p auth.Principal, services ...string) store.Run {
	h.t.Helper()
	run := h.planned(p, services...)
	if err := h.eng.Start(context.Background(), p, run.ID); err != nil {
		h.t.Fatalf("Start: %v", err)
	}
	return h.expectState(run.ID, "waiting_approval")
}

func (h *harness) latestApproval(runID int64) store.Approval {
	h.t.Helper()
	a, ok, err := store.LatestApproval(context.Background(), h.pool, runID)
	if err != nil || !ok {
		h.t.Fatalf("LatestApproval(%d) = %v, %v, %v; want an approval", runID, a, ok, err)
	}
	return a
}

func (h *harness) approvalCount(runID int64) int {
	h.t.Helper()
	var n int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM approvals WHERE run_id = $1`, runID).Scan(&n); err != nil {
		h.t.Fatalf("count approvals: %v", err)
	}
	return n
}

func TestWaitingApprovalCreatesRequestWithExpiryFromJob(t *testing.T) {
	t.Run("through Start, with the job's expiry", func(t *testing.T) {
		h := newHarness(t, harnessOptions{approvalFor: []string{testLabel}, approvalExpiry: 90 * time.Minute})
		run := h.waitingRun(h.starter, "s1")

		a := h.latestApproval(run.ID)
		if a.Decision != "" || a.DecidedBy != nil || a.DecidedAt != nil {
			t.Errorf("approval = %+v, want it open", a)
		}
		if a.ManifestID == "" || a.ManifestID != run.ManifestID {
			t.Errorf("approval manifest %q, run manifest %q; want the run's, not empty", a.ManifestID, run.ManifestID)
		}
		if a.RequestedBy != h.starter.UserID {
			t.Errorf("requested by %d, want the starter %d", a.RequestedBy, h.starter.UserID)
		}
		if !a.RequestedAt.Equal(h.clock.Now()) || !a.ExpiresAt.Equal(h.clock.Now().Add(90*time.Minute)) {
			t.Errorf("requested %v expires %v, want now and now+90m (now %v)", a.RequestedAt, a.ExpiresAt, h.clock.Now())
		}
	})

	t.Run("default expiry is 24 hours", func(t *testing.T) {
		h := newHarness(t, harnessOptions{approvalFor: []string{testLabel}})
		run := h.waitingRun(h.starter, "s1")
		if a := h.latestApproval(run.ID); !a.ExpiresAt.Equal(h.clock.Now().Add(24 * time.Hour)) {
			t.Errorf("expires %v, want now+24h", a.ExpiresAt)
		}
	})

	t.Run("through Advance when the run starts itself", func(t *testing.T) {
		h := newHarness(t, harnessOptions{approvalFor: []string{testLabel}, approvalExpiry: time.Hour})
		run := h.create(h.starter, true, "s1") // auto start
		h.advance(run.ID)
		h.resolved(run.ID)
		h.advance(run.ID)
		run = h.expectState(run.ID, "waiting_approval")
		a := h.latestApproval(run.ID)
		if a.Decision != "" || a.ManifestID == "" || a.ManifestID != run.ManifestID || a.RequestedBy != h.starter.UserID {
			t.Errorf("approval = %+v, want open with the run's manifest %q, requested by the starter", a, run.ManifestID)
		}
		if h.approvalCount(run.ID) != 1 {
			t.Errorf("%d approvals, want exactly one", h.approvalCount(run.ID))
		}
		h.advance(run.ID) // more advancing must not ask again
		if h.approvalCount(run.ID) != 1 {
			t.Errorf("%d approvals after another Advance, want exactly one", h.approvalCount(run.ID))
		}
	})

	t.Run("a run that needs no approval gets none", func(t *testing.T) {
		h := newHarness(t, harnessOptions{})
		run := h.planned(h.starter, "s1")
		if err := h.eng.Start(context.Background(), h.starter, run.ID); err != nil {
			t.Fatal(err)
		}
		if h.approvalCount(run.ID) != 0 {
			t.Errorf("%d approvals for a run that needs none", h.approvalCount(run.ID))
		}
	})
}

func TestApproveMovesToQueuedAndRecordsWho(t *testing.T) {
	h := newHarness(t, harnessOptions{approvalFor: []string{testLabel}})
	run := h.waitingRun(h.starter, "s1")
	h.clock.Advance(5 * time.Minute)

	if err := h.eng.Approve(context.Background(), h.approver, run.ID, run.ManifestID, "looks right"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	h.expectState(run.ID, "queued")
	a := h.latestApproval(run.ID)
	if a.Decision != "approved" || a.DecidedBy == nil || *a.DecidedBy != h.approver.UserID || a.Comment != "looks right" {
		t.Errorf("approval = %+v, want approved by the approver with the comment", a)
	}
	if a.DecidedAt == nil || !a.DecidedAt.Equal(h.clock.Now()) {
		t.Errorf("decided at %v, want now %v", a.DecidedAt, h.clock.Now())
	}
	if _, open, _ := store.OpenApproval(context.Background(), h.pool, run.ID); open {
		t.Errorf("the approval is still open after the decision")
	}
}

func TestApproveOwnRunRefusedButAdminAllowed(t *testing.T) {
	h := newHarness(t, harnessOptions{approvalFor: []string{testLabel}})
	ctx := context.Background()

	own := h.waitingRun(h.starterApprover, "s1")
	if err := h.eng.Approve(ctx, h.starterApprover, own.ID, own.ManifestID, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("approving one's own run = %v, want ErrForbidden", err)
	}
	if err := h.eng.Approve(ctx, h.stranger, own.ID, own.ManifestID, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("approving without the grant = %v, want ErrForbidden", err)
	}
	h.expectState(own.ID, "waiting_approval")
	if a := h.latestApproval(own.ID); a.Decision != "" {
		t.Errorf("a refused approval left decision %q", a.Decision)
	}

	adminRun := h.waitingRun(h.admin, "s2")
	if err := h.eng.Approve(ctx, h.admin, adminRun.ID, adminRun.ManifestID, ""); err != nil {
		t.Fatalf("admin approving own run: %v", err)
	}
	h.expectState(adminRun.ID, "queued")
}

func TestApproveWithStaleManifestIsRefused(t *testing.T) {
	h := newHarness(t, harnessOptions{approvalFor: []string{testLabel}})
	run := h.waitingRun(h.starter, "s1")

	for _, stale := range []string{"", "not-the-plan-that-was-shown"} {
		err := h.eng.Approve(context.Background(), h.approver, run.ID, stale, "c")
		if !errors.Is(err, ErrManifestChanged) {
			t.Fatalf("Approve(manifest %q) = %v, want ErrManifestChanged", stale, err)
		}
	}
	h.expectState(run.ID, "waiting_approval")
	if a := h.latestApproval(run.ID); a.Decision != "" || a.DecidedBy != nil {
		t.Errorf("approval = %+v, want nothing recorded", a)
	}
	if n := len(h.audits("run.approve")); n != 0 {
		t.Errorf("%d approve audit entries for refused approvals", n)
	}
	// The real manifest still works afterwards.
	if err := h.eng.Approve(context.Background(), h.approver, run.ID, run.ManifestID, ""); err != nil {
		t.Fatalf("Approve with the right manifest: %v", err)
	}
}

func TestApproveAndRejectRaceHasOneWinner(t *testing.T) {
	h := newHarness(t, harnessOptions{approvalFor: []string{testLabel}})
	ctx := context.Background()

	for round := 0; round < 8; round++ {
		run := h.waitingRun(h.starter, "s1")
		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			errs[0] = h.eng.Approve(ctx, h.approver, run.ID, run.ManifestID, "yes")
		}()
		go func() {
			defer wg.Done()
			<-start
			errs[1] = h.eng.Reject(ctx, h.approver, run.ID, "no")
		}()
		close(start)
		wg.Wait()

		winners, losers := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				winners++
			case errors.Is(err, store.ErrAlreadyDecided):
				losers++
			default:
				t.Fatalf("round %d: loser got %v, want store.ErrAlreadyDecided", round, err)
			}
		}
		if winners != 1 || losers != 1 {
			t.Fatalf("round %d: %d winners and %d losers (%v), want exactly one of each", round, winners, losers, errs)
		}
		if got := h.run(run.ID).State; got != "queued" && got != "rejected" {
			t.Fatalf("round %d: run is %s, want queued or rejected", round, got)
		}
		if h.approvalCount(run.ID) != 1 {
			t.Fatalf("round %d: %d approvals, want 1", round, h.approvalCount(run.ID))
		}
		a := h.latestApproval(run.ID)
		want := map[string]string{"queued": "approved", "rejected": "rejected"}[h.run(run.ID).State]
		if a.Decision != want {
			t.Fatalf("round %d: decision %q but run is %s", round, a.Decision, h.run(run.ID).State)
		}
	}
	// One decision and one audit entry per round, never two.
	if got := len(h.audits("run.approve")) + len(h.audits("run.reject")); got != 8 {
		t.Errorf("%d decision audit entries over 8 rounds, want 8", got)
	}
}

func TestDecisionAfterTheRunMovedOnIsAlreadyDecided(t *testing.T) {
	h := newHarness(t, harnessOptions{approvalFor: []string{testLabel}})
	ctx := context.Background()
	run := h.waitingRun(h.starter, "s1")
	if err := h.eng.Approve(ctx, h.approver, run.ID, run.ManifestID, ""); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.Approve(ctx, h.approver, run.ID, run.ManifestID, ""); !errors.Is(err, store.ErrAlreadyDecided) {
		t.Errorf("second Approve = %v, want store.ErrAlreadyDecided", err)
	}
	if err := h.eng.Reject(ctx, h.approver, run.ID, ""); !errors.Is(err, store.ErrAlreadyDecided) {
		t.Errorf("Reject after Approve = %v, want store.ErrAlreadyDecided", err)
	}
	// A run that never waited is a wrong state, not a lost race.
	planned := h.planned(h.starter, "s2")
	if err := h.eng.Approve(ctx, h.approver, planned.ID, "x", ""); !errors.Is(err, ErrWrongState) {
		t.Errorf("Approve of a planned run = %v, want ErrWrongState", err)
	}
}

func TestRejectMovesToRejected(t *testing.T) {
	h := newHarness(t, harnessOptions{approvalFor: []string{testLabel}})
	run := h.waitingRun(h.starter, "s1")
	if err := h.eng.Reject(context.Background(), h.approver, run.ID, "wrong version"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if got := h.expectState(run.ID, "rejected"); got.EndReason == "" {
		t.Errorf("rejected run has no end reason")
	}
	a := h.latestApproval(run.ID)
	if a.Decision != "rejected" || a.DecidedBy == nil || *a.DecidedBy != h.approver.UserID || a.Comment != "wrong version" {
		t.Errorf("approval = %+v, want rejected by the approver with the comment", a)
	}
	if err := h.eng.Reject(context.Background(), h.starter, run.ID, ""); !errors.Is(err, ErrForbidden) {
		t.Errorf("Reject by someone without the grant = %v", err)
	}
}

func TestExpiryMovesToExpired(t *testing.T) {
	h := newHarness(t, harnessOptions{approvalFor: []string{testLabel}, approvalExpiry: 2 * time.Hour})
	ctx := context.Background()
	run := h.waitingRun(h.starter, "s1")

	h.clock.Advance(119 * time.Minute)
	if err := h.eng.Expire(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	h.expectState(run.ID, "waiting_approval") // the approval row says 2h

	h.clock.Advance(2 * time.Minute)
	if err := h.eng.Expire(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if got := h.expectState(run.ID, "expired"); got.EndReason == "" {
		t.Errorf("expired run has no end reason")
	}
	a := h.latestApproval(run.ID)
	if a.Decision != "expired" || a.DecidedBy != nil || a.DecidedAt == nil {
		t.Errorf("approval = %+v, want decision expired, by nobody, with a time", a)
	}
	if err := h.eng.Approve(ctx, h.approver, run.ID, run.ManifestID, ""); !errors.Is(err, store.ErrAlreadyDecided) {
		t.Errorf("Approve after expiry = %v, want store.ErrAlreadyDecided", err)
	}
}

func TestLoopExpiresRunsFromTheApprovalRow(t *testing.T) {
	h := newHarness(t, harnessOptions{approvalFor: []string{testLabel}, approvalExpiry: time.Hour})
	run := h.waitingRun(h.starter, "s1")
	h.startLoop(nil, 20*time.Millisecond, nil)
	h.clock.Advance(30 * time.Minute)
	time.Sleep(200 * time.Millisecond)
	h.expectState(run.ID, "waiting_approval")
	h.clock.Advance(2 * time.Hour)
	h.waitRunState(run.ID, "expired")
	if a := h.latestApproval(run.ID); a.Decision != "expired" {
		t.Errorf("approval decision %q after the loop expired the run, want expired", a.Decision)
	}
}

func TestDecisionsAreAudited(t *testing.T) {
	h := newHarness(t, harnessOptions{approvalFor: []string{testLabel}})
	ctx := context.Background()
	approved := h.waitingRun(h.starter, "s1")
	rejected := h.waitingRun(h.starter, "s2")
	if err := h.eng.Approve(ctx, h.approver, approved.ID, approved.ManifestID, "ship it"); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.Reject(ctx, h.approver, rejected.ID, "not today"); err != nil {
		t.Fatal(err)
	}
	check := func(action string, run store.Run, comment string) {
		t.Helper()
		entries := h.audits(action)
		if len(entries) != 1 {
			t.Fatalf("%s: %d audit entries, want 1", action, len(entries))
		}
		var detail map[string]any
		if err := json.Unmarshal(entries[0].Detail, &detail); err != nil {
			t.Fatalf("%s detail: %v", action, err)
		}
		if detail["manifest_id"] != run.ManifestID || detail["comment"] != comment {
			t.Errorf("%s detail = %v, want manifest_id %q and comment %q", action, detail, run.ManifestID, comment)
		}
	}
	check("run.approve", approved, "ship it")
	check("run.reject", rejected, "not today")
}
