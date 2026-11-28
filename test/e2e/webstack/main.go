// Command webstack brings up what the browser test (web/e2e) needs and holds it
// until told to stop: its own database on the Compose PostgreSQL, a fake GitHub
// holding the shared valid fixture, and the real orchestrator binary serving the
// embedded web application on a free port.
//
// It prints one JSON line {"base_url","email","password"} on standard output
// when everything is ready, then waits. It stops, kills the orchestrator and
// drops its database when standard input closes or on SIGINT or SIGTERM. All
// diagnostics go to standard error, never the password.
//
// It is a program, not a test, so `make test` never starts it.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/yamdrake/test/e2e/fakegithub"
)

const (
	usage = `usage: webstack [--binary bin/yumdrake-orchestrator] [--fixture internal/spec/testdata/valid]

Needs YUMDRAKE_TEST_DATABASE_URL and YUMDRAKE_TEST_VALKEY_ADDR (see make dev-up).
Exit codes: 0 stopped normally, 1 failure, 2 usage.
`

	// The same fixed public development key compose.yaml and the in-process e2e tests use.
	localSecretKey = "ZGV2LW9ubHktaW5zZWN1cmUta2V5LTMyLWJ5dGVzISE=" //nolint:gosec // public development key, not a secret
	githubToken    = "web-e2e-fake-github-token"                    //nolint:gosec // token of the fake GitHub only
	adminEmail     = "admin@web-e2e.example.test"
	adminName      = "Web E2E Admin"
	repo           = "acme/platform"
	branch         = "main"
	commit         = "1111111111111111111111111111111111111111"
	jobPath        = "jobs/deploy-backend.yaml"
	sessionCookie  = "yumdrake_session"
	waitLimit      = 90 * time.Second
)

