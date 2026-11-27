//go:build integration

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3/lock"

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

// Development only: base64 of a fixed, public 32-byte key. Never use outside tests.
const devSecretKey = "ZGV2LW9ubHktaW5zZWN1cmUta2V5LTMyLWJ5dGVzISE="

func baseEnv(t *testing.T, databaseURL, httpAddr string) map[string]string {
	t.Helper()
	return map[string]string{
		"YUMDRAKE_DATABASE_URL":     databaseURL,
		"YUMDRAKE_VALKEY_ADDR":      valkeyAddr(t),
		"YUMDRAKE_LOCAL_SECRET_KEY": devSecretKey,
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
		"YUMDRAKE_DATABASE_URL":     "postgres://yumdrake:" + password + "@127.0.0.1:1/yumdrake?sslmode=disable",
		"YUMDRAKE_VALKEY_ADDR":      "127.0.0.1:1",
		"YUMDRAKE_HTTP_ADDR":        freeAddr(t),
		"YUMDRAKE_STARTUP_TIMEOUT":  "2s",
		"YUMDRAKE_LOCAL_SECRET_KEY": devSecretKey,
	}
	var stdout, stderr syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- run(context.Background(), []string{"yumdrake-orchestrator"}, envFrom(env), &stdout, &stderr)
	}()
	select {
	case code := <-done:
		if code != 1 {
			t.Fatalf("run() = %d, want 1", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not give up within 10s of a 2s start-up timeout")
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

// TestRunMigrationOutlivesStartupTimeout holds the migration lock for longer
// than YUMDRAKE_STARTUP_TIMEOUT, so the migration can only finish if it is
// bounded by YUMDRAKE_MIGRATE_TIMEOUT instead.
func TestRunMigrationOutlivesStartupTimeout(t *testing.T) {
	dbURL := testdb.New(t)
	addr := freeAddr(t)

	connConfig, err := pgx.ParseConfig(dbURL)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	holder := stdlib.OpenDB(*connConfig)
	defer func() { _ = holder.Close() }()
	ctx := context.Background()
	conn, err := holder.Conn(ctx)
	if err != nil {
		t.Fatalf("open lock connection: %v", err)
	}
	defer func() { _ = conn.Close() }()
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		t.Fatalf("create locker: %v", err)
	}
	if err := locker.SessionLock(ctx, conn); err != nil {
		t.Fatalf("hold migration lock: %v", err)
	}
	released := false
	release := func(c *sql.Conn) {
		if !released {
			released = true
			if err := locker.SessionUnlock(ctx, c); err != nil {
				t.Errorf("release migration lock: %v", err)
			}
		}
	}
	defer release(conn)

	env := baseEnv(t, dbURL, addr)
	env["YUMDRAKE_STARTUP_TIMEOUT"] = "1s"
	env["YUMDRAKE_MIGRATE_TIMEOUT"] = "30s"
	r := start(t, []string{"yumdrake-orchestrator"}, env)

	// The migration is now blocked on the lock; outlast the start-up timeout.
	select {
	case code := <-r.done:
		t.Fatalf("run returned %d while the migration was waiting; stdout: %s", code, r.stdout.String())
	case <-time.After(2500 * time.Millisecond):
	}
	release(conn)

	waitStatus(t, addr, "/readyz", http.StatusOK, 15*time.Second)
}

// flakyProxy forwards TCP connections to upstream until cut is called, which
// closes every open connection and makes new ones fail at once. restore lets
// new connections through again. The listener stays up throughout, so the
// address never changes and nothing else can claim the port.
type flakyProxy struct {
	ln       net.Listener
	upstream string

	mu    sync.Mutex
	up    bool
	conns map[net.Conn]struct{}
}

func newFlakyProxy(t *testing.T, upstream string) *flakyProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for proxy: %v", err)
	}
	p := &flakyProxy{ln: ln, upstream: upstream, up: true, conns: map[net.Conn]struct{}{}}
	go p.acceptLoop()
	t.Cleanup(func() {
		_ = ln.Close()
		p.closeAll()
	})
	return p
}

func (p *flakyProxy) addr() string { return p.ln.Addr().String() }

func (p *flakyProxy) acceptLoop() {
	for {
		client, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		up := p.up
		p.mu.Unlock()
		if !up {
			_ = client.Close()
			continue
		}
		go p.forward(client)
	}
}

func (p *flakyProxy) forward(client net.Conn) {
	server, err := net.DialTimeout("tcp", p.upstream, 2*time.Second)
	if err != nil {
		_ = client.Close()
		return
	}
	p.mu.Lock()
	if !p.up { // cut between accept and now
		p.mu.Unlock()
		_ = client.Close()
		_ = server.Close()
		return
	}
	p.conns[client] = struct{}{}
	p.conns[server] = struct{}{}
	p.mu.Unlock()

	go func() { _, _ = io.Copy(server, client); _ = server.Close(); _ = client.Close() }()
	_, _ = io.Copy(client, server)
	_ = client.Close()
	_ = server.Close()
}

func (p *flakyProxy) closeAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for c := range p.conns {
		_ = c.Close()
	}
	p.conns = map[net.Conn]struct{}{}
}

func (p *flakyProxy) cut() {
	p.mu.Lock()
	p.up = false
	p.mu.Unlock()
	p.closeAll()
}

func (p *flakyProxy) restore() {
	p.mu.Lock()
	p.up = true
	p.mu.Unlock()
}

func TestRunSurvivesValkeyDroppingMidRun(t *testing.T) {
	dbURL := testdb.New(t)
	addr := freeAddr(t)
	proxy := newFlakyProxy(t, valkeyAddr(t))
	env := baseEnv(t, dbURL, addr)
	env["YUMDRAKE_VALKEY_ADDR"] = proxy.addr()
	r := start(t, []string{"yumdrake-orchestrator"}, env)

	waitStatus(t, addr, "/readyz", http.StatusOK, 15*time.Second)

	proxy.cut()
	body := waitStatus(t, addr, "/readyz", http.StatusServiceUnavailable, 15*time.Second)
	var parsed struct {
		Checks map[string]string `json:"checks"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("decode /readyz body %q: %v", body, err)
	}
	if parsed.Checks["valkey"] != "failed" || parsed.Checks["postgres"] != "ok" {
		t.Errorf("checks = %v, want valkey failed and postgres ok (body %s)", parsed.Checks, body)
	}
	if code, _, err := get(addr, "/healthz"); err != nil || code != http.StatusOK {
		t.Errorf("/healthz while Valkey is cut = %d, %v; want 200", code, err)
	}
	select {
	case code := <-r.done:
		t.Fatalf("run returned %d while Valkey is cut; it must keep serving", code)
	default:
	}

	proxy.restore()
	waitStatus(t, addr, "/readyz", http.StatusOK, 15*time.Second)
}
