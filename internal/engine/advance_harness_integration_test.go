//go:build integration

package engine

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rcss-labs/yamdrake/internal/auth"
	"github.com/rcss-labs/yamdrake/internal/db"
	"github.com/rcss-labs/yamdrake/internal/spec"
	"github.com/rcss-labs/yamdrake/internal/store"
	"github.com/rcss-labs/yamdrake/internal/testutil/awsseed"
	"github.com/rcss-labs/yamdrake/internal/testutil/testdb"
)

const (
	testEnvironment = "staging"
	testLabel       = "dev"
	testAWSAccount  = "111122223333"
)

// testClock is a clock the tests move by hand.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// recordingPublisher remembers every wake-up.
type recordingPublisher struct {
	mu     sync.Mutex
	events []string
}

func (p *recordingPublisher) Publish(_ context.Context, topic string, id int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, fmt.Sprintf("%s:%d", topic, id))
}

func (p *recordingPublisher) has(topic string, id int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.events {
		if e == fmt.Sprintf("%s:%d", topic, id) {
			return true
		}
	}
	return false
}

type harnessOptions struct {
	approvalFor    []string // job.approval.when.labels
	approvalExpiry time.Duration
	noInventory    bool
	limits         Limits
	slackChannel   string // job.notify.slack; empty means no notifications
}

// harness is a migrated database with a job, an inventory and some people.
type harness struct {
	t        *testing.T
	pool     *pgxpool.Pool
	eng      *Engine
	clock    *testClock
	pub      *recordingPublisher
	folderID int64
	sourceID int64
	snapshot store.Snapshot
	job      spec.Job

	admin, starter, approver, starterApprover, stranger auth.Principal
}

func newHarness(t *testing.T, opts harnessOptions) *harness {
	t.Helper()
	ctx := context.Background()
	url := testdb.New(t)
	if err := db.Migrate(ctx, url); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(pool.Close)

	h := &harness{t: t, pool: pool, clock: &testClock{now: time.Now().UTC().Truncate(time.Microsecond)}, pub: &recordingPublisher{}}

	person := func(name string, admin bool, actions ...string) auth.Principal {
		user, err := store.CreateLocalUser(ctx, pool, name+"@example.com", name, "local", "")
		if err != nil {
			t.Fatalf("CreateLocalUser %s: %v", name, err)
		}
		p := auth.Principal{UserID: user.ID, Email: user.Email, Name: name, Admin: admin}
		for _, action := range actions {
			p.Grants = append(p.Grants, store.Grant{Action: action})
		}
		// The engine re-resolves the requester from the database when a queued
		// run starts, so the person's group and grants are stored as well.
		group, err := store.CreateGroup(ctx, pool, name+"-group", admin)
		if err != nil {
			t.Fatalf("CreateGroup %s: %v", name, err)
		}
		if err := store.SetManualMembership(ctx, pool, user.ID, group.ID, true); err != nil {
			t.Fatalf("SetManualMembership %s: %v", name, err)
		}
		for _, action := range actions {
			if _, err := store.CreateGrant(ctx, pool, store.Grant{GroupID: group.ID, Action: action}); err != nil {
				t.Fatalf("CreateGrant %s %s: %v", name, action, err)
			}
		}
		return p
	}
	h.admin = person("admin", true)
	h.starter = person("starter", false, "run", "cancel", "restore")
	h.approver = person("approver", false, "approve")
	h.starterApprover = person("starter-approver", false, "run", "approve")
	h.stranger = person("stranger", false)

	folder, err := store.CreateFolder(ctx, pool, "services")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	h.folderID = folder.ID

	inv := spec.Inventory{
		Environments: []spec.Environment{{Name: testEnvironment, Label: testLabel, AWSAccount: testAWSAccount}},
		Services:     map[string]spec.Service{},
	}
	var names []string
	for i := 1; i <= 8; i++ {
		name := fmt.Sprintf("s%d", i)
		names = append(names, name)
		inv.Services[name] = spec.Service{Name: name, Environments: map[string]spec.ServiceEnv{
			testEnvironment: {
				ECSService: fmt.Sprintf("arn:aws:ecs:eu-west-1:%s:service/cluster/%s-staging", testAWSAccount, name),
				Tomls:      []spec.Toml{{Name: "app", Destination: fmt.Sprintf("s3://cfg/staging/%s.toml", name)}},
			},
		}}
	}
	h.job = spec.Job{
		ID: "release", Title: "Release", Labels: []string{testLabel},
		Groups: []spec.Group{{ID: "g", Services: names}},
		Steps: []spec.Step{
			{Name: "config", Use: "copy-config", Group: "g"},
			{Name: "deploy", Use: "ecs-deploy", Group: "g", Needs: []spec.Need{{Step: "config", Scope: "service"}}},
		},
	}
	h.job.SlackChannel = opts.slackChannel
	if len(opts.approvalFor) > 0 {
		h.job.Approval = &spec.Approval{Labels: opts.approvalFor, Expires: opts.approvalExpiry}
	}

	source, err := store.CreateJobSource(ctx, pool, store.JobSource{FolderID: folder.ID, Repo: "acme/jobs", Branch: "main", Path: "release.yaml"})
	if err != nil {
		t.Fatalf("CreateJobSource: %v", err)
	}
	h.sourceID = source.ID
	if err := store.SaveJobGood(ctx, pool, source.ID, mustJSON(t, h.job), "", h.job.ID, h.job.Title, "jobcommit1", h.clock.Now()); err != nil {
		t.Fatalf("SaveJobGood: %v", err)
	}
	if err := store.SaveJobProblems(ctx, pool, source.ID, nil, "jobcommit1", true, h.clock.Now()); err != nil {
		t.Fatalf("SaveJobProblems: %v", err)
	}
	if !opts.noInventory {
		snapshot, err := store.ActivateSnapshot(ctx, pool, "invcommit1", mustJSON(t, inv), nil, h.clock.Now())
		if err != nil {
			t.Fatalf("ActivateSnapshot: %v", err)
		}
		h.snapshot = snapshot
	}

	awsseed.Connect(t, pool, testAWSAccount)
	h.eng = New(Deps{DB: pool, Limits: opts.limits, Now: h.clock.Now, Publisher: h.pub, PublicURL: "https://yum.example"})
	return h
}

