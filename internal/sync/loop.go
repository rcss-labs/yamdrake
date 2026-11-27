package sync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rcss-labs/yamdrake/internal/github"
	"github.com/rcss-labs/yamdrake/internal/store"
)

// advisoryLockKey is the PostgreSQL advisory lock that makes one orchestrator
// poll at a time. It is the ASCII bytes of "yumdrake" read as a bigint; no
// other code in this database may use it.
const advisoryLockKey int64 = 0x79756d6472616b65

// SyncAll syncs the inventory, then every job source. A failure in one does not
// stop the others; the errors are joined.
func (e *Engine) SyncAll(ctx context.Context) error {
	var errs []error
	if err := e.SyncInventory(ctx); err != nil {
		errs = append(errs, fmt.Errorf("sync inventory: %w", err))
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
		if err := e.SyncJob(ctx, job.ID); err != nil {
			errs = append(errs, fmt.Errorf("sync job source %d (%s %s): %w", job.ID, job.Repo, job.Path, err))
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

// nextWait is the pause before the next round.
func nextWait(roundErr error, interval time.Duration) time.Duration {
	var limited *github.RateLimitError
	if errors.As(roundErr, &limited) && limited.RetryAfter > 0 {
		return limited.RetryAfter
	}
	return interval
}

// runRound runs SyncAll under the advisory lock. ran is false when another
// process holds the lock and nothing was done.
func (e *Engine) runRound(ctx context.Context) (ran bool, err error) {
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
	defer func() {
		// Unlock on the same connection, even when ctx has ended. If that
		// fails, close the connection: a session lock dies with its session.
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var released bool
		if uerr := conn.QueryRow(unlockCtx, "SELECT pg_advisory_unlock($1)", advisoryLockKey).Scan(&released); uerr != nil || !released {
			e.log.Warn("could not release sync lock; closing its connection")
			_ = conn.Conn().Close(unlockCtx)
		}
	}()
	return true, e.SyncAll(ctx)
}
