//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/yamdrake/internal/db"
	"github.com/rcss-labs/yamdrake/internal/testutil/testdb"
	"github.com/rcss-labs/yamdrake/internal/version"
)

// syncBuffer is a bytes.Buffer safe for concurrent use by the service goroutine and the test.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().String()
}

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func valkeyAddr(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("YUMDRAKE_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Fatal("YUMDRAKE_TEST_VALKEY_ADDR must be set for integration tests")
	}
	return addr
}

type running struct {
	done   chan int // receives run's exit code once
	cancel context.CancelFunc
	stdout *syncBuffer
	stderr *syncBuffer
	once   sync.Once
	code   int
}

// wait returns the exit code of run, or fails the test if it does not return in time.
// It may be called repeatedly (the code is cached).
func (r *running) wait(t *testing.T, within time.Duration) int {
	t.Helper()
	r.once.Do(func() {
		select {
		case r.code = <-r.done:
		case <-time.After(within):
			r.code = -1
		}
	})
	if r.code == -1 {
		t.Fatalf("run did not return within %v", within)
	}
	return r.code
}

func start(t *testing.T, args []string, env map[string]string) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{done: make(chan int, 1), cancel: cancel, stdout: &syncBuffer{}, stderr: &syncBuffer{}}
	go func() { r.done <- run(ctx, args, envFrom(env), r.stdout, r.stderr) }()
	t.Cleanup(func() {
		cancel()
		r.wait(t, 10*time.Second)
	})
	return r
}

func get(addr, path string) (int, string, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + addr + path)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), nil
}

// waitStatus polls path until it returns want or the deadline passes.
func waitStatus(t *testing.T, addr, path string, want int, within time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(within)
	var lastCode int
	var lastBody string
	for time.Now().Before(deadline) {
		code, body, err := get(addr, path)
		if err == nil {
			lastCode, lastBody = code, body
			if code == want {
				return body
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s did not reach %d within %v (last %d %q)", path, want, within, lastCode, lastBody)
	return ""
}

func baseEnv(t *testing.T, databaseURL, httpAddr string) map[string]string {
	t.Helper()
	return map[string]string{
		"YUMDRAKE_DATABASE_URL":     databaseURL,
		"YUMDRAKE_VALKEY_ADDR":      valkeyAddr(t),
		"YUMDRAKE_HTTP_ADDR":        httpAddr,
		"YUMDRAKE_SHUTDOWN_TIMEOUT": "5s",
		"YUMDRAKE_STARTUP_TIMEOUT":  "20s",
	}
}

func TestRunVersionPrintsVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"yumdrake-orchestrator", "version"}, envFrom(nil), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() = %d, want 0", code)
	}
	if want := version.String() + "\n"; stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestRunUnknownSubcommandReturns2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"yumdrake-orchestrator", "bogus"}, envFrom(nil), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("run() = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage") {
		t.Errorf("stderr = %q, want usage text", stderr.String())
	}
}

func TestRunWithBadConfigReturns2AndNamesVariable(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"yumdrake-orchestrator"}, envFrom(nil), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("run() = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "YUMDRAKE_DATABASE_URL") {
		t.Errorf("stderr = %q, want it to name YUMDRAKE_DATABASE_URL", stderr.String())
	}
}

func TestRunMigratesThenBecomesReady(t *testing.T) {
	dbURL := testdb.New(t)
	addr := freeAddr(t)
	r := start(t, []string{"yumdrake-orchestrator"}, baseEnv(t, dbURL, addr))

	waitStatus(t, addr, "/readyz", http.StatusOK, 10*time.Second)

	ctx := context.Background()
	pool, err := db.Open(ctx, dbURL)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer pool.Close()
	got, err := db.SchemaVersion(ctx, pool)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if got != db.LatestVersion() {
		t.Errorf("schema version = %d, want %d", got, db.LatestVersion())
	}

	r.cancel()
	if code := r.wait(t, 10*time.Second); code != 0 {
		t.Errorf("run() = %d after cancel, want 0; stdout: %s", code, r.stdout.String())
	}
	if !strings.Contains(r.stdout.String(), `"msg":"stopped"`) {
		t.Errorf("stdout lacks a stopped line: %s", r.stdout.String())
	}
}

func TestRunStaysLiveButNotReadyWhenValkeyIsDown(t *testing.T) {
	dbURL := testdb.New(t)
	addr := freeAddr(t)
	env := baseEnv(t, dbURL, addr)
	env["YUMDRAKE_VALKEY_ADDR"] = "127.0.0.1:1"
	r := start(t, []string{"yumdrake-orchestrator"}, env)

	waitStatus(t, addr, "/healthz", http.StatusOK, 10*time.Second)
	body := waitStatus(t, addr, "/readyz", http.StatusServiceUnavailable, 10*time.Second)
	var parsed struct {
		Checks map[string]string `json:"checks"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("decode /readyz body %q: %v", body, err)
	}
	if parsed.Checks["valkey"] != "failed" {
		t.Errorf("checks.valkey = %q, want failed (body %s)", parsed.Checks["valkey"], body)
	}

	select {
	case code := <-r.done:
		t.Fatalf("run returned %d while Valkey is down; it must keep serving", code)
	case <-time.After(3 * time.Second):
	}
	// The failing check must be logged (password scrubbing is covered in internal/app).
	if !strings.Contains(r.stdout.String(), "readiness check failed") {
		t.Errorf("expected a logged readiness failure: %s", r.stdout.String())
	}
}

func TestRunExits1WhenDatabaseNeverAppears(t *testing.T) {
	const password = "pw-never-log-me"
	env := map[string]string{
		"YUMDRAKE_DATABASE_URL":    "postgres://yumdrake:" + password + "@127.0.0.1:1/yumdrake?sslmode=disable",
		"YUMDRAKE_VALKEY_ADDR":     "127.0.0.1:1",
		"YUMDRAKE_HTTP_ADDR":       freeAddr(t),
		"YUMDRAKE_STARTUP_TIMEOUT": "2s",
	}
	var stdout, stderr syncBuffer
	code := run(context.Background(), []string{"yumdrake-orchestrator"}, envFrom(env), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("run() = %d, want 1", code)
	}
	if strings.Contains(stdout.String(), password) || strings.Contains(stderr.String(), password) {
		t.Errorf("password leaked: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "REDACTED") {
		t.Errorf("stdout should show the redacted URL: %s", stdout.String())
	}
}

func TestHealthcheckSubcommand(t *testing.T) {
	dbURL := testdb.New(t)
	addr := freeAddr(t)
	env := baseEnv(t, dbURL, addr)
	r := start(t, []string{"yumdrake-orchestrator"}, env)
	waitStatus(t, addr, "/healthz", http.StatusOK, 10*time.Second)

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"yumdrake-orchestrator", "healthcheck"}, envFrom(env), &stdout, &stderr); code != 0 {
		t.Errorf("healthcheck against a live instance = %d, want 0 (stderr %q)", code, stderr.String())
	}

	r.cancel()
	r.wait(t, 10*time.Second)
	stderr.Reset()
	if code := run(context.Background(), []string{"yumdrake-orchestrator", "healthcheck"}, envFrom(env), &stdout, &stderr); code != 1 {
		t.Errorf("healthcheck with nothing listening = %d, want 1", code)
	}

	bad := map[string]string{"YUMDRAKE_HTTP_ADDR": "not-an-address"}
	stderr.Reset()
	if code := run(context.Background(), []string{"yumdrake-orchestrator", "healthcheck"}, envFrom(bad), &stdout, &stderr); code != 2 {
		t.Errorf("healthcheck with invalid address = %d, want 2", code)
	}
}
