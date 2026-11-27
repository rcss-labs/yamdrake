package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const inventorySourceKey = "inventory_source"

// InventorySource locates the inventory file; it lives in instance_settings.
type InventorySource struct{ Repo, Branch, Path string }

// SetInventorySource stores (or replaces) the inventory source. When the
// source differs from the stored one, the poll status is reset in the same
// transaction (no check time, no problems), so the next sync reads the new
// source again even if the branch head has not moved; "already seen" is only
// meaningful for the source it was seen with.
func SetInventorySource(ctx context.Context, db DBTX, s InventorySource) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("set inventory source: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The status row is the lock row for inventory writers.
	if _, err := tx.Exec(ctx, "SELECT 1 FROM inventory_status WHERE id = 1 FOR UPDATE"); err != nil {
		return fmt.Errorf("set inventory source: lock: %w", err)
	}
	previous, configured, err := GetInventorySource(ctx, tx)
	if err != nil {
		return err
	}
	value, err := json.Marshal(map[string]string{"repo": s.Repo, "branch": s.Branch, "path": s.Path})
	if err != nil {
		return fmt.Errorf("encode inventory source: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO instance_settings (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		inventorySourceKey, value); err != nil {
		return fmt.Errorf("set inventory source: %w", err)
	}
	if configured && previous != s {
		if _, err := tx.Exec(ctx,
			"UPDATE inventory_status SET checked_at = NULL, problems = NULL, problems_commit = '' WHERE id = 1"); err != nil {
			return fmt.Errorf("set inventory source: reset status: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("set inventory source: commit: %w", err)
	}
	return nil
}

// GetInventorySource reports false when no source has been set.
func GetInventorySource(ctx context.Context, db DBTX) (InventorySource, bool, error) {
	var raw []byte
	err := db.QueryRow(ctx, "SELECT value FROM instance_settings WHERE key = $1", inventorySourceKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return InventorySource{}, false, nil
	}
	if err != nil {
		return InventorySource{}, false, fmt.Errorf("get inventory source: %w", err)
	}
	var v struct{ Repo, Branch, Path string }
	if err := json.Unmarshal(raw, &v); err != nil {
		return InventorySource{}, false, fmt.Errorf("decode inventory source: %w", err)
	}
	return InventorySource{Repo: v.Repo, Branch: v.Branch, Path: v.Path}, true, nil
}

// Snapshot is one validated inventory at a commit.
type Snapshot struct {
	ID          int64
	Commit      string
	Inventory   []byte // JSON of spec.Inventory
	ActivatedAt time.Time
}

const snapshotColumns = "id, commit_sha, inventory, activated_at"

func scanSnapshot(row interface{ Scan(...any) error }) (Snapshot, error) {
	var s Snapshot
	err := row.Scan(&s.ID, &s.Commit, &s.Inventory, &s.ActivatedAt)
	return s, err
}

// ActivateSnapshot makes the snapshot for commit the single active one and
// keeps older snapshots. It is a no-op returning the existing row when commit
// is already active.
//
// Concurrency: it opens its own transaction via db.Begin (a savepoint inside
// the caller's transaction when db is a pgx.Tx, in which case the lock below
// is held until the caller's transaction ends) and first locks the single
// inventory_status row with SELECT ... FOR UPDATE. Concurrent activations
// queue on that lock, so the deactivate-then-insert pair never interleaves and
// the partial unique index is never hit.
func ActivateSnapshot(ctx context.Context, db DBTX, commit string, inventory []byte, at time.Time) (Snapshot, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("activate snapshot: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT 1 FROM inventory_status WHERE id = 1 FOR UPDATE"); err != nil {
		return Snapshot{}, fmt.Errorf("activate snapshot: lock: %w", err)
	}
	current, err := scanSnapshot(tx.QueryRow(ctx,
		"SELECT "+snapshotColumns+" FROM inventory_snapshots WHERE active"))
	switch {
	case err == nil && current.Commit == commit:
		if err := tx.Commit(ctx); err != nil {
			return Snapshot{}, fmt.Errorf("activate snapshot: commit: %w", err)
		}
		return current, nil
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return Snapshot{}, fmt.Errorf("activate snapshot: read active: %w", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE inventory_snapshots SET active = false WHERE active"); err != nil {
		return Snapshot{}, fmt.Errorf("activate snapshot: deactivate: %w", err)
	}
	created, err := scanSnapshot(tx.QueryRow(ctx,
		`INSERT INTO inventory_snapshots (commit_sha, inventory, active, activated_at)
		 VALUES ($1, $2, true, $3) RETURNING `+snapshotColumns, commit, inventory, dbTime(at)))
	if err != nil {
		return Snapshot{}, fmt.Errorf("activate snapshot: insert: %w", mapError(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return Snapshot{}, fmt.Errorf("activate snapshot: commit: %w", err)
	}
	return created, nil
}

// DeactivateActiveSnapshot clears the active flag so ActivateSnapshot can store
// a new snapshot for the same commit (the inventory source changed, so the same
// commit now yields different content). The caller does this inside the
// transaction that activates the replacement.
func DeactivateActiveSnapshot(ctx context.Context, db DBTX) error {
	if _, err := db.Exec(ctx, "UPDATE inventory_snapshots SET active = false WHERE active"); err != nil {
		return fmt.Errorf("deactivate snapshot: %w", err)
	}
	return nil
}

// ActiveSnapshot reports false when no snapshot is active.
func ActiveSnapshot(ctx context.Context, db DBTX) (Snapshot, bool, error) {
	s, err := scanSnapshot(db.QueryRow(ctx, "SELECT "+snapshotColumns+" FROM inventory_snapshots WHERE active"))
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, fmt.Errorf("get active snapshot: %w", err)
	}
	return s, true, nil
}

// InventoryStatus is the outcome of the latest inventory poll.
type InventoryStatus struct {
	CheckedAt      *time.Time
	Problems       []byte
	ProblemsCommit string
	// SyncError is the last failure to reach GitHub (empty when the last
	// contact succeeded). SaveInventoryStatus never touches it.
	SyncError   string
	SyncErrorAt *time.Time
}

// SaveInventoryStatus overwrites the single status row.
func SaveInventoryStatus(ctx context.Context, db DBTX, st InventoryStatus) error {
	_, err := db.Exec(ctx,
		"UPDATE inventory_status SET checked_at = $1, problems = $2, problems_commit = $3 WHERE id = 1",
		dbTimePtr(st.CheckedAt), st.Problems, st.ProblemsCommit)
	if err != nil {
		return fmt.Errorf("save inventory status: %w", err)
	}
	return nil
}

// GetInventoryStatus returns the single status row (zero values before the
// first poll).
func GetInventoryStatus(ctx context.Context, db DBTX) (InventoryStatus, error) {
	var st InventoryStatus
	err := db.QueryRow(ctx, "SELECT checked_at, problems, problems_commit, COALESCE(sync_error, ''), sync_error_at FROM inventory_status WHERE id = 1").
		Scan(&st.CheckedAt, &st.Problems, &st.ProblemsCommit, &st.SyncError, &st.SyncErrorAt)
	if err != nil {
		return InventoryStatus{}, fmt.Errorf("get inventory status: %w", err)
	}
	return st, nil
}

// SetInventorySyncError records a failure to reach GitHub; the snapshot,
// problems and check time are untouched.
func SetInventorySyncError(ctx context.Context, db DBTX, message string, at time.Time) error {
	if _, err := db.Exec(ctx, "UPDATE inventory_status SET sync_error = $1, sync_error_at = $2 WHERE id = 1", message, dbTime(at)); err != nil {
		return fmt.Errorf("set inventory sync error: %w", err)
	}
	return nil
}

// ClearInventorySyncError removes the recorded failure.
func ClearInventorySyncError(ctx context.Context, db DBTX) error {
	if _, err := db.Exec(ctx, "UPDATE inventory_status SET sync_error = NULL, sync_error_at = NULL WHERE id = 1 AND sync_error IS NOT NULL"); err != nil {
		return fmt.Errorf("clear inventory sync error: %w", err)
	}
	return nil
}
