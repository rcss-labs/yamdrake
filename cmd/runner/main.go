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
	"github.com/rcss-labs/yamdrake/internal/awsx"
	"github.com/rcss-labs/yamdrake/internal/awsx/deploy"
	"github.com/rcss-labs/yamdrake/internal/config"
	"github.com/rcss-labs/yamdrake/internal/db"
	"github.com/rcss-labs/yamdrake/internal/logs"
	"github.com/rcss-labs/yamdrake/internal/queue"
	"github.com/rcss-labs/yamdrake/internal/runner"
	"github.com/rcss-labs/yamdrake/internal/steps/fake"
	"github.com/rcss-labs/yamdrake/internal/version"
	"github.com/rcss-labs/yamdrake/internal/wake"
)

const (
	serviceName       = "yumdrake-runner"
	healthAddrVar     = "YUMDRAKE_HEALTH_ADDR"
	defaultHealthAddr = "127.0.0.1:8081"

	// The log writer stores a leg's lines every logFlushEvery or whenever
	// logMaxBatch of them are waiting.
	logFlushEvery   = 500 * time.Millisecond
	logMaxBatch     = 100
	logCloseTimeout = 10 * time.Second
)

var _ runner.LineSink = (*logs.Writer)(nil)

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
		Extend: func(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, client valkey.Client) (app.Extension, error) {
			credentials, err := newDeployCredentials(ctx, log, cfg, pool)
			if err != nil {
				return app.Extension{}, err
			}
			// The runner does not deploy yet: nothing takes the provider until
			// the deployment engine does.
			_ = credentials
			bus := wake.New(client, log, wake.WithKeyPrefix(cfg.ValkeyKeyPrefix))
			sink := logs.NewWriter(pool, logFlushEvery, logMaxBatch, time.Now, func(runID int64) {
				bus.Publish(context.Background(), wake.TopicLogs, runID)
			}, log)
			r := newRunner(cfg, pool, sink, bus, log)
			return app.Extension{Background: []func(context.Context){
				func(ctx context.Context) {
					if err := r.Run(ctx, legWakeups(ctx, bus)); err != nil {
						log.Error("runner stopped with an error", "error", err.Error())
					}
					// Every leg has settled or been released: store what is left of their logs.
					closeCtx, cancel := context.WithTimeout(context.Background(), logCloseTimeout)
					defer cancel()
					if err := sink.Close(closeCtx); err != nil {
						log.Error("could not store the last log lines", "error", err.Error())
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
	return deploy.New(pool, cipher, awsConfig, cfg.AWS).WithLogger(log), nil
}

// legWakeups turns "legs" wake-ups into the signal Runner.Run waits on. The
// runner claims by id-less poll, so the run id does not matter.
func legWakeups(ctx context.Context, bus *wake.Bus) <-chan struct{} {
	out := make(chan struct{}, 1)
	ids := bus.Subscribe(ctx, wake.TopicLegs)
	go func() {
		for range ids {
			select {
			case out <- struct{}{}:
			default: // one pending signal is enough
			}
		}
	}()
	return out
}

// newRunner builds the worker pool. Lines go to sink; after each settled leg
// the runner announces its run on the "runs" topic so the orchestrator
// advances it at once. The fake steps are registered only when
// the configuration allows them (YUMDRAKE_ENV=dev and YUMDRAKE_FAKE_STEPS=true;
// config.LoadRunner refuses fakes anywhere else). The real executors arrive
// with plans 5 and 6, so until then a runner without fakes fails every leg it
// claims with "no executor for this step".
func newRunner(cfg config.Runner, pool *pgxpool.Pool, sink runner.LineSink, bus *wake.Bus, log *slog.Logger) *runner.Runner {
	q := queue.New(pool, cfg.Lease, nil) // nil: the database clock, shared by every runner and orchestrator
	r := runner.New(q, sink, cfg.RunnerID, cfg.Concurrency, cfg.Heartbeat, cfg.Poll, log).
		WithShutdownTimeout(cfg.ShutdownTimeout).
		WithSettledNotifier(func(runID int64) { bus.Publish(context.Background(), wake.TopicRuns, runID) })
	if cfg.FakeSteps && cfg.Env == "dev" {
		fake.Register(r, fake.Options{StepTime: 2 * time.Second, SlowFor: 30 * time.Second})
		log.Warn("fake steps are registered; nothing real will be changed")
	}
	return r
}
