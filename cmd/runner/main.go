// Command runner is the Yumdrake worker service. It never migrates the schema.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	valkey "github.com/valkey-io/valkey-go"

	"github.com/rcss-labs/yamdrake/internal/app"
	"github.com/rcss-labs/yamdrake/internal/config"
	"github.com/rcss-labs/yamdrake/internal/db"
	"github.com/rcss-labs/yamdrake/internal/queue"
	"github.com/rcss-labs/yamdrake/internal/runner"
	"github.com/rcss-labs/yamdrake/internal/steps/fake"
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
		Fields: []any{"runner_id", cfg.RunnerID, "env", cfg.Env, "fake_steps", cfg.FakeSteps},
		Extend: func(_ context.Context, log *slog.Logger, pool *pgxpool.Pool, _ valkey.Client) (app.Extension, error) {
			r := newRunner(cfg, pool, log)
			return app.Extension{Background: []func(context.Context){
				func(ctx context.Context) {
					if err := r.Run(ctx, nil); err != nil {
						log.Error("runner stopped with an error", "error", err.Error())
					}
				},
			}}, nil
		},
		Prepare: func(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool) error {
			log.Info("waiting for schema", "want_version", db.LatestVersion())
			return db.WaitSchema(ctx, pool, cfg.StartupTimeout)
		},
	}, stdout)
}

// newRunner builds the worker pool. The fake steps are registered only when
// the configuration allows them (YUMDRAKE_ENV=dev and YUMDRAKE_FAKE_STEPS=true;
// config.LoadRunner refuses fakes anywhere else). The real executors arrive
// with plans 5 and 6, so until then a runner without fakes fails every leg it
// claims with "no executor for this step".
func newRunner(cfg config.Runner, pool *pgxpool.Pool, log *slog.Logger) *runner.Runner {
	q := queue.New(pool, cfg.Lease, time.Now)
	r := runner.New(q, discardSink{}, cfg.RunnerID, cfg.Concurrency, cfg.Heartbeat, cfg.Poll, log).
		WithShutdownTimeout(cfg.ShutdownTimeout)
	if cfg.FakeSteps && cfg.Env == "dev" {
		fake.Register(r, fake.Options{StepTime: 2 * time.Second, SlowFor: 30 * time.Second})
		log.Warn("fake steps are registered; nothing real will be changed")
	}
	return r
}

// discardSink drops log lines. It stands in until the log writer is wired in
// (plan 4, task 8); legs run and settle normally without stored lines.
type discardSink struct{}

func (discardSink) Write(_, _, _ int64, _, _ string)   {}
func (discardSink) Flush(context.Context, int64) error { return nil }