// selection picks the named services of the job's one group at version 1.0.
func selection(services ...string) Selection {
	chosen := map[string]string{}
	for _, name := range services {
		chosen[name] = ""
	}
	return Selection{"g": {Version: "1.0", Services: chosen}}
}

func (h *harness) create(p auth.Principal, autoStart bool, services ...string) store.Run {
	h.t.Helper()
	run, err := h.eng.Create(context.Background(), p, h.sourceID, testEnvironment, selection(services...), "web", autoStart)
	if err != nil {
		h.t.Fatalf("Create: %v", err)
	}
	return run
}

func (h *harness) run(id int64) store.Run {
	h.t.Helper()
	run, err := store.GetRun(context.Background(), h.pool, id)
	if err != nil {
		h.t.Fatalf("GetRun %d: %v", id, err)
	}
	return run
}

func (h *harness) legs(runID int64) []store.Leg {
	h.t.Helper()
	legs, err := store.LegsOfRun(context.Background(), h.pool, runID)
	if err != nil {
		h.t.Fatalf("LegsOfRun %d: %v", runID, err)
	}
	return legs
}

func (h *harness) locks(runID int64) []string {
	h.t.Helper()
	locks, err := store.LocksOfRun(context.Background(), h.pool, runID)
	if err != nil {
		h.t.Fatalf("LocksOfRun %d: %v", runID, err)
	}
	return locks
}

func (h *harness) advance(runID int64) {
	h.t.Helper()
	if err := h.eng.Advance(context.Background(), runID); err != nil {
		h.t.Fatalf("Advance %d: %v", runID, err)
	}
}

func (h *harness) expectState(runID int64, want string) store.Run {
	h.t.Helper()
	run := h.run(runID)
	if run.State != want {
		h.t.Fatalf("run %d is %q (wait reason %q), want %q", runID, run.State, run.WaitReason, want)
	}
	return run
}

