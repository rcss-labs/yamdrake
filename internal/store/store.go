// Package store persists connections, folders, job sources and inventory
// snapshots in PostgreSQL. It does no encryption: secrets arrive and leave as
// the ciphertext bytes the caller supplies.
package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrNotFound is returned when no row matches.
	ErrNotFound = errors.New("not found")
	// ErrDuplicate is returned when a uniqueness rule is violated.
	ErrDuplicate = errors.New("already exists")
	// ErrInvalidReference is returned when a row refers to one that does not
	// exist (for example a job source in an unknown folder).
	ErrInvalidReference = errors.New("referenced row does not exist")
)

// DBTX is satisfied by *pgxpool.Pool and pgx.Tx. Begin starts a transaction on
// a pool and a savepoint (nested transaction) on a pgx.Tx.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

const (
	uniqueViolation     = "23505"
	foreignKeyViolation = "23503"
)

// mapError converts PostgreSQL unique violations to ErrDuplicate, foreign key
// violations to ErrInvalidReference and missing
// rows to ErrNotFound; other errors pass through unchanged.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case uniqueViolation:
			return ErrDuplicate
		case foreignKeyViolation:
			return ErrInvalidReference
		}
	}
	return err
}
