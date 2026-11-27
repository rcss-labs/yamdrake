//go:build integration

package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rcss-labs/yamdrake/internal/db"
	"github.com/rcss-labs/yamdrake/internal/testutil/testdb"
)

func newPool(t *testing.T) *pgxpool.Pool {
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
	return pool
}

func TestConnectionsRoundTripAndSecretStaysCiphertext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	ciphertext := []byte{0x00, 0x01, 0xfe, 0xff, 'c', 'i', 'p', 'h'}

	created, err := CreateConnection(ctx, pool, Connection{Owner: "acme", Method: "app", AppID: 11, InstallationID: 22}, ciphertext)
	if err != nil {
		t.Fatalf("CreateConnection: %v", err)
	}
	if created.ID == 0 || created.CreatedAt.IsZero() || created.Owner != "acme" || created.AppID != 11 || created.InstallationID != 22 {
		t.Fatalf("unexpected created connection: %+v", created)
	}

	var stored []byte
	if err := pool.QueryRow(ctx, "SELECT secret_ciphertext FROM github_connections WHERE owner = $1", "acme").Scan(&stored); err != nil {
		t.Fatalf("read stored secret: %v", err)
	}
	if !bytes.Equal(stored, ciphertext) {
		t.Fatalf("stored secret = %x, want %x", stored, ciphertext)
	}

	got, secret, err := ConnectionSecret(ctx, pool, "acme")
	if err != nil {
		t.Fatalf("ConnectionSecret: %v", err)
	}
	if !bytes.Equal(secret, ciphertext) || got.ID != created.ID || got.Method != "app" {
		t.Fatalf("ConnectionSecret = %+v, %x", got, secret)
	}
	if _, _, err := ConnectionSecret(ctx, pool, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ConnectionSecret missing error = %v, want ErrNotFound", err)
	}

	list, err := ListConnections(ctx, pool)
	if err != nil || len(list) != 1 || list[0].Owner != "acme" {
		t.Fatalf("ListConnections = %+v, %v", list, err)
	}
	// The Connection type carries no secret field at all.
	for _, field := range reflect.VisibleFields(reflect.TypeOf(Connection{})) {
		if field.Type.Kind() == reflect.Slice {
			t.Fatalf("Connection has a byte-slice field %q that could hold a secret", field.Name)
		}
	}
}

func TestDuplicateOwnerIsErrDuplicate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	if _, err := CreateConnection(ctx, pool, Connection{Owner: "acme", Method: "token"}, []byte("a")); err != nil {
		t.Fatalf("first CreateConnection: %v", err)
	}
	_, err := CreateConnection(ctx, pool, Connection{Owner: "acme", Method: "token"}, []byte("b"))
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second CreateConnection error = %v, want ErrDuplicate", err)
	}
}

func TestFolderNamesAreCaseInsensitivelyUnique(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	if _, err := CreateFolder(ctx, pool, "Payments"); err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	if _, err := CreateFolder(ctx, pool, "payMENTS"); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("CreateFolder case variant error = %v, want ErrDuplicate", err)
	}
	if _, err := CreateFolder(ctx, pool, "Billing"); err != nil {
		t.Fatalf("CreateFolder other: %v", err)
	}
	folders, err := ListFolders(ctx, pool)
	if err != nil || len(folders) != 2 {
		t.Fatalf("ListFolders = %+v, %v", folders, err)
	}
}

func newFolder(t *testing.T, pool *pgxpool.Pool, name string) Folder {
	t.Helper()
	f, err := CreateFolder(context.Background(), pool, name)
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	return f
}

func TestJobSourceLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	folder := newFolder(t, pool, "one")
	other := newFolder(t, pool, "two")

	created, err := CreateJobSource(ctx, pool, JobSource{FolderID: folder.ID, Repo: "acme/ops", Branch: "main", Path: "jobs/deploy.yaml"})
	if err != nil {
		t.Fatalf("CreateJobSource: %v", err)
	}
	if created.ID == 0 || created.JobID != "" || created.LastGood != nil || created.CheckedAt != nil || created.SyncedAt != nil || created.Runnable {
		t.Fatalf("unexpected fresh source: %+v", created)
	}
	if _, err := CreateJobSource(ctx, pool, JobSource{FolderID: other.ID, Repo: "acme/ops", Branch: "main", Path: "jobs/deploy.yaml"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate CreateJobSource error = %v, want ErrDuplicate", err)
	}

	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	good := []byte(`{"name": "deploy"}`) // jsonb normalises spacing to this form
	if err := SaveJobGood(ctx, pool, created.ID, good, "deploy-backend", "Deploy backend", "c1", at); err != nil {
		t.Fatalf("SaveJobGood: %v", err)
	}
	got, err := GetJobSource(ctx, pool, created.ID)
	if err != nil {
		t.Fatalf("GetJobSource: %v", err)
	}
	if got.JobID != "deploy-backend" || got.Title != "Deploy backend" || got.LastGoodCommit != "c1" ||
		string(got.LastGood) != string(good) ||
		got.SyncedAt == nil || !got.SyncedAt.Equal(at) || got.CheckedAt == nil || !got.CheckedAt.Equal(at) {
		t.Fatalf("after SaveJobGood: %+v", got)
	}
	if got.Runnable || got.Problems != nil {
		t.Fatalf("SaveJobGood must not touch runnable/problems: %+v", got)
	}

	later := at.Add(time.Minute)
	problems := []byte(`[{"file":"jobs/deploy.yaml","line":3,"message":"bad"}]`)
	if err := SaveJobProblems(ctx, pool, created.ID, problems, "c2", false, later); err != nil {
		t.Fatalf("SaveJobProblems: %v", err)
	}
	got, _ = GetJobSource(ctx, pool, created.ID)
	if got.Runnable || got.ProblemsCommit != "c2" || len(got.Problems) == 0 || got.CheckedAt == nil || !got.CheckedAt.Equal(later) ||
		got.LastGoodCommit != "c1" || got.JobID != "deploy-backend" || !got.SyncedAt.Equal(at) {
		t.Fatalf("after SaveJobProblems: %+v", got)
	}

	// R1: a valid read saves good and clears problems in one transaction.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := SaveJobGood(ctx, tx, created.ID, good, "deploy-backend", "Deploy backend", "c3", later); err != nil {
		t.Fatalf("SaveJobGood in tx: %v", err)
	}
	if err := SaveJobProblems(ctx, tx, created.ID, nil, "c3", true, later); err != nil {
		t.Fatalf("SaveJobProblems in tx: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	got, _ = GetJobSource(ctx, pool, created.ID)
	if !got.Runnable || got.Problems != nil || got.ProblemsCommit != "c3" || got.LastGoodCommit != "c3" {
		t.Fatalf("after valid read: %+v", got)
	}

	touch := later.Add(time.Hour)
	if err := TouchJobChecked(ctx, pool, created.ID, touch); err != nil {
		t.Fatalf("TouchJobChecked: %v", err)
	}
	got, _ = GetJobSource(ctx, pool, created.ID)
	if !got.CheckedAt.Equal(touch) || !got.SyncedAt.Equal(later) {
		t.Fatalf("after touch: %+v", got)
	}

	if err := MoveJobSource(ctx, pool, created.ID, other.ID); err != nil {
		t.Fatalf("MoveJobSource: %v", err)
	}
	got, _ = GetJobSource(ctx, pool, created.ID)
	if got.FolderID != other.ID {
		t.Fatalf("FolderID = %d, want %d", got.FolderID, other.ID)
	}
	if err := MoveJobSource(ctx, pool, 9999, other.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MoveJobSource missing error = %v, want ErrNotFound", err)
	}
	if _, err := GetJobSource(ctx, pool, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetJobSource missing error = %v, want ErrNotFound", err)
	}
	list, err := ListJobSources(ctx, pool)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListJobSources = %+v, %v", list, err)
	}
}

func TestJobIDIsUniqueAcrossSources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	folder := newFolder(t, pool, "one")
	a, _ := CreateJobSource(ctx, pool, JobSource{FolderID: folder.ID, Repo: "acme/ops", Branch: "main", Path: "a.yaml"})
	b, _ := CreateJobSource(ctx, pool, JobSource{FolderID: folder.ID, Repo: "acme/ops", Branch: "main", Path: "b.yaml"})
	at := time.Now().UTC()
	if err := SaveJobGood(ctx, pool, a.ID, []byte(`{}`), "same-id", "A", "c1", at); err != nil {
		t.Fatalf("SaveJobGood a: %v", err)
	}
	if err := SaveJobGood(ctx, pool, b.ID, []byte(`{}`), "same-id", "B", "c1", at); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("SaveJobGood b error = %v, want ErrDuplicate", err)
	}
	// Re-saving the same id on the same source is fine.
	if err := SaveJobGood(ctx, pool, a.ID, []byte(`{}`), "same-id", "A2", "c2", at); err != nil {
		t.Fatalf("SaveJobGood a again: %v", err)
	}
}

