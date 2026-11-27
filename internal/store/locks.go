package store

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// maxAcquireAttempts bounds the retry for the rare case where the run holding
// a contested resource released it between our insert and our read of it.
const maxAcquireAttempts = 5

// AcquireLocks takes every resource for the run, or none of them. An empty
// result means all are held by runID now (resources it already held stay held).
// Otherwise nothing was taken and the map names the run holding each contested
// resource.
//
// Concurrency: the resources are de-duplicated and sorted, so every caller
// inserts in the same global order and two callers can never wait on each
// other in a cycle (no deadlock). They go in one INSERT ... ON CONFLICT DO
// NOTHING; a conflicting uncommitted insert of another transaction makes the
// statement wait for it, then skip that resource. If anything was skipped
// because another run holds it, the whole attempt is rolled back to a
// savepoint so the rows inserted before the conflict do not survive, even if
// the caller commits the surrounding transaction.
func AcquireLocks(ctx context.Context, tx DBTX, runID int64, resources []string, at time.Time) (map[string]int64, error) {
	sorted := uniqueSorted(resources)
	heldBy := map[string]int64{}
	if len(sorted) == 0 {
		return heldBy, nil
	}
	for attempt := 0; attempt < maxAcquireAttempts; attempt++ {
		held, retry, err := tryAcquire(ctx, tx, runID, sorted, at)
		if err != nil {
			return nil, fmt.Errorf("acquire locks: %w", mapError(err))
		}
		if !retry {
			return held, nil
		}
	}
	return nil, fmt.Errorf("acquire locks: holders kept changing during %d attempts", maxAcquireAttempts)
}

func tryAcquire(ctx context.Context, tx DBTX, runID int64, sorted []string, at time.Time) (held map[string]int64, retry bool, err error) {
	sp, err := tx.Begin(ctx) // a savepoint when tx is a transaction
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = sp.Rollback(ctx) }() // no-op after Commit

	if _, err := sp.Exec(ctx,
		`INSERT INTO run_locks (resource, run_id, acquired_at)
		 SELECT r, $2, $3 FROM unnest($1::text[]) WITH ORDINALITY AS t(r, n) ORDER BY n
		 ON CONFLICT (resource) DO NOTHING`, sorted, runID, dbTime(at)); err != nil {
		return nil, false, err
	}
	rows, err := sp.Query(ctx, "SELECT resource, run_id FROM run_locks WHERE resource = ANY($1)", sorted)
	if err != nil {
		return nil, false, err
	}
	owners := map[string]int64{}
	for rows.Next() {
		var resource string
		var owner int64
		if err := rows.Scan(&resource, &owner); err != nil {
			rows.Close()
			return nil, false, err
		}
		owners[resource] = owner
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	held = map[string]int64{}
	for _, resource := range sorted {
		owner, present := owners[resource]
		switch {
		case !present:
			// Skipped because of a holder that has since gone: try again.
			return nil, true, nil
		case owner != runID:
			held[resource] = owner
		}
	}
	if len(held) > 0 {
		return held, false, sp.Rollback(ctx) // drop the partial inserts
	}
	return held, false, sp.Commit(ctx)
}

func uniqueSorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	n := 0
	for i, s := range out {
		if i == 0 || s != out[i-1] {
			out[n] = s
			n++
		}
	}
	return out[:n]
}

// ReleaseLocks frees every resource the run holds.
func ReleaseLocks(ctx context.Context, tx DBTX, runID int64) error {
	if _, err := tx.Exec(ctx, "DELETE FROM run_locks WHERE run_id = $1", runID); err != nil {
		return fmt.Errorf("release locks: %w", mapError(err))
	}
	return nil
}

// LocksOfRun returns the resources the run holds, sorted.
func LocksOfRun(ctx context.Context, db DBTX, runID int64) ([]string, error) {
	rows, err := db.Query(ctx, "SELECT resource FROM run_locks WHERE run_id = $1 ORDER BY resource", runID)
	if err != nil {
		return nil, fmt.Errorf("locks of run: %w", mapError(err))
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("locks of run: %w", mapError(err))
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("locks of run: %w", mapError(err))
	}
	return out, nil
}
