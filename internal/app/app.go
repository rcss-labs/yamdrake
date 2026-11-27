// Package app holds the service lifecycle shared by the orchestrator and runner:
// serving HTTP servers with graceful shutdown, the common start-up sequence and
// the container health-check subcommand.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	valkey "github.com/valkey-io/valkey-go"

	"github.com/rcss-labs/yamdrake/internal/cache"
	"github.com/rcss-labs/yamdrake/internal/config"
	"github.com/rcss-labs/yamdrake/internal/db"
	"github.com/rcss-labs/yamdrake/internal/health"
	"github.com/rcss-labs/yamdrake/internal/logging"
)

// Exit codes returned by the service entry points.
const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
)

const probeTimeout = 3 * time.Second

// Serve starts every server and blocks until ctx is cancelled or a server fails.
// All listeners are bound before any server starts, so a listen error is
// returned immediately. On cancellation every server is shut down gracefully
// within shutdownTimeout. It returns nil for a clean shutdown.
func Serve(ctx context.Context, log *slog.Logger, shutdownTimeout time.Duration, servers ...*http.Server) error {
	listeners := make([]net.Listener, 0, len(servers))
	for _, srv := range servers {
		ln, err := net.Listen("tcp", srv.Addr)
		if err != nil {
			for _, bound := range listeners {
				_ = bound.Close()
			}
			return fmt.Errorf("listen on %q: %w", srv.Addr, err)
		}
		listeners = append(listeners, ln)
	}

	serveErrs := make(chan error, len(servers))
	for i, srv := range servers {
		log.Info("listening", "addr", listeners[i].Addr().String())
		go func() {
			if err := srv.Serve(listeners[i]); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serveErrs <- fmt.Errorf("serve on %q: %w", srv.Addr, err)
			}
		}()
	}

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-serveErrs:
	}

	log.Info("shutting down", "timeout", shutdownTimeout.String())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	results := make(chan error, len(servers))
	for _, srv := range servers {
		go func() {
			err := srv.Shutdown(shutdownCtx)
			if err != nil {
				// Shutdown gave up waiting; drop the remaining connections.
				_ = srv.Close()
				err = fmt.Errorf("shut down %q: %w", srv.Addr, err)
			}
			results <- err
		}()
	}
	errs := []error{serveErr}
	for range servers {
		errs = append(errs, <-results)
	}
	return errors.Join(errs...)
}

// PostgresCheck is the readiness check for the database pool. The returned
// error carries only the error type, never the driver's message, because the
// health handler logs check errors verbatim and driver messages may echo
// connection details.
func PostgresCheck(pool *pgxpool.Pool) health.Check {
	return health.Check{Name: "postgres", Fn: func(ctx context.Context) error {
		if err := pool.Ping(ctx); err != nil {
			return fmt.Errorf("postgres ping failed (%T)", err)
		}
		return nil
	}}
}

// ValkeyCheck is the readiness check for the Valkey client.
func ValkeyCheck(client valkey.Client) health.Check {
	return health.Check{Name: "valkey", Fn: func(ctx context.Context) error {
		if err := cache.Ping(ctx, client); err != nil {
			return fmt.Errorf("valkey ping failed: %w", err)
		}
		return nil
	}}
}

// Service describes one service binary for Run.
type Service struct {
	Name   string
	Common config.Common
	// Addr is the address of the health/HTTP server.
	Addr string
	// Fields are extra attributes for the "starting" log line.
	Fields []any
	// Prepare runs after the database is open and before serving (migrate or
	// wait for the schema). It receives the service context and must bound
	// itself by the start-up timeout.
	Prepare func(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool) error
}

// Run executes the shared start-up sequence and serves until ctx is cancelled.
// It returns ExitOK on a clean shutdown and ExitFailure otherwise.
func Run(ctx context.Context, svc Service, stdout io.Writer) int {
	log := logging.New(stdout, svc.Common.LogLevel, svc.Name)
	log.Info("starting", append([]any{
		"database", config.RedactURL(svc.Common.DatabaseURL),
		"valkey", svc.Common.ValkeyAddr,
		"addr", svc.Addr,
	}, svc.Fields...)...)

	startCtx, cancelStart := context.WithTimeout(ctx, svc.Common.StartupTimeout)
	defer cancelStart()

	pool, err := db.WaitOpen(startCtx, svc.Common.DatabaseURL, svc.Common.StartupTimeout, log)
	if err != nil {
		log.Error("database unavailable", "error", err.Error())
		return ExitFailure
	}
	defer pool.Close()

	if svc.Prepare != nil {
		if err := svc.Prepare(ctx, log, pool); err != nil {
			log.Error("start-up failed", "error", err.Error())
			return ExitFailure
		}
	}

	client, err := cache.Open(svc.Common.ValkeyAddr)
	if err != nil {
		log.Error("valkey client unavailable", "error", err.Error())
		return ExitFailure
	}
	defer client.Close()

	server := &http.Server{
		Addr: svc.Addr,
		Handler: health.Handler(
			[]health.Check{PostgresCheck(pool), ValkeyCheck(client)},
			health.WithLogger(log),
		),
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := Serve(ctx, log, svc.Common.ShutdownTimeout, server); err != nil {
		log.Error("serve failed", "error", err.Error())
		return ExitFailure
	}
	log.Info("stopped")
	return ExitOK
}

// Healthcheck implements the healthcheck subcommand: it reads only addrVar
// (falling back to defaultAddr), probes /healthz on loopback at that port and
// returns ExitOK or ExitFailure; an invalid address returns ExitUsage.
func Healthcheck(ctx context.Context, getenv func(string) string, addrVar, defaultAddr string, stderr io.Writer) int {
	addr := getenv(addrVar)
	if addr == "" {
		addr = defaultAddr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s: must be host:port\n", addrVar)
		return ExitUsage
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		_, _ = fmt.Fprintf(stderr, "%s: port must be a number between 1 and 65535\n", addrVar)
		return ExitUsage
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}

	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if err := health.Probe(probeCtx, "http://"+net.JoinHostPort(host, port)+"/healthz"); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return ExitFailure
	}
	return ExitOK
}
