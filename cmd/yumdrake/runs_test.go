package main

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestRunsListTextAndFilters(t *testing.T) {
	h, api := apiHarness(t)
	code, stdout, stderr := h.run("runs", "list", "--job", "roll-backends", "--env", "dev-01", "--state", "failed", "--state", "cancelled", "--limit", "5")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	contains(t, "runs list", stdout, "12", "failed", "roll-backends", "dev-01", "11", "succeeded")
	got := api.requests("GET /api/v1/runs")
	if len(got) != 1 {
		t.Fatalf("requests = %+v", got)
	}
	q, _ := url.ParseQuery(got[0].Query)
	if q.Get("job") != "3" || q.Get("environment") != "dev-01" || q.Get("limit") != "5" || strings.Join(q["state"], ",") != "failed,cancelled" {
		t.Errorf("query = %v (the job id must become the server id, the state must repeat)", q)
	}
}

func TestRunsListFollowsPagesUpToTheLimit(t *testing.T) {
	h, api := apiHarness(t)
	api.set("GET /api/v1/runs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("before") == "" {
			writeTestJSON(w, 200, map[string]any{"runs": []map[string]any{runFixture(12, "failed"), runFixture(11, "failed")}, "next_before": 11})
			return
		}
		writeTestJSON(w, 200, map[string]any{"runs": []map[string]any{runFixture(10, "failed")}, "next_before": nil})
	})
	_, stdout, _ := h.run("runs", "list", "--limit", "3", "-o", "json")
	var out struct {
		Runs []struct{ ID int } `json:"runs"`
	}
	decodeOne(t, stdout, &out)
	if len(out.Runs) != 3 {
		t.Errorf("runs = %+v, want 3", out.Runs)
	}
}

func TestRunsListRejectsAnUnknownState(t *testing.T) {
	h, api := apiHarness(t)
	code, _, stderr := h.run("runs", "list", "--state", "done")
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	contains(t, "stderr", stderr, `"done"`, "waiting_approval", "planned")
	if len(api.requests("GET /api/v1/runs")) != 0 {
		t.Error("the server was asked")
	}
}

func TestRunsShowTextHasFailuresInFull(t *testing.T) {
	h, api := apiHarness(t)
	detail := runDetailFixture(12, "partially_succeeded")
	detail["end_reason"] = "1 of 2 services failed"
	api.reply("GET /api/v1/runs/{id}", 200, detail)
	code, stdout, stderr := h.run("runs", "show", "12")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	contains(t, "runs show", stdout, "Run 12", "partially_succeeded", "1 of 2 services failed", "roll-backends", "billing", "orders",
		"health checks failed", "task billing-7 stopped: essential container exited with code 1", "dev-01")

	waiting := runDetailFixture(13, "waiting_approval")
	waiting["wait_reason"] = "waiting for approval"
	api.reply("GET /api/v1/runs/{id}", 200, waiting)
	_, stdout, _ = h.run("runs", "show", "13")
	contains(t, "waiting", stdout, "waiting for approval")
}

func TestRunsShowJSONAndMissingRun(t *testing.T) {
	h, api := apiHarness(t)
	api.reply("GET /api/v1/runs/{id}", 200, runDetailFixture(12, "failed"))
	code, stdout, _ := h.run("runs", "show", "12", "-o", "json")
	var out map[string]any
	decodeOne(t, stdout, &out)
	if code != 0 || out["state"] != "failed" {
		t.Errorf("exit %d out %v", code, out)
	}
	api.reply("GET /api/v1/runs/{id}", 404, errBody("not_found", "run not found"))
	code, _, stderr := h.run("runs", "show", "99")
	if code != 2 || !strings.Contains(stderr, "run not found") {
		t.Errorf("exit %d stderr %q", code, stderr)
	}
	if code, _, _ := h.run("runs", "show", "abc"); code != 2 {
		t.Errorf("non-numeric run: exit %d", code)
	}
}

