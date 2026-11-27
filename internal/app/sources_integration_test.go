//go:build integration

package app

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/rcss-labs/yamdrake/internal/db"
	"github.com/rcss-labs/yamdrake/internal/github"
	"github.com/rcss-labs/yamdrake/internal/secrets"
	"github.com/rcss-labs/yamdrake/internal/store"
	"github.com/rcss-labs/yamdrake/internal/testutil/testdb"
)

func newSources(t *testing.T) (*githubSources, secrets.Cipher, store.DBTX) {
	t.Helper()
	ctx := context.Background()
	url := testdb.New(t)
	if err := db.Migrate(ctx, url); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)
	cipher, err := secrets.NewLocal([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return newGitHubSources(pool, cipher, "https://api.github.test", log), cipher, pool
}

func TestSourceForBuildsOneCachedClientPerOwnerIgnoringCase(t *testing.T) {
	sources, cipher, pool := newSources(t)
	ctx := context.Background()
	ciphertext, err := cipher.Encrypt(ctx, "github-connection", []byte("tok-123"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := store.CreateConnection(ctx, pool, store.Connection{Owner: "acme", Method: "token"}, ciphertext); err != nil {
		t.Fatalf("CreateConnection: %v", err)
	}

	first, err := sources.For(ctx, "Acme")
	if err != nil {
		t.Fatalf("For(Acme): %v", err)
	}
	if _, ok := first.(*github.Client); !ok {
		t.Fatalf("source is %T, want *github.Client", first)
	}
	second, err := sources.For(ctx, "ACME")
	if err != nil || second != first {
		t.Fatalf("For(ACME) = %v, %v; want the cached client", second, err)
	}
	sources.Forget("acme")
	third, err := sources.For(ctx, "acme")
	if err != nil || third == first {
		t.Fatalf("after Forget For = %v, %v; want a new client", third, err)
	}
}

func TestSourceForErrorsNeverCarryCredentials(t *testing.T) {
	sources, cipher, pool := newSources(t)
	ctx := context.Background()

	if _, err := sources.For(ctx, "nobody"); err == nil || !strings.Contains(err.Error(), "nobody") {
		t.Errorf("unknown owner error = %v, want it to name the owner", err)
	}

	const secret = "tok-must-not-leak"
	other, err := secrets.NewLocal([]byte("ffffffffffffffffffffffffffffffff"))
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	foreign, err := other.Encrypt(ctx, "github-connection", []byte(secret))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := store.CreateConnection(ctx, pool, store.Connection{Owner: "wrongkey", Method: "token"}, foreign); err != nil {
		t.Fatalf("CreateConnection: %v", err)
	}
	_, err = sources.For(ctx, "wrongkey")
	if err == nil || !strings.Contains(err.Error(), "cannot decrypt") {
		t.Fatalf("wrong-key error = %v, want a fixed decrypt message", err)
	}

	// A decryptable secret that is not a usable App key is reported without it.
	badKey, err := cipher.Encrypt(ctx, "github-connection", []byte("not a pem "+secret))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := store.CreateConnection(ctx, pool, store.Connection{Owner: "badkey", Method: "app", AppID: 1, InstallationID: 2}, badKey); err != nil {
		t.Fatalf("CreateConnection: %v", err)
	}
	_, err = sources.For(ctx, "badkey")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("bad-key error = %v, want an error without the key material", err)
	}
}
