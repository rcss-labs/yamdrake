package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/jackc/pgx/v5/pgxpool"
	valkey "github.com/valkey-io/valkey-go"

	"github.com/rcss-labs/yamdrake/internal/api"
	"github.com/rcss-labs/yamdrake/internal/awsx"
	"github.com/rcss-labs/yamdrake/internal/config"
	"github.com/rcss-labs/yamdrake/internal/db"
	"github.com/rcss-labs/yamdrake/internal/secrets"
	"github.com/rcss-labs/yamdrake/internal/session"
	yumsync "github.com/rcss-labs/yamdrake/internal/sync"
	"github.com/rcss-labs/yamdrake/internal/webui"
)

// OrchestratorServiceName is the name the orchestrator logs under.
const OrchestratorServiceName = "yumdrake-orchestrator"

// NewOrchestratorService describes the orchestrator for Run: the shared
// start-up with the schema migration (when cfg.MigrateOnStart is set) and the
// orchestrator's own parts. cmd/orchestrator and the end-to-end tests both
// start it this way.
func NewOrchestratorService(cfg config.Orchestrator) Service {
	return Service{
		Name:   OrchestratorServiceName,
		Common: cfg.Common,
		Addr:   cfg.HTTPAddr,
		Extend: OrchestratorExtension(cfg),
		Web:    webui.Handler(),
		Fields: []any{"migrate_on_start", cfg.MigrateOnStart, "migrate_timeout", cfg.MigrateTimeout.String()},
		Prepare: func(ctx context.Context, log *slog.Logger, _ *pgxpool.Pool) error {
			if !cfg.MigrateOnStart {
				return nil
			}
			log.Info("applying migrations")
			migrateCtx, cancel := context.WithTimeout(ctx, cfg.MigrateTimeout)
			defer cancel()
			return db.Migrate(migrateCtx, cfg.DatabaseURL)
		},
	}
}

// OrchestratorExtension wires the orchestrator's own parts into Run: the
// secret cipher, the sync engine with its polling loop, and the /api/v1 API
// with its sessions. Without an inventory source the loop finds nothing to do.
func OrchestratorExtension(cfg config.Orchestrator) func(context.Context, *slog.Logger, *pgxpool.Pool, valkey.Client) (Extension, error) {
	return func(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, client valkey.Client) (Extension, error) {
		awsConfig, err := awsx.BaseConfig(ctx, cfg.AWS)
		if err != nil {
			if cfg.SecretsMode == "kms" {
				return Extension{}, err
			}
			// Without KMS the AWS configuration is only for the connection test.
			log.Warn("the default AWS configuration could not be loaded; testing AWS accounts will fail", "error", err.Error())
		}
		cipher, err := newCipher(cfg.Common, awsConfig)
		if err != nil {
			return Extension{}, err
		}
		sources := newGitHubSources(pool, cipher, cfg.GitHubAPIURL, log)
		engine := yumsync.NewEngine(pool, sources.For, log, time.Now)
		handler := api.New(api.Deps{
			Pool:                pool,
			Engine:              engine,
			Cipher:              cipher,
			Log:                 log,
			OnConnectionCreated: sources.Forget,
			Sessions:            session.NewManager(client, time.Now, session.WithKeyPrefix(cfg.ValkeyKeyPrefix)),
			Valkey:              client,
			KeyPrefix:           cfg.ValkeyKeyPrefix,
			PublicURL:           cfg.PublicURL,
			TokenSSOMaxAge:      cfg.TokenSSOMaxAge,
			AWSConfig:           awsConfig,
		})
		return Extension{
			Handlers: map[string]http.Handler{"/api/v1/": handler, "/saml/": handler},
			Background: []func(context.Context){func(ctx context.Context) {
				log.Info("sync loop started", "interval", cfg.SyncInterval.String(), "github_api_url", cfg.GitHubAPIURL)
				engine.Run(ctx, cfg.SyncInterval)
				log.Info("sync loop stopped")
			}},
		}, nil
	}
}

// newCipher builds the secret cipher of the configured mode. KMS needs a
// region (from the default AWS chain, for example AWS_REGION) and, when it is
// used, credentials; the error never carries key material.
func newCipher(cfg config.Common, awsConfig aws.Config) (secrets.Cipher, error) {
	if cfg.SecretsMode == "kms" {
		if awsConfig.Region == "" {
			return nil, errors.New("secret cipher: kms mode needs an AWS region (set AWS_REGION)")
		}
		return secrets.NewKMS(kms.NewFromConfig(awsConfig), cfg.KMSKeyID), nil
	}
	cipher, err := secrets.NewLocal([]byte(cfg.LocalSecretKey.Reveal()))
	if err != nil {
		return nil, fmt.Errorf("secret cipher: %w", err)
	}
	return cipher, nil
}
