// Command runner is the Yumdrake worker service. It never migrates the schema.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	valkey "github.com/valkey-io/valkey-go"

	"github.com/rcss-labs/yamdrake/internal/app"
	"github.com/rcss-labs/yamdrake/internal/awsx"
	"github.com/rcss-labs/yamdrake/internal/awsx/deploy"
	"github.com/rcss-labs/yamdrake/internal/config"
	"github.com/rcss-labs/yamdrake/internal/db"
	"github.com/rcss-labs/yamdrake/internal/version"
)

const (
	serviceName       = "yumdrake-runner"
	healthAddrVar     = "YUMDRAKE_HEALTH_ADDR"
	defaultHealthAddr = "127.0.0.1:8081"
)

func main() {
	ctx, stop := app.SignalContext(context.Background())
	code := run(ctx, os.Args, os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run executes the subcommand named by args[1] (default: serve) and returns the exit code.
func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	subcommand := ""
	if len(args) > 1 {
		subcommand = args[1]
	}
	switch subcommand {
	case "":
		return serve(ctx, getenv, stdout, stderr)
	case "version":
		_, _ = fmt.Fprintln(stdout, version.String())
		return app.ExitOK
	case "healthcheck":
		return app.Healthcheck(ctx, getenv, healthAddrVar, defaultHealthAddr, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "usage: %s [version|healthcheck]\n\nWith no subcommand the service starts and is configured through YUMDRAKE_* environment variables.\n", serviceName)
		return app.ExitUsage
	}
}

func serve(ctx context.Context, getenv func(string) string, stdout, stderr io.Writer) int {
	cfg, err := config.LoadRunner(getenv)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return app.ExitUsage
	}
	return app.Run(ctx, app.Service{
		Name:   serviceName,
		Common: cfg.Common,
		Addr:   cfg.HealthAddr,
		Fields: []any{"runner_id", cfg.RunnerID},
		Extend: func(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, _ valkey.Client) (app.Extension, error) {
			credentials, err := newDeployCredentials(ctx, log, cfg, pool)
			if err != nil {
				return app.Extension{}, err
			}
			// The runner does not deploy yet: nothing takes the provider until
			// the deployment engine does.
			_ = credentials
			return app.Extension{}, nil
		},
		Prepare: func(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool) error {
			log.Info("waiting for schema", "want_version", db.LatestVersion())
			return db.WaitSchema(ctx, pool, cfg.StartupTimeout)
		},
	}, stdout)
}

// newDeployCredentials builds the provider of deployment credentials from the
// runner's own AWS identity. Only the runner binary imports internal/awsx/deploy
// (internal/awsx/boundary_test.go). Nothing is resolved here, so a runner
// without AWS credentials still starts; a missing KMS region does fail it, as
// it fails the orchestrator, because the secret cipher cannot work.
func newDeployCredentials(ctx context.Context, log *slog.Logger, cfg config.Runner, pool *pgxpool.Pool) (*deploy.Provider, error) {
	awsConfig, err := awsx.BaseConfig(ctx, cfg.AWS)
	if err != nil {
		if cfg.SecretsMode == "kms" {
			return nil, err
		}
		log.Warn("the default AWS configuration could not be loaded; deploying to AWS accounts will fail", "error", err.Error())
	}
	cipher, err := app.NewCipher(cfg.Common, awsConfig)
	if err != nil {
		return nil, err
	}
	return deploy.New(pool, cipher, awsConfig, cfg.AWS), nil
}
