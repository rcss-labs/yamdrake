package store

import (
	"context"
	"fmt"
	"time"
)

// Freeze blocks runs from starting. It names exactly one of a label or an
// environment, and is active from creation until ExpiresAt or until lifted.
type Freeze struct {
	ID                   int64
	Label, Environment   string // exactly one is set
	Reason               string
	CreatedBy            int64
	CreatedAt, ExpiresAt time.Time
	LiftedBy             *int64
	LiftedAt             *time.Time
}

const freezeColumns = `id, label, environment, reason, created_by, created_at, expires_at, lifted_by, lifted_at`

func scanFreeze(r rowScanner) (Freeze, error) {
	var f Freeze
	err := r.Scan(&f.ID, &f.Label, &f.Environment, &f.Reason, &f.CreatedBy, &f.CreatedAt, &f.ExpiresAt, &f.LiftedBy, &f.LiftedAt)
	return f, err
}

// CreateFreeze stores a freeze. ErrInvalid unless exactly one of Label and
// Environment is set (checked here and by a CHECK constraint); a zero
// CreatedAt means now.
func CreateFreeze(ctx context.Context, tx DBTX, f Freeze) (Freeze, error) {
	if (f.Label == "") == (f.Environment == "") {
		return Freeze{}, fmt.Errorf("create freeze: %w: exactly one of label and environment is required", ErrInvalid)
	}
	var created any
	if !f.CreatedAt.IsZero() {
		created = dbTime(f.CreatedAt)
	}
	row := tx.QueryRow(ctx,
		`INSERT INTO freezes (label, environment, reason, created_by, created_at, expires_at)
		 VALUES ($1, $2, $3, $4, coalesce($5::timestamptz, now()), $6) RETURNING `+freezeColumns,
		f.Label, f.Environment, f.Reason, f.CreatedBy, created, dbTime(f.ExpiresAt))
	got, err := scanFreeze(row)
	if err != nil {
		return Freeze{}, fmt.Errorf("create freeze: %w", mapError(err))
	}
	return got, nil
}

// LiftFreeze ends a freeze early. ErrNotFound when there is no such freeze or
// it is not active any more (already lifted or expired as of at).
func LiftFreeze(ctx context.Context, tx DBTX, id, by int64, at time.Time) error {
	tag, err := tx.Exec(ctx,
		`UPDATE freezes SET lifted_by = $2, lifted_at = $3
		 WHERE id = $1 AND lifted_at IS NULL AND expires_at > $3`, id, by, dbTime(at))
	if err != nil {
		return fmt.Errorf("lift freeze: %w", mapError(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("lift freeze: %w", ErrNotFound)
	}
	return nil
}

// ActiveFreezes returns the freezes that are neither lifted nor expired at
// now, oldest first.
func ActiveFreezes(ctx context.Context, db DBTX, now time.Time) ([]Freeze, error) {
	return queryFreezes(ctx, db,
		"SELECT "+freezeColumns+" FROM freezes WHERE lifted_at IS NULL AND expires_at > $1 ORDER BY id", dbTime(now))
}

// ListFreezes returns freezes of every state, newest first. A limit below 1
// means 50; at most 500.
func ListFreezes(ctx context.Context, db DBTX, limit int) ([]Freeze, error) {
	if limit < 1 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	return queryFreezes(ctx, db, "SELECT "+freezeColumns+" FROM freezes ORDER BY id DESC LIMIT $1", limit)
}

func queryFreezes(ctx context.Context, db DBTX, query string, args ...any) ([]Freeze, error) {
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list freezes: %w", mapError(err))
	}
	defer rows.Close()
	freezes := []Freeze{}
	for rows.Next() {
		f, err := scanFreeze(rows)
		if err != nil {
			return nil, fmt.Errorf("list freezes: %w", mapError(err))
		}
		freezes = append(freezes, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list freezes: %w", mapError(err))
	}
	return freezes, nil
}

// freezeTimeLayout is how a freeze's end is worded.
const freezeTimeLayout = "2006-01-02 15:04 UTC"

// FrozenReason words the freeze that blocks a run, or "" when none does. When
// several apply it names the one that ends last (the lowest id on a tie), as
// that is the one the run has to wait for.
func FrozenReason(run Run, active []Freeze) string {
	var blocking *Freeze
	for i := range active {
		f := active[i]
		applies := (f.Label != "" && f.Label == run.Label) || (f.Environment != "" && f.Environment == run.Environment)
		if applies && (blocking == nil || f.ExpiresAt.After(blocking.ExpiresAt)) {
			blocking = &active[i]
		}
	}
	if blocking == nil {
		return ""
	}
	return fmt.Sprintf("%s, until %s", blocking.Reason, blocking.ExpiresAt.UTC().Format(freezeTimeLayout))
}
