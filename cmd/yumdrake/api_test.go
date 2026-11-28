package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/rcss-labs/yumdrake/internal/cli"
)

// recorded is one request the fake server saw.
type recorded struct {
	Method, Path, Query, Auth, Body string
}

// fakeAPI is a stand-in for the /api/v1 routes the commands call. Tests set
// the answer of a route with set; every request is recorded.
type fakeAPI struct {
	*httptest.Server
	mu       sync.Mutex
	handlers map[string]http.HandlerFunc
	mux      *http.ServeMux
	reqs     []recorded
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{handlers: map[string]http.HandlerFunc{}, mux: http.NewServeMux()}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, recorded{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), string(body)})
		f.mu.Unlock()
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		f.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

// set registers (or replaces) the handler of a route such as "GET /api/v1/jobs".
func (f *fakeAPI) set(pattern string, h http.HandlerFunc) {
	f.mu.Lock()
	_, exists := f.handlers[pattern]
	f.handlers[pattern] = h
	f.mu.Unlock()
	if !exists {
		f.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			handler := f.handlers[pattern]
			f.mu.Unlock()
			handler(w, r)
		})
	}
}

// reply registers a route that always answers status with v.
func (f *fakeAPI) reply(pattern string, status int, v any) {
	f.set(pattern, func(w http.ResponseWriter, _ *http.Request) { writeTestJSON(w, status, v) })
}

// requests returns the requests whose "METHOD path" equals route.
func (f *fakeAPI) requests(route string) []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recorded
	for _, r := range f.reqs {
		if r.Method+" "+r.Path == route {
			out = append(out, r)
		}
	}
	return out
}

// apiHarness is a harness signed in to a fake server that knows the fixtures.
func apiHarness(t *testing.T) (*harness, *fakeAPI) {
	t.Helper()
	api := newFakeAPI(t)
	h := newHarness(t)
	h.seed(t, cli.Config{Server: api.URL, Token: testToken})
	loadFixtures(api)
	return h, api
}

// ---- fixtures ----

func jobFixtures() []map[string]any {
	return []map[string]any{
		{"id": 3, "job_id": "roll-backends", "title": "Roll backends", "folder_id": 1, "folder": "Platform", "runnable": true,
			"services": 2, "approval_labels": []string{"prod"}, "problem_count": 0},
		{"id": 4, "job_id": "roll-frontends", "title": "Roll frontends", "folder_id": 2, "folder": "Web", "runnable": true,
			"services": 1, "approval_labels": []string{}, "problem_count": 0},
		{"id": 5, "job_id": "nightly-cleanup", "title": "Nightly cleanup", "folder_id": 1, "folder": "Platform", "runnable": false,
			"services": 0, "approval_labels": []string{}, "problem_count": 2},
	}
}

func jobDetailFixture() map[string]any {
	detail := jobFixtures()[0]
	detail["definition"] = map[string]any{
		"id": "roll-backends", "title": "Roll backends", "labels": []string{"dev", "prod"},
		"groups": []map[string]any{
			{"id": "backends", "label": "Backends", "services": []string{"billing", "orders"}},
			{"id": "workers", "label": "Workers", "services": []string{"mailer"}},
		},
		"approval": map[string]any{"labels": []string{"prod"}, "expires": 86400000000000},
		"steps": []map[string]any{
			{"name": "copy-backends", "use": "copy-config", "group": "backends", "needs": []any{}},
			{"name": "roll-backends", "use": "ecs-deploy", "group": "backends",
				"needs": []map[string]any{{"step": "copy-backends", "scope": "service"}}},
			{"name": "roll-workers", "use": "ecs-deploy", "group": "workers",
				"needs": []map[string]any{{"step": "roll-backends", "scope": "step"}}},
		},
	}
	detail["source"] = "id: roll-backends\n"
	detail["problems"] = []any{}
	return detail
}

func runFixture(id int, state string) map[string]any {
	return map[string]any{
		"id": id, "kind": "deploy", "job_source_id": 3, "job_id": "roll-backends", "environment": "dev-01", "label": "dev",
		"state": state, "wait_reason": "", "end_reason": "", "manifest_id": "abc123", "requested_by": 7, "via": "cli",
		"created_at": "2026-11-27T10:00:00Z", "state_changed_at": "2026-11-27T10:05:00Z",
		"started_at": "2026-11-27T10:01:00Z", "finished_at": nil,
	}
}

func runDetailFixture(id int, state string) map[string]any {
	d := runFixture(id, state)
	d["aws_account"] = "111111111111"
	d["approval_required"] = false
	d["auto_start"] = false
	d["cancel_requested"] = false
	d["locks"] = []string{}
	d["steps"] = []map[string]any{
		{"id": 1, "name": "copy-backends", "use": "copy-config", "group": "backends", "position": 0, "max_parallel": 0},
		{"id": 2, "name": "roll-backends", "use": "ecs-deploy", "group": "backends", "position": 1, "max_parallel": 0},
	}
	d["legs"] = []map[string]any{
		{"id": 100, "step_id": nil, "step": "", "kind": "resolve", "service": "", "version": "", "state": "succeeded", "attempt": 1},
		{"id": 101, "step_id": 2, "step": "roll-backends", "kind": "step", "service": "billing", "version": "1.4.2", "state": "failed",
			"attempt": 1, "failure_short": "health checks failed", "failure_detail": "task billing-7 stopped: essential container exited with code 1"},
		{"id": 102, "step_id": 2, "step": "roll-backends", "kind": "step", "service": "orders", "version": "1.4.2", "state": "succeeded", "attempt": 1},
	}
	d["summary"] = map[string]any{
		"counts": map[string]int{"succeeded": 2, "failed": 1},
		"failures": []map[string]any{
			{"leg_id": 101, "service": "billing", "step": "roll-backends", "kind": "step", "state": "failed",
				"short": "health checks failed", "detail": "task billing-7 stopped: essential container exited with code 1"},
		},
	}
	return d
}

func loadFixtures(api *fakeAPI) {
	api.reply("GET /api/v1/jobs", 200, map[string]any{"jobs": jobFixtures()})
	api.reply("GET /api/v1/folders", 200, map[string]any{"folders": []map[string]any{{"id": 1, "name": "Platform"}, {"id": 2, "name": "Web"}}})
	api.reply("GET /api/v1/jobs/{id}", 200, jobDetailFixture())
	api.set("GET /api/v1/runs", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, 200, map[string]any{"runs": []map[string]any{runFixture(12, "failed"), runFixture(11, "succeeded")}, "next_before": nil})
	})
}

// decodeOne decodes the whole of s as one JSON document into v and fails when
// anything else is on the stream.
func decodeOne(t *testing.T, s string, v any) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	if err := dec.Decode(v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, s)
	}
	if dec.More() {
		t.Fatalf("more than one document:\n%s", s)
	}
}

func contains(t *testing.T, what, text string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(text, w) {
			t.Errorf("%s does not contain %q:\n%s", what, w, text)
		}
	}
}

func cliConfig(server, token string) cli.Config { return cli.Config{Server: server, Token: token} }
