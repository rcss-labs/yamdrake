// Command orchestrator is the Yumdrake control-plane service.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rcss-labs/yamdrake/internal/app"
	"github.com/rcss-labs/yamdrake/internal/config"
	"github.com/rcss-labs/yamdrake/internal/db"
	"github.com/rcss-labs/yamdrake/internal/version"
)

const (
	serviceName     = "yumdrake-orchestrator"
	httpAddrVar     = "YUMDRAKE_HTTP_ADDR"
	defaultHTTPAddr = ":8080"
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
		return app.Healthcheck(ctx, getenv, httpAddrVar, defaultHTTPAddr, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "usage: %s [version|healthcheck]\n\nWith no subcommand the service starts and is configured through YUMDRAKE_* environment variables.\n", serviceName)
		return app.ExitUsage
	}
}

func serve(ctx context.Context, getenv func(string) string, stdout, stderr io.Writer) int {
	cfg, err := config.LoadOrchestrator(getenv)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return app.ExitUsage
	}
	return app.Run(ctx, app.Service{
		Name:   serviceName,
		Common: cfg.Common,
		Addr:   cfg.HTTPAddr,
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
	}, stdout)
}
