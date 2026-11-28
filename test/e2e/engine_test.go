//go:build e2e

package e2e

// End-to-end proof of the run engine. Each test starts one or two
// orchestrators and two runners in-process against the Compose PostgreSQL and
// Valkey, with a fake GitHub serving a fixture inventory and job, signs in as
// an admin and drives runs through the API. The runners execute the fake steps
// (internal/steps/fake) against a fake.World that counts every pretend change.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	valkey "github.com/valkey-io/valkey-go"

	"github.com/rcss-labs/yamdrake/internal/app"
	"github.com/rcss-labs/yamdrake/internal/config"
	"github.com/rcss-labs/yamdrake/internal/db"
	"github.com/rcss-labs/yamdrake/internal/logs"
	"github.com/rcss-labs/yamdrake/internal/queue"
	"github.com/rcss-labs/yamdrake/internal/runner"
	"github.com/rcss-labs/yamdrake/internal/steps/fake"
	"github.com/rcss-labs/yamdrake/internal/store"
	"github.com/rcss-labs/yamdrake/internal/testutil/testdb"
	"github.com/rcss-labs/yamdrake/internal/wake"
	"github.com/rcss-labs/yamdrake/test/e2e/fakegithub"
)

const (
	engineCommit = "4444444444444444444444444444444444444444"
	// runWait bounds every wait for a run to reach a state.
	runWait = 90 * time.Second

	backendServices = "b1 b2 b3 b4"
	microServices   = "m1 m2 m3"
)

// ---- the fixture: one job over two groups of 4 and 3 services ----

