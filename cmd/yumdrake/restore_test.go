package main

import (
	"net/http"
	"strings"
	"testing"
)

func restoreManifest() map[string]any {
	m := manifestFixture(false)
	m["services"] = []map[string]any{
		{"name": "billing", "group": "backends", "version": "", "previous_task_definition": "arn:billing:41", "target_task_definition": "arn:billing:30",
			"config": []map[string]any{
				{"kind": "env", "name": "app.env", "change": "changed", "keys": map[string]any{"added": []string{}, "removed": []string{"NEW_FLAG"}, "changed": []string{}}},
				{"kind": "toml", "name": "limits.toml", "change": "unchanged", "not_restored": "config copy expired; only the task definition is restored"},
			}},
	}
	return m
}

// restoreFlow is a flow where run 12 can be restored into new run 14.
func restoreFlow(t *testing.T) (*harness, *flow) {
	t.Helper()
	h, f := newFlow(t)
	f.reply("POST /api/v1/runs/{id}/restore", 201, runDetailFixture(flowRunID, "resolving"))
	f.reply("POST /api/v1/runs/{id}/rerun-failed", 201, runDetailFixture(flowRunID, "resolving"))
	f.reply("GET /api/v1/runs/{id}/plan", 200, restoreManifest())
	return h, f
}

func TestRestorePrintsThePlanWithWarningsThenStartsOnYes(t *testing.T) {
	h, f := restoreFlow(t)
	h.stdinTerminal, h.stdin = true, "y\n"
	code, stdout, stderr := h.run("restore", "12", "--service", "billing")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	contains(t, "plan", stdout, "task definition arn:billing:41 -> arn:billing:30", "WARNING: limits.toml is not restored: config copy expired", "removed NEW_FLAG", "Run 14 started")
	contains(t, "prompt", stderr, "Start this run? [y/N]")
	posts := f.requests("POST /api/v1/runs/12/restore")
	if len(posts) != 1 || !strings.Contains(posts[0].Body, `"services":["billing"]`) {
		t.Errorf("restore requests = %+v", posts)
	}
	if !f.started() {
		t.Error("the restore run was not started")
	}
}

func TestRestoreOfEveryServiceSendsNoNames(t *testing.T) {
	h, f := restoreFlow(t)
	code, _, _ := h.run("restore", "12", "--yes")
	posts := f.requests("POST /api/v1/runs/12/restore")
	if code != 0 || len(posts) != 1 || !strings.Contains(posts[0].Body, `"services":[]`) {
		t.Errorf("exit %d requests %+v", code, posts)
	}
}

func TestRestoreChecksServiceNamesBeforeCreatingAnything(t *testing.T) {
	h, f := restoreFlow(t)
	code, _, stderr := h.run("restore", "12", "--service", "biling", "--yes")
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	contains(t, "stderr", stderr, `"biling"`, "billing", "orders")
	if n := len(f.requests("POST /api/v1/runs/12/restore")); n != 0 {
		t.Errorf("%d restore runs were created", n)
	}
}

func TestRestoreWithoutATerminalLeavesItPlanned(t *testing.T) {
	h, f := restoreFlow(t)
	code, _, stderr := h.run("restore", "12")
	if code != 5 || f.started() || !strings.Contains(stderr, "yumdrake runs start 14") {
		t.Errorf("exit %d started %v stderr %q", code, f.started(), stderr)
	}
}

func TestRestoreYesWatchEndsWithTheRunsExitCode(t *testing.T) {
	h, f := restoreFlow(t)
	f.stream(frame("end", "", stateBody("succeeded", "")))
	if code, stdout, _ := h.run("restore", "12", "--yes", "--watch"); code != 0 || !strings.Contains(stdout, "Run 14 finished: succeeded") {
		t.Errorf("exit %d stdout %q", code, stdout)
	}
}

func TestRestoreRefusedByTheServerShowsItsReason(t *testing.T) {
	h, f := restoreFlow(t)
	f.reply("POST /api/v1/runs/{id}/restore", 409, errBody("conflict", "run 12 changed nothing, so there is nothing to restore"))
	code, _, stderr := h.run("restore", "12", "--yes")
	if code != 2 || !strings.Contains(stderr, "nothing to restore") {
		t.Errorf("exit %d stderr %q", code, stderr)
	}
}

func TestRerunFailedPlansAndStartsTheSelection(t *testing.T) {
	h, f := restoreFlow(t)
	f.reply("GET /api/v1/runs/{id}/plan", 200, manifestFixture(false))
	code, stdout, stderr := h.run("runs", "rerun-failed", "12", "--yes")
	if code != 0 || !f.started() {
		t.Fatalf("exit %d started %v: %s", code, f.started(), stderr)
	}
	contains(t, "stdout", stdout, "billing", "Run 14 started")
	if n := len(f.requests("POST /api/v1/runs/12/rerun-failed")); n != 1 {
		t.Errorf("rerun requests = %d", n)
	}

	f.reply("POST /api/v1/runs/{id}/rerun-failed", 409, errBody("conflict", "run 12 has no failed, skipped or cancelled services"))
	code, _, stderr = h.run("runs", "rerun-failed", "12", "--yes")
	if code != 2 || !strings.Contains(stderr, "no failed") {
		t.Errorf("exit %d stderr %q", code, stderr)
	}
}