type ready struct {
	BaseURL  string `json:"base_url"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

func main() {
	os.Exit(run())
}

func logf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "webstack: "+format+"\n", args...)
}

func run() int {
	flags := flag.NewFlagSet("webstack", flag.ContinueOnError)
	flags.Usage = func() { _, _ = io.WriteString(os.Stderr, usage) }
	binary := flags.String("binary", "bin/yumdrake-orchestrator", "path to the orchestrator binary built by make build")
	fixture := flags.String("fixture", "internal/spec/testdata/valid", "directory committed to the fake GitHub repository")
	if err := flags.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	adminURL, valkeyAddr := os.Getenv("YUMDRAKE_TEST_DATABASE_URL"), os.Getenv("YUMDRAKE_TEST_VALKEY_ADDR")
	if flags.NArg() > 0 || adminURL == "" || valkeyAddr == "" {
		_, _ = io.WriteString(os.Stderr, "webstack: YUMDRAKE_TEST_DATABASE_URL and YUMDRAKE_TEST_VALKEY_ADDR are required\n\n"+usage)
		return 2
	}

	// Stop on a signal or when the parent closes our standard input.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	stdinClosed := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		close(stdinClosed)
	}()

	cleanup := &cleaner{}
	defer cleanup.run()

	out, err := bringUp(ctx, cleanup, *binary, *fixture, adminURL, valkeyAddr)
	if err != nil {
		logf("%v", err)
		return 1
	}
	line, _ := json.Marshal(out) //nolint:gosec // the password is generated per run and handed only to the parent over a pipe
	_, _ = fmt.Fprintln(os.Stdout, string(line))
	logf("ready at %s", out.BaseURL)

	select {
	case <-ctx.Done():
		logf("signal received, stopping")
	case <-stdinClosed:
		logf("standard input closed, stopping")
	}
	return 0
}

// cleaner runs registered steps in reverse order, once.
type cleaner struct{ steps []func() }

func (c *cleaner) add(step func()) { c.steps = append(c.steps, step) }

func (c *cleaner) run() {
	for i := len(c.steps) - 1; i >= 0; i-- {
		c.steps[i]()
	}
	c.steps = nil
}

func randomHex(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func freeAddr() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().String(), nil
}

func bringUp(ctx context.Context, cleanup *cleaner, binary, fixture, adminURL, valkeyAddr string) (ready, error) {
	suffix, err := randomHex(6)
	if err != nil {
		return ready{}, err
	}
	password, err := randomHex(12)
	if err != nil {
		return ready{}, err
	}
	keySuffix, err := randomHex(8)
	if err != nil {
		return ready{}, err
	}

	// A database of our own, dropped at the end.
	dbName := "yumdrake_web_e2e_" + suffix
	databaseURL, err := createDatabase(ctx, cleanup, adminURL, dbName)
	if err != nil {
		return ready{}, err
	}

	gh := fakegithub.New(githubToken)
	cleanup.add(gh.Close)
	if err := gh.LoadDir(repo, commit, fixture); err != nil {
		return ready{}, fmt.Errorf("load fixture %s: %w", fixture, err)
	}
	gh.SetHead(repo, branch, commit)

	httpAddr, err := freeAddr()
	if err != nil {
		return ready{}, fmt.Errorf("find a free port: %w", err)
	}
	healthAddr, err := freeAddr()
	if err != nil {
		return ready{}, fmt.Errorf("find a free port: %w", err)
	}
	base := "http://" + httpAddr
	env := append(os.Environ(),
		"YUMDRAKE_DATABASE_URL="+databaseURL,
		"YUMDRAKE_VALKEY_ADDR="+valkeyAddr,
		"YUMDRAKE_HTTP_ADDR="+httpAddr,
		"YUMDRAKE_HEALTH_ADDR="+healthAddr,
		"YUMDRAKE_PUBLIC_URL="+base,
		"YUMDRAKE_LOCAL_SECRET_KEY="+localSecretKey,
		"YUMDRAKE_VALKEY_KEY_PREFIX=webe2e-"+keySuffix+":",
		"YUMDRAKE_SYNC_INTERVAL=10s",
		"YUMDRAKE_GITHUB_API_URL="+gh.URL(),
		"YUMDRAKE_LOG_LEVEL=warn",
	)

	// The service migrates the empty database on start.
	svc := exec.Command(binary) //nolint:gosec // the binary path is an operator-given flag
	svc.Env = env
	svc.Stdout, svc.Stderr = os.Stderr, os.Stderr
	if err := svc.Start(); err != nil {
		return ready{}, fmt.Errorf("start %s: %w", binary, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- svc.Wait() }()
	cleanup.add(func() { stopChild(svc, exited) })

	if err := waitReady(ctx, base+"/readyz", exited); err != nil {
		return ready{}, err
	}

	// An operator's way in: the create-admin subcommand, password on standard input.
	create := exec.Command(binary, "create-admin", "--email", adminEmail, "--name", adminName) //nolint:gosec // see above
	create.Env = env
	create.Stdin = strings.NewReader(password + "\n")
	var stdout, stderr bytes.Buffer
	create.Stdout, create.Stderr = &stdout, &stderr
	if err := create.Run(); err != nil {
		return ready{}, fmt.Errorf("create-admin: %w\n%s", err, stderr.String())
	}

	api := &caller{base: base}
	var cookie string
	if cookie, err = api.login(adminEmail, password); err != nil {
		return ready{}, err
	}
	api.session = cookie

	// Set-up as the Go e2e tests do it; each step syncs at once.
	steps := []struct {
		method, path string
		body         any
		out          any
	}{
		{http.MethodPost, "/api/v1/admin/github-connections", map[string]any{"owner": "acme", "method": "token", "token": githubToken}, nil},
		{http.MethodPut, "/api/v1/admin/inventory-source", map[string]any{"repo": repo, "branch": branch}, nil},
	}
	for _, s := range steps {
		if err := api.call(s.method, s.path, s.body, s.out); err != nil {
			return ready{}, err
		}
	}
	var folder struct {
		ID int64 `json:"id"`
	}
	if err := api.call(http.MethodPost, "/api/v1/folders", map[string]any{"name": "Backend"}, &folder); err != nil {
		return ready{}, err
	}
	var job struct {
		ID       int64 `json:"id"`
		Runnable bool  `json:"runnable"`
	}
	if err := api.call(http.MethodPost, "/api/v1/jobs", map[string]any{
		"folder_id": folder.ID, "repo": repo, "branch": branch, "path": jobPath}, &job); err != nil {
		return ready{}, err
	}

	if err := waitUntil(ctx, "the job to be runnable and the inventory active", func() (bool, error) {
		var j struct {
			Runnable bool `json:"runnable"`
		}
		if err := api.call(http.MethodGet, fmt.Sprintf("/api/v1/jobs/%d", job.ID), nil, &j); err != nil {
			return false, err
		}
		var inv struct {
			Commit       string            `json:"commit"`
			Environments []json.RawMessage `json:"environments"`
		}
		if err := api.call(http.MethodGet, "/api/v1/inventory", nil, &inv); err != nil {
			return false, err
		}
		return j.Runnable && inv.Commit == commit && len(inv.Environments) == 5, nil
	}); err != nil {
		return ready{}, err
	}
	return ready{BaseURL: base, Email: adminEmail, Password: password}, nil
}

func createDatabase(ctx context.Context, cleanup *cleaner, adminURL, name string) (string, error) {
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return "", fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		return "", fmt.Errorf("create database %s: %w", name, err)
	}
	cleanup.add(func() {
		// The caller's context may be cancelled by now, so use a fresh one.
		dropCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		drop, err := pgx.Connect(dropCtx, adminURL)
		if err != nil {
			logf("connect to drop %s: %v", name, err)
			return
		}
		defer func() { _ = drop.Close(dropCtx) }()
		if _, err := drop.Exec(dropCtx, "DROP DATABASE IF EXISTS "+quoted+" WITH (FORCE)"); err != nil {
			logf("drop %s: %v", name, err)
			return
		}
		logf("dropped database %s", name)
	})
	u, err := url.Parse(adminURL)
	if err != nil {
		return "", fmt.Errorf("parse YUMDRAKE_TEST_DATABASE_URL: %w", err)
	}
	u.Path = "/" + name
	return u.String(), nil
}

// stopChild asks the orchestrator to stop and kills it if it does not.
func stopChild(cmd *exec.Cmd, exited <-chan error) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case err := <-exited:
		logf("orchestrator stopped: %v", err)
	case <-time.After(25 * time.Second):
		logf("orchestrator ignored SIGTERM, killing it")
		_ = cmd.Process.Kill()
		logf("orchestrator killed: %v", <-exited)
	}
}

func waitUntil(ctx context.Context, what string, cond func() (bool, error)) error {
	deadline := time.Now().Add(waitLimit)
	for {
		ok, err := cond()
		if err != nil {
			return fmt.Errorf("waiting for %s: %w", what, err)
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s", what)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func waitReady(ctx context.Context, readyURL string, exited <-chan error) error {
	return waitUntil(ctx, "the orchestrator to answer /readyz", func() (bool, error) {
		select {
		case err := <-exited:
			return false, fmt.Errorf("the orchestrator exited early: %w", err)
		default:
		}
		resp, err := http.Get(readyURL) //nolint:gosec,noctx // local service
		if err != nil {
			return false, nil
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK, nil
	})
}

// caller sends API requests with a session cookie replayed by hand and the
// Origin the API requires on writes. Failures report the status and error code
// only, never a body: it may hold a credential.
type caller struct {
	base    string
	session string
}

func (c *caller) login(email, password string) (string, error) {
	resp, err := c.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": password})
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	for _, cookie := range resp.Cookies() {
		if cookie.Name == sessionCookie {
			return cookie.Value, nil
		}
	}
	return "", errors.New("sign-in set no session cookie")
}

func (c *caller) call(method, path string, body, out any) error {
	resp, err := c.do(method, path, body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil {
		raw, _ := io.ReadAll(resp.Body)
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode %s %s: %w", method, path, err)
		}
	}
	return nil
}

func (c *caller) do(method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		return nil, err
	}
	if c.session != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: c.session}) //nolint:gosec // a request cookie; the attributes only matter on Set-Cookie
	}
	req.Header.Set("Origin", c.base)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	if resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		var failure struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(raw, &failure)
		return nil, fmt.Errorf("%s %s: status %d, error code %q", method, path, resp.StatusCode, failure.Error.Code)
	}
	return resp, nil
}
