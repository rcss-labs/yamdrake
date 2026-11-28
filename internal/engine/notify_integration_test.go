//go:build integration

package engine

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testSlackChannel = "#deployments"

type queuedMessage struct {
	Kind, Channel, Text string
}

// notifications lists the run's queued messages in the order they were queued.
func (h *harness) notifications(runID int64) []queuedMessage {
	h.t.Helper()
	rows, err := h.pool.Query(context.Background(),
		`SELECT kind, channel, payload FROM notifications WHERE run_id = $1 ORDER BY id`, runID)
	if err != nil {
		h.t.Fatalf("query notifications: %v", err)
	}
	defer rows.Close()
	var out []queuedMessage
	for rows.Next() {
		var m queuedMessage
		var payload []byte
		if err := rows.Scan(&m.Kind, &m.Channel, &payload); err != nil {
			h.t.Fatalf("scan notification: %v", err)
		}
		var body struct{ Text string }
		if err := json.Unmarshal(payload, &body); err != nil {
			h.t.Fatalf("payload %q: %v", payload, err)
		}
		m.Text = body.Text
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		h.t.Fatalf("notifications: %v", err)
	}
	return out
}

func kindsOf(messages []queuedMessage) []string {
	kinds := []string{}
	for _, m := range messages {
		kinds = append(kinds, m.Kind)
	}
	return kinds
}

func (h *harness) expectKinds(runID int64, want ...string) []queuedMessage {
	h.t.Helper()
	got := h.notifications(runID)
	if len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(kindsOf(got), want)) {
		h.t.Fatalf("notification kinds = %v, want %v", kindsOf(got), want)
	}
	return got
}

func TestEachTransitionEnqueuesItsNotificationOnce(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, harnessOptions{approvalFor: []string{testLabel}, slackChannel: testSlackChannel})
	run := h.waitingRun(h.starter, "s1")
	requested := h.expectKinds(run.ID, "approval_requested")
	if requested[0].Channel != testSlackChannel {
		t.Errorf("channel = %q, want %q", requested[0].Channel, testSlackChannel)
	}
	for _, want := range []string{`starter wants to run "Release" in staging`, "Review and approve:", "https://yum.example/runs/"} {
		if !strings.Contains(requested[0].Text, want) {
			t.Errorf("approval_requested text %q lacks %q", requested[0].Text, want)
		}
	}

	if err := h.eng.Approve(ctx, h.approver, run.ID, run.ManifestID, ""); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	approved := h.expectKinds(run.ID, "approval_requested", "approved")
	if !strings.Contains(approved[1].Text, "Approved by approver") {
		t.Errorf("approved text = %q, want the approver's name", approved[1].Text)
	}

	h.advance(run.ID)
	h.expectState(run.ID, "running")
	h.expectKinds(run.ID, "approval_requested", "approved", "started")

	h.pastRevalidation(run.ID)
	leg := h.stepLeg(run.ID, "config", "s1")
	h.setLeg(leg.ID, "failed")
	if _, err := h.pool.Exec(ctx, `UPDATE legs SET failure_short = 'access denied', failure_detail = 'SECRET-DETAIL arn:aws:s3:::cfg' WHERE id = $1`, leg.ID); err != nil {
		t.Fatal(err)
	}
	h.advance(run.ID) // skips the deploy leg that needed the failed config
	h.advance(run.ID) // everything settled: the run ends
	h.expectState(run.ID, "failed")
	finished := h.expectKinds(run.ID, "approval_requested", "approved", "started", "finished")
	if !strings.Contains(finished[3].Text, "Failed: s1 (access denied)") {
		t.Errorf("finished text %q does not list the failed service with its short reason", finished[3].Text)
	}
	if strings.Contains(finished[3].Text, "SECRET-DETAIL") {
		t.Errorf("finished text %q leaks the failure detail", finished[3].Text)
	}
	if !strings.Contains(finished[3].Text, "0 of 1 services updated") {
		t.Errorf("finished text %q lacks the service counts", finished[3].Text)
	}

	h.advance(run.ID)
	h.advance(run.ID)
	h.expectKinds(run.ID, "approval_requested", "approved", "started", "finished")
}

func TestRejectedRunGivesRequestedThenRejected(t *testing.T) {
	h := newHarness(t, harnessOptions{approvalFor: []string{testLabel}, slackChannel: testSlackChannel})
	run := h.waitingRun(h.starter, "s1")
	if err := h.eng.Reject(context.Background(), h.approver, run.ID, "no"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	got := h.expectKinds(run.ID, "approval_requested", "rejected")
	if !strings.Contains(got[1].Text, "Rejected by approver") {
		t.Errorf("rejected text = %q, want the approver's name", got[1].Text)
	}
}

func TestExpiredApprovalEnqueuesApprovalExpired(t *testing.T) {
	h := newHarness(t, harnessOptions{approvalFor: []string{testLabel}, approvalExpiry: time.Hour, slackChannel: testSlackChannel})
	run := h.waitingRun(h.starter, "s1")
	h.clock.Advance(2 * time.Hour)
	if err := h.eng.Expire(context.Background(), run.ID); err != nil {
		t.Fatalf("Expire: %v", err)
	}
	h.expectState(run.ID, "expired")
	h.expectKinds(run.ID, "approval_requested", "approval_expired")
}

func TestJobWithoutSlackChannelEnqueuesNothing(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, harnessOptions{approvalFor: []string{testLabel}})
	run := h.waitingRun(h.starter, "s1")
	if err := h.eng.Approve(ctx, h.approver, run.ID, run.ManifestID, ""); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	h.advance(run.ID)
	h.expectState(run.ID, "running")
	h.expectKinds(run.ID)
}

// A transaction that fails after the notification was queued leaves no
// notification: the failure is forced by a trigger that refuses run-level log
// lines for the rest of the transition.
func TestRolledBackTransitionEnqueuesNothing(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, harnessOptions{approvalFor: []string{testLabel}, slackChannel: testSlackChannel})
	run := h.waitingRun(h.starter, "s1")
	h.expectKinds(run.ID, "approval_requested")

	for _, stmt := range []string{
		`CREATE FUNCTION refuse_lines() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced failure'; END $$`,
		`CREATE TRIGGER refuse_lines BEFORE INSERT ON log_lines FOR EACH ROW EXECUTE FUNCTION refuse_lines()`,
	} {
		if _, err := h.pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := h.eng.Reject(ctx, h.approver, run.ID, "no"); err == nil {
		t.Fatal("Reject succeeded although its transaction was made to fail")
	}
	h.expectState(run.ID, "waiting_approval")
	h.expectKinds(run.ID, "approval_requested") // no "rejected"

	if _, err := h.pool.Exec(ctx, `DROP TRIGGER refuse_lines ON log_lines`); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.Reject(ctx, h.approver, run.ID, "no"); err != nil {
		t.Fatalf("Reject after the fault was removed: %v", err)
	}
	h.expectKinds(run.ID, "approval_requested", "rejected")
}

// A run that stops in needs_reconciliation is announced once; settling it
// later must not announce a second finish.
func TestSettledReconciliationDoesNotAnnounceFinishTwice(t *testing.T) {
	h := newHarness(t, harnessOptions{slackChannel: testSlackChannel})
	run, unknown := unknownRun(t, h)
	h.expectKinds(run.ID, "started", "finished")

	if err := h.eng.Reconcile(context.Background(), h.starter, run.ID, unknown.ID, "succeeded"); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	h.expectState(run.ID, "partially_succeeded")
	h.expectKinds(run.ID, "started", "finished")
}
