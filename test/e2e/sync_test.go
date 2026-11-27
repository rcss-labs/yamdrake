//go:build e2e

// Package e2e runs the orchestrator in-process against a fake GitHub and the
// real PostgreSQL and Valkey services.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rcss-labs/yamdrake/internal/app"
	"github.com/rcss-labs/yamdrake/internal/config"
	"github.com/rcss-labs/yamdrake/internal/db"
	"github.com/rcss-labs/yamdrake/internal/testutil/testdb"
	"github.com/rcss-labs/yamdrake/test/e2e/fakegithub"
)

const (
	// Dev-only values; the key is the same fixed public key compose.yaml uses.
	bootstrapToken = "e2e-bootstrap-token-0123456789abcdef"
	localSecretKey = "ZGV2LW9ubHktaW5zZWN1cmUta2V5LTMyLWJ5dGVzISE="
	githubToken    = "e2e-fake-github-token"

	repo         = "acme/platform"
	branch       = "main"
	syncInterval = 10 * time.Second
	// Within two intervals, plus a margin for the sync itself.
	deadline = 25 * time.Second
	pollStep = 250 * time.Millisecond

	goodCommit   = "1111111111111111111111111111111111111111"
	brokenCommit = "2222222222222222222222222222222222222222"
	fixedCommit  = "3333333333333333333333333333333333333333"
)

type apiTime = *time.Time

type inventoryView struct {
	Commit       string  `json:"commit"`
	CheckedAt    apiTime `json:"checked_at"`
	SyncError    string  `json:"sync_error"`
	SyncErrorAt  apiTime `json:"sync_error_at"`
	Environments []struct {
		Name string `json:"name"`
	} `json:"environments"`
	Services []struct {
		Name string `json:"name"`
	} `json:"services"`
	Rejected *struct {
		Commit   string `json:"commit"`
		Problems []struct {
			File    string `json:"file"`
			Line    int    `json:"line"`
			Message string `json:"message"`
		} `json:"problems"`
	} `json:"rejected"`
}

type jobView struct {
	ID          int64   `json:"id"`
	JobID       string  `json:"job_id"`
	Runnable    bool    `json:"runnable"`
	Commit      string  `json:"commit"`
	CheckedAt   apiTime `json:"checked_at"`
	SyncError   string  `json:"sync_error"`
	SyncErrorAt apiTime `json:"sync_error_at"`
}

type client struct {
	t    *testing.T
	base string
}

// call sends one API request and decodes a JSON answer into out (when non-nil).
func (c client) call(method, path string, body, out any) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("encode %s %s: %v", method, path, err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		c.t.Fatalf("build %s %s: %v", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+bootstrapToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		c.t.Fatalf("%s %s: status %d: %s", method, path, resp.StatusCode, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("decode %s %s: %v (%s)", method, path, err, raw)
		}
	}
}

func (c client) inventory() inventoryView {
	c.t.Helper()
	var v inventoryView
	c.call(http.MethodGet, "/api/v1/inventory", nil, &v)
	return v
}

func (c client) job(id int64) jobView {
	c.t.Helper()
	var v jobView
	c.call(http.MethodGet, fmt.Sprintf("/api/v1/jobs/%d", id), nil, &v)
	return v
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(pollStep)
	}
	t.Fatalf("timed out after %s waiting for %s", deadline, what)
}

// holdsFor fails the test if cond is false at any poll during d.
func holdsFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if !cond() {
			t.Fatalf("%s did not hold", what)
		}
		time.Sleep(pollStep)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a free port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// startOrchestrator runs the orchestrator through app.Run, the way