func TestActivateSnapshotKeepsOneActive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	if _, ok, err := ActiveSnapshot(ctx, pool); err != nil || ok {
		t.Fatalf("ActiveSnapshot on empty = %v, %v", ok, err)
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := ActivateSnapshot(ctx, pool, "A", []byte(`{"v":"a"}`), at); err != nil {
		t.Fatalf("activate A: %v", err)
	}
	if _, err := ActivateSnapshot(ctx, pool, "B", []byte(`{"v":"b"}`), at.Add(time.Second)); err != nil {
		t.Fatalf("activate B: %v", err)
	}
	active, ok, err := ActiveSnapshot(ctx, pool)
	if err != nil || !ok || active.Commit != "B" {
		t.Fatalf("ActiveSnapshot = %+v, %v, %v", active, ok, err)
	}
	assertSnapshotCounts(t, pool, 2, 1)
}

func assertSnapshotCounts(t *testing.T, pool *pgxpool.Pool, total, active int) {
	t.Helper()
	var gotTotal, gotActive int
	err := pool.QueryRow(context.Background(),
		"SELECT count(*), count(*) FILTER (WHERE active) FROM inventory_snapshots").Scan(&gotTotal, &gotActive)
	if err != nil {
		t.Fatalf("count snapshots: %v", err)
	}
	if gotTotal != total || gotActive != active {
		t.Fatalf("snapshots total/active = %d/%d, want %d/%d", gotTotal, gotActive, total, active)
	}
}

func TestActivateSameCommitTwiceIsNoOp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	at := time.Now().UTC()
	first, err := ActivateSnapshot(ctx, pool, "A", []byte(`{"v":1}`), at)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := ActivateSnapshot(ctx, pool, "A", []byte(`{"v":2}`), at.Add(time.Hour))
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.ID != first.ID || !second.ActivatedAt.Equal(first.ActivatedAt) {
		t.Fatalf("second = %+v, want existing %+v", second, first)
	}
	assertSnapshotCounts(t, pool, 1, 1)
}

func TestActivateSnapshotConcurrentlyKeepsOneActive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	start := make(chan struct{})
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := ActivateSnapshot(ctx, pool, fmt.Sprintf("commit-%d", i), []byte(`{}`), time.Now().UTC())
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("ActivateSnapshot: %v", err)
		}
	}
	assertSnapshotCounts(t, pool, workers, 1)
}

func TestInventorySourceAndStatus(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	if _, ok, err := GetInventorySource(ctx, pool); err != nil || ok {
		t.Fatalf("GetInventorySource on empty = %v, %v", ok, err)
	}
	want := InventorySource{Repo: "acme/ops", Branch: "main", Path: "inventory.yaml"}
	if err := SetInventorySource(ctx, pool, want); err != nil {
		t.Fatalf("SetInventorySource: %v", err)
	}
	if err := SetInventorySource(ctx, pool, want); err != nil {
		t.Fatalf("SetInventorySource again: %v", err)
	}
	got, ok, err := GetInventorySource(ctx, pool)
	if err != nil || !ok || got != want {
		t.Fatalf("GetInventorySource = %+v, %v, %v", got, ok, err)
	}

	st, err := GetInventoryStatus(ctx, pool)
	if err != nil || st.CheckedAt != nil || st.Problems != nil {
		t.Fatalf("empty status = %+v, %v", st, err)
	}
	at := time.Now().UTC()
	if err := SaveInventoryStatus(ctx, pool, InventoryStatus{CheckedAt: &at, Problems: []byte(`[{"line":1}]`), ProblemsCommit: "c9"}); err != nil {
		t.Fatalf("SaveInventoryStatus: %v", err)
	}
	st, err = GetInventoryStatus(ctx, pool)
	if err != nil || st.CheckedAt == nil || !st.CheckedAt.Equal(at) || st.ProblemsCommit != "c9" || len(st.Problems) == 0 {
		t.Fatalf("status = %+v, %v", st, err)
	}
}
