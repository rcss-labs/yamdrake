//go:build integration

package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// runFixture holds the rows every run refers to.
type runFixture struct {
	userID, folderID, sourceID int64
}

func newRunFixture(t *testing.T, pool *pgxpool.Pool) runFixture {
	t.Helper()
	ctx := context.Background()
	user, err := CreateLocalUser(ctx, pool, fmt.Sprintf("runner-%d@example.com", time.Now().UnixNano()), "Requester", "local", "")
	if err != nil {
		t.Fatalf("CreateLocalUser: %v", err)
	}
	folder, err := CreateFolder(ctx, pool, fmt.Sprintf("folder-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	source, err := CreateJobSource(ctx, pool, JobSource{FolderID: folder.ID, Repo: "acme/jobs", Branch: "main", Path: "deploy.yaml"})
	if err != nil {
		t.Fatalf("CreateJobSource: %v", err)
	}
	return runFixture{userID: user.ID, folderID: folder.ID, sourceID: source.ID}
}

func (f runFixture) run(state string) Run {
	return Run{
		Kind: "deploy", JobSourceID: f.sourceID, JobID: "deploy-api",
		JobSnapshot: []byte(`{"id":"deploy-api"}`), InventoryCommit: "inv1", JobCommit: "job1",
		Environment: "prod-eu", Label: "prod", AWSAccount: "111122223333", FolderID: f.folderID,
		Selection: []byte(`{"services":["a"]}`), State: state, RequestedBy: f.userID, Via: "web",
	}
}

func (f runFixture) insert(t *testing.T, pool *pgxpool.Pool, state string) Run {
	t.Helper()
	r, err := InsertRun(context.Background(), pool, f.run(state), nil, nil)
	if err != nil {
		t.Fatalf("InsertRun: %v", err)
	}
	return r
}

func TestInsertRunCreatesStepsAndLegsAtomically(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	fx := newRunFixture(t, pool)
	created := nanosecondNow()

	r := fx.run("resolving")
	r.CreatedAt = created
	zero, one := int64(0), int64(1)
	steps := []RunStep{
		{Name: "copy", Use: "copy-config", GroupID: "g1", Position: 0, MaxParallel: 2, Timeout: 90 * time.Second, Needs: []byte(`[]`)},
		{Name: "deploy", Use: "ecs-deploy", GroupID: "g1", Position: 1, Timeout: time.Minute, Needs: []byte(`[{"step":"copy"}]`)},
	}
	// For a leg of a new run, StepID is the index into steps.
	legs := []Leg{
		{Kind: "resolve", State: "queued"},
		{Kind: "step", StepID: &zero, Service: "a", Version: "1.0", State: "pending"},
		{Kind: "step", StepID: &one, Service: "a", Version: "1.0", State: "pending"},
	}
	got, err := InsertRun(ctx, pool, r, steps, legs)
	if err != nil {
		t.Fatalf("InsertRun: %v", err)
	}
	if got.ID == 0 || !got.CreatedAt.Equal(micro(created)) || !got.StateChangedAt.Equal(micro(created)) {
		t.Fatalf("returned run: %+v", got)
	}
	back, err := GetRun(ctx, pool, got.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if back.Kind != "deploy" || back.State != "resolving" || back.Environment != "prod-eu" || back.Manifest != nil || back.ParentRunID != nil {
		t.Fatalf("GetRun: %+v", back)
	}
	if string(back.JobSnapshot) == "" || string(back.Selection) == "" {
		t.Fatalf("snapshot/selection not stored: %+v", back)
	}
	gotSteps, err := StepsOfRun(ctx, pool, got.ID)
	if err != nil || len(gotSteps) != 2 {
		t.Fatalf("StepsOfRun = %+v, %v", gotSteps, err)
	}
	if gotSteps[0].Name != "copy" || gotSteps[0].Timeout != 90*time.Second || gotSteps[0].MaxParallel != 2 || gotSteps[1].Position != 1 || gotSteps[1].RunID != got.ID {
		t.Fatalf("steps = %+v", gotSteps)
	}
	gotLegs, err := LegsOfRun(ctx, pool, got.ID)
	if err != nil || len(gotLegs) != 3 {
		t.Fatalf("LegsOfRun = %+v, %v", gotLegs, err)
	}
	if gotLegs[0].StepID != nil || gotLegs[0].Kind != "resolve" {
		t.Fatalf("resolve leg = %+v", gotLegs[0])
	}
	if gotLegs[1].StepID == nil || *gotLegs[1].StepID != gotSteps[0].ID || *gotLegs[2].StepID != gotSteps[1].ID {
		t.Fatalf("legs not linked to the stored step ids: %+v / %+v", gotLegs, gotSteps)
	}

	// A bad leg (unknown kind) must leave no run, step or leg behind.
	var runsBefore, stepsBefore, legsBefore int
	count := func() (int, int, int) {
		var a, b, c int
		if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM runs), (SELECT count(*) FROM run_steps), (SELECT count(*) FROM legs)").Scan(&a, &b, &c); err != nil {
			t.Fatalf("count: %v", err)
		}
		return a, b, c
	}
	runsBefore, stepsBefore, legsBefore = count()
	bad := []Leg{
		{Kind: "resolve", State: "queued"},
		{Kind: "nonsense", State: "pending"},
	}
	if _, err := InsertRun(ctx, pool, fx.run("resolving"), steps, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("InsertRun with a bad leg: %v, want ErrInvalid", err)
	}
	runs, stepsN, legsN := count()
	if runs != runsBefore || stepsN != stepsBefore || legsN != legsBefore {
		t.Fatalf("rows left behind: runs %d->%d steps %d->%d legs %d->%d", runsBefore, runs, stepsBefore, stepsN, legsBefore, legsN)
	}
	// A leg pointing past the steps is refused too.
	out := int64(5)
	if _, err := InsertRun(ctx, pool, fx.run("resolving"), steps, []Leg{{Kind: "step", StepID: &out, State: "pending"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("InsertRun with a step index out of range: %v, want ErrInvalid", err)
	}
}

func TestLockRunBlocksSecondTransaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	fx := newRunFixture(t, pool)
	run := fx.insert(t, pool, "planned")

	tx1, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx1.Rollback(ctx) }()
	locked, err := LockRun(ctx, tx1, run.ID)
	if err != nil || locked.ID != run.ID {
		t.Fatalf("LockRun = %+v, %v", locked, err)
	}

	acquired := make(chan error, 1)
	go func() {
		tx2, err := pool.Begin(ctx)
		if err != nil {
			acquired <- err
			return
		}
		defer func() { _ = tx2.Rollback(ctx) }()
		_, err = LockRun(ctx, tx2, run.ID)
		acquired <- err
	}()
	select {
	case err := <-acquired:
		t.Fatalf("second LockRun returned (%v) while the first transaction still held the row", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("second LockRun: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second LockRun did not proceed after the first committed")
	}

	tx3, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx3.Rollback(ctx) }()
	if _, err := LockRun(ctx, tx3, run.ID+1000); !errors.Is(err, ErrNotFound) {
		t.Fatalf("LockRun of unknown id: %v, want ErrNotFound", err)
	}
}

func TestAcquireLocksAllOrNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	fx := newRunFixture(t, pool)
	run1, run2 := fx.insert(t, pool, "queued"), fx.insert(t, pool, "queued")
	at := nanosecondNow()

	held, err := AcquireLocks(ctx, pool, run1.ID, []string{"b"}, at)
	if err != nil || len(held) != 0 {
		t.Fatalf("run 1 AcquireLocks = %v, %v", held, err)
	}
	held, err = AcquireLocks(ctx, pool, run2.ID, []string{"c", "a", "b"}, at)
	if err != nil {
		t.Fatalf("run 2 AcquireLocks: %v", err)
	}
	if !reflect.DeepEqual(held, map[string]int64{"b": run1.ID}) {
		t.Fatalf("heldBy = %v, want b held by run 1", held)
	}
	if got, _ := LocksOfRun(ctx, pool, run2.ID); len(got) != 0 {
		t.Fatalf("run 2 holds %v after a refused acquire, want nothing", got)
	}
	if got, _ := LocksOfRun(ctx, pool, run1.ID); !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("run 1 holds %v, want [b]", got)
	}

	if err := ReleaseLocks(ctx, pool, run1.ID); err != nil {
		t.Fatalf("ReleaseLocks: %v", err)
	}
	held, err = AcquireLocks(ctx, pool, run2.ID, []string{"c", "a", "b"}, at)
	if err != nil || len(held) != 0 {
		t.Fatalf("run 2 AcquireLocks after release = %v, %v", held, err)
	}
	got, err := LocksOfRun(ctx, pool, run2.ID)
	if err != nil || !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("run 2 holds %v, %v, want [a b c]", got, err)
	}
}

func TestAcquireLocksIsIdempotentForSameRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	fx := newRunFixture(t, pool)
	run := fx.insert(t, pool, "queued")
	at := nanosecondNow()

	for i := 0; i < 2; i++ {
		held, err := AcquireLocks(ctx, pool, run.ID, []string{"x", "y"}, at)
		if err != nil || len(held) != 0 {
			t.Fatalf("attempt %d: %v, %v", i, held, err)
		}
	}
	// A superset, with a duplicate in the input, adds only the new resource.
	held, err := AcquireLocks(ctx, pool, run.ID, []string{"z", "x", "y", "z"}, at)
	if err != nil || len(held) != 0 {
		t.Fatalf("superset: %v, %v", held, err)
	}
	got, _ := LocksOfRun(ctx, pool, run.ID)
	if !reflect.DeepEqual(got, []string{"x", "y", "z"}) {
		t.Fatalf("holds %v, want [x y z]", got)
	}
	// An empty request is a no-op.
	if held, err := AcquireLocks(ctx, pool, run.ID, nil, at); err != nil || len(held) != 0 {
		t.Fatalf("empty request: %v, %v", held, err)
	}
}

// TestAcquireLocksConcurrentlyOneWinner starts two runs that ask for
// overlapping sets in opposite order at the same instant. Exactly one must end
// up with every resource it asked for and the other with none: unsorted
// inserts deadlock, and a refused acquire that leaves partial inserts behind
// strands the loser with some of its resources.
func TestAcquireLocksConcurrentlyOneWinner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	fx := newRunFixture(t, pool)
	run1, run2 := fx.insert(t, pool, "queued"), fx.insert(t, pool, "queued")
	at := nanosecondNow()

	acquire := func(runID int64, resources []string, start <-chan struct{}) (map[string]int64, error) {
		<-start
		tx, err := pool.Begin(ctx)
		if err != nil {
			return nil, err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		held, err := AcquireLocks(ctx, tx, runID, resources, at)
		if err != nil {
			return nil, err
		}
		return held, tx.Commit(ctx)
	}

	for round := 0; round < 50; round++ {
		name := func(s string) string { return fmt.Sprintf("r%d-%s", round, s) }
		set1 := []string{name("a"), name("b"), name("c"), name("d")}
		set2 := []string{name("f"), name("d"), name("c"), name("b"), name("e")}
		start := make(chan struct{})
		var wg sync.WaitGroup
		var held1, held2 map[string]int64
		var err1, err2 error
		wg.Add(2)
		go func() { defer wg.Done(); held1, err1 = acquire(run1.ID, set1, start) }()
		go func() { defer wg.Done(); held2, err2 = acquire(run2.ID, set2, start) }()
		close(start)
		wg.Wait()
		if err1 != nil || err2 != nil {
			t.Fatalf("round %d: errors %v / %v (a deadlock surfaces here)", round, err1, err2)
		}
		got1, _ := LocksOfRun(ctx, pool, run1.ID)
		got2, _ := LocksOfRun(ctx, pool, run2.ID)
		won1 := len(held1) == 0
		won2 := len(held2) == 0
		if won1 == won2 {
			t.Fatalf("round %d: both reported win=%v (held1=%v held2=%v)", round, won1, held1, held2)
		}
		wantWinner, wantLoserHolds := set1, got2
		winnerHolds, loser := got1, run2.ID
		if won2 {
			wantWinner, wantLoserHolds = set2, got1
			winnerHolds, loser = got2, run1.ID
		}
		sorted := append([]string(nil), wantWinner...)
		sort.Strings(sorted)
		if !reflect.DeepEqual(winnerHolds, sorted) {
			t.Fatalf("round %d: winner holds %v, want %v", round, winnerHolds, sorted)
		}
		if len(wantLoserHolds) != 0 {
			t.Fatalf("round %d: loser run %d holds %v, want nothing", round, loser, wantLoserHolds)
		}
		if err := ReleaseLocks(ctx, pool, run1.ID); err != nil {
			t.Fatal(err)
		}
		if err := ReleaseLocks(ctx, pool, run2.ID); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStateCheckConstraintsRejectUnknownValues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	fx := newRunFixture(t, pool)

	badState := fx.run("exploding")
	if _, err := InsertRun(ctx, pool, badState, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown run state: %v, want ErrInvalid", err)
	}
	badKind := fx.run("resolving")
	badKind.Kind = "rollback"
	if _, err := InsertRun(ctx, pool, badKind, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown run kind: %v, want ErrInvalid", err)
	}
	badVia := fx.run("resolving")
	badVia.Via = "fax"
	if _, err := InsertRun(ctx, pool, badVia, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown via: %v, want ErrInvalid", err)
	}

	run := fx.insert(t, pool, "resolving")
	if err := UpdateRunState(ctx, pool, run.ID, "exploding", "", nanosecondNow()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("UpdateRunState to unknown state: %v, want ErrInvalid", err)
	}
	if _, err := InsertLeg(ctx, pool, Leg{RunID: run.ID, Kind: "step", State: "exploding"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown leg state: %v, want ErrInvalid", err)
	}
	if _, err := InsertLeg(ctx, pool, Leg{RunID: run.ID, Kind: "deploy", State: "pending"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown leg kind: %v, want ErrInvalid", err)
	}
	err := InsertLogLines(ctx, pool, []LogLine{{RunID: run.ID, Seq: 1, At: nanosecondNow(), Level: "debug", Text: "x"}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown log level: %v, want ErrInvalid", err)
	}
	leg, err := InsertLeg(ctx, pool, Leg{RunID: run.ID, Kind: "resolve", State: "queued"})
	if err != nil {
		t.Fatalf("InsertLeg: %v", err)
	}
	_, err = pool.Exec(ctx, "INSERT INTO operations (leg_id, seq, name, key, state, at) VALUES ($1, 1, 'n', 'k', 'weird', now())", leg.ID)
	if !errors.Is(mapError(err), ErrInvalid) {
		t.Fatalf("unknown operation state: %v, want ErrInvalid", err)
	}
}

func TestListRunsFiltersAndPages(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	fx := newRunFixture(t, pool)
	other, err := CreateJobSource(ctx, pool, JobSource{FolderID: fx.folderID, Repo: "acme/jobs", Branch: "main", Path: "other.yaml"})
	if err != nil {
		t.Fatal(err)
	}

	mk := func(state, env, label string, source int64, jobID string) Run {
		r := fx.run(state)
		r.Environment, r.Label, r.JobSourceID, r.JobID = env, label, source, jobID
		got, err := InsertRun(ctx, pool, r, nil, nil)
		if err != nil {
			t.Fatalf("InsertRun: %v", err)
		}
		return got
	}
	r1 := mk("succeeded", "dev-eu", "dev", fx.sourceID, "deploy-api")
	r2 := mk("running", "prod-eu", "prod", fx.sourceID, "deploy-api")
	r3 := mk("failed", "prod-eu", "prod", other.ID, "deploy-web")
	r4 := mk("planned", "dev-eu", "dev", other.ID, "deploy-web")
	r5 := mk("running", "prod-us", "prod", fx.sourceID, "deploy-api")

	idsOf := func(f RunFilter) []int64 {
		t.Helper()
		got, err := ListRuns(ctx, pool, f)
		if err != nil {
			t.Fatalf("ListRuns(%+v): %v", f, err)
		}
		out := []int64{}
		for _, r := range got {
			out = append(out, r.ID)
		}
		return out
	}
	check := func(name string, f RunFilter, want ...int64) {
		t.Helper()
		if got := idsOf(f); !reflect.DeepEqual(got, append([]int64{}, want...)) {
			t.Fatalf("%s: got %v, want %v", name, got, want)
		}
	}
	check("all, newest first", RunFilter{}, r5.ID, r4.ID, r3.ID, r2.ID, r1.ID)
	check("by source", RunFilter{JobSourceID: other.ID}, r4.ID, r3.ID)
	check("by environment", RunFilter{Environment: "prod-eu"}, r3.ID, r2.ID)
	check("by states", RunFilter{States: []string{"running", "planned"}}, r5.ID, r4.ID, r2.ID)
	check("query on job id", RunFilter{Query: "deploy-web"}, r4.ID, r3.ID)
	check("query is case-insensitive and a substring", RunFilter{Query: "PROD-U"}, r5.ID)
	check("query wildcard is literal", RunFilter{Query: "%"})
	check("query by run id", RunFilter{Query: fmt.Sprint(r2.ID)}, r2.ID)
	check("combined", RunFilter{Environment: "prod-eu", States: []string{"failed"}}, r3.ID)
	check("limit", RunFilter{Limit: 2}, r5.ID, r4.ID)
	check("before id", RunFilter{BeforeID: r3.ID, Limit: 2}, r2.ID, r1.ID)
	check("before id, last page", RunFilter{BeforeID: r2.ID}, r1.ID)

	active, err := ActiveRunIDs(ctx, pool)
	if err != nil || !reflect.DeepEqual(active, []int64{r2.ID, r4.ID, r5.ID}) {
		t.Fatalf("ActiveRunIDs = %v, %v, want %v", active, err, []int64{r2.ID, r4.ID, r5.ID})
	}
}

func TestExpiredLegsReturnsOnlyClaimedOrRunningPastLease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	fx := newRunFixture(t, pool)
	run := fx.insert(t, pool, "running")
	now := nanosecondNow()
	past, future := now.Add(-time.Minute), now.Add(time.Minute)

	mk := func(state string, lease *time.Time) Leg {
		l, err := InsertLeg(ctx, pool, Leg{RunID: run.ID, Kind: "step", Service: state, State: state, LeaseUntil: lease, RunnerID: "r1", Generation: 3})
		if err != nil {
			t.Fatalf("InsertLeg(%s): %v", state, err)
		}
		return l
	}
	claimedPast := mk("claimed", &past)
	runningPast := mk("running", &past)
	mk("claimed", &future)
	mk("running", &future)
	mk("queued", &past)    // not claimed yet: no lease to expire
	mk("succeeded", &past) // settled: lease is history
	mk("unknown", &past)
	mk("running", nil)

	got, err := ExpiredLegs(ctx, pool, now)
	if err != nil {
		t.Fatalf("ExpiredLegs: %v", err)
	}
	var gotIDs []int64
	for _, l := range got {
		gotIDs = append(gotIDs, l.ID)
	}
	if want := []int64{claimedPast.ID, runningPast.ID}; !reflect.DeepEqual(gotIDs, want) {
		t.Fatalf("ExpiredLegs ids = %v, want %v", gotIDs, want)
	}
	if got[0].Generation != 3 || got[0].RunnerID != "r1" || got[0].LeaseUntil == nil || !got[0].LeaseUntil.Equal(micro(past)) {
		t.Fatalf("expired leg fields: %+v", got[0])
	}
}

func TestSetLegStateIsConditional(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	fx := newRunFixture(t, pool)
	run := fx.insert(t, pool, "running")
	at := nanosecondNow()
	mk := func(state string) Leg {
		l, err := InsertLeg(ctx, pool, Leg{RunID: run.ID, Kind: "step", State: state})
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	stateOf := func(id int64) Leg {
		legs, _ := LegsOfRun(ctx, pool, run.ID)
		for _, l := range legs {
			if l.ID == id {
				return l
			}
		}
		t.Fatalf("leg %d missing", id)
		return Leg{}
	}

	allowed := []struct{ from, to string }{
		{"pending", "queued"}, {"pending", "skipped"}, {"pending", "cancelled"},
		{"queued", "cancelled"},
		{"unknown", "succeeded"}, {"unknown", "failed"}, {"unknown", "queued"},
	}
	for _, m := range allowed {
		leg := mk(m.from)
		if err := SetLegState(ctx, pool, leg.ID, m.to, at); err != nil {
			t.Fatalf("%s -> %s: %v", m.from, m.to, err)
		}
		got := stateOf(leg.ID)
		if got.State != m.to {
			t.Fatalf("%s -> %s: state is %s", m.from, m.to, got.State)
		}
		if m.to == "queued" && (got.QueuedAt == nil || !got.QueuedAt.Equal(micro(at))) {
			t.Fatalf("%s -> queued did not set queued_at: %+v", m.from, got)
		}
	}

	refused := []struct{ from, to string }{
		{"claimed", "cancelled"}, // a runner just claimed it
		{"running", "cancelled"},
		{"queued", "queued"},
		{"succeeded", "failed"},
		{"pending", "succeeded"},
		{"queued", "skipped"},
	}
	for _, m := range refused {
		leg := mk(m.from)
		if err := SetLegState(ctx, pool, leg.ID, m.to, at); !errors.Is(err, ErrLegMoved) {
			t.Fatalf("%s -> %s: %v, want ErrLegMoved", m.from, m.to, err)
		}
		if got := stateOf(leg.ID); got.State != m.from {
			t.Fatalf("%s -> %s refused but state is now %s", m.from, m.to, got.State)
		}
	}
	if err := SetLegState(ctx, pool, 999999, "queued", at); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown leg: %v, want ErrNotFound", err)
	}
	if err := SetLegState(ctx, pool, mk("pending").ID, "running", at); !errors.Is(err, ErrInvalid) {
		t.Fatalf("pending -> running by the orchestrator: %v, want ErrInvalid", err)
	}

	leg := mk("claimed")
	if err := FlagLegCancel(ctx, pool, leg.ID); err != nil {
		t.Fatalf("FlagLegCancel: %v", err)
	}
	if got := stateOf(leg.ID); !got.CancelRequested || got.State != "claimed" {
		t.Fatalf("after FlagLegCancel: %+v", got)
	}
	if err := FlagLegCancel(ctx, pool, 999999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("FlagLegCancel unknown: %v, want ErrNotFound", err)
	}
}

func TestSetLegStateRacesWithClaimExactlyOneWins(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	fx := newRunFixture(t, pool)
	run := fx.insert(t, pool, "running")
	at := nanosecondNow()

	for round := 0; round < 20; round++ {
		leg, err := InsertLeg(ctx, pool, Leg{RunID: run.ID, Kind: "step", State: "queued"})
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		var cancelErr error
		var claimed int64
		wg.Add(2)
		go func() { defer wg.Done(); <-start; cancelErr = SetLegState(ctx, pool, leg.ID, "cancelled", at) }()
		go func() {
			defer wg.Done()
			<-start
			tag, err := pool.Exec(ctx, "UPDATE legs SET state = 'claimed' WHERE id = $1 AND state = 'queued'", leg.ID)
			if err != nil {
				t.Error(err)
				return
			}
			claimed = tag.RowsAffected()
		}()
		close(start)
		wg.Wait()
		cancelled := cancelErr == nil
		if cancelled == (claimed == 1) {
			t.Fatalf("round %d: cancel err=%v claimed=%d: exactly one must win", round, cancelErr, claimed)
		}
		if !cancelled && !errors.Is(cancelErr, ErrLegMoved) {
			t.Fatalf("round %d: losing cancel returned %v, want ErrLegMoved", round, cancelErr)
		}
	}
}

func TestRunStateAndCancelAndManifest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	fx := newRunFixture(t, pool)
	run := fx.insert(t, pool, "resolving")

	t1 := nanosecondNow().Add(time.Hour)
	if err := UpdateRunState(ctx, pool, run.ID, "planned", "", t1); err != nil {
		t.Fatalf("UpdateRunState: %v", err)
	}
	got, _ := GetRun(ctx, pool, run.ID)
	if got.State != "planned" || !got.StateChangedAt.Equal(micro(t1)) || got.StartedAt != nil || got.FinishedAt != nil {
		t.Fatalf("after planned: %+v", got)
	}
	t2 := t1.Add(time.Minute)
	if err := UpdateRunState(ctx, pool, run.ID, "queued", "waiting for run #3", t2); err != nil {
		t.Fatal(err)
	}
	t3 := t2.Add(time.Minute)
	if err := UpdateRunState(ctx, pool, run.ID, "running", "", t3); err != nil {
		t.Fatal(err)
	}
	got, _ = GetRun(ctx, pool, run.ID)
	if got.WaitReason != "" || got.StartedAt == nil || !got.StartedAt.Equal(micro(t3)) {
		t.Fatalf("after running: %+v", got)
	}
	t4 := t3.Add(time.Minute)
	if err := UpdateRunState(ctx, pool, run.ID, "failed", "", t4); err != nil {
		t.Fatal(err)
	}
	got, _ = GetRun(ctx, pool, run.ID)
	if got.FinishedAt == nil || !got.FinishedAt.Equal(micro(t4)) || !got.StartedAt.Equal(micro(t3)) {
		t.Fatalf("after failed: %+v", got)
	}
	if err := UpdateRunState(ctx, pool, run.ID+1000, "failed", "", t4); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown run: %v, want ErrNotFound", err)
	}

	if err := SetManifest(ctx, pool, run.ID, []byte(`{"steps":1}`), "abc123"); err != nil {
		t.Fatalf("SetManifest: %v", err)
	}
	got, _ = GetRun(ctx, pool, run.ID)
	if got.ManifestID != "abc123" || len(got.Manifest) == 0 {
		t.Fatalf("manifest: %+v", got)
	}

	if err := RequestCancel(ctx, pool, run.ID, fx.userID, t4); err != nil {
		t.Fatalf("RequestCancel: %v", err)
	}
	// A second request keeps the first requester and time.
	if err := RequestCancel(ctx, pool, run.ID, fx.userID, t4.Add(time.Hour)); err != nil {
		t.Fatalf("second RequestCancel: %v", err)
	}
	got, _ = GetRun(ctx, pool, run.ID)
	if got.CancelRequestedBy == nil || *got.CancelRequestedBy != fx.userID || got.CancelRequestedAt == nil || !got.CancelRequestedAt.Equal(micro(t4)) {
		t.Fatalf("cancel request: %+v", got)
	}
	if _, err := GetRun(ctx, pool, run.ID+1000); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRun unknown: %v, want ErrNotFound", err)
	}
}

