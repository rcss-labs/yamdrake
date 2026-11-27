//go:build integration

package app

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	valkey "github.com/valkey-io/valkey-go"

	"github.com/rcss-labs/yamdrake/internal/config"
	"github.com/rcss-labs/yamdrake/internal/testutil/testdb"
)

// runWithHandler starts Run with one extra handler at /hold and returns the
// service address and a channel with the exit code.
func runWithHandler(t *testing.T, ctx context.Context, shutdown time.Duration, hold http.HandlerFunc) (string, <-chan int) {
	t.Helper()
	valkeyAddr := os.Getenv("YUMDRAKE_TEST_VALKEY_ADDR")
	if valkeyAddr == "" {
		t.Fatal("YUMDRAKE_TEST_VALKEY_ADDR must be set for integration tests")
	}
	addr := freeAddr(t)
	svc := Service{
		Name: "test",
		Common: config.Common{
			DatabaseURL: testdb.New(t), ValkeyAddr: valkeyAddr, LogLevel: slog.LevelError,
			ShutdownTimeout: shutdown, StartupTimeout: 20 * time.Second,
		},
		Addr: addr,
		Extend: func(context.Context, *slog.Logger, *pgxpool.Pool, valkey.Client) (Extension, error) {
			return Extension{Handlers: map[string]http.Handler{"/hold": hold}}, nil
		},
	}
	done := make(chan int, 1)
	go func() { done <- Run(ctx, svc, &bytes.Buffer{}) }()
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("service never came up: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return addr, done
}

func TestRunLetsInFlightRequestFinishDuringShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	cancelled := make(chan bool, 1)
	addr, done := runWithHandler(t, ctx, 10*time.Second, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		cancelled <- r.Context().Err() != nil
		w.WriteHeader(http.StatusOK)
	})

	result := make(chan int, 1)
	go func() {
		resp, err := http.Get("http://" + addr + "/hold")
		if err != nil {
			result <- -1
			return
		}
		_ = resp.Body.Close()
		result <- resp.StatusCode
	}()
	<-started
	cancel() // shutdown begins with the request still running
	time.Sleep(300 * time.Millisecond)
	close(release)

	if code := <-result; code != http.StatusOK {
		t.Errorf("in-flight request = %d, want 200", code)
	}
	if <-cancelled {
		t.Error("the request context was cancelled when shutdown began; it must be drained")
	}
	if code := <-done; code != ExitOK {
		t.Errorf("Run = %d, want %d", code, ExitOK)
	}
}

func TestRunCancelsRequestsStillRunningWhenShutdownTimesOut(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	ended := make(chan struct{})
	addr, done := runWithHandler(t, ctx, time.Second, func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(ended)
	})
	go func() {
		resp, err := http.Get("http://" + addr + "/hold")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-started
	cancel()
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("a request still running after the shutdown timeout was never cancelled")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
}
