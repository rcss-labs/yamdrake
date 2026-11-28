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
	"encoding/base64"
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

	"github.com/rcss-labs/yumdrake/test/awstest"
	"github.com/rcss-labs/yumdrake/test/e2e/fakegithub"
)

const (
	usage = `usage: webstack [--binary bin/yumdrake-orchestrator] [--runner-binary web/.e2e-bin/webrunner] [--fixture internal/spec/testdata/valid]
                [--aws-endpoint http://localhost:4577 [--aws-runner-binary bin/yumdrake-runner]]

Needs YUMDRAKE_TEST_DATABASE_URL and YUMDRAKE_TEST_VALKEY_ADDR (see make dev-up).
With --aws-endpoint the stack runs the production runner with real steps against
that Floci instead of the fake steps: it seeds two random AWS accounts there and
adds two people (a deployer and an approver) to the printed JSON.
Exit codes: 0 stopped normally, 1 failure, 2 usage.
`

	githubToken   = "web-e2e-fake-github-token" //nolint:gosec // token of the fake GitHub only
	adminEmail    = "admin@web-e2e.example.test"
	adminName     = "Web E2E Admin"
	repo          = "acme/platform"
	branch        = "main"
	commit        = "1111111111111111111111111111111111111111"
	jobPath       = "jobs/deploy-backend.yaml"
	sessionCookie = "yumdrake_session"
	waitLimit     = 90 * time.Second
)

type ready struct {
	BaseURL  string `json:"base_url"`
	Email    string `json:"email"`
	Password string `json:"password"`
	// RunnerPID lets the browser test pause and resume the runner process.
	RunnerPID int `json:"runner_pid"`
	// AWS is set only with --aws-endpoint: the seeded world and the two other people.
	AWS *awsReady `json:"aws,omitempty"`
}

// awsReady is what the browser test needs to drive the Floci stack.
type awsReady struct {
	Endpoint        string `json:"endpoint"`
	Suffix          string `json:"suffix"`
	DevAccount      string `json:"dev_account"`
	ProdAccount     string `json:"prod_account"`
	PlatformAccount string `json:"platform_account"`
	DeployerEmail   string `json:"deployer_email"`
	DeployerName    string `json:"deployer_name"`
	DeployerPass    string `json:"deployer_password"`
	ApproverEmail   string `json:"approver_email"`
	ApproverName    string `json:"approver_name"`
	ApproverPass    string `json:"approver_password"`
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
	runnerBinary := flags.String("runner-binary", "web/.e2e-bin/webrunner", "path to the test runner (test/e2e/webrunner), which runs only fake steps; not the production bin/yumdrake-runner, which wires the real resolve and revalidate")
	fixture := flags.String("fixture", "internal/spec/testdata/valid", "directory committed to the fake GitHub repository")
	awsEndpoint := flags.String("aws-endpoint", "", "URL of a Floci to run real steps against (instead of the fake steps and the fixture)")
	awsRunnerBinary := flags.String("aws-runner-binary", "bin/yumdrake-runner", "the production runner, used with --aws-endpoint")
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

	// A write to a parent that is gone must come back as an error, not kill us before the cleanup below.
	signal.Ignore(syscall.SIGPIPE)
	// One context for the whole life of the helper, set-up included: it ends on a signal or as soon as
	// the parent closes our standard input, so a parent that dies half way through set-up still gets
	// everything started so far cleaned up.
	signalCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		cancel()
	}()

	cleanup := &cleaner{}
	defer cleanup.run()

	mode := stackMode{runnerBinary: *runnerBinary, fixture: *fixture}
	if *awsEndpoint != "" {
		mode = stackMode{runnerBinary: *awsRunnerBinary, awsEndpoint: *awsEndpoint}
	}
	out, err := bringUp(ctx, cleanup, *binary, mode, adminURL, valkeyAddr)
	if err != nil {
		logf("%v", err)
		return 1
	}
	line, _ := json.Marshal(out) //nolint:gosec // the password is generated per run and handed only to the parent over a pipe
	if _, err := fmt.Fprintln(os.Stdout, string(line)); err != nil {
		logf("the parent is gone, stopping: %v", err)
		return 1
	}
	logf("ready at %s", out.BaseURL)

	<-ctx.Done()
	logf("signal received or standard input closed, stopping")
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

