package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestJobsListText(t *testing.T) {
	h, _ := apiHarness(t)
	code, stdout, stderr := h.run("jobs", "list")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	contains(t, "jobs list", stdout, "roll-backends", "Roll backends", "Platform", "prod", "roll-frontends", "Web", "#12 failed", "nightly-cleanup")
}

func TestJobsListJSONIsOneDocumentAndFolderFilters(t *testing.T) {
	h, _ := apiHarness(t)
	code, stdout, stderr := h.run("jobs", "list", "--folder", "Web", "-o", "json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var out struct {
		Jobs []struct {
			JobID string `json:"job_id"`
		} `json:"jobs"`
	}
	decodeOne(t, stdout, &out)
	if len(out.Jobs) != 1 || out.Jobs[0].JobID != "roll-frontends" {
		t.Errorf("jobs = %+v", out.Jobs)
	}
}

func TestJobsListUnknownFolderNamesTheValidOnes(t *testing.T) {
	h, _ := apiHarness(t)
	code, _, stderr := h.run("jobs", "list", "--folder", "Platfom")
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	contains(t, "stderr", stderr, "Platfom", "Platform", "Web")
}

func TestJobsShowDescribesGroupsStepsAndProblems(t *testing.T) {
	h, api := apiHarness(t)
	code, stdout, stderr := h.run("jobs", "show", "roll-backends")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	contains(t, "jobs show", stdout, "backends", "billing", "orders", "workers", "mailer",
		"roll-backends", "needs copy-backends (per service)", "needs roll-backends (whole step)")
	if got := api.requests("GET /api/v1/jobs/3"); len(got) != 1 {
		t.Errorf("job detail requests = %d, want 1 (the id resolves to server id 3)", len(got))
	}

	detail := jobDetailFixture()
	detail["problems"] = []map[string]any{{"file": "jobs/roll.yaml", "line": 4, "message": "unknown group"}}
	api.reply("GET /api/v1/jobs/{id}", 200, detail)
	_, stdout, _ = h.run("jobs", "show", "roll-backends")
	contains(t, "problems", stdout, "jobs/roll.yaml:4: unknown group")
}

func TestJobsShowJSON(t *testing.T) {
	h, _ := apiHarness(t)
	code, stdout, _ := h.run("jobs", "show", "roll-backends", "-o", "json")
	var out map[string]any
	decodeOne(t, stdout, &out)
	if code != 0 || out["job_id"] != "roll-backends" {
		t.Errorf("exit %d out %v", code, out)
	}
}

func TestUnknownJobListsNearbyIds(t *testing.T) {
	h, api := apiHarness(t)
	code, _, stderr := h.run("jobs", "show", "roll-backend")
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	contains(t, "stderr", stderr, `"roll-backend"`, "roll-backends")
	if strings.Contains(stderr, "nightly-cleanup") {
		t.Errorf("an unrelated job is listed as nearby: %s", stderr)
	}
	if got := api.requests("GET /api/v1/jobs/3"); len(got) != 0 {
		t.Errorf("job detail was fetched for an unknown id")
	}
}

func versionsFixture() map[string]any {
	return map[string]any{"groups": map[string]any{
		"backends": map[string]any{
			"common": []string{"1.4.2", "1.4.1"},
			"services": map[string]any{
				"billing": []map[string]any{{"tag": "1.4.2", "digest": "sha256:aaaaaaaaaaaaaaaaaaaa", "pushed_at": "2026-11-26T09:00:00Z"}, {"tag": "1.4.1", "digest": "sha256:bbbbbbbbbbbbbbbbbbbb", "pushed_at": "2026-11-20T09:00:00Z"}},
				"orders":  []map[string]any{{"tag": "1.4.2", "digest": "sha256:cccccccccccccccccccc", "pushed_at": "2026-11-26T09:10:00Z"}, {"tag": "1.4.1", "digest": "sha256:dddddddddddddddddddd", "pushed_at": "2026-11-20T09:10:00Z"}, {"tag": "1.3.9", "digest": "sha256:eeeeeeeeeeeeeeeeeeee", "pushed_at": "2026-11-10T09:10:00Z"}},
			},
			"unavailable": map[string]string{},
		},
		"workers": map[string]any{
			"common":      []string{},
			"services":    map[string]any{},
			"unavailable": map[string]string{"mailer": "AccessDenied"},
		},
	}}
}

