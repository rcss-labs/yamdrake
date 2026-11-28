package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/yumdrake/internal/audit"
	"github.com/rcss-labs/yumdrake/internal/store"
)

// recordAudit writes an audit entry for the request's principal into db. Its
// error deliberately does not wrap the store's sentinels: a failure to audit
// must not be mistaken for a rule of the change itself (such as an unknown
// folder).
func (s *server) recordAudit(ctx context.Context, db store.DBTX, r *http.Request, e audit.Event) error {
	principal, ok := PrincipalFrom(r.Context())
	if !ok {
		return fmt.Errorf("record audit entry: no principal")
	}
	if err := audit.Record(ctx, db, &principal, s.Now(), e); err != nil {
		return fmt.Errorf("record audit entry: %v", err) //nolint:errorlint // see above: the chain is cut on purpose
	}
	return nil
}

// auditedWrite runs write and the audit entry it makes in one transaction:
// both commit or neither does.
func (s *server) auditedWrite(r *http.Request, write func(ctx context.Context, tx pgx.Tx) (audit.Event, error)) error {
	ctx := r.Context()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	event, err := write(ctx, tx)
	if err != nil {
		return err
	}
	if err := s.recordAudit(ctx, tx, r, event); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