// cmd/orchestrator does, and stops it (expecting a clean exit) at cleanup.
func startOrchestrator(t *testing.T, githubURL string) string {
	t.Helper()
	valkeyAddr := os.Getenv("YUMDRAKE_TEST_VALKEY_ADDR")
	if valkeyAddr == "" {
		t.Fatal("YUMDRAKE_TEST_VALKEY_ADDR must be set for end-to-end tests")
	}
	addr := freeAddr(t)
	env := map[string]string{
		"YUMDRAKE_DATABASE_URL":     testdb.New(t),
		"YUMDRAKE_VALKEY_ADDR":      valkeyAddr,
		"YUMDRAKE_HTTP_ADDR":        addr,
		"YUMDRAKE_LOCAL_SECRET_KEY": localSecretKey,
		"YUMDRAKE_BOOTSTRAP_TOKEN":  bootstrapToken,
		"YUMDRAKE_SYNC_INTERVAL":    syncInterval.String(),
		"YUMDRAKE_GITHUB_API_URL":   githubURL,
		"YUMDRAKE_LOG_LEVEL":        "warn",
	}
	cfg, err := config.LoadOrchestrator(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("load orchestrator config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	exited := make(chan int, 1)
	go func() {
		exited <- app.Run(ctx, app.Service{
			Name:   "yumdrake-orchestrator",
			Common: cfg.Common,
			Addr:   cfg.HTTPAddr,
			Extend: app.OrchestratorExtension(cfg),
			Prepare: func(ctx context.Context, _ *slog.Logger, _ *pgxpool.Pool) error {
				return db.Migrate(ctx, cfg.DatabaseURL)
			},
		}, io.Discard)
	}()
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

	base := "http://" + addr
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

// fixtureCommits builds the three commits from the shared valid fixture: the
// good one as is, a broken one whose orders service has an unknown field, and
// a fixed one that differs from the good one only by a comment.
func fixtureCommits(t *testing.T, gh *fakegithub.Server) {
	t.Helper()
	fixture := filepath.Join("..", "..", "internal", "spec", "testdata", "valid")
	variant := func(sha, appendToOrders string) {
		dir := t.TempDir()
		if err := os.CopyFS(dir, os.DirFS(fixture)); err != nil {
			t.Fatalf("copy fixture: %v", err)
		}
		if appendToOrders != "" {
			orders := filepath.Join(dir, "services", "orders.yaml")
			raw, err := os.ReadFile(orders) //nolint:gosec // path built from a temp dir
			if err != nil {
				t.Fatalf("read orders.yaml: %v", err)
			}
			if err := os.WriteFile(orders, append(raw, []byte(appendToOrders)...), 0o600); err != nil {
				t.Fatalf("write orders.yaml: %v", err)
			}
		}
		if err := gh.LoadDir(repo, sha, dir); err != nil {
			t.Fatalf("load commit %s: %v", sha, err)
		}
	}
	variant(goodCommit, "")
	variant(brokenCommit, "unexpected_field: true\n")
	variant(fixedCommit, "# fixed\n")
}

func TestInventoryAndJobFlowEndToEnd(t *testing.T) {
	gh := fakegithub.New(githubToken)
	t.Cleanup(gh.Close)
	fixtureCommits(t, gh)
	gh.SetHead(repo, branch, goodCommit)

	api := client{t: t, base: startOrchestrator(t, gh.URL())}

	// Set-up through the API; each step syncs at once.
	api.call(http.MethodPost, "/api/v1/admin/github-connections",
		map[string]any{"owner": "acme", "method": "token", "token": githubToken}, nil)
	var folder struct {
		ID int64 `json:"id"`
	}
	api.call(http.MethodPost, "/api/v1/folders", map[string]any{"name": "Backend"}, &folder)
	api.call(http.MethodPut, "/api/v1/admin/inventory-source",
		map[string]any{"repo": repo, "branch": branch}, nil)
	var registered jobView
	api.call(http.MethodPost, "/api/v1/jobs", map[string]any{
		"folder_id": folder.ID, "repo": repo, "branch": branch, "path": "jobs/deploy-backend.yaml"}, &registered)

	inv := api.inventory()
	if inv.Commit != goodCommit || inv.Rejected != nil || inv.SyncError != "" {
		t.Fatalf("inventory = commit %q rejected %v sync_error %q, want active %s and clean", inv.Commit, inv.Rejected, inv.SyncError, goodCommit)
	}
	if len(inv.Environments) != 5 || len(inv.Services) != 2 {
		t.Fatalf("inventory has %d environments and %d services, want 5 and 2", len(inv.Environments), len(inv.Services))
	}
	job := api.job(registered.ID)
	if job.JobID != "deploy-backend" || !job.Runnable || job.Commit != goodCommit {
		t.Fatalf("job = %+v, want deploy-backend runnable at %s", job, goodCommit)
	}

	// A broken commit: the old inventory stays active and the problems show.
	gh.SetHead(repo, branch, brokenCommit)
	waitFor(t, "the broken commit to be reported as rejected", func() bool {
		v := api.inventory()
		return v.Rejected != nil && v.Rejected.Commit == brokenCommit
	})
	inv = api.inventory()
	if inv.Commit != goodCommit {
		t.Errorf("active commit = %s while the new commit is broken, want %s", inv.Commit, goodCommit)
	}
	if len(inv.Services) != 2 {
		t.Errorf("active inventory has %d services, want the old 2", len(inv.Services))
	}
	if inv.Rejected == nil || len(inv.Rejected.Problems) == 0 ||
		!strings.Contains(inv.Rejected.Problems[0].File, "orders.yaml") ||
		inv.Rejected.Problems[0].Line == 0 ||
		!strings.Contains(inv.Rejected.Problems[0].Message, "unexpected_field") {
		t.Errorf("rejected = %+v, want a problem in orders.yaml with a line naming unexpected_field", inv.Rejected)
	}

	// The fixed commit becomes active and the problems disappear.
	gh.SetHead(repo, branch, fixedCommit)
	waitFor(t, "the fixed commit to become active", func() bool {
		v := api.inventory()
		return v.Commit == fixedCommit && v.Rejected == nil
	})

	// GitHub down: nothing changes, the check time stands still, the error shows.
	gh.SetUnavailable(true)
	waitFor(t, "the sync error to appear", func() bool {
		return api.inventory().SyncError != "" && api.job(registered.ID).SyncError != ""
	})
	frozenInv, frozenJob := api.inventory(), api.job(registered.ID)
	if frozenInv.SyncErrorAt == nil {
		t.Error("inventory sync_error_at is not set while GitHub is down")
	}
	holdsFor(t, syncInterval+2*time.Second, "the inventory and job staying unchanged while GitHub is down", func() bool {
		inv, job := api.inventory(), api.job(registered.ID)
		return inv.Commit == fixedCommit && inv.Rejected == nil && inv.CheckedAt.Equal(*frozenInv.CheckedAt) &&
			job.Commit == frozenJob.Commit && job.Runnable && job.CheckedAt.Equal(*frozenJob.CheckedAt)
	})

	// GitHub back: the check time advances and the errors clear.
	gh.SetUnavailable(false)
	waitFor(t, "the check times to advance and the errors to clear", func() bool {
		inv, job := api.inventory(), api.job(registered.ID)
		return inv.SyncError == "" && inv.CheckedAt.After(*frozenInv.CheckedAt) &&
			job.SyncError == "" && job.CheckedAt.After(*frozenJob.CheckedAt)
	})
	if inv := api.inventory(); inv.Commit != fixedCommit {
		t.Errorf("active commit after recovery = %s, want %s", inv.Commit, fixedCommit)
	}
}
