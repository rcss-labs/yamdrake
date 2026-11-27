package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rcss-labs/yamdrake/internal/api"
	"github.com/rcss-labs/yamdrake/internal/config"
	"github.com/rcss-labs/yamdrake/internal/secrets"
	yumsync "github.com/rcss-labs/yamdrake/internal/sync"
)

// OrchestratorExtension wires the orchestrator's own parts into Run: the
// secret cipher, the sync engine with its polling loop, and the /api/v1 API.
// Without a bootstrap token the API answers 404; without an inventory source
// the loop finds nothing to do.
func OrchestratorExtension(cfg config.Orchestrator) func(context.Context, *slog.Logger, *pgxpool.Pool) (Extension, error) {
	return func(_ context.Context, log *slog.Logger, pool *pgxpool.Pool) (Extension, error) {
		cipher, err := secrets.NewLocal([]byte(cfg.LocalSecretKey.Reveal()))
		if err != nil {
			return Extension{}, fmt.Errorf("secret cipher: %w", err)
		}
		sources := newGitHubSources(pool, cipher, cfg.GitHubAPIURL, log)
		engine := yumsync.NewEngine(pool, sources.For, log, time.Now)
		handler := api.New(api.Deps{
			Pool:                pool,
			Engine:              engine,
			Cipher:              cipher,
			BootstrapToken:      cfg.BootstrapToken,
			Log:                 log,
			OnConnectionCreated: sources.Forget,
		})
		if cfg.BootstrapToken.Reveal() == "" {
			log.Info("the API is disabled: YUMDRAKE_BOOTSTRAP_TOKEN is not set")
		}
		return Extension{
			Handlers: map[string]http.Handler{"/api/v1/": handler},
			Background: []func(context.Context){func(ctx context.Context) {
				log.Info("sync loop started", "interval", cfg.SyncInterval.String(), "github_api_url", cfg.GitHubAPIURL)
				engine.Run(ctx, cfg.SyncInterval)
				log.Info("sync loop stopped")
			}},
		}, nil
	}
}