func TestVersionsListsCommonTagsPerGroup(t *testing.T) {
	h, api := apiHarness(t)
	api.set("GET /api/v1/jobs/{id}/versions", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, 200, versionsFixture())
	})
	code, stdout, stderr := h.run("versions", "roll-backends", "--env", "dev-01")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	contains(t, "versions", stdout, "backends", "1.4.2, 1.4.1", "workers", "mailer: AccessDenied")
	if got := api.requests("GET /api/v1/jobs/3/versions"); len(got) != 1 || got[0].Query != "environment=dev-01" {
		t.Errorf("requests = %+v", got)
	}
}

func TestVersionsForOneServiceAndOneGroup(t *testing.T) {
	h, api := apiHarness(t)
	api.reply("GET /api/v1/jobs/{id}/versions", 200, versionsFixture())
	code, stdout, _ := h.run("versions", "roll-backends", "--env", "dev-01", "--service", "orders")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	contains(t, "service versions", stdout, "1.4.2", "1.4.1", "1.3.9", "sha256:cccccccccccc")
	if strings.Contains(stdout, "billing") {
		t.Errorf("another service is shown: %s", stdout)
	}
	// Newest first: the server's order is kept.
	if strings.Index(stdout, "1.4.2") > strings.Index(stdout, "1.3.9") {
		t.Errorf("not newest first: %s", stdout)
	}
	code, stdout, _ = h.run("versions", "roll-backends", "--env", "dev-01", "--group", "backends")
	contains(t, "group versions", stdout, "1.4.2, 1.4.1", "billing", "orders")
	if code != 0 || strings.Contains(stdout, "mailer") {
		t.Errorf("exit %d, another group is shown: %s", code, stdout)
	}
}

func TestVersionsNeedsAnEnvironmentAndKnownNames(t *testing.T) {
	h, api := apiHarness(t)
	api.reply("GET /api/v1/jobs/{id}/versions", 200, versionsFixture())
	if code, _, stderr := h.run("versions", "roll-backends"); code != 2 || !strings.Contains(stderr, "--env") {
		t.Errorf("no env: exit %d stderr %q", code, stderr)
	}
	code, _, stderr := h.run("versions", "roll-backends", "--env", "dev-01", "--service", "biling")
	if code != 2 {
		t.Errorf("unknown service: exit %d", code)
	}
	contains(t, "stderr", stderr, `"biling"`, "billing", "orders", "mailer")
	code, _, stderr = h.run("versions", "roll-backends", "--env", "dev-01", "--group", "backend")
	if code != 2 {
		t.Errorf("unknown group: exit %d", code)
	}
	contains(t, "stderr", stderr, `"backend"`, "backends", "workers")
}

func TestVersionsOnAServerWithoutTheRoute(t *testing.T) {
	h, api := apiHarness(t)
	api.reply("GET /api/v1/jobs/{id}/versions", 404, errBody("not_found", "not found"))
	code, _, stderr := h.run("versions", "roll-backends", "--env", "dev-01")
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	contains(t, "stderr", stderr, "does not support")
}

func runningFixture() map[string]any {
	return map[string]any{
		"environments": []map[string]any{{"name": "dev-01", "label": "dev"}, {"name": "prod-01", "label": "prod"}},
		"rows": []map[string]any{
			{"service": "billing", "cells": []map[string]any{
				{"state": "ok", "version": "1.4.2", "task_definition": "billing:41", "by_yumdrake": true, "run_id": 12, "behind": false},
				{"state": "ok", "version": "1.3.9", "task_definition": "billing:30", "by_yumdrake": false, "behind": true},
			}},
			{"service": "orders", "cells": []map[string]any{
				{"state": "absent"},
				{"state": "error", "error": "AccessDenied"},
			}},
		},
	}
}