// writeEngineFixture writes the inventory and job the engine tests share: three
// environments that all hold every service, a "backends" group of four
// services with three steps (config, roll, smoke) and a "micro" group of three
// services with two (config, roll). smokeScope is the scope of smoke's
// dependency on roll: "service" or "step".
func writeEngineFixture(t *testing.T, smokeScope string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("make %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	var environments strings.Builder
	environments.WriteString("apiVersion: yumdrake/v1\nkind: Environments\nenvironments:\n")
	for _, name := range []string{"env-a", "env-b", "env-c"} {
		fmt.Fprintf(&environments, "  - name: %s\n    label: dev\n    aws_account: \"111111111111\"\n", name)
	}
	write("environments.yaml", environments.String())
	for _, service := range strings.Fields(backendServices + " " + microServices) {
		var b strings.Builder
		fmt.Fprintf(&b, "apiVersion: yumdrake/v1\nkind: Service\nname: %s\nenvironments:\n", service)
		for _, env := range []string{"env-a", "env-b", "env-c"} {
			fmt.Fprintf(&b, "  %s:\n    ecs_service: arn:aws:ecs:eu-central-1:111111111111:service/%s/%s\n", env, env, service)
			fmt.Fprintf(&b, "    ecr_repository: 111111111111.dkr.ecr.eu-central-1.amazonaws.com/%s\n", service)
		}
		write("services/"+service+".yaml", b.String())
	}
	write("jobs/deploy-all.yaml", fmt.Sprintf(`apiVersion: yumdrake/v1
kind: Job
job: deploy-all
title: "Backends and microservices"
environment:
  labels: [dev]
groups:
  backends:
    label: Backend services
    services: [%s]
  micro:
    label: Microservices
    services: [%s]
steps:
  - name: config-backends
    use: copy-config
    group: backends
  - name: roll-backends
    use: ecs-deploy
    group: backends
    needs:
      - step: config-backends
        scope: service
  - name: smoke-backends
    use: copy-config
    group: backends
    needs:
      - step: roll-backends
        scope: %s
  - name: config-micro
    use: copy-config
    group: micro
  - name: roll-micro
    use: ecs-deploy
    group: micro
    needs:
      - step: config-micro
        scope: service
`, strings.Join(strings.Fields(backendServices), ", "), strings.Join(strings.Fields(microServices), ", "), smokeScope))
	return dir
}

// ---- the stack: orchestrators, runners, a signed-in admin ----

type engineOpts struct {
	orchestrators int
	runners       int
	stepTime      time.Duration
	slowFor       time.Duration
	lease         time.Duration
	heartbeat     time.Duration
	smokeScope    string
}

func (o engineOpts) withDefaults() engineOpts {
	if o.orchestrators == 0 {
		o.orchestrators = 1
	}
	if o.runners == 0 {
		o.runners = 2
	}
	if o.stepTime == 0 {
		o.stepTime = 150 * time.Millisecond
	}
	if o.slowFor == 0 {
		o.slowFor = 3 * time.Second
	}
	if o.lease == 0 {
		o.lease = 3 * time.Second
	}
	if o.heartbeat == 0 {
		o.heartbeat = 500 * time.Millisecond
	}
	if o.smokeScope == "" {
		o.smokeScope = "service"
	}
	return o
}

type engineStack struct {
	t           *testing.T
	opts        engineOpts
	api         client
	jobID       int64
	databaseURL string
	keyPrefix   string
	pool        *pgxpool.Pool // the test's own view of the database
	valkey      valkey.Client
	world       *fake.World
	runners     []*testRunner
}

func startEngine(t *testing.T, opts engineOpts) *engineStack {
	t.Helper()
	opts = opts.withDefaults()
	valkeyAddr := os.Getenv("YUMDRAKE_TEST_VALKEY_ADDR")
	if valkeyAddr == "" {
		t.Fatal("YUMDRAKE_TEST_VALKEY_ADDR must be set for end-to-end tests")
	}

	gh := fakegithub.New(githubToken)
	t.Cleanup(gh.Close)
	if err := gh.LoadDir(repo, engineCommit, writeEngineFixture(t, opts.smokeScope)); err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	gh.SetHead(repo, branch, engineCommit)

	s := &engineStack{t: t, opts: opts, databaseURL: testdb.New(t), keyPrefix: randomKeyPrefix(t), world: fake.NewWorld()}
	base := startEngineOrchestrator(t, s.databaseURL, s.keyPrefix, valkeyAddr, gh.URL(), true)
	s.api = signIn(t, base, s.databaseURL)
	// More orchestrators share the database and Valkey; the first one migrated.
	for i := 1; i < opts.orchestrators; i++ {
		startEngineOrchestrator(t, s.databaseURL, s.keyPrefix, valkeyAddr, gh.URL(), false)
	}

	var err error
	if s.pool, err = db.Open(context.Background(), s.databaseURL); err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(s.pool.Close)
	if s.valkey, err = valkey.NewClient(valkey.ClientOption{InitAddress: []string{valkeyAddr}, ForceSingleClient: true}); err != nil {
		t.Fatalf("open valkey: %v", err)
	}
	t.Cleanup(s.valkey.Close)

	// Set up through the API: connection, folder, inventory source, job.
	s.api.call(http.MethodPost, "/api/v1/admin/github-connections", map[string]any{"owner": "acme", "method": "token", "token": githubToken}, nil)
	var folder struct {
		ID int64 `json:"id"`
	}
	s.api.call(http.MethodPost, "/api/v1/folders", map[string]any{"name": "Everything"}, &folder)
	s.api.call(http.MethodPut, "/api/v1/admin/inventory-source", map[string]any{"repo": repo, "branch": branch}, nil)
	var job jobView
	s.api.call(http.MethodPost, "/api/v1/jobs", map[string]any{"folder_id": folder.ID, "repo": repo, "branch": branch, "path": "jobs/deploy-all.yaml"}, &job)
	if !s.api.job(job.ID).Runnable {
		t.Fatal("the fixture job is not runnable")
	}
	s.jobID = job.ID

	for i := range opts.runners {
		s.startRunner(fmt.Sprintf("runner-%c", 'a'+i))
	}
	return s
}

// startEngineOrchestrator runs one orchestrator through app.Run, as
// cmd/orchestrator does. Orchestrators started against one database share a
// Valkey key prefix (and so their sessions and wake-ups) like replicas would.
func startEngineOrchestrator(t *testing.T, databaseURL, keyPrefix, valkeyAddr, githubURL string, migrate bool) (base string) {
	t.Helper()
	addr := freeAddr(t)
	env := map[string]string{
		"YUMDRAKE_DATABASE_URL":      databaseURL,
		"YUMDRAKE_VALKEY_ADDR":       valkeyAddr,
		"YUMDRAKE_HTTP_ADDR":         addr,
		"YUMDRAKE_LOCAL_SECRET_KEY":  localSecretKey,
		"YUMDRAKE_PUBLIC_URL":        "http://" + addr,
		"YUMDRAKE_VALKEY_KEY_PREFIX": keyPrefix,
		"YUMDRAKE_SYNC_INTERVAL":     syncInterval.String(),
		"YUMDRAKE_GITHUB_API_URL":    githubURL,
		"YUMDRAKE_LOG_LEVEL":         "error",
		"YUMDRAKE_SWEEP_INTERVAL":    "300ms",
	}
	if !migrate {
		env["YUMDRAKE_MIGRATE_ON_START"] = "false"
	}
	cfg, err := config.LoadOrchestrator(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("load orchestrator config: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	exited := make(chan int, 1)
	go func() { exited <- app.Run(ctx, app.NewOrchestratorService(cfg), io.Discard) }()
	t.Cleanup(func() {
		cancel()
		select {
		case code := <-exited:
			if code != app.ExitOK {
				t.Errorf("orchestrator exit code = %d, want %d", code, app.ExitOK)
			}
		case <-time.After(30 * time.Second):
			t.Error("orchestrator did not stop within 30s")
		}
	})
	base = "http://" + addr
	waitFor(t, "the orchestrator to answer /readyz", func() bool {
		resp, err := http.Get(base + "/readyz") //nolint:gosec,noctx // local test server
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
	return base
}

// testRunner is one in-process runner built the way cmd/runner builds it, with
// its own database pool (as a separate process would have).
type testRunner struct {
	id     string
	pool   *pgxpool.Pool
	sink   *logs.Writer
	cancel context.CancelFunc
	done   chan struct{}
}

func (s *engineStack) startRunner(id string) *testRunner {
	t := s.t
	t.Helper()
	pool, err := db.Open(context.Background(), s.databaseURL)
	if err != nil {
		t.Fatalf("open database for %s: %v", id, err)
	}
	quiet := slog.New(slog.DiscardHandler)
	bus := wake.New(s.valkey, quiet, wake.WithKeyPrefix(s.keyPrefix))
	sink := logs.NewWriter(pool, 100*time.Millisecond, 100, time.Now, func(runID int64) {
		bus.Publish(context.Background(), wake.TopicLogs, runID)
	}, quiet)
	q := queue.New(pool, s.opts.lease, nil) // nil: the database clock, as in production
	r := runner.New(q, sink, id, 8, s.opts.heartbeat, 100*time.Millisecond, quiet).
		WithShutdownTimeout(10 * time.Second).
		WithSettledNotifier(func(runID int64) { bus.Publish(context.Background(), wake.TopicRuns, runID) })
	fake.Register(r, fake.Options{StepTime: s.opts.stepTime, SlowFor: s.opts.slowFor, World: s.world})

	ctx, cancel := context.WithCancel(context.Background())
	tr := &testRunner{id: id, pool: pool, sink: sink, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(tr.done)
		_ = r.Run(ctx, nil)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-tr.done:
		case <-time.After(30 * time.Second):
			t.Errorf("runner %s did not stop within 30s", id)
		}
		closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = sink.Close(closeCtx) // fails for a killed runner: its database is gone
		pool.Close()
	})
	s.runners = append(s.runners, tr)
	return tr
}

// kill simulates the runner's process dying with a leg in hand. Cancelling its
// context would not do: that is a graceful shutdown, in which the runner
// releases its legs and says so. Instead the runner keeps running, never told
// to stop, but every connection it has to the database is closed: its
// heartbeats, its log flushes, its result write and its release all fail, as
// they would from a process that no longer exists. Its executor, like a call
// already on the wire, may still change the pretend external system once.
func (r *testRunner) kill() { r.pool.Close() }

// ---- API helpers ----

type legInfo struct {
	ID           int64      `json:"id"`
	Step         string     `json:"step"`
	Kind         string     `json:"kind"`
	Service      string     `json:"service"`
	Version      string     `json:"version"`
	State        string     `json:"state"`
	Attempt      int        `json:"attempt"`
	FailureShort string     `json:"failure_short"`
	QueuedAt     *time.Time `json:"queued_at"`
	StartedAt    *time.Time `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
}

type runInfo struct {
	ID         int64      `json:"id"`
	State      string     `json:"state"`
	WaitReason string     `json:"wait_reason"`
	EndReason  string     `json:"end_reason"`
	FinishedAt *time.Time `json:"finished_at"`
	Locks      []string   `json:"locks"`
	Legs       []legInfo  `json:"legs"`
	Summary    struct {
		Counts   map[string]int `json:"counts"`
		Failures []struct {
			LegID   int64  `json:"leg_id"`
			Service string `json:"service"`
			Step    string `json:"step"`
			State   string `json:"state"`
			Short   string `json:"short"`
		} `json:"failures"`
	} `json:"summary"`
}

// leg finds the step leg of a service in a step.
func (r runInfo) leg(t *testing.T, step, service string) legInfo {
	t.Helper()
	for _, l := range r.Legs {
		if l.Step == step && l.Service == service {
			return l
		}
	}
	t.Fatalf("run %d has no leg for service %q in step %q", r.ID, service, step)
	return legInfo{}
}

func (s *engineStack) run(id int64) runInfo {
	s.t.Helper()
	var info runInfo
	s.api.call(http.MethodGet, fmt.Sprintf("/api/v1/runs/%d", id), nil, &info)
	return info
}

// selection picks the given services (a version override per service, in
// "name" or "name=version" form) and gives each group a default version.
func selection(version string, picks ...string) map[string]any {
	groups := map[string]map[string]string{"backends": {}, "micro": {}}
	for _, pick := range picks {
		name, override, _ := strings.Cut(pick, "=")
		group := "micro"
		if strings.HasPrefix(name, "b") {
			group = "backends"
		}
		groups[group][name] = override
	}
	out := map[string]any{}
	for group, services := range groups {
		if len(services) > 0 {
			out[group] = map[string]any{"version": version, "services": services}
		}
	}
	return out
}

func (s *engineStack) createRun(env string, sel map[string]any, start bool) int64 {
	s.t.Helper()
	var created struct {
		ID int64 `json:"id"`
	}
	s.api.call(http.MethodPost, fmt.Sprintf("/api/v1/jobs/%d/runs", s.jobID),
		map[string]any{"environment": env, "selection": sel, "start": start}, &created)
	return created.ID
}

func (s *engineStack) post(path string, body any) {
	s.t.Helper()
	s.api.call(http.MethodPost, path, body, nil)
}

// eventually polls cond until it holds or the timeout passes.
func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	end := time.Now().Add(timeout)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// waitRun waits until the run is in one of the states and returns it.
func (s *engineStack) waitRun(id int64, states ...string) runInfo {
	s.t.Helper()
	var last runInfo
	eventually(s.t, runWait, fmt.Sprintf("run %d to be %s", id, strings.Join(states, " or ")), func() bool {
		last = s.run(id)
		for _, state := range states {
			if last.State == state {
				return true
			}
		}
		return false
	})
	return last
}

type logLine struct {
	LegID int64  `json:"leg_id"`
	Seq   int64  `json:"seq"`
	Level string `json:"level"`
	Text  string `json:"text"`
}

// fullLog reads a run's whole log through the API, page by page.
func (s *engineStack) fullLog(runID int64) []logLine {
	s.t.Helper()
	var all []logLine
	after := ""
	for {
		path := fmt.Sprintf("/api/v1/runs/%d/log?limit=1000", runID)
		if after != "" {
			path += "&after=" + url.QueryEscape(after)
		}
		var page struct {
			Lines []logLine `json:"lines"`
			Next  string    `json:"next"`
		}
		s.api.call(http.MethodGet, path, nil, &page)
		if len(page.Lines) == 0 {
			return all
		}
		all = append(all, page.Lines...)
		after = page.Next
	}
}

func countLines(lines []logLine, legID int64, contains string) int {
	n := 0
	for _, l := range lines {
		if l.LegID == legID && strings.Contains(l.Text, contains) {
			n++
		}
	}
	return n
}

func allServices() []string { return strings.Fields(backendServices + " " + microServices) }

// ---- the tests ----

func TestRunSucceedsEndToEnd(t *testing.T) {
	s := startEngine(t, engineOpts{})
	id := s.createRun("env-a", selection("v1.0.0", allServices()...), false)

	planned := s.waitRun(id, "planned")
	if got := planned.Summary.Counts["succeeded"]; got != 1 || len(planned.Legs) != 1+18 {
		t.Fatalf("planned run: %d succeeded of %d legs, want only the resolve leg done of 19", got, len(planned.Legs))
	}
	if len(planned.Locks) != 0 {
		t.Errorf("a planned run holds locks %v, want none", planned.Locks)
	}
	s.post(fmt.Sprintf("/api/v1/runs/%d/start", id), nil)
	done := s.waitRun(id, "succeeded")

	if len(done.Legs) != 20 { // resolve, revalidate and 4*3 + 3*2 step legs
		t.Errorf("run has %d legs, want 20", len(done.Legs))
	}
	for _, l := range done.Legs {
		if l.State != "succeeded" || l.Attempt != 1 {
			t.Errorf("leg %d (%s %s %s) is %s after attempt %d, want succeeded at attempt 1", l.ID, l.Kind, l.Step, l.Service, l.State, l.Attempt)
		}
	}
	if len(done.Locks) != 0 {
		t.Errorf("a finished run still holds locks %v", done.Locks)
	}

	lines := s.fullLog(id)
	for _, l := range done.Legs {
		if countLines(lines, l.ID, "") == 0 {
			t.Errorf("the log has no line for leg %d (%s %s %s)", l.ID, l.Kind, l.Step, l.Service)
		}
	}
	for _, transition := range []string{
		"Run created", "moved from resolving to planned", "moved from planned to queued",
		"moved from queued to running", "moved from running to succeeded",
	} {
		if countLines(lines, 0, transition) != 1 {
			t.Errorf("the run-level log has %d lines containing %q, want 1", countLines(lines, 0, transition), transition)
		}
	}
}

// runWithFailingService runs every service with b2 failing its deploy and
// returns the finished run. smokeScope is how smoke depends on roll.
func runWithFailingService(t *testing.T, smokeScope string) runInfo {
	t.Helper()
	s := startEngine(t, engineOpts{smokeScope: smokeScope})
	picks := append([]string{"b2=v1.0.0-fail"}, "b1", "b3", "b4", "m1", "m2", "m3")
	id := s.createRun("env-a", selection("v1.0.0", picks...), true)
	done := s.waitRun(id, "partially_succeeded", "succeeded", "failed")
	if done.State != "partially_succeeded" {
		t.Fatalf("run ended %s, want partially_succeeded", done.State)
	}
	if len(done.Locks) != 0 {
		t.Errorf("a finished run still holds locks %v", done.Locks)
	}
	if len(done.Summary.Failures) != 1 {
		t.Fatalf("summary names %d failures, want 1: %+v", len(done.Summary.Failures), done.Summary.Failures)
	}
	f := done.Summary.Failures[0]
	if f.Service != "b2" || f.Step != "roll-backends" || f.Short != "health checks failed" || f.State != "failed" {
		t.Errorf("failure = %+v, want service b2, step roll-backends, health checks failed", f)
	}
	if st := done.leg(t, "config-backends", "b2").State; st != "succeeded" {
		t.Errorf("b2's config leg is %s, want succeeded (it ran before the failure)", st)
	}
	for _, service := range strings.Fields(microServices) {
		for _, step := range []string{"config-micro", "roll-micro"} {
			if st := done.leg(t, step, service).State; st != "succeeded" {
				t.Errorf("%s in %s is %s, want succeeded: another group is unaffected", service, step, st)
			}
		}
	}
	return done
}

func TestOneServiceFailsOthersContinue(t *testing.T) {
	done := runWithFailingService(t, "service")
	if st := done.leg(t, "smoke-backends", "b2").State; st != "skipped" {
		t.Errorf("b2's smoke leg is %s, want skipped", st)
	}
	for _, service := range []string{"b1", "b3", "b4"} {
		for _, step := range []string{"roll-backends", "smoke-backends"} {
			if st := done.leg(t, step, service).State; st != "succeeded" {
				t.Errorf("%s in %s is %s, want succeeded: a service-scoped dependency only skips the failing service", service, step, st)
			}
		}
	}
	if done.Summary.Counts["skipped"] != 1 {
		t.Errorf("%d legs skipped, want only b2's smoke leg", done.Summary.Counts["skipped"])
	}
}

func TestStepScopedBarrierSkipsWholeStep(t *testing.T) {
	done := runWithFailingService(t, "step")
	for _, service := range strings.Fields(backendServices) {
		if st := done.leg(t, "smoke-backends", service).State; st != "skipped" {
			t.Errorf("%s's smoke leg is %s, want skipped: a step-scoped dependency waits for the whole step", service, st)
		}
	}
	for _, service := range []string{"b1", "b3", "b4"} {
		if st := done.leg(t, "roll-backends", service).State; st != "succeeded" {
			t.Errorf("%s's roll leg is %s, want succeeded", service, st)
		}
	}
}

func TestRunnerKilledMidLegIsResumedByTheOther(t *testing.T) {
	s := startEngine(t, engineOpts{slowFor: 1500 * time.Millisecond})
	const (
		slowVersion = "v1.0.0-slow"
		configKey   = "copy-config/m1/" + slowVersion
		rollKey     = "ecs-deploy/m1/" + slowVersion
	)
	id := s.createRun("env-a", selection("v1.0.0", "m1="+slowVersion), true)

	// Wait for a runner to be inside the slow config leg with its change
	// intended (recorded) but not yet made: the moment a crash is worst.
	var victim store.Leg
	eventually(t, runWait, "a runner to hold the slow leg with its change intended", func() bool {
		legs, err := store.LegsOfRun(context.Background(), s.pool, id)
		if err != nil {
			t.Fatalf("read legs: %v", err)
		}
		for _, l := range legs {
			if l.Kind != "step" || l.State != "running" {
				continue
			}
			ops, err := store.OpsOfLeg(context.Background(), s.pool, l.ID)
			if err != nil {
				t.Fatalf("read ops: %v", err)
			}
			if len(ops) == 1 && ops[0].State == "intended" {
				victim = l
				return true
			}
		}
		return false
	})
	if s.world.Count(configKey) != 0 {
		t.Fatalf("the change was already made when the runner was killed; the test needs it in flight")
	}
	var killed *testRunner
	for _, r := range s.runners {
		if r.id == victim.RunnerID {
			killed = r
		}
	}
	if killed == nil {
		t.Fatalf("no runner is named %q", victim.RunnerID)
	}
	killed.kill()

	done := s.waitRun(id, "succeeded")
	resumed := done.leg(t, "config-micro", "m1")
	if resumed.ID != victim.ID || resumed.Attempt != 2 {
		t.Errorf("leg %d is at attempt %d, want the killed leg %d at attempt 2", resumed.ID, resumed.Attempt, victim.ID)
	}
	takenOver, err := store.GetLeg(context.Background(), s.pool, victim.ID)
	if err != nil {
		t.Fatalf("read leg: %v", err)
	}
	if takenOver.RunnerID == killed.id || takenOver.LeaseExpiries != 1 {
		t.Errorf("leg finished on runner %q after %d lease expiries, want the other runner after 1", takenOver.RunnerID, takenOver.LeaseExpiries)
	}
	want := fmt.Sprintf("attempt 2: a previous attempt by runner %s did not finish; continuing from its last checkpoint", killed.id)
	if got := countLines(s.fullLog(id), victim.ID, want); got != 1 {
		t.Errorf("the leg's log has the continuation line %d times, want once: %q", got, want)
	}
	// The pretend change made by the dead runner's in-flight call is the only
	// one: the survivor saw it was intended, checked the world and did not repeat it.
	if got := s.world.Count(configKey); got != 1 {
		t.Errorf("%s was applied %d times, want exactly once", configKey, got)
	}
	if got := s.world.Count(rollKey); got != 1 {
		t.Errorf("%s was applied %d times, want exactly once", rollKey, got)
	}
}

func TestSecondRunWaitsThenProceeds(t *testing.T) {
	s := startEngine(t, engineOpts{slowFor: 2 * time.Second})
	first := s.createRun("env-a", selection("v1.0.0", "m1=v1.0.0-slow"), true)
	holding := s.waitRun(first, "running")
	if len(holding.Locks) == 0 {
		t.Fatal("the first run is running without locks")
	}

	second := s.createRun("env-a", selection("v2.0.0", "m1"), true)
	waiting := s.waitRun(second, "queued")
	eventually(t, runWait, "the second run to say whom it waits for", func() bool {
		waiting = s.run(second)
		return waiting.WaitReason != ""
	})
	if want := fmt.Sprintf("waiting for run #%d", first); waiting.WaitReason != want {
		t.Errorf("wait reason = %q, want %q", waiting.WaitReason, want)
	}
	if len(waiting.Locks) != 0 {
		t.Errorf("the waiting run holds locks %v: locks are all or nothing", waiting.Locks)
	}
	for _, l := range waiting.Legs {
		if l.Kind == "step" && l.State != "pending" {
			t.Errorf("the waiting run's leg %s %s is %s, want pending", l.Step, l.Service, l.State)
		}
	}

	firstDone := s.waitRun(first, "succeeded")
	secondDone := s.waitRun(second, "succeeded")
	if firstDone.FinishedAt == nil {
		t.Fatal("the first run has no finish time")
	}
	for _, l := range secondDone.Legs {
		if l.Kind == "step" && (l.StartedAt == nil || l.StartedAt.Before(*firstDone.FinishedAt)) {
			t.Errorf("the second run's leg %s %s started at %v, before the first run finished at %v", l.Step, l.Service, l.StartedAt, firstDone.FinishedAt)
		}
	}
	if len(secondDone.Locks) != 0 || len(firstDone.Locks) != 0 {
		t.Errorf("locks remain after both runs finished: %v %v", firstDone.Locks, secondDone.Locks)
	}
}

func TestCancelMidRun(t *testing.T) {
	s := startEngine(t, engineOpts{stepTime: 400 * time.Millisecond})
	id := s.createRun("env-a", selection("v1.0.0", allServices()...), true)

	var before runInfo
	eventually(t, runWait, "some step legs to have finished and others not", func() bool {
		before = s.run(id)
		done := 0
		for _, l := range before.Legs {
			if l.Kind == "step" && l.State == "succeeded" {
				done++
			}
		}
		return before.State == "running" && done >= 3
	})
	finished := map[int64]bool{}
	for _, l := range before.Legs {
		if l.State == "succeeded" {
			finished[l.ID] = true
		}
	}

	s.post(fmt.Sprintf("/api/v1/runs/%d/cancel", id), nil)
	acknowledged := time.Now()
	done := s.waitRun(id, "cancelled", "succeeded", "partially_succeeded", "failed")
	if done.State != "cancelled" {
		t.Fatalf("run ended %s, want cancelled", done.State)
	}
	if len(done.Locks) != 0 {
		t.Errorf("a cancelled run still holds locks %v", done.Locks)
	}
	untouched := 0
	for _, l := range done.Legs {
		if finished[l.ID] && l.State != "succeeded" {
			t.Errorf("leg %d (%s %s) was succeeded before the cancel and is %s now", l.ID, l.Step, l.Service, l.State)
		}
		// No leg is queued once the cancel is acknowledged. A leg that marks
		// itself running after the acknowledgement was claimed before the
		// cancel; the cancel was committed first, so the runner sees the flag
		// after MarkRunning and settles the leg cancelled without executing it.
		// Legs started before the cancel was committed may finish normally.
		if l.QueuedAt != nil && l.QueuedAt.After(acknowledged) {
			t.Errorf("leg %d (%s %s) was queued at %v, after the cancel was acknowledged at %v", l.ID, l.Step, l.Service, l.QueuedAt, acknowledged)
		}
		if l.StartedAt != nil && l.StartedAt.After(acknowledged) && l.State != "cancelled" {
			t.Errorf("leg %d (%s %s) started at %v, after the cancel was acknowledged at %v, and is %s, want cancelled without executing", l.ID, l.Step, l.Service, l.StartedAt, acknowledged, l.State)
		}
		switch l.State {
		case "pending", "queued", "claimed", "running":
			t.Errorf("leg %d (%s %s) is %s in a finished run", l.ID, l.Step, l.Service, l.State)
		}
		if l.StartedAt == nil {
			untouched++
		}
	}
	if untouched == 0 {
		t.Error("every leg had started before the cancel; the test did not cancel mid-run")
	}
}

func TestUnknownOutcomeNeedsReconciliationThenFinishes(t *testing.T) {
	s := startEngine(t, engineOpts{})
	id := s.createRun("env-a", selection("v1.0.0", "m1", "m2=v1.0.0-unknown", "m3"), true)

	held := s.waitRun(id, "needs_reconciliation")
	if len(held.Locks) == 0 {
		t.Fatal("a run in needs_reconciliation holds no locks")
	}
	if len(held.Summary.Failures) != 1 || held.Summary.Failures[0].State != "unknown" || held.Summary.Failures[0].Service != "m2" {
		t.Fatalf("failures = %+v, want m2's deploy unknown", held.Summary.Failures)
	}
	unknownLeg := held.Summary.Failures[0].LegID
	// It stays put, locks held, across several sweeps of the orchestrator.
	for range 10 {
		time.Sleep(200 * time.Millisecond)
		if now := s.run(id); now.State != "needs_reconciliation" || len(now.Locks) != len(held.Locks) {
			t.Fatalf("run is %s with %d locks, want needs_reconciliation with %d", now.State, len(now.Locks), len(held.Locks))
		}
	}

	s.post(fmt.Sprintf("/api/v1/runs/%d/legs/%d/reconcile", id, unknownLeg), map[string]string{"outcome": "succeeded"})
	done := s.waitRun(id, "succeeded")
	if len(done.Locks) != 0 {
		t.Errorf("the finished run still holds locks %v", done.Locks)
	}
	if st := done.leg(t, "roll-micro", "m2").State; st != "succeeded" {
		t.Errorf("the reconciled leg is %s, want succeeded", st)
	}
}

func TestRunWaitsForRunInNeedsReconciliation(t *testing.T) {
	s := startEngine(t, engineOpts{})
	a := s.createRun("env-a", selection("v1.0.0", "m1", "m2=v1.0.0-unknown", "m3"), true)
	held := s.waitRun(a, "needs_reconciliation")
	if len(held.Locks) == 0 || len(held.Summary.Failures) != 1 {
		t.Fatalf("run A holds %d locks with %d failures, want locks held and one unknown leg", len(held.Locks), len(held.Summary.Failures))
	}
	unknownLeg := held.Summary.Failures[0].LegID

	// Run B wants one of A's services: it must wait, holding nothing, for as
	// long as A sits in needs_reconciliation.
	b := s.createRun("env-a", selection("v2.0.0", "m2"), true)
	waitReason := fmt.Sprintf("waiting for run #%d", a)
	eventually(t, runWait, "run B to say whom it waits for", func() bool { return s.run(b).WaitReason == waitReason })
	for range 15 {
		time.Sleep(200 * time.Millisecond)
		waiting, holder := s.run(b), s.run(a)
		if waiting.State != "queued" || waiting.WaitReason != waitReason || len(waiting.Locks) != 0 {
			t.Fatalf("run B is %s (%q) with %d locks, want queued (%q) with none", waiting.State, waiting.WaitReason, len(waiting.Locks), waitReason)
		}
		if holder.State != "needs_reconciliation" || len(holder.Locks) == 0 {
			t.Fatalf("run A is %s with %d locks, want needs_reconciliation with its locks held", holder.State, len(holder.Locks))
		}
		for _, l := range waiting.Legs {
			if l.Kind == "step" && l.State != "pending" {
				t.Fatalf("run B's leg %s %s is %s while it waits, want pending", l.Step, l.Service, l.State)
			}
		}
	}

	s.post(fmt.Sprintf("/api/v1/runs/%d/legs/%d/reconcile", a, unknownLeg), map[string]string{"outcome": "succeeded"})
	aDone := s.waitRun(a, "succeeded")
	bDone := s.waitRun(b, "succeeded")
	if aDone.FinishedAt == nil {
		t.Fatal("run A has no finish time")
	}
	for _, l := range bDone.Legs {
		if l.Kind == "step" && (l.StartedAt == nil || l.StartedAt.Before(*aDone.FinishedAt)) {
			t.Errorf("run B's leg %s %s started at %v, before run A finished at %v", l.Step, l.Service, l.StartedAt, aDone.FinishedAt)
		}
	}
	if len(aDone.Locks) != 0 || len(bDone.Locks) != 0 {
		t.Errorf("locks remain after both runs finished: %v %v", aDone.Locks, bDone.Locks)
	}
}

// sseEvent is one Server-Sent Event.
type sseEvent struct{ name, id, data string }

// readEvents opens the run's event stream (resuming after lastEventID when it
// is not empty) and returns the events until stop says so or the stream ends.
func readEvents(t *testing.T, c client, runID int64, lastEventID string, stop func([]sseEvent) bool) []sseEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), runWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/v1/runs/%d/events", c.base, runID), nil)
	if err != nil {
		t.Fatalf("build stream request: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: "yumdrake_session", Value: c.session})
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream answered %d %q, want 200 text/event-stream", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var events []sseEvent
	var current sseEvent
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if current.name != "" {
				events = append(events, current)
				if stop(events) {
					return events
				}
			}
			current = sseEvent{}
		case strings.HasPrefix(line, "event: "):
			current.name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "id: "):
			current.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "data: "):
			current.data = strings.TrimPrefix(line, "data: ")
		}
	}
	return events
}

func TestStreamFollowsRunAndSurvivesReconnect(t *testing.T) {
	s := startEngine(t, engineOpts{stepTime: 400 * time.Millisecond})
	id := s.createRun("env-a", selection("v1.0.0", "b1", "b2", "m1", "m2"), true)

	// First connection: read until some log lines have arrived, then drop it.
	logEvents := func(events []sseEvent) (n int) {
		for _, e := range events {
			if e.name == "log" {
				n++
			}
		}
		return n
	}
	first := readEvents(t, s.api, id, "", func(events []sseEvent) bool { return logEvents(events) >= 2 })
	if first[0].name != "state" {
		t.Fatalf("the first event is %q, want state", first[0].name)
	}
	cursor := ""
	for _, e := range first {
		if e.name == "end" {
			t.Fatal("the run was over before the connection was dropped; the test needs it in progress")
		}
		if e.name == "log" {
			if e.id == "" {
				t.Fatal("a log event has no id to resume from")
			}
			cursor = e.id
		}
	}

	// Reconnect the way EventSource does: Last-Event-ID is the last log id seen.
	second := readEvents(t, s.api, id, cursor, func(events []sseEvent) bool { return events[len(events)-1].name == "end" })
	if second[0].name != "state" {
		t.Errorf("after the reconnect the first event is %q, want state", second[0].name)
	}
	last := second[len(second)-1]
	if last.name != "end" {
		t.Fatalf("the stream ended with %q, want end", last.name)
	}
	var final struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(last.data), &final); err != nil || final.State != "succeeded" {
		t.Errorf("the end event carries state %q (%v), want succeeded", final.State, err)
	}

	// Every line arrived once, across the two connections, and nothing is missing.
	type position struct{ leg, seq int64 }
	seen := map[position]bool{}
	for _, e := range append(first, second...) {
		if e.name != "log" {
			continue
		}
		var page struct {
			Lines []logLine `json:"lines"`
		}
		if err := json.Unmarshal([]byte(e.data), &page); err != nil {
			t.Fatalf("decode log event: %v", err)
		}
		for _, l := range page.Lines {
			p := position{l.LegID, l.Seq}
			if seen[p] {
				t.Errorf("line %d of leg %d was delivered twice", l.Seq, l.LegID)
			}
			seen[p] = true
		}
	}
	stored := s.fullLog(id)
	for _, l := range stored {
		if !seen[position{l.LegID, l.Seq}] {
			t.Errorf("line %d of leg %d is in the log but was never streamed", l.Seq, l.LegID)
		}
	}
	if len(seen) != len(stored) {
		t.Errorf("streamed %d distinct lines, the log has %d", len(seen), len(stored))
	}
}

func TestTwoOrchestratorsProduceOneConsistentRun(t *testing.T) {
	s := startEngine(t, engineOpts{orchestrators: 2})
	// Six runs over three environments: each environment's second run waits for
	// its first, so both orchestrators keep advancing runs in every state.
	var ids []int64
	for round, version := range []string{"v1.0.0", "v2.0.0"} {
		for _, env := range []string{"env-a", "env-b", "env-c"} {
			ids = append(ids, s.createRun(env, selection(version+"-"+env, "m1", "m2", "m3"), true))
		}
		if round == 0 { // the second round must find the first holding its locks
			for _, first := range ids {
				s.waitRun(first, "running", "succeeded")
			}
		}
	}
	for _, id := range ids {
		done := s.waitRun(id, "succeeded")
		if len(done.Locks) != 0 {
			t.Errorf("run %d still holds locks %v", id, done.Locks)
		}
		revalidations := 0
		for _, l := range done.Legs {
			if l.Attempt != 1 || l.State != "succeeded" {
				t.Errorf("run %d leg %d (%s %s %s) is %s at attempt %d, want succeeded at attempt 1", id, l.ID, l.Kind, l.Step, l.Service, l.State, l.Attempt)
			}
			if l.Kind == "revalidate" {
				revalidations++
			}
		}
		if revalidations != 1 {
			t.Errorf("run %d has %d revalidate legs, want 1", id, revalidations)
		}
		lines := s.fullLog(id)
		for _, transition := range []string{"moved from resolving to queued", "moved from queued to running", "moved from running to succeeded"} {
			if n := countLines(lines, 0, transition); n != 1 {
				t.Errorf("run %d's log has %d lines containing %q, want 1", id, n, transition)
			}
		}
	}
	// Every pretend change (use, service, version) was made exactly once:
	// 6 runs x 3 services x 2 steps.
	keys := s.world.Keys()
	if len(keys) != 36 {
		t.Errorf("%d distinct changes were made, want 36: %v", len(keys), keys)
	}
	for _, key := range keys {
		if n := s.world.Count(key); n != 1 {
			t.Errorf("%s was applied %d times, want once", key, n)
		}
	}
}
