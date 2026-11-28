//go:build integration

package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/yumdrake/internal/notify"
)

const slackFakeToken = "xoxb-api-fake-token" //nolint:gosec // a fake token for tests

// slackSpy is a fake Slack that records what it was sent and answers as reply says.
type slackSpy struct {
	srv *httptest.Server

	mu    sync.Mutex
	auth  []string
	posts []map[string]string
	reply string
}

func newSlackSpy(t *testing.T, reply string) *slackSpy {
	t.Helper()
	spy := &slackSpy{reply: reply}
	spy.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]string
		_ = json.Unmarshal(raw, &body)
		spy.mu.Lock()
		spy.auth = append(spy.auth, r.Header.Get("Authorization"))
		spy.posts = append(spy.posts, body)
		reply := spy.reply
		spy.mu.Unlock()
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(spy.srv.Close)
	return spy
}

func (s *slackSpy) setReply(reply string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reply = reply
}

func TestSlackSettingsNeverReturnToken(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	got := h.mustDo("GET", "/api/v1/admin/slack", nil, http.StatusOK)
	if got["token_set"] != false || got["set_at"] != nil || got["set_by_name"] != "" {
		t.Fatalf("before any token: %v, want token_set false with null set_at and empty set_by_name", got)
	}

	for _, bad := range []any{map[string]any{"token": ""}, map[string]any{"token": "   "}, map[string]any{}} {
		if code, body := h.do("PUT", "/api/v1/admin/slack", bad); code != http.StatusBadRequest || errorCode(t, body) != "invalid" {
			t.Errorf("PUT %v = %d %v, want 400 invalid", bad, code, body)
		}
	}
	if got := h.mustDo("GET", "/api/v1/admin/slack", nil, http.StatusOK); got["token_set"] != false {
		t.Fatalf("a refused PUT stored something: %v", got)
	}

	put := h.mustDo("PUT", "/api/v1/admin/slack", map[string]any{"token": slackFakeToken}, http.StatusOK)
	if put["token_set"] != true || put["set_by_name"] != h.admin.Name {
		t.Fatalf("PUT = %v, want token_set true, set_by_name the admin", put)
	}
	at, _ := put["set_at"].(string)
	if parsed, err := time.Parse(time.RFC3339, at); err != nil || time.Since(parsed) > time.Minute {
		t.Errorf("set_at = %v, want a recent RFC 3339 time", put["set_at"])
	}
	if len(put) != 5 || put["token_invalid"] != false || put["token_invalid_since"] != nil {
		t.Errorf("PUT answered %v, want exactly token_set, set_at, set_by_name, token_invalid false and token_invalid_since null", put)
	}
	refusedAt := time.Now().UTC().Truncate(time.Second)
	status, err := notify.LoadSlackStatus(ctx, h.pool)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := notify.MarkSlackTokenInvalid(ctx, h.pool, status, refusedAt); err != nil || !changed {
		t.Fatalf("MarkSlackTokenInvalid = %v, %v", changed, err)
	}
	if got := h.mustDo("GET", "/api/v1/admin/slack", nil, http.StatusOK); got["token_invalid"] != true || got["token_invalid_since"] != refusedAt.Format(time.RFC3339) || got["token_set"] != true {
		t.Errorf("GET after Slack refused the token = %v, want token_invalid true since %v", got, refusedAt)
	}
	// Storing a token again clears it; the next assertions compare with that answer.
	put = h.mustDo("PUT", "/api/v1/admin/slack", map[string]any{"token": slackFakeToken}, http.StatusOK)
	if put["token_invalid"] != false || put["token_invalid_since"] != nil {
		t.Errorf("PUT after a refusal = %v, want the token valid again", put)
	}
	if again := h.mustDo("GET", "/api/v1/admin/slack", nil, http.StatusOK); again["token_set"] != true || again["set_by_name"] != h.admin.Name || again["set_at"] != put["set_at"] {
		t.Errorf("GET after PUT = %v, want the same as the PUT answer %v", again, put)
	}

	// The token is nowhere a person or an operator can read it.
	for i, body := range h.bodies {
		if strings.Contains(body, slackFakeToken) {
			t.Errorf("response #%d contains the token", i)
		}
	}
	var stored string
	if err := h.pool.QueryRow(ctx, "SELECT value::text FROM instance_settings WHERE key = 'slack'").Scan(&stored); err != nil || strings.Contains(stored, slackFakeToken) {
		t.Errorf("stored setting readable as plain token or missing: %v", err)
	}
	if token, err := notify.SlackTokenSource(h.pool, h.cipher)(ctx); err != nil || token != slackFakeToken {
		t.Errorf("the stored token does not decrypt with purpose slack: %v", err)
	}
	for _, e := range h.auditEntries() {
		if strings.Contains(string(e.Detail), slackFakeToken) || strings.Contains(e.TargetID, slackFakeToken) {
			t.Errorf("audit entry %s holds the token", e.Action)
		}
	}
	if actions := strings.Join(h.auditActions(), ","); !strings.Contains(actions, "slack.set") || strings.Count(actions, "slack.set") != 2 {
		t.Errorf("audit actions %s, want exactly two slack.set (one per accepted PUT) (the refused PUTs write none)", actions)
	}
}

