//go:build integration

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/yamdrake/internal/config"
	"github.com/rcss-labs/yamdrake/internal/db"
	"github.com/rcss-labs/yamdrake/internal/notify"
	"github.com/rcss-labs/yamdrake/internal/secrets"
	"github.com/rcss-labs/yamdrake/internal/store"
	"github.com/rcss-labs/yamdrake/internal/testutil/testdb"
)

const (
	appSecretKey = "ZGV2LW9ubHktaW5zZWN1cmUta2V5LTMyLWJ5dGVzISE=" // 32 bytes, base64
	appFakeToken = "xoxb-app-fake-token"                          //nolint:gosec // a fake token for tests
)

type readyBody struct {
	Notifications *struct {
		Pending         int        `json:"pending"`
		Failed          int        `json:"failed"`
		OldestPendingAt *time.Time `json:"oldest_pending_at"`
	} `json:"notifications"`
}

func readReady(t *testing.T, addr string) readyBody {
	t.Helper()
	resp, err := http.Get("http://" + addr + "/readyz") //nolint:gosec,noctx // local test server
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var body readyBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode /readyz %s: %v", raw, err)
	}
	return body
}

// The orchestrator starts the Slack sender with the configured API address and
// interval, and reports the outbox in /readyz.
func TestOrchestratorDeliversNotificationsAndReportsThem(t *testing.T) {
	ctx := context.Background()
	databaseURL := testdb.New(t)
	if err := db.Migrate(ctx, databaseURL); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := db.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pool.Close()

	var mu sync.Mutex
	var got []string
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct{ Text string }
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		got = append(got, r.Header.Get("Authorization")+"|"+body.Text)
		mu.Unlock()
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer slack.Close()

	// A run to hang the message on, a stored token and a pending message.
	stamp := time.Now().UnixNano()
	user, err := store.CreateLocalUser(ctx, pool, fmt.Sprintf("app-%d@example.com", stamp), "Requester", "local", "")
	if err != nil {
		t.Fatalf("CreateLocalUser: %v", err)
	}
	folder, err := store.CreateFolder(ctx, pool, fmt.Sprintf("folder-%d", stamp))
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	source, err := store.CreateJobSource(ctx, pool, store.JobSource{FolderID: folder.ID, Repo: "acme/jobs", Branch: "main", Path: "deploy.yaml"})
	if err != nil {
		t.Fatalf("CreateJobSource: %v", err)
	}
	snapshot, err := store.ActivateSnapshot(ctx, pool, fmt.Sprintf("inv-%d", stamp), []byte(`{}`), nil, time.Now())
	if err != nil {
		t.Fatalf("ActivateSnapshot: %v", err)
	}
	run, err := store.InsertRun(ctx, pool, store.Run{
		Kind: "deploy", JobSourceID: source.ID, JobID: "deploy-api", JobSnapshot: []byte(`{"id":"deploy-api"}`),
		InventoryCommit: "inv1", JobCommit: "job1", InventorySnapshotID: snapshot.ID, LockResources: []string{"arn:svc/a"},
		Environment: "prod-eu", Label: "prod", AWSAccount: "111122223333", FolderID: folder.ID,
		Selection: []byte(`{"services":["a"]}`), State: "running", RequestedBy: user.ID, Via: "web",
	}, nil, nil)
	if err != nil {
		t.Fatalf("InsertRun: %v", err)
	}
	// Pending for now (not due for an hour), so /readyz can be read first.
	if err := store.EnqueueNotification(ctx, pool, store.Notification{
		RunID: run.ID, Kind: "started", Channel: "#deploys", Payload: []byte(`{"text":"hello from the outbox"}`),
		NextAttemptAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("EnqueueNotification: %v", err)
	}

	addr := freeAddr(t)
	env := map[string]string{
		"YUMDRAKE_DATABASE_URL":     databaseURL,
		"YUMDRAKE_VALKEY_ADDR":      os.Getenv("YUMDRAKE_TEST_VALKEY_ADDR"),
		"YUMDRAKE_HTTP_ADDR":        addr,
		"YUMDRAKE_LOCAL_SECRET_KEY": appSecretKey,
		"YUMDRAKE_PUBLIC_URL":       "http://" + addr,
		"YUMDRAKE_MIGRATE_ON_START": "false",
		"YUMDRAKE_SLACK_API_URL":    slack.URL,
		"YUMDRAKE_NOTIFY_INTERVAL":  "100ms",
		"YUMDRAKE_LOG_LEVEL":        "error",
	}
	cfg, err := config.LoadOrchestrator(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("LoadOrchestrator: %v", err)
	}
	cipher, err := secrets.NewLocal([]byte(cfg.LocalSecretKey.Reveal()))
	if err != nil {
		t.Fatal(err)
	}
	if err := notify.SaveSlackToken(ctx, pool, cipher, appFakeToken, user.ID, time.Now()); err != nil {
		t.Fatalf("SaveSlackToken: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	exited := make(chan int, 1)
	go func() { exited <- Run(runCtx, NewOrchestratorService(cfg), io.Discard) }()
	defer func() {
		cancel()
		select {
		case <-exited:
		case <-time.After(30 * time.Second):
			t.Error("orchestrator did not stop within 30s")
		}
	}()
	waitUntil := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	var ready readyBody
	waitUntil("/readyz to report the outbox", func() bool {
		resp, err := http.Get("http://" + addr + "/readyz") //nolint:gosec,noctx // local test server
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		ready = readReady(t, addr)
		return ready.Notifications != nil
	})
	if n := ready.Notifications; n.Pending != 1 || n.Failed != 0 || n.OldestPendingAt == nil {
		t.Fatalf("notifications = %+v, want 1 pending, 0 failed and an oldest_pending_at", n)
	}

	// Make it due: the running sender picks it up on its next round.
	if _, err := pool.Exec(ctx, "UPDATE notifications SET next_attempt_at = now() WHERE run_id = $1", run.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil("the sender to deliver", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 1
	})
	if want := "Bearer " + appFakeToken + "|hello from the outbox"; got[0] != want {
		t.Errorf("Slack received %q, want the stored token and the payload text", got[0])
	}
	waitUntil("the outbox to read as empty", func() bool {
		n := readReady(t, addr).Notifications
		return n != nil && n.Pending == 0 && n.OldestPendingAt == nil
	})
}
