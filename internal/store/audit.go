package store

import (
	"context"
	"fmt"
	"time"
)

// AuditEntry is one append-only record of a change. Detail is a JSON document
// or empty. It must never hold secrets.
type AuditEntry struct {
	ID         int64
	At         time.Time
	ActorID    *int64
	Actor      string
	Action     string
	TargetType string
	TargetID   string
	Detail     []byte
}

// InsertAudit appends an entry. A nil or empty Detail is stored as NULL.
// Invalid JSON in Detail is an error.
func InsertAudit(ctx context.Context, db DBTX, e AuditEntry) error {
	var detail any // nil becomes SQL NULL
	if len(e.Detail) > 0 {
		detail = string(e.Detail)
	}
	_, err := db.Exec(ctx, `INSERT INTO audit_log (at, actor_id, actor, action, target_type, target_id, detail)
VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)`,
		dbTime(e.At), e.ActorID, e.Actor, e.Action, e.TargetType, e.TargetID, detail)
	if err != nil {
		return fmt.Errorf("insert audit: %w", mapError(err))
	}
	return nil
}

// ListAudit returns up to limit entries, newest first. before is the id of the
// last entry of the previous page (0 starts from the newest). limit must be
// positive.
func ListAudit(ctx context.Context, db DBTX, before int64, limit int) ([]AuditEntry, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("list audit: %w: limit must be positive", ErrInvalid)
	}
	rows, err := db.Query(ctx, `SELECT id, at, actor_id, actor, action, target_type, target_id, detail::text
FROM audit_log WHERE ($1::bigint = 0 OR id < $1) ORDER BY id DESC LIMIT $2`, before, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit: %w", err)
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var detail *string
		if err := rows.Scan(&e.ID, &e.At, &e.ActorID, &e.Actor, &e.Action, &e.TargetType, &e.TargetID, &detail); err != nil {
			return nil, fmt.Errorf("scan audit: %w", err)
		}
		if detail != nil {
			e.Detail = []byte(*detail)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
