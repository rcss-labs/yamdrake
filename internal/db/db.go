// Package db owns the PostgreSQL connection pool and schema migrations.
package db

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rcss-labs/yumdrake/internal/config"
)

const (
	waitOpenInitialBackoff = 500 * time.Millisecond
	waitOpenMaxBackoff     = 5 * time.Second
	waitSchemaPollInterval = time.Second
)

// Open creates a connection pool and pings the database once. Errors never
// contain the password from url.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, redactedError("parse database URL", url, err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, redactedError("create connection pool", url, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, redactedError("ping database", url, err)
	}
	return pool, nil
}

// WaitOpen retries Open with exponential backoff (500ms up to 5s) until it
// succeeds, maxWait passes or ctx is cancelled.
func WaitOpen(ctx context.Context, url string, maxWait time.Duration, log *slog.Logger) (*pgxpool.Pool, error) {
	started := time.Now()
	deadline := started.Add(maxWait)
	backoff := waitOpenInitialBackoff
	for attempt := 1; ; attempt++ {
		// Bound each attempt by the overall deadline so one hanging dial cannot
		// outlast maxWait. The pool does not depend on this context after Open
		// returns (it only governs the initial connection and ping).
		attemptCtx, cancelAttempt := context.WithDeadline(ctx, deadline)
		pool, err := Open(attemptCtx, url)
		cancelAttempt()
		if err == nil {
			return pool, nil
		}
		log.Warn("database not ready", "attempt", attempt, "url", config.RedactURL(url), "error", err.Error())

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, redactedError(fmt.Sprintf("database not available after waiting %s", maxWait), url, err)
		}
		if ctx.Err() != nil {
			return nil, redactedError("waiting for database cancelled", url, ctx.Err())
		}
		// Never sleep past the deadline.
		sleep := min(backoff, remaining)
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, redactedError("waiting for database cancelled", url, ctx.Err())
		case <-timer.C:
		}
		backoff = min(backoff*2, waitOpenMaxBackoff)
	}
}

// SchemaVersion returns the highest applied migration version, or 0 when goose
// has never run on the database. It does not create the goose version table.
func SchemaVersion(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var tableExists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('goose_db_version') IS NOT NULL").Scan(&tableExists); err != nil {
		return 0, fmt.Errorf("check migration table: %w", err)
	}
	if !tableExists {
		return 0, nil
	}
	var version int64
	if err := pool.QueryRow(ctx, "SELECT coalesce(max(version_id), 0) FROM goose_db_version WHERE is_applied").Scan(&version); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return version, nil
}

// WaitSchema polls SchemaVersion every second until it reaches LatestVersion.
func WaitSchema(ctx context.Context, pool *pgxpool.Pool, maxWait time.Duration) error {
	deadline := time.Now().Add(maxWait)
	want := LatestVersion()
	for {
		got, err := SchemaVersion(ctx, pool)
		if err == nil && got >= want {
			return nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if err != nil {
				return fmt.Errorf("schema not ready after %s: want version %d: %w", maxWait, want, err)
			}
			return fmt.Errorf("schema not ready after %s: at version %d, want at least %d", maxWait, got, want)
		}
		timer := time.NewTimer(min(waitSchemaPollInterval, remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("waiting for schema cancelled: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

// scrubbedError keeps the wrapped error for errors.Is/As (callers must not print
// the unwrapped error) but prints a message
// with the connection URL and its password removed.
type scrubbedError struct {
	message string
	err     error
}

func (e *scrubbedError) Error() string { return e.message }
func (e *scrubbedError) Unwrap() error { return e.err }

// redactedError wraps err with context and removes the raw URL and password
// from the message, since driver errors may echo connection details.
func redactedError(context, rawURL string, err error) error {
	return &scrubbedError{message: config.Scrub(context+": "+err.Error(), rawURL), err: err}
}
