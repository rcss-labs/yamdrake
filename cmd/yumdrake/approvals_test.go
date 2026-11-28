package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// waitingFlow is a flow whose run 14 waits for approval.
func waitingFlow(t *testing.T) (*harness, *flow) {
	t.Helper()
	h, f := newFlow(t)
	f.runState = "waiting_approval"
	return h, f
}

func approveCalls(f *flow) []recorded { return f.requests("POST /api/v1/runs/14/approve") }

func TestApproveShowsThePlanAndSendsTheManifestIDItShowed(t *testing.T) {
	h, f := waitingFlow(t)
	plan := manifestFixture(false)
	plan["id"] = "zzz999" // not the id the run detail carries: the one shown is the one sent
	f.reply("GET /api/v1/runs/{id}/plan", 200, plan)
	f.reply("POST /api/v1/runs/{id}/approve", 200, runDetailFixture(flowRunID, "queued"))
	h.stdinTerminal, h.stdin = true, "y\n"
	code, stdout, stderr := h.run("approvals", "approve", "14", "--comment", "looks good")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	contains(t, "plan shown", stdout, "billing", "1.3.9 -> 1.4.2", "zzz999")
	contains(t, "prompt", stderr, "Approve this plan? [y/N]")
	contains(t, "result", stdout, "Approved run 14")
	calls := approveCalls(f)
	if len(calls) != 1 {
		t.Fatalf("approve calls = %d", len(calls))
	}
	var body struct{ ManifestID, Comment string }
	_ = json.Unmarshal([]byte(strings.NewReplacer("manifest_id", "ManifestID", "comment", "Comment").Replace(calls[0].Body)), &body)
	if body.ManifestID != "zzz999" || body.Comment != "looks good" {
		t.Errorf("body = %s", calls[0].Body)
	}
}

func TestApproveReportsThatThePlanChanged(t *testing.T) {
	h, f := waitingFlow(t)
	f.reply("POST /api/v1/runs/{id}/approve", 409, errBody("conflict", "The plan changed. Reload and review it again."))
	code, _, stderr := h.run("approvals", "approve", "14", "--yes")
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	contains(t, "stderr", stderr, "The plan changed", "yumdrake approvals approve 14")
}

func TestApproveAsksAndRespectsNo(t *testing.T) {
	h, f := waitingFlow(t)
	h.stdinTerminal, h.stdin = true, "n\n"
	code, _, _ := h.run("approvals", "approve", "14")
	if code != 1 || len(approveCalls(f)) != 0 {
		t.Errorf("answer n: exit %d, approve calls %d", code, len(approveCalls(f)))
	}
}

func TestApproveWithoutATerminalNeedsYes(t *testing.T) {
	h, f := waitingFlow(t)
	f.reply("POST /api/v1/runs/{id}/approve", 200, runDetailFixture(flowRunID, "queued"))
	code, _, stderr := h.run("approvals", "approve", "14")
	if code != 2 || len(approveCalls(f)) != 0 || !strings.Contains(stderr, "--yes") {
		t.Errorf("exit %d calls %d stderr %q", code, len(approveCalls(f)), stderr)
	}
	if code, _, _ := h.run("approvals", "approve", "14", "--yes"); code != 0 || len(approveCalls(f)) != 1 {
		t.Errorf("--yes: exit %d calls %d", code, len(approveCalls(f)))
	}
}

func TestApproveRefusesARunThatIsNotWaiting(t *testing.T) {
	h, f := newFlow(t) // planned
	code, _, stderr := h.run("approvals", "approve", "14", "--yes")
	if code != 2 || len(approveCalls(f)) != 0 || !strings.Contains(stderr, "not waiting for approval") {
		t.Errorf("exit %d calls %d stderr %q", code, len(approveCalls(f)), stderr)
	}
}

func TestRejectSendsTheComment(t *testing.T) {
	h, f := waitingFlow(t)
	f.reply("POST /api/v1/runs/{id}/reject", 200, runDetailFixture(flowRunID, "rejected"))
	code, stdout, stderr := h.run("approvals", "reject", "14", "--comment", "not today")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	contains(t, "stdout", stdout, "Rejected run 14")
	calls := f.requests("POST /api/v1/runs/14/reject")
	if len(calls) != 1 || !strings.Contains(calls[0].Body, `"comment":"not today"`) {
		t.Errorf("calls = %+v", calls)
	}
}

func TestApprovalHelpHasAnExample(t *testing.T) {
	h, _ := newFlow(t)
	for _, args := range [][]string{{"approvals", "approve"}, {"approvals", "reject"}, {"restore"}, {"runs", "rerun-failed"}, {"runs", "reconcile"}, {"runs", "check-again"}} {
		code, stdout, _ := h.run(append(args, "-h")...)
		if code != 0 || !strings.Contains(stdout, "Example:") {
			t.Errorf("%v -h: exit %d stdout %q", args, code, stdout)
		}
	}
}
