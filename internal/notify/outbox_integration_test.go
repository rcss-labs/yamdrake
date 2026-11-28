//go:build integration

package notify

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rcss-labs/yamdrake/internal/db"
	"github.com/rcss-labs/yamdrake/internal/store"
	"github.com/rcss-labs/yamdrake/internal/testutil/testdb"
)

// runInDatabase migrates a database and inserts one planned run.
func runInDatabase(t *testing.T) (*pgxpool.Pool, store.Run) {
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

	user, err := store.CreateLocalUser(ctx, pool, "requester@example.com", "Requester", "local", "")
	if err != nil {
		t.Fatalf("CreateLocalUser: %v", err)
	}
	folder, err := store.CreateFolder(ctx, pool, "services")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	source, err := store.CreateJobSource(ctx, pool, store.JobSource{FolderID: folder.ID, Repo: "acme/jobs", Branch: "main", Path: "deploy.yaml"})
	if err != nil {
		t.Fatalf("CreateJobSource: %v", err)
	}
	snapshot, err := store.ActivateSnapshot(ctx, pool, "inv1", []byte(`{}`), nil, time.Now())
	if err != nil {
		t.Fatalf("ActivateSnapshot: %v", err)
	}
	run, err := store.InsertRun(ctx, pool, store.Run{
		Kind: "deploy", JobSourceID: source.ID, JobID: "deploy-api",
		JobSnapshot: []byte(`{"id":"deploy-api"}`), InventoryCommit: "inv1", JobCommit: "job1", InventorySnapshotID: snapshot.ID,
		LockResources: []string{"arn:svc/a"},
		Environment:   "prod-eu", Label: "prod", AWSAccount: "111122223333", FolderID: folder.ID,
		Selection: []byte(`{}`), State: "planned", RequestedBy: user.ID, Via: "web",
	}, nil, nil)
	if err != nil {
		t.Fatalf("InsertRun: %v", err)
	}
	return pool, run
}

func countNotifications(t *testing.T, pool *pgxpool.Pool, runID int64) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications WHERE run_id = $1`, runID).Scan(&n); err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	return n
}

func TestEnqueueNoChannelIsNoOp(t *testing.T) {
	pool, run := runInDatabase(t)
	e := Event{Kind: KindStarted, Run: run, JobTitle: "Deploy", Requester: "Requester"}
	if err := Enqueue(context.Background(), pool, "", e, "https://yum.example", time.Now()); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if n := countNotifications(t, pool, run.ID); n != 0 {
		t.Errorf("%d notifications for an empty channel, want 0", n)
	}
}

func TestEnqueueStoresRenderedTextForTheChannelOncePerKind(t *testing.T) {
	ctx := context.Background()
	pool, run := runInDatabase(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	e := Event{Kind: KindStarted, Run: run, JobTitle: "Deploy", Requester: "Requester"}

	for i := 0; i < 2; i++ {
		if err := Enqueue(ctx, pool, "#deploys", e, "https://yum.example/", now); err != nil {
			t.Fatalf("Enqueue #%d: %v", i, err)
		}
	}
	if n := countNotifications(t, pool, run.ID); n != 1 {
		t.Fatalf("%d notifications after enqueuing the same kind twice, want 1", n)
	}
	e.Kind = KindFinished
	if err := Enqueue(ctx, pool, "#deploys", e, "https://yum.example/", now); err != nil {
		t.Fatalf("Enqueue finished: %v", err)
	}
	if n := countNotifications(t, pool, run.ID); n != 2 {
		t.Errorf("%d notifications for two kinds, want 2", n)
	}

	var channel, kind, state string
	var payload []byte
	if err := pool.QueryRow(ctx,
		`SELECT channel, kind, state, payload FROM notifications WHERE run_id = $1 AND kind = 'started'`, run.ID).
		Scan(&channel, &kind, &state, &payload); err != nil {
		t.Fatalf("read notification: %v", err)
	}
	var body struct{ Text string }
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("payload %q is not JSON: %v", payload, err)
	}
	started := Event{Kind: KindStarted, Run: run, JobTitle: "Deploy", Requester: "Requester"}
	if want := Text(started, "https://yum.example/"); body.Text != want {
		t.Errorf("payload text = %q, want %q", body.Text, want)
	}
	if channel != "#deploys" || state != "pending" {
		t.Errorf("channel %q state %q, want #deploys pending", channel, state)
	}
}