// legByKind returns the newest leg of the kind (resolve, revalidate).
func (h *harness) legByKind(runID int64, kind string) store.Leg {
	h.t.Helper()
	leg, ok := latestLeg(h.legs(runID), kind)
	if !ok {
		h.t.Fatalf("run %d has no %s leg", runID, kind)
	}
	return leg
}

// stepLeg returns the leg of the named step for the service.
func (h *harness) stepLeg(runID int64, step, service string) store.Leg {
	h.t.Helper()
	steps, err := store.StepsOfRun(context.Background(), h.pool, runID)
	if err != nil {
		h.t.Fatalf("StepsOfRun: %v", err)
	}
	for _, s := range steps {
		if s.Name != step {
			continue
		}
		for _, l := range h.legs(runID) {
			if l.StepID != nil && *l.StepID == s.ID && l.Service == service {
				return l
			}
		}
	}
	h.t.Fatalf("run %d has no leg for step %q and service %q", runID, step, service)
	return store.Leg{}
}

// setLeg stands in for a runner: it moves a leg to any state, as the runner
// would, without the orchestrator's conditions.
func (h *harness) setLeg(legID int64, state string) {
	h.t.Helper()
	_, err := h.pool.Exec(context.Background(),
		`UPDATE legs SET state = $2, generation = generation + CASE WHEN $2 IN ('claimed', 'running') THEN 1 ELSE 0 END,
			runner_id = CASE WHEN $2 IN ('claimed', 'running') THEN 'runner-1' ELSE runner_id END,
			result = CASE WHEN $2 = 'succeeded' THEN '{"ok":true}'::jsonb ELSE result END,
			finished_at = CASE WHEN $2 IN ('succeeded', 'failed', 'unknown') THEN now() ELSE finished_at END
		 WHERE id = $1`, legID, state)
	if err != nil {
		h.t.Fatalf("setLeg %d to %s: %v", legID, state, err)
	}
}

// resolved settles the run's resolve leg as succeeded with a plan result.
func (h *harness) resolved(runID int64) {
	h.t.Helper()
	h.settleResolve(runID, "succeeded", sampleManifest(runID))
}

// planned creates a run, lets it resolve and returns it in state planned.
func (h *harness) planned(p auth.Principal, services ...string) store.Run {
	h.t.Helper()
	run := h.create(p, false, services...)
	h.advance(run.ID) // resolve leg is already queued: nothing to do
	h.resolved(run.ID)
	h.advance(run.ID)
	return h.expectState(run.ID, "planned")
}

// running creates a run and starts it; the revalidate leg is queued.
func (h *harness) running(services ...string) store.Run {
	h.t.Helper()
	run := h.planned(h.starter, services...)
	if err := h.eng.Start(context.Background(), h.starter, run.ID); err != nil {
		h.t.Fatalf("Start: %v", err)
	}
	if h.run(run.ID).State == "waiting_approval" {
		if err := h.eng.Approve(context.Background(), h.approver, run.ID, h.run(run.ID).ManifestID, ""); err != nil {
			h.t.Fatalf("Approve: %v", err)
		}
		h.advance(run.ID)
	}
	return h.expectState(run.ID, "running")
}

// pastRevalidation settles the revalidate leg and advances, so the first step's
// legs are queued.
func (h *harness) pastRevalidation(runID int64) {
	h.t.Helper()
	h.setLeg(h.legByKind(runID, "revalidate").ID, "succeeded")
	h.advance(runID)
}

func (h *harness) runLines(runID int64) []string {
	h.t.Helper()
	rows, err := h.pool.Query(context.Background(), "SELECT text FROM log_lines WHERE run_id = $1 AND leg_id = 0 ORDER BY seq", runID)
	if err != nil {
		h.t.Fatalf("run lines: %v", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			h.t.Fatalf("scan run line: %v", err)
		}
		lines = append(lines, text)
	}
	return lines
}

func (h *harness) audits(action string) []store.AuditEntry {
	h.t.Helper()
	all, err := store.ListAudit(context.Background(), h.pool, 0, 500)
	if err != nil {
		h.t.Fatalf("ListAudit: %v", err)
	}
	var matching []store.AuditEntry
	for _, e := range all {
		if e.Action == action {
			matching = append(matching, e)
		}
	}
	return matching
}
