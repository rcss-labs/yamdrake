//go:build integration && floci

package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rcss-labs/yamdrake/internal/app"
	"github.com/rcss-labs/yamdrake/internal/config"
	"github.com/rcss-labs/yamdrake/internal/db"
	"github.com/rcss-labs/yamdrake/internal/testutil/testdb"
	"github.com/rcss-labs/yamdrake/test/awstest"
)

// startOrchestrator runs a real orchestrator in this process (as cmd/orchestrator does) on its own
// database and Valkey key prefix, pointed at the fake GitHub, and returns its URL and database URL.
func startOrchestrator(t *testing.T, githubURL string) (baseURL, databaseURL string) {
	t.Helper()
	valkeyAddr := os.Getenv("YUMDRAKE_TEST_VALKEY_ADDR")
	if valkeyAddr == "" {
		t.Fatal("YUMDRAKE_TEST_VALKEY_ADDR must be set for integration tests")
	}
	databaseURL = testdb.New(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	raw := make([]byte, 8)
	_, _ = rand.Read(raw)
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	env := map[string]string{
		"YUMDRAKE_DATABASE_URL":      databaseURL,
		"YUMDRAKE_VALKEY_ADDR":       valkeyAddr,
		"YUMDRAKE_HTTP_ADDR":         addr,
		"YUMDRAKE_HEALTH_ADDR":       "",
		"YUMDRAKE_LOCAL_SECRET_KEY":  base64.StdEncoding.EncodeToString(key),
		"YUMDRAKE_PUBLIC_URL":        "http://" + addr,
		"YUMDRAKE_VALKEY_KEY_PREFIX": "demo-test-" + hex.EncodeToString(raw) + ":",
		"YUMDRAKE_SYNC_INTERVAL":     "10s",
		"YUMDRAKE_GITHUB_API_URL":    githubURL,
		"YUMDRAKE_LOG_LEVEL":         "error",
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
		case <-exited:
		case <-time.After(30 * time.Second):
			t.Error("the orchestrator did not stop within 30s")
		}
	})
	return "http://" + addr, databaseURL
}

func countRows(t *testing.T, pool *pgxpool.Pool, tables ...string) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, table := range tables {
		var n int
		// The table names are the fixed literals below.
		if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil { //nolint:gosec // fixed table names
			t.Fatalf("count %s: %v", table, err)
		}
		counts[table] = n
	}
	return counts
}

// TestSeedIsIdempotent seeds the demo twice into the same Yumdrake and the same Floci. The second run must create
// nothing: no row in Yumdrake, and nothing in AWS (the same services, task definition revisions, image tags and
// digests). It needs Docker, because the images are built and pushed with it (the demo itself, which has no
// Docker, writes them over the registry API). The accounts and names are random, so a Floci shared with other
// tests is not disturbed; the Yumdrake side connects whichever accounts it is given.
func TestSeedIsIdempotent(t *testing.T) {
	gh, err := newDemoGitHub(demoDir)
	if err != nil {
		t.Fatalf("fake GitHub: %v", err)
	}
	ghServer := httptest.NewServer(gh.Handler())
	t.Cleanup(ghServer.Close)

	baseURL, databaseURL := startOrchestrator(t, ghServer.URL)
	world := awstest.New(t)
	aws := awstest.StackOptions{Suffix: awstest.RandomSuffix(), PlatformAccount: awstest.RandomAccountID()}
	accounts := map[string]string{} // the demo's fixed account ids -> random ones of this test
	for _, env := range demoEnvironments {
		if accounts[env.Account] == "" {
			accounts[env.Account] = awstest.RandomAccountID()
		}
		env.Account = accounts[env.Account]
		aws.Environments = append(aws.Environments, env)
	}
	cfg := seedConfig{OrchestratorURL: baseURL, PublicURL: baseURL, DatabaseURL: databaseURL, AWSEndpoint: world.Endpoint, AWS: aws}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := seed(ctx, cfg); err != nil {
		t.Fatalf("first seed: %v", err)
	}

	pool, err := db.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tables := []string{"users", "groups", "grants", "github_connections", "aws_accounts", "folders", "job_sources", "audit_log"}
	first := countRows(t, pool, tables...)
	if first["users"] != 3 || first["github_connections"] != 1 || first["aws_accounts"] != 2 || first["job_sources"] < 2 || first["grants"] == 0 || first["audit_log"] == 0 {
		t.Fatalf("after the first seed the counts are %v; want 3 users, 1 GitHub connection, 2 AWS accounts, jobs, grants and audit entries", first)
	}

	awsFirst := awstest.SeedStack(world, aws)
	if err := seed(ctx, cfg); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if awsSecond := awstest.SeedStack(world, aws); !reflect.DeepEqual(awsFirst, awsSecond) {
		t.Errorf("the second seed changed the local AWS:\nbefore: %+v\nafter:  %+v", awsFirst, awsSecond)
	}
	second := countRows(t, pool, tables...)
	for _, table := range tables {
		if first[table] != second[table] {
			t.Errorf("%s: %d rows after the first seed, %d after the second; the second run must create nothing", table, first[table], second[table])
		}
	}
	// The orchestrator still answers: the second run did not break anything.
	resp, err := http.Get(baseURL + "/readyz") //nolint:gosec,noctx // local test server
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}