func withoutYumdrakeVariables(environ []string) []string {
	kept := make([]string, 0, len(environ))
	for _, entry := range environ {
		if !strings.HasPrefix(entry, "YUMDRAKE_") {
			kept = append(kept, entry)
		}
	}
	return kept
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

// stackMode says what the runner is. With awsEndpoint empty it is the fake-step test runner and the fixture
// directory is the inventory; with it, the production runner and an inventory generated for a seeded Floci.
type stackMode struct {
	runnerBinary string
	fixture      string
	awsEndpoint  string
}

func bringUp(ctx context.Context, cleanup *cleaner, binary string, mode stackMode, adminURL, valkeyAddr string) (ready, error) {
	runnerBinary, fixture := mode.runnerBinary, mode.fixture
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
	// A new local secret key per run; it is never printed.
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		return ready{}, err
	}
	localSecretKey := base64.StdEncoding.EncodeToString(keyBytes)

	// A database of our own, dropped at the end.
	dbName := "yumdrake_web_e2e_" + suffix
	databaseURL, err := createDatabase(ctx, cleanup, adminURL, dbName)
	if err != nil {
		return ready{}, err
	}

	gh := fakegithub.New(githubToken)
	cleanup.add(gh.Close)
	var world *awsWorld
	if mode.awsEndpoint != "" {
		if world, err = seedAWS(mode.awsEndpoint, cleanup); err != nil {
			return ready{}, err
		}
		if err := loadAWSFixture(gh, world); err != nil {
			return ready{}, err
		}
	} else {
		if err := gh.LoadDir(repo, commit, fixture); err != nil {
			return ready{}, fmt.Errorf("load fixture %s: %w", fixture, err)
		}
		gh.SetHead(repo, branch, commit)
	}

	httpAddr, err := freeAddr()
	if err != nil {
		return ready{}, fmt.Errorf("find a free port: %w", err)
	}
	healthAddr, err := freeAddr()
	if err != nil {
		return ready{}, fmt.Errorf("find a free port: %w", err)
	}
	base := "http://" + httpAddr
	// Only the values set here configure the stack: a developer's own YUMDRAKE_* variables are left out.
	env := append(withoutYumdrakeVariables(os.Environ()),
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
		// No request may reach real AWS: the running versions and connection checks the screens trigger go
		// to a local port nothing listens on (the discard port) and fail at once.
		"YUMDRAKE_AWS_ENDPOINT_URL=http://127.0.0.1:9",
		// The visibility spec stops the runner and expects System health to say so; 20s is the smallest the setting allows.
		"YUMDRAKE_RUNNER_GONE_AFTER=20s",
	)
	if world != nil {
		// Real AWS calls go to Floci, with the platform account as the process's own identity (the runner writes
		// the run's artifacts with it), as the Go end-to-end tests set it up.
		env = append(env,
			"YUMDRAKE_AWS_ENDPOINT_URL="+world.endpoint,
			"AWS_ACCESS_KEY_ID="+world.stack.PlatformAccount,
			"AWS_SECRET_ACCESS_KEY="+awsKeyPassword,
			"AWS_REGION="+awstest.Region,
			"AWS_PROFILE=",
			"AWS_EC2_METADATA_DISABLED=true",
		)
	}

	// The service migrates the empty database on start.
	svc := exec.Command(binary) //nolint:gosec // the binary path is an operator-given flag
	svc.Env = env
	svc.Stdout, svc.Stderr = os.Stderr, os.Stderr
	if err := svc.Start(); err != nil {
		return ready{}, fmt.Errorf("start %s: %w", binary, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- svc.Wait() }()
	cleanup.add(func() { stopChild("orchestrator", svc, exited) })

	if err := waitReady(ctx, "the orchestrator", base+"/readyz", exited); err != nil {
		return ready{}, err
	}

	// The test runner (test/e2e/webrunner) with only fake steps, allowed only in dev: the production
	// runner wires the real resolve and revalidate, which need AWS. It shares the database, Valkey and key
	// prefix, so the runs the browser starts are executed for real, with short polling. The fake step
	// times are fixed in the test runner (2s a step, 30s for a "slow" version). The configuration still
	// demands an artifact bucket, which the fake steps never touch.
	runnerHealthAddr, err := freeAddr()
	if err != nil {
		return ready{}, fmt.Errorf("find a free port: %w", err)
	}
	runnerEnv := append(append([]string{}, env...),
		"YUMDRAKE_HEALTH_ADDR="+runnerHealthAddr,
		"YUMDRAKE_ENV=dev",
		"YUMDRAKE_FAKE_STEPS=true",
		"YUMDRAKE_ARTIFACT_BUCKET=web-e2e-unused-bucket",
		"YUMDRAKE_RUNNER_ID=web-e2e-runner",
		"YUMDRAKE_RUNNER_POLL=500ms",
	)
	if world != nil {
		// The production runner: real steps, no fake. It needs the artifact bucket of the platform account.
		runnerEnv = append(append([]string{}, env...),
			"YUMDRAKE_HEALTH_ADDR="+runnerHealthAddr,
			"YUMDRAKE_ARTIFACT_BUCKET="+world.stack.ArtifactBucket,
			"YUMDRAKE_RUNNER_ID=web-e2e-aws-runner",
			"YUMDRAKE_RUNNER_POLL=500ms",
			"YUMDRAKE_ROLLOUT_POLL=1s",
			"YUMDRAKE_ROLLOUT_TIMEOUT=3m",
		)
	}
	runnerCmd := exec.Command(runnerBinary) //nolint:gosec // the binary path is an operator-given flag
	runnerCmd.Env = runnerEnv
	runnerCmd.Stdout, runnerCmd.Stderr = os.Stderr, os.Stderr
	if err := runnerCmd.Start(); err != nil {
		return ready{}, fmt.Errorf("start %s: %w", runnerBinary, err)
	}
	runnerExited := make(chan error, 1)
	go func() { runnerExited <- runnerCmd.Wait() }()
	cleanup.add(func() { stopChild("runner", runnerCmd, runnerExited) })
	if err := waitReady(ctx, "the runner", "http://"+runnerHealthAddr+"/readyz", runnerExited); err != nil {
		return ready{}, err
	}

	// An operator's way in: the create-admin subcommand, password on standard input.
	create := exec.CommandContext(ctx, binary, "create-admin", "--email", adminEmail, "--name", adminName) //nolint:gosec // see above
	create.Env = env
	create.Stdin = strings.NewReader(password + "\n")
	var stdout, stderr bytes.Buffer
	create.Stdout, create.Stderr = &stdout, &stderr
	if err := create.Run(); err != nil {
		return ready{}, fmt.Errorf("create-admin: %w\n%s", err, stderr.String())
	}

	api := &caller{ctx: ctx, base: base}
	var cookie string
	if cookie, err = api.login(adminEmail, password); err != nil {
		return ready{}, err
	}
	api.session = cookie

	// Set-up as the Go e2e tests do it; each step syncs at once.
	// A run is refused unless the AWS account of its environment (hetzner-01, in the fixture) is connected.
	// Creating the account makes no AWS call, and the fake steps never use the keys, which are not real.
	fakeKey := map[string]any{"method": "key", "access_key_id": "AKIAWEBE2EFAKEKEY00", "secret_access_key": "web-e2e-fake-secret-key"} //nolint:gosec // a made-up key for the fake steps; no AWS call is ever made with it
	type step struct {
		method, path string
		body         any
		out          any
	}
	accountSteps := []step{{http.MethodPost, "/api/v1/admin/aws-accounts", map[string]any{"name": "web-e2e-dev", "account_id": "111111111111", "discovery": fakeKey, "deploy": fakeKey}, nil}}
	wantCommit, wantEnvironments, jobFile, folderName := commit, 5, jobPath, "Backend"
	if world != nil {
		// The seeded accounts, with keys Floci accepts: the account id is the access key id.
		accountSteps = nil
		for _, name := range []string{awstest.DevEnvironment, awstest.ProdEnvironment} {
			e := world.environment(name)
			accountSteps = append(accountSteps, step{http.MethodPost, "/api/v1/admin/aws-accounts",
				map[string]any{"name": "web-e2e-" + name, "account_id": e.Account, "discovery": awsKey(e.Account), "deploy": awsKey(e.Account)}, nil})
		}
		wantCommit, wantEnvironments, jobFile, folderName = awsCommit, 2, awsJobPath, "Everything"
	}
	steps := append(accountSteps,
		step{http.MethodPost, "/api/v1/admin/github-connections", map[string]any{"owner": "acme", "method": "token", "token": githubToken}, nil},
		step{http.MethodPut, "/api/v1/admin/inventory-source", map[string]any{"repo": repo, "branch": branch}, nil},
	)
	for _, s := range steps {
		if err := api.call(s.method, s.path, s.body, s.out); err != nil {
			return ready{}, err
		}
	}
	var folder struct {
		ID int64 `json:"id"`
	}
	if err := api.call(http.MethodPost, "/api/v1/folders", map[string]any{"name": folderName}, &folder); err != nil {
		return ready{}, err
	}
	var job struct {
		ID       int64 `json:"id"`
		Runnable bool  `json:"runnable"`
	}
	if err := api.call(http.MethodPost, "/api/v1/jobs", map[string]any{
		"folder_id": folder.ID, "repo": repo, "branch": branch, "path": jobFile}, &job); err != nil {
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
		return j.Runnable && inv.Commit == wantCommit && len(inv.Environments) == wantEnvironments, nil
	}); err != nil {
		return ready{}, err
	}
	out := ready{BaseURL: base, Email: adminEmail, Password: password, RunnerPID: runnerCmd.Process.Pid}
	if world != nil {
		if out.AWS, err = addPeople(api, world); err != nil {
			return ready{}, err
		}
	}
	return out, nil
}

