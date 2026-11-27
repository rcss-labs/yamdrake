package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rcss-labs/yamdrake/internal/github"
	"github.com/rcss-labs/yamdrake/internal/secrets"
	"github.com/rcss-labs/yamdrake/internal/store"
	yumsync "github.com/rcss-labs/yamdrake/internal/sync"
)

// connectionPurpose must match the purpose the API encrypts connection
// secrets with.
const connectionPurpose = "github-connection"

// githubSources builds GitHub clients from stored connections, one cached
// client per owner.
//
// Its errors are stored in sync_error and shown through the API, so none of
// them may carry credentials, keys or decrypted bytes: each failure maps to a
// fixed message, and the underlying error is only logged (it never contains
// secrets either, but the message that reaches users is the fixed one).
type githubSources struct {
	pool   *pgxpool.Pool
	cipher secrets.Cipher
	apiURL string
	log    *slog.Logger

	mu      sync.Mutex
	clients map[string]*github.Client
}

func newGitHubSources(pool *pgxpool.Pool, cipher secrets.Cipher, apiURL string, log *slog.Logger) *githubSources {
	return &githubSources{pool: pool, cipher: cipher, apiURL: apiURL, log: log, clients: map[string]*github.Client{}}
}

// For implements sync.SourceFor. GitHub owners are case-insensitive while the
// database is not, so connections are stored and looked up in lower case.
func (g *githubSources) For(ctx context.Context, owner string) (yumsync.Source, error) {
	key := strings.ToLower(owner)
	g.mu.Lock()
	defer g.mu.Unlock()
	if client, ok := g.clients[key]; ok {
		return client, nil
	}
	connection, ciphertext, err := store.ConnectionSecret(ctx, g.pool, key)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("no GitHub connection is configured for owner %q", key)
	}
	if err != nil {
		g.log.Warn("could not load GitHub connection", "owner", key, "error", err.Error())
		return nil, fmt.Errorf("cannot load the GitHub connection for owner %q", key)
	}
	plaintext, err := g.cipher.Decrypt(ctx, connectionPurpose, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("cannot decrypt the GitHub connection for owner %q; check that YUMDRAKE_LOCAL_SECRET_KEY is the key it was stored with", key)
	}
	creds := github.Credentials{Method: connection.Method, AppID: connection.AppID, InstallationID: connection.InstallationID}
	if connection.Method == "token" {
		creds.Token = string(plaintext)
	} else {
		creds.PrivateKeyPEM = plaintext
	}
	client, err := github.New(g.apiURL, nil, creds)
	if err != nil {
		g.log.Warn("stored GitHub credentials are unusable", "owner", key)
		return nil, fmt.Errorf("the stored GitHub connection for owner %q is unusable", key)
	}
	g.clients[key] = client
	return client, nil
}

// Forget drops the cached client for owner (called when its connection is
// created, so no stale client outlives a change).
func (g *githubSources) Forget(owner string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.clients, strings.ToLower(owner))
}