// ---- reconcile and check-again ----

func reconcileFlow(t *testing.T) (*harness, *flow) {
	t.Helper()
	h, f := newFlow(t)
	f.set("GET /api/v1/runs/{id}", func(w http.ResponseWriter, _ *http.Request) {
		d := runDetailFixture(flowRunID, "needs_reconciliation")
		d["legs"] = []map[string]any{
			{"id": 103, "step": "roll-backends", "kind": "step", "service": "billing", "version": "1.4.2", "state": "unknown", "attempt": 1, "failure_short": "outcome not known"},
			{"id": 104, "step": "roll-backends", "kind": "step", "service": "orders", "version": "1.4.2", "state": "succeeded", "attempt": 1},
		}
		writeTestJSON(w, 200, d)
	})
	f.reply("POST /api/v1/runs/{id}/legs/{leg}/reconcile", 200, runDetailFixture(flowRunID, "running"))
	f.reply("POST /api/v1/runs/{id}/legs/{leg}/check-again", 200, runDetailFixture(flowRunID, "needs_reconciliation"))
	return h, f
}

func TestReconcileValidatesBeforeAnythingIsSent(t *testing.T) {
	h, f := reconcileFlow(t)
	tests := []struct {
		name  string
		args  []string
		wants []string
	}{
		{"no leg", []string{"--outcome", "succeeded", "--yes"}, []string{"--leg"}},
		{"no outcome", []string{"--leg", "103", "--yes"}, []string{"--outcome"}},
		{"bad outcome", []string{"--leg", "103", "--outcome", "done", "--yes"}, []string{`"done"`, "succeeded", "failed"}},
		{"unknown leg", []string{"--leg", "999", "--outcome", "failed", "--yes"}, []string{"999", "103", "billing"}},
		{"leg that is not unknown", []string{"--leg", "104", "--outcome", "failed", "--yes"}, []string{"104", "succeeded", "103"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _, stderr := h.run(append([]string{"runs", "reconcile", "14"}, tt.args...)...)
			if code != 2 {
				t.Errorf("exit = %d, want 2", code)
			}
			contains(t, "stderr", stderr, tt.wants...)
		})
	}
	if n := len(f.requests("POST /api/v1/runs/14/legs/103/reconcile")) + len(f.requests("POST /api/v1/runs/14/legs/104/reconcile")); n != 0 {
		t.Errorf("%d reconcile calls were sent", n)
	}
}

func TestReconcileAsksForConfirmation(t *testing.T) {
	calls := func(f *flow) []recorded { return f.requests("POST /api/v1/runs/14/legs/103/reconcile") }

	h, f := reconcileFlow(t)
	h.stdinTerminal, h.stdin = true, "y\n"
	code, stdout, stderr := h.run("runs", "reconcile", "14", "--leg", "103", "--outcome", "succeeded")
	if code != 0 || len(calls(f)) != 1 || !strings.Contains(calls(f)[0].Body, `"outcome":"succeeded"`) {
		t.Errorf("answer y: exit %d calls %+v", code, calls(f))
	}
	contains(t, "prompt", stderr, "billing", "succeeded", "[y/N]")
	contains(t, "stdout", stdout, "Run 14")

	h, f = reconcileFlow(t)
	h.stdinTerminal, h.stdin = true, "n\n"
	if code, _, _ := h.run("runs", "reconcile", "14", "--leg", "103", "--outcome", "failed"); code != 1 || len(calls(f)) != 0 {
		t.Errorf("answer n: exit %d calls %d", code, len(calls(f)))
	}

	h, f = reconcileFlow(t)
	if code, _, stderr := h.run("runs", "reconcile", "14", "--leg", "103", "--outcome", "failed"); code != 2 || len(calls(f)) != 0 || !strings.Contains(stderr, "--yes") {
		t.Errorf("no terminal: exit %d calls %d stderr %q", code, len(calls(f)), stderr)
	}

	h, f = reconcileFlow(t)
	if code, _, _ := h.run("runs", "reconcile", "14", "--leg", "103", "--outcome", "failed", "--yes"); code != 0 || len(calls(f)) != 1 {
		t.Errorf("--yes: exit %d calls %d", code, len(calls(f)))
	}
}

func TestCheckAgainAsksTheServerToLookAgain(t *testing.T) {
	h, f := reconcileFlow(t)
	code, stdout, stderr := h.run("runs", "check-again", "14", "--leg", "103")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if n := len(f.requests("POST /api/v1/runs/14/legs/103/check-again")); n != 1 {
		t.Errorf("check-again calls = %d", n)
	}
	contains(t, "stdout", stdout, "Run 14")
	if code, _, stderr := h.run("runs", "check-again", "14"); code != 2 || !strings.Contains(stderr, "--leg") {
		t.Errorf("no leg: exit %d stderr %q", code, stderr)
	}
}