func TestLogLinesAppendInsertAndMaxSeq(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	fx := newRunFixture(t, pool)
	run := fx.insert(t, pool, "running")
	at := nanosecondNow()

	if max, err := MaxLogSeq(ctx, pool, run.ID, 0); err != nil || max != 0 {
		t.Fatalf("MaxLogSeq empty = %d, %v, want 0", max, err)
	}
	for i := 1; i <= 3; i++ {
		if err := AppendRunLine(ctx, pool, run.ID, "info", fmt.Sprintf("line %d", i), at); err != nil {
			t.Fatalf("AppendRunLine: %v", err)
		}
	}
	if max, _ := MaxLogSeq(ctx, pool, run.ID, 0); max != 3 {
		t.Fatalf("MaxLogSeq(run lines) = %d, want 3", max)
	}

	leg, _ := InsertLeg(ctx, pool, Leg{RunID: run.ID, Kind: "step", State: "running"})
	batch := []LogLine{
		{RunID: run.ID, LegID: leg.ID, Seq: 1, At: at, Level: "info", Text: "one"},
		{RunID: run.ID, LegID: leg.ID, Seq: 2, At: at, Level: "ok", Text: "two"},
	}
	if err := InsertLogLines(ctx, pool, batch); err != nil {
		t.Fatalf("InsertLogLines: %v", err)
	}
	// A repeated, overlapping batch is dropped by the primary key, not duplicated.
	batch = append(batch, LogLine{RunID: run.ID, LegID: leg.ID, Seq: 3, At: at, Level: "warn", Text: "three"})
	batch[0].Text = "changed"
	if err := InsertLogLines(ctx, pool, batch); err != nil {
		t.Fatalf("repeated InsertLogLines: %v", err)
	}
	if err := InsertLogLines(ctx, pool, nil); err != nil {
		t.Fatalf("empty InsertLogLines: %v", err)
	}
	var n int
	var text string
	if err := pool.QueryRow(ctx, "SELECT count(*), min(text) FILTER (WHERE seq = 1) FROM log_lines WHERE run_id = $1 AND leg_id = $2", run.ID, leg.ID).Scan(&n, &text); err != nil {
		t.Fatal(err)
	}
	if n != 3 || text != "one" {
		t.Fatalf("leg lines = %d, seq 1 text %q, want 3 and the original", n, text)
	}
	if max, _ := MaxLogSeq(ctx, pool, run.ID, leg.ID); max != 3 {
		t.Fatalf("MaxLogSeq(leg) = %d, want 3", max)
	}
	// Run lines and leg lines are numbered independently.
	if max, _ := MaxLogSeq(ctx, pool, run.ID, 0); max != 3 {
		t.Fatalf("MaxLogSeq(run lines) = %d, want 3", max)
	}
}

func TestAppendRunLineConcurrentCallersKeepOwnSequence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	fx := newRunFixture(t, pool)
	run := fx.insert(t, pool, "running")

	// The contract: the caller holds the run row lock, so appends serialize.
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tx, err := pool.Begin(ctx)
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := LockRun(ctx, tx, run.ID); err != nil {
				errs <- err
				return
			}
			if err := AppendRunLine(ctx, tx, run.ID, "info", fmt.Sprint(i), nanosecondNow()); err != nil {
				errs <- err
				return
			}
			errs <- tx.Commit(ctx)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("append under run lock: %v", err)
		}
	}
	if max, _ := MaxLogSeq(ctx, pool, run.ID, 0); max != 8 {
		t.Fatalf("MaxLogSeq = %d, want 8 distinct sequences", max)
	}
}