func TestVersionsRunningShowsTheMatrixAndSlices(t *testing.T) {
	h, api := apiHarness(t)
	api.reply("GET /api/v1/versions/running", 200, runningFixture())
	code, stdout, stderr := h.run("versions", "running")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	contains(t, "matrix", stdout, "dev-01", "prod-01", "billing", "1.4.2", "1.3.9 (behind, not by yumdrake)", "orders", "AccessDenied", "-")

	_, stdout, _ = h.run("versions", "running", "--service", "billing", "--env", "prod-01")
	contains(t, "slice", stdout, "billing", "1.3.9")
	for _, unwanted := range []string{"orders", "dev-01", "1.4.2"} {
		if strings.Contains(stdout, unwanted) {
			t.Errorf("slice shows %q: %s", unwanted, stdout)
		}
	}
	code, stdout, _ = h.run("versions", "running", "-o", "json")
	var out map[string]any
	decodeOne(t, stdout, &out)
	if code != 0 || out["rows"] == nil {
		t.Errorf("json: exit %d %v", code, out)
	}
}

func TestVersionsRunningWithoutServerSupportSaysSo(t *testing.T) {
	h, api := apiHarness(t)
	api.reply("GET /api/v1/versions/running", 404, errBody("not_found", "not found"))
	code, _, stderr := h.run("versions", "running")
	if code != 2 || !strings.Contains(stderr, "does not support") {
		t.Errorf("exit %d stderr %q", code, stderr)
	}
}

func TestReadingCommandsHaveHelpWithAnExample(t *testing.T) {
	h, _ := apiHarness(t)
	for _, args := range [][]string{
		{"jobs", "list"}, {"jobs", "show"}, {"versions"}, {"versions", "running"},
		{"runs", "list"}, {"runs", "show"}, {"runs", "log"}, {"approvals", "list"},
	} {
		code, stdout, _ := h.run(append(args, "-h")...)
		if code != 0 || !strings.Contains(stdout, "Example:") || !strings.Contains(stdout, "yumdrake") {
			t.Errorf("%v -h: exit %d stdout %q", args, code, stdout)
		}
	}
}

func TestReadingCommandsNeedSignIn(t *testing.T) {
	h := newHarness(t)
	h.seed(t, cliConfig("http://127.0.0.1:1", ""))
	for _, args := range [][]string{{"jobs", "list"}, {"runs", "list"}, {"approvals", "list"}, {"runs", "show", "1"}} {
		code, _, stderr := h.run(args...)
		if code != 6 || !strings.Contains(stderr, "Not signed in. Run yumdrake login.") {
			t.Errorf("%v: exit %d stderr %q", args, code, stderr)
		}
	}
}

func ambiguousJobs(api *fakeAPI) {
	api.reply("GET /api/v1/jobs", 200, map[string]any{"jobs": []map[string]any{
		{"id": 3, "job_id": "roll", "path": "backend/roll.yaml", "folder": "Platform", "runnable": true, "approval_labels": []string{}},
		{"id": 4, "job_id": "roll", "path": "web/roll.yaml", "folder": "Web", "runnable": true, "approval_labels": []string{}},
		{"id": 5, "job_id": "cleanup", "path": "ops/roll.yaml", "folder": "Platform", "runnable": true, "approval_labels": []string{}},
	}})
}

func TestJobLookupAmbiguousListsMatches(t *testing.T) {
	h, api := apiHarness(t)
	ambiguousJobs(api)
	code, _, stderr := h.run("jobs", "show", "roll")
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	contains(t, "stderr", stderr, "more than one job", "backend/roll.yaml", "web/roll.yaml")
	if got := api.requests("GET /api/v1/jobs/3"); len(got) != 0 {
		t.Errorf("a job was picked: %v", got)
	}
	if got := api.requests("GET /api/v1/jobs/4"); len(got) != 0 {
		t.Errorf("a job was picked: %v", got)
	}
}

func TestJobLookupExactPathWins(t *testing.T) {
	h, api := apiHarness(t)
	ambiguousJobs(api)
	api.reply("GET /api/v1/jobs/{id}", 200, jobDetailFixture())
	code, _, stderr := h.run("jobs", "show", "web/roll.yaml")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if got := api.requests("GET /api/v1/jobs/4"); len(got) != 1 {
		t.Errorf("job 4 was fetched %d times, want 1", len(got))
	}
}
