package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/yamdrake/internal/health"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// freeAddr returns a 127.0.0.1 address whose port was free a moment ago.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().String()
}

// startServe runs Serve in a goroutine and returns the channel of its result.
func startServe(ctx context.Context, timeout time.Duration, servers ...*http.Server) <-chan error {
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, discardLogger(), timeout, servers...) }()
	return done
}

// waitListening polls until addr accepts connections.
func waitListening(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("nothing listening on %s", addr)
}

func testServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

func TestServeReturnsNilOnCancel(t *testing.T) {
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := startServe(ctx, 2*time.Second, testServer(addr, okHandler()))
	waitListening(t, addr)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve() = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}

func TestServeLetsInFlightRequestFinish(t *testing.T) {
	addr := freeAddr(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := startServe(ctx, 3*time.Second, testServer(addr, handler))
	waitListening(t, addr)

	status := make(chan int, 1)
	go func() {
		resp, err := http.Get("http://" + addr + "/")
		if err != nil {
			status <- -1
			return
		}
		_ = resp.Body.Close()
		status <- resp.StatusCode
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()

	if got := <-status; got != http.StatusOK {
		t.Errorf("in-flight request status = %d, want 200", got)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve() = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return")
	}
}

func TestServeReportsListenError(t *testing.T) {
	addr := freeAddr(t)
	done := startServe(context.Background(), time.Second,
		testServer(addr, okHandler()),
		testServer(addr, okHandler()),
	)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Serve() = nil, want a listen error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve hung on a listen error")
	}
}

func TestServeErrorsWhenShutdownExceedsTimeout(t *testing.T) {
	addr := freeAddr(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := startServe(ctx, 100*time.Millisecond, testServer(addr, handler))
	waitListening(t, addr)

	go func() {
		resp, err := http.Get("http://" + addr + "/")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	time.Sleep(50 * time.Millisecond)
	started := time.Now()
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Serve() = nil, want a shutdown timeout error")
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Errorf("Serve took %v to give up, want about 100ms", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return")
	}
}

// TestPostgresCheckFailureNeverLeaksPassword covers the readiness path: the
// health handler logs check errors verbatim, so the check must scrub the
// password from the driver's message. The fake ping returns an error whose text
// contains the password and the full URL, so a plain %w would fail this test.
func TestPostgresCheckFailureNeverLeaksPassword(t *testing.T) {
	const password = "s3cr3t-pw-xyz"
	databaseURL := "postgres://u:" + password + "@127.0.0.1:1/d"
	ping := func(context.Context) error {
		return errors.New("failed to connect to " + databaseURL + " with password=" + password + ": connection refused")
	}

	var logs bytes.Buffer
	handler := health.Handler([]health.Check{postgresCheck(ping, databaseURL)},
		health.WithLogger(slog.New(slog.NewJSONHandler(&logs, nil))))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz status = %d, want 503", rec.Code)
	}
	if !strings.Contains(logs.String(), "readiness check failed") || !strings.Contains(logs.String(), "connection refused") {
		t.Fatalf("expected the failure and the driver reason to be logged, got %q", logs.String())
	}
	if strings.Contains(logs.String(), password) || strings.Contains(rec.Body.String(), password) {
		t.Errorf("password leaked: logs=%q body=%q", logs.String(), rec.Body.String())
	}
}
