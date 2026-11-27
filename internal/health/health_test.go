package health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type readyResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

func get(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func decodeReady(t *testing.T, rec *httptest.ResponseRecorder) readyResponse {
	t.Helper()
	var body readyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v: %q", err, rec.Body.String())
	}
	return body
}

func TestHealthzIsOKWithoutRunningChecks(t *testing.T) {
	var called atomic.Bool
	h := Handler([]Check{{Name: "bad", Fn: func(context.Context) error {
		called.Store(true)
		return errors.New("boom")
	}}})
	rec := get(t, h, http.MethodGet, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"status":"ok"}` {
		t.Fatalf("body = %q", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type = %q", ct)
	}
	if called.Load() {
		t.Fatal("check was called by /healthz")
	}
}

func TestReadyzOKWhenAllChecksPass(t *testing.T) {
	ok := func(context.Context) error { return nil }
	h := Handler([]Check{{Name: "postgres", Fn: ok}, {Name: "valkey", Fn: ok}})
	rec := get(t, h, http.MethodGet, "/readyz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := decodeReady(t, rec)
	if body.Status != "ok" || body.Checks["postgres"] != "ok" || body.Checks["valkey"] != "ok" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestReadyzWithNoChecksIsOKWithEmptyChecks(t *testing.T) {
	rec := get(t, Handler(nil), http.MethodGet, "/readyz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"checks":{}`) {
		t.Fatalf("body = %q, want empty checks object", rec.Body.String())
	}
}

func TestReadyzUnavailableNamesFailingCheck(t *testing.T) {
	h := Handler([]Check{
		{Name: "postgres", Fn: func(context.Context) error { return nil }},
		{Name: "valkey", Fn: func(context.Context) error { return errors.New("down") }},
	})
	rec := get(t, h, http.MethodGet, "/readyz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	body := decodeReady(t, rec)
	if body.Status != "unavailable" || body.Checks["valkey"] != "failed" || body.Checks["postgres"] != "ok" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestReadyzDoesNotLeakErrorText(t *testing.T) {
	h := Handler([]Check{{Name: "postgres", Fn: func(context.Context) error {
		return errors.New("dial postgres://u:s3cret@h/db: refused")
	}}})
	rec := get(t, h, http.MethodGet, "/readyz")
	for _, leaked := range []string{"s3cret", "refused"} {
		if strings.Contains(rec.Body.String(), leaked) {
			t.Fatalf("body leaks %q: %s", leaked, rec.Body.String())
		}
	}
}

func TestReadyzTimesOutSlowCheck(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	h := Handler([]Check{
		{Name: "ctx-aware", Fn: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }},
		{Name: "ignores-ctx", Fn: func(context.Context) error { <-release; return nil }},
	})
	start := time.Now()
	rec := get(t, h, http.MethodGet, "/readyz")
	if elapsed := time.Since(start); elapsed >= 3*time.Second {
		t.Fatalf("response took %s, want < 3s", elapsed)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	body := decodeReady(t, rec)
	if body.Checks["ctx-aware"] != "failed" || body.Checks["ignores-ctx"] != "failed" {
		t.Fatalf("unexpected checks: %+v", body.Checks)
	}
}

func TestUnknownPathIs404AndPostIs405(t *testing.T) {
	h := Handler(nil)
	if rec := get(t, h, http.MethodGet, "/nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown path status = %d, want 404", rec.Code)
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		if rec := get(t, h, http.MethodPost, path); rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s status = %d, want 405", path, rec.Code)
		}
	}
}

func TestProbe(t *testing.T) {
	ctx := context.Background()

	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer okServer.Close()
	if err := Probe(ctx, okServer.URL); err != nil {
		t.Fatalf("Probe against 200 = %v, want nil", err)
	}

	badServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer badServer.Close()
	err := Probe(ctx, badServer.URL)
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("Probe against 503 = %v, want error naming 503", err)
	}

	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()
	if err := Probe(ctx, url); err == nil {
		t.Fatal("Probe against closed port = nil, want error")
	}
}
