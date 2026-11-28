package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrAlreadyDecided is returned by DecideApproval when the approval has been
// decided (approved, rejected or expired) already; nothing was changed.
var ErrAlreadyDecided = errors.New("approval is already decided")

// Approval is one request for a person to approve a run. A run has at most one
// open approval (Decision empty) at a time.
type Approval struct {
	ID, RunID   int64
	ManifestID  string
	RequestedBy int64
	// RequestedAt and ExpiresAt bound the request; the row's ExpiresAt is the
	// authority for when a waiting run expires.
	RequestedAt, ExpiresAt time.Time
	DecidedBy              *int64
	DecidedAt              *time.Time
	Decision               string // "" (open) | "approved" | "rejected" | "expired"
	Comment                string
}

const approvalColumns = `id, run_id, manifest_id, requested_by, requested_at, expires_at, decided_by, decided_at, decision, comment`

func scanApproval(r rowScanner) (Approval, error) {
	var a Approval
	err := r.Scan(&a.ID, &a.RunID, &a.ManifestID, &a.RequestedBy, &a.RequestedAt, &a.ExpiresAt, &a.DecidedBy, &a.DecidedAt, &a.Decision, &a.Comment)
	return a, err
}

// RequestApproval opens an approval for a run. ErrDuplicate when the run
// already has an open one, ErrInvalidReference for an unknown run or user.
// A zero RequestedAt means now.
func RequestApproval(ctx context.Context, tx DBTX, a Approval) (Approval, error) {
	var requested any // NULL makes the default apply
	if !a.RequestedAt.IsZero() {
		requested = dbTime(a.RequestedAt)
	}
	row := tx.QueryRow(ctx,
		`INSERT INTO approvals (run_id, manifest_id, requested_by, requested_at, expires_at)
		 VALUES ($1, $2, $3, coalesce($4::timestamptz, now()), $5) RETURNING `+approvalColumns,
		a.RunID, a.ManifestID, a.RequestedBy, requested, dbTime(a.ExpiresAt))
	got, err := scanApproval(row)
	if err != nil {
		return Approval{}, fmt.Errorf("request approval: %w", mapError(err))
	}
	return got, nil
}

// OpenApproval returns the run's open approval, if it has one.
func OpenApproval(ctx context.Context, db DBTX, runID int64) (Approval, bool, error) {
	a, err := scanApproval(db.QueryRow(ctx,
		"SELECT "+approvalColumns+" FROM approvals WHERE run_id = $1 AND decision = ''", runID))
	if errors.Is(mapError(err), ErrNotFound) {
		return Approval{}, false, nil
	}
	if err != nil {
		return Approval{}, false, fmt.Errorf("open approval: %w", mapError(err))
	}
	return a, true, nil
}

// DecideApproval records the decision ("approved", "rejected" or "expired";
// ErrInvalid otherwise) on an open approval. The decision is one conditional
// UPDATE, so of two concurrent deciders the second waits for the first's
// transaction and then finds the approval decided: ErrAlreadyDecided. ErrNotFound
// for an unknown id.
func DecideApproval(ctx context.Context, tx DBTX, id int64, decision string, by *int64, comment string, at time.Time) error {
	switch decision {
	case "approved", "rejected", "expired":
	default:
		return fmt.Errorf("decide approval: %w: decision %q", ErrInvalid, decision)
	}
	tag, err := tx.Exec(ctx,
		`UPDATE approvals SET decision = $2, decided_by = $3, comment = $4, decided_at = $5
		 WHERE id = $1 AND decision = ''`, id, decision, by, comment, dbTime(at))
	if err != nil {
		return fmt.Errorf("decide approval: %w", mapError(err))
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM approvals WHERE id = $1)", id).Scan(&exists); err != nil {
		return fmt.Errorf("decide approval: %w", mapError(err))
	}
	if !exists {
		return fmt.Errorf("decide approval: %w", ErrNotFound)
	}
	return fmt.Errorf("decide approval: %w", ErrAlreadyDecided)
}

// OpenApprovals returns every open approval, oldest first.
func OpenApprovals(ctx context.Context, db DBTX) ([]Approval, error) {
	return queryApprovals(ctx, db, "SELECT "+approvalColumns+" FROM approvals WHERE decision = '' ORDER BY id")
}

// ExpiredApprovals returns the open approvals whose ExpiresAt is not after now,
// oldest first.
func ExpiredApprovals(ctx context.Context, db DBTX, now time.Time) ([]Approval, error) {
	return queryApprovals(ctx, db,
		"SELECT "+approvalColumns+" FROM approvals WHERE decision = '' AND expires_at <= $1 ORDER BY id", dbTime(now))
}

func queryApprovals(ctx context.Context, db DBTX, query string, args ...any) ([]Approval, error) {
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list approvals: %w", mapError(err))
	}
	defer rows.Close()
	approvals := []Approval{}
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, fmt.Errorf("list approvals: %w", mapError(err))
		}
		approvals = append(approvals, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list approvals: %w", mapError(err))
	}
	return approvals, nil
}
