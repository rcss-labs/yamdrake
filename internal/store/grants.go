package store

import (
	"context"
	"fmt"
)

// Grant gives a group an action, optionally limited to a label, an environment
// or a folder. Empty strings and a zero FolderID mean "any".
type Grant struct {
	ID          int64
	GroupID     int64
	Action      string
	Label       string
	Environment string
	FolderID    int64
}

// grantActions are the actions that can be stored. "view" is held by every
// signed-in principal and is never a grant.
var grantActions = map[string]bool{
	"view_logs": true, "run": true, "cancel": true, "approve": true, "restore": true,
	"manage_jobs": true, "manage_connections": true, "manage_access": true, "view_audit": true,
}

// unscopedActions cannot be limited to a label, environment or folder.
var unscopedActions = map[string]bool{
	"manage_jobs": true, "manage_connections": true, "manage_access": true, "view_audit": true,
}

var grantLabels = map[string]bool{"": true, "dev": true, "preprod": true, "prod": true}

func validateGrant(g Grant) error {
	switch {
	case !grantActions[g.Action]:
		return fmt.Errorf("%w: action %q cannot be granted", ErrInvalid, g.Action)
	case !grantLabels[g.Label]:
		return fmt.Errorf("%w: unknown label %q", ErrInvalid, g.Label)
	case g.Label != "" && g.Environment != "":
		return fmt.Errorf("%w: a grant has a label or an environment, not both", ErrInvalid)
	case g.FolderID < 0:
		return fmt.Errorf("%w: negative folder id", ErrInvalid)
	case unscopedActions[g.Action] && (g.Label != "" || g.Environment != "" || g.FolderID != 0):
		return fmt.Errorf("%w: %s cannot be limited to a label, environment or folder", ErrInvalid, g.Action)
	}
	return nil
}

const grantColumns = "id, group_id, action, label, environment, coalesce(folder_id, 0)"

func scanGrant(r rowScanner) (Grant, error) {
	var g Grant
	err := r.Scan(&g.ID, &g.GroupID, &g.Action, &g.Label, &g.Environment, &g.FolderID)
	return g, err
}

func queryGrants(ctx context.Context, db DBTX, op, sql string, args ...any) ([]Grant, error) {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, fmt.Errorf("%s: scan: %w", op, err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	return out, nil
}

// CreateGrant returns ErrInvalid for a grant that breaks the rules above,
// ErrDuplicate for an identical existing grant and ErrInvalidReference for an
// unknown group or folder.
func CreateGrant(ctx context.Context, db DBTX, g Grant) (Grant, error) {
	if err := validateGrant(g); err != nil {
		return Grant{}, fmt.Errorf("create grant: %w", err)
	}
	var folder *int64
	if g.FolderID != 0 {
		folder = &g.FolderID
	}
	out, err := scanGrant(db.QueryRow(ctx,
		"INSERT INTO grants (group_id, action, label, environment, folder_id) VALUES ($1, $2, $3, $4, $5) RETURNING "+grantColumns,
		g.GroupID, g.Action, g.Label, g.Environment, folder))
	if err != nil {
		return Grant{}, fmt.Errorf("create grant: %w", mapError(err))
	}
	return out, nil
}

// DeleteGrant returns ErrNotFound for an unknown grant.
func DeleteGrant(ctx context.Context, db DBTX, id int64) error {
	tag, err := db.Exec(ctx, "DELETE FROM grants WHERE id = $1", id)
	if err != nil {
		return fmt.Errorf("delete grant: %w", mapError(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("delete grant: %w", ErrNotFound)
	}
	return nil
}

func ListGrants(ctx context.Context, db DBTX) ([]Grant, error) {
	return queryGrants(ctx, db, "list grants", "SELECT "+grantColumns+" FROM grants ORDER BY id")
}

// GrantsOfUser returns the grants of every group the user belongs to, each
// grant once.
func GrantsOfUser(ctx context.Context, db DBTX, userID int64) ([]Grant, error) {
	return queryGrants(ctx, db, "grants of user", `SELECT `+grantColumns+` FROM grants
WHERE group_id IN (SELECT group_id FROM group_members WHERE user_id = $1) ORDER BY id`, userID)
}