func TestRunsLogFollowsTheCursorToTheEndAndReportsGaps(t *testing.T) {
	h, api := apiHarness(t)
	api.reply("GET /api/v1/runs/{id}", 200, runDetailFixture(12, "failed"))
	line := func(leg, seq int, level, text string) map[string]any {
		return map[string]any{"leg_id": leg, "seq": seq, "at": "2026-11-27T10:01:02Z", "level": level, "text": text}
	}
	api.set("GET /api/v1/runs/{id}/log", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("after") {
		case "":
			writeTestJSON(w, 200, map[string]any{"lines": []any{line(101, 1, "info", "deploying billing"), line(102, 1, "ok", "orders healthy")},
				"next": "c1", "gaps": []any{}})
		case "c1":
			writeTestJSON(w, 200, map[string]any{"lines": []any{line(101, 5, "error", "health checks failed")},
				"next": "c2", "gaps": []any{map[string]any{"leg_id": 101, "from_seq": 2, "to_seq": 4}}})
		default:
			writeTestJSON(w, 200, map[string]any{"lines": []any{}, "next": "c2", "gaps": []any{}})
		}
	})
	code, stdout, stderr := h.run("runs", "log", "12")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	contains(t, "log", stdout, "deploying billing", "orders healthy", "health checks failed", "billing", "error")
	contains(t, "stderr", stderr, "billing", "lines 2 to 4")
	if strings.Contains(stdout, "lines 2 to 4") {
		t.Error("the gap notice is on stdout")
	}
	if got := api.requests("GET /api/v1/runs/12/log"); len(got) != 3 {
		t.Errorf("log requests = %d, want 3 (two pages and the empty one)", len(got))
	}
}

func TestRunsLogPassesFiltersAndJSONIsOneDocument(t *testing.T) {
	h, api := apiHarness(t)
	api.reply("GET /api/v1/runs/{id}", 200, runDetailFixture(12, "failed"))
	api.reply("GET /api/v1/runs/{id}/log", 200, map[string]any{"lines": []any{}, "next": "", "gaps": []any{}})
	code, stdout, _ := h.run("runs", "log", "12", "--step", "roll-backends", "--service", "billing", "--level", "error", "--grep", "health", "-o", "json")
	var out map[string]any
	decodeOne(t, stdout, &out)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	got := api.requests("GET /api/v1/runs/12/log")
	if len(got) == 0 {
		t.Fatal("no log request")
	}
	q, _ := url.ParseQuery(got[0].Query)
	if q.Get("step") != "roll-backends" || q.Get("service") != "billing" || q.Get("level") != "error" || q.Get("q") != "health" {
		t.Errorf("query = %v", q)
	}
}

func TestApprovalsListShowsWhatCanBeApproved(t *testing.T) {
	h, api := apiHarness(t)
	api.reply("GET /api/v1/approvals", 200, map[string]any{"approvals": []map[string]any{
		{"run_id": 20, "job_id": "roll-backends", "environment": "prod-01", "requested_by_name": "Ada", "requested_at": "2026-11-27T09:00:00Z",
			"expires_at": "2026-11-28T09:00:00Z", "can_approve": true, "reason": ""},
		{"run_id": 21, "job_id": "roll-frontends", "environment": "prod-01", "requested_by_name": "You", "requested_at": "2026-11-27T09:30:00Z",
			"expires_at": "2026-11-28T09:30:00Z", "can_approve": false, "reason": "you started this run"},
	}})
	code, stdout, stderr := h.run("approvals", "list")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	contains(t, "approvals", stdout, "20", "roll-backends", "prod-01", "Ada", "yes", "21", "you started this run")
	_, stdout, _ = h.run("approvals", "list", "-o", "json")
	var out map[string]any
	decodeOne(t, stdout, &out)

	api.reply("GET /api/v1/approvals", 200, map[string]any{"approvals": []any{}})
	_, stdout, _ = h.run("approvals", "list")
	contains(t, "empty", stdout, "No runs are waiting for approval")
}