func TestSlackTestEndpointReportsCode(t *testing.T) {
	h := newHarness(t)
	spy := newSlackSpy(t, `{"ok":true}`)
	h.rebuild(func(d *Deps) { d.Slack = notify.NewSlack(spy.srv.URL, nil) })

	// No token: nothing to test with.
	code, body := h.do("POST", "/api/v1/admin/slack/test", map[string]any{"channel": "#deploys"})
	if code != http.StatusConflict || errorCode(t, body) != "conflict" || !strings.Contains(body["error"].(map[string]any)["message"].(string), "Slack is not connected.") {
		t.Fatalf("without a token: %d %v, want 409 conflict \"Slack is not connected.\"", code, body)
	}
	if len(spy.posts) != 0 {
		t.Fatalf("%d posts without a token", len(spy.posts))
	}

	h.mustDo("PUT", "/api/v1/admin/slack", map[string]any{"token": slackFakeToken}, http.StatusOK)

	if code, body := h.do("POST", "/api/v1/admin/slack/test", map[string]any{"channel": "  "}); code != http.StatusBadRequest || errorCode(t, body) != "invalid" {
		t.Errorf("empty channel: %d %v, want 400 invalid", code, body)
	}

	if got := h.mustDo("POST", "/api/v1/admin/slack/test", map[string]any{"channel": "#deploys"}, http.StatusOK); len(got) != 2 || got["ok"] != true || got["code"] != "" {
		t.Fatalf("success = %v, want exactly {ok: true, code: \"\"}", got)
	}
	if len(spy.posts) != 1 || spy.auth[0] != "Bearer "+slackFakeToken || spy.posts[0]["channel"] != "#deploys" || spy.posts[0]["text"] != "Yumdrake can post here." {
		t.Fatalf("Slack received %v with %v, want the text to #deploys using the stored token", spy.posts, spy.auth)
	}

	// Slack's code comes back; a hostile reply cannot carry the token out.
	spy.setReply(`{"ok":false,"error":"channel_not_found","detail":"` + slackFakeToken + `"}`)
	if got := h.mustDo("POST", "/api/v1/admin/slack/test", map[string]any{"channel": "#nope"}, http.StatusOK); len(got) != 2 || got["ok"] != false || got["code"] != "channel_not_found" {
		t.Fatalf("failure = %v, want {ok: false, code: channel_not_found}", got)
	}
	spy.setReply(`not slack`)
	if got := h.mustDo("POST", "/api/v1/admin/slack/test", map[string]any{"channel": "#x"}, http.StatusOK); got["ok"] != false || got["code"] != "invalid_response" {
		t.Fatalf("garbage reply = %v, want ok false and invalid_response", got)
	}

	// A Slack that cannot be reached is a failure to report, not a server error.
	spy.srv.Close()
	if got := h.mustDo("POST", "/api/v1/admin/slack/test", map[string]any{"channel": "#x"}, http.StatusOK); got["ok"] != false || got["code"] != "transport" {
		t.Fatalf("unreachable Slack = %v, want ok false and transport", got)
	}

	for i, body := range h.bodies {
		if strings.Contains(body, slackFakeToken) {
			t.Errorf("response #%d contains the token", i)
		}
	}
	actions := h.auditActions()
	tests := 0
	for _, a := range actions {
		if a == "slack.test" {
			tests++
		}
	}
	if tests != 4 { // the 409 and the 400 reached no Slack and are not audited
		t.Errorf("audit actions %v, want four slack.test entries (one per call that reached Slack)", actions)
	}
	for _, e := range h.auditEntries() {
		if strings.Contains(string(e.Detail), slackFakeToken) {
			t.Errorf("audit entry %s holds the token", e.Action)
		}
	}
}