// addPeople creates the two people of the Floci flow: a deployer who may run in dev and prod, and an approver who
// may approve in prod. A run needing approval is approved by a person other than the one who asked for it.
func addPeople(api *caller, world *awsWorld) (*awsReady, error) {
	out := &awsReady{
		Endpoint: world.endpoint, Suffix: world.stack.Suffix,
		DevAccount: world.opts.DevAccount, ProdAccount: world.opts.ProdAccount, PlatformAccount: world.opts.PlatformAccount,
		DeployerEmail: "deployer@web-e2e.example.test", DeployerName: "Dana Deployer",
		ApproverEmail: "approver@web-e2e.example.test", ApproverName: "Alex Approver",
	}
	var err error
	if out.DeployerPass, err = randomHex(12); err != nil {
		return nil, err
	}
	if out.ApproverPass, err = randomHex(12); err != nil {
		return nil, err
	}
	people := []struct {
		email, name, pass, group string
		grants                   [][2]string
	}{
		{out.DeployerEmail, out.DeployerName, out.DeployerPass, "Deployers", [][2]string{{"run", "dev"}, {"run", "prod"}, {"restore", "dev"}, {"view_logs", "dev"}, {"view_logs", "prod"}}},
		{out.ApproverEmail, out.ApproverName, out.ApproverPass, "Approvers", [][2]string{{"approve", "prod"}}},
	}
	for _, p := range people {
		var group, user struct {
			ID int64 `json:"id"`
		}
		if err := api.call(http.MethodPost, "/api/v1/admin/groups", map[string]any{"name": p.group}, &group); err != nil {
			return nil, err
		}
		for _, grant := range p.grants {
			if err := api.call(http.MethodPost, "/api/v1/admin/grants", map[string]any{"group_id": group.ID, "action": grant[0], "label": grant[1]}, nil); err != nil {
				return nil, err
			}
		}
		if err := api.call(http.MethodPost, "/api/v1/admin/users", map[string]any{"email": p.email, "name": p.name, "source": "local", "password": p.pass}, &user); err != nil {
			return nil, err
		}
		if err := api.call(http.MethodPatch, fmt.Sprintf("/api/v1/admin/users/%d", user.ID), map[string]any{"group_ids": []int64{group.ID}}, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
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

// stopChild asks a child process to stop and kills it if it does not.
func stopChild(name string, cmd *exec.Cmd, exited <-chan error) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case err := <-exited:
		logf("%s stopped: %v", name, err)
	case <-time.After(25 * time.Second):
		logf("%s ignored SIGTERM, killing it", name)
		_ = cmd.Process.Kill()
		logf("%s killed: %v", name, <-exited)
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

// waitReady waits for the named service ("the orchestrator", "the runner") to answer /readyz.
func waitReady(ctx context.Context, service, readyURL string, exited chan error) error {
	return waitUntil(ctx, service+" to answer /readyz", func() (bool, error) {
		select {
		case err := <-exited:
			// Put the exit back (the channel holds one): stopChild reads it too, and without it would
			// wait for a process that is already gone, then block on the kill.
			exited <- err
			return false, fmt.Errorf("%s exited early: %w", service, err)
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
	ctx     context.Context //nolint:containedctx // a small helper object for one run
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
	req, err := http.NewRequestWithContext(c.ctx, method, c.base+path, reader)
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
