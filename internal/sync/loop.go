package sync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rcss-labs/yumdrake/internal/github"
	"github.com/rcss-labs/yumdrake/internal/store"
)

// advisoryLockKey is the PostgreSQL advisory lock that makes one orchestrator
// poll at a time. It is the ASCII bytes of "yumdrake" read as a bigint; no
// other code in this database may use it.
const advisoryLockKey int64 = 0x79756d6472616b65

// maxRateLimitWait caps the pause after a rate limited round, whatever
// Retry-After says.
const maxRateLimitWait = 15 * time.Minute

// SyncAll syncs the inventory, then every job source. A failure in one does not
// stop the others; the errors are joined.
func (e *Engine) SyncAll(ctx context.Context) error {
	return e.withLock(ctx, e.syncAll)
}

// syncAll is SyncAll without the lock; the caller must hold it.
func (e *Engine) syncAll(ctx context.Context) error {
	var errs []error
	if err := e.syncInventory(ctx); err != nil {
		errs = append(errs, fmt.Errorf("sync inventory: %w", err))
		if isRateLimited(err) {
			return errors.Join(errs...)
		}
	}
	jobs, err := store.ListJobSources(ctx, e.pool)
	if err != nil {
		return errors.Join(append(errs, fmt.Errorf("list job sources: %w", err))...)
	}
	for _, job := range jobs {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		if err := e.syncJob(ctx, job.ID); err != nil {
			errs = append(errs, fmt.Errorf("sync job source %d (%s %s): %w", job.ID, job.Repo, job.Path, err))
			if isRateLimited(err) {
				break // more calls would only extend the limit
			}
		}
	}
	return errors.Join(errs...)
}

// Run loops SyncAll until ctx ends. Each round holds the advisory lock; a round
// that cannot get it is skipped. After a round the loop waits interval, or the
// delay GitHub asked for when the round was rate limited.
func (e *Engine) Run(ctx context.Context, interval time.Duration) {
	for ctx.Err() == nil {
		_, err := e.runRound(ctx)
		if err != nil && ctx.Err() == nil {
			e.log.Warn("sync round finished with errors", "error", err.Error())
		}
		timer := time.NewTimer(nextWait(err, interval))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func isRateLimited(err error) bool {
	var limited *github.RateLimitError
	return errors.As(err, &limited)
}

// nextWait is the pause before the next round: the interval, or after a rate
// limited round the delay GitHub asked for, never shorter than the interval and
// never longer than maxRateLimitWait (unless the interval itself is longer).
func nextWait(roundErr error, interval time.Duration) time.Duration {
	var limited *github.RateLimitError
	if errors.As(roundErr, &limited) {
		return max(interval, min(limited.RetryAfter, maxRateLimitWait))
	}
	return interval
}

// withLock runs fn while holding the advisory lock, waiting for it if another
// caller (Run's round or an on-demand sync) has it. The exported Sync methods
// all go through here so that an activation, a job save and another activation
// never interleave: a job validated against the old inventory cannot be saved
// after the activation that re-validated it. The wait uses pg_advisory_lock on
// a dedicated connection, so a cancelled ctx ends it; on any failure to unlock
// the connection is closed, which drops the session lock.
//
// In-process callers queue on e.local first, before taking any pool
// connection. Otherwise each waiter would sit in pg_advisory_lock holding a
// connection, and enough of them leave the lock holder unable to get the
// connection it needs for its own queries (pool exhaustion deadlock). So at
// most one connection per process waits on or holds the lock.
func (e *Engine) withLock(ctx context.Context, fn func(context.Context) error) error {
	select {
	case e.local <- struct{}{}:
		defer func() { <-e.local }()
	case <-ctx.Done():
		return ctx.Err()
	}
	conn, err := e.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire lock connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		// The wait may have been cancelled mid-request; do not return a
		// connection whose lock state is unknown to the pool.
		_ = conn.Conn().Close(context.WithoutCancel(ctx))
		return fmt.Errorf("take advisory lock: %w", err)
	}
	defer e.unlock(ctx, conn)
	return fn(ctx)
}

// unlock releases the advisory lock on the connection that took it, even when
// ctx has ended; if that fails the connection is closed instead.
func (e *Engine) unlock(ctx context.Context, conn *pgxpool.Conn) {
	unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var released bool
	if err := conn.QueryRow(unlockCtx, "SELECT pg_advisory_unlock($1)", advisoryLockKey).Scan(&released); err != nil || !released {
		e.log.Warn("could not release sync lock; closing its connection")
		_ = conn.Conn().Close(unlockCtx)
	}
}

// runRound runs syncAll under the advisory lock, or does nothing when another
// holder has it (Run's rounds skip rather than queue). ran is false when
// skipped.
func (e *Engine) runRound(ctx context.Context) (ran bool, err error) {
	select {
	case e.local <- struct{}{}:
		defer func() { <-e.local }()
	default:
		e.log.Debug("sync round skipped: a sync is already running in this process")
		return false, nil
	}
	conn, err := e.pool.Acquire(ctx)
	if err != nil {
		return false, fmt.Errorf("acquire lock connection: %w", err)
	}
	defer conn.Release()

	var acquired bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", advisoryLockKey).Scan(&acquired); err != nil {
		return false, fmt.Errorf("take advisory lock: %w", err)
	}
	if !acquired {
		e.log.Debug("sync round skipped: another process holds the lock")
		return false, nil
	}
	defer e.unlock(ctx, conn)
	return true, e.syncAll(ctx)
}
