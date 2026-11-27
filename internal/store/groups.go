package store

import (
	"context"
	"fmt"
)

// Group is a set of users that grants attach to. Members of an Admin group are
// global admins. Names are unique ignoring case.
type Group struct {
	ID    int64
	Name  string
	Admin bool
}

func CreateGroup(ctx context.Context, db DBTX, name string, admin bool) (Group, error) {
	g := Group{}
	err := db.QueryRow(ctx, "INSERT INTO groups (name, admin) VALUES ($1, $2) RETURNING id, name, admin", name, admin).
		Scan(&g.ID, &g.Name, &g.Admin)
	if err != nil {
		return Group{}, fmt.Errorf("create group: %w", mapError(err))
	}
	return g, nil
}

func scanGroups(op string, rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}) ([]Group, error) {
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Admin); err != nil {
			return nil, fmt.Errorf("%s: scan: %w", op, err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	return out, nil
}

// ListGroups returns groups ordered by name.
func ListGroups(ctx context.Context, db DBTX) ([]Group, error) {
	rows, err := db.Query(ctx, "SELECT id, name, admin FROM groups ORDER BY lower(name), id")
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	return scanGroups("list groups", rows)
}

// SetGroupAdmin returns ErrNotFound for an unknown group.
func SetGroupAdmin(ctx context.Context, db DBTX, id int64, admin bool) error {
	tag, err := db.Exec(ctx, "UPDATE groups SET admin = $2 WHERE id = $1", id, admin)
	if err != nil {
		return fmt.Errorf("set group admin: %w", mapError(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("set group admin: %w", ErrNotFound)
	}
	return nil
}

// SetManualMembership adds or removes a membership an administrator manages.
// It never touches memberships that come from the identity provider, so a user
// can hold both kinds for the same group. Unknown user or group give
// ErrInvalidReference when adding.
func SetManualMembership(ctx context.Context, db DBTX, userID, groupID int64, member bool) error {
	var err error
	if member {
		_, err = db.Exec(ctx, `INSERT INTO group_members (user_id, group_id, source) VALUES ($1, $2, 'manual')
ON CONFLICT DO NOTHING`, userID, groupID)
	} else {
		_, err = db.Exec(ctx, "DELETE FROM group_members WHERE user_id = $1 AND group_id = $2 AND source = 'manual'", userID, groupID)
	}
	if err != nil {
		return fmt.Errorf("set manual membership: %w", mapError(err))
	}
	return nil
}

// ReplaceIDPMemberships makes the user's identity-provider memberships exactly
// groupIDs (duplicates are ignored) in one transaction. Manual memberships are
// left alone. An unknown group gives ErrInvalidReference and changes nothing.
func ReplaceIDPMemberships(ctx context.Context, db DBTX, userID int64, groupIDs []int64) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("replace idp memberships: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "DELETE FROM group_members WHERE user_id = $1 AND source = 'idp'", userID); err != nil {
		return fmt.Errorf("replace idp memberships: %w", mapError(err))
	}
	for _, groupID := range groupIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO group_members (user_id, group_id, source) VALUES ($1, $2, 'idp')
ON CONFLICT DO NOTHING`, userID, groupID); err != nil {
			return fmt.Errorf("replace idp memberships: %w", mapError(err))
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("replace idp memberships: commit: %w", err)
	}
	return nil
}

// GroupsOfUser returns each group the user belongs to, manually or through the
// identity provider, once, ordered by name.
func GroupsOfUser(ctx context.Context, db DBTX, userID int64) ([]Group, error) {
	rows, err := db.Query(ctx, `SELECT g.id, g.name, g.admin FROM groups g
WHERE g.id IN (SELECT group_id FROM group_members WHERE user_id = $1)
ORDER BY lower(g.name), g.id`, userID)
	if err != nil {
		return nil, fmt.Errorf("groups of user: %w", err)
	}
	return scanGroups("groups of user", rows)
}

// SetIDPMapping maps an identity-provider group name to a group, replacing any
// earlier mapping of that name. An unknown group gives ErrInvalidReference.
func SetIDPMapping(ctx context.Context, db DBTX, idpGroup string, groupID int64) error {
	if idpGroup == "" {
		return fmt.Errorf("set idp mapping: %w: empty idp group", ErrInvalid)
	}
	_, err := db.Exec(ctx, `INSERT INTO idp_group_mappings (idp_group, group_id) VALUES ($1, $2)
ON CONFLICT (idp_group) DO UPDATE SET group_id = EXCLUDED.group_id`, idpGroup, groupID)
	if err != nil {
		return fmt.Errorf("set idp mapping: %w", mapError(err))
	}
	return nil
}

// DeleteIDPMapping returns ErrNotFound when the name is not mapped.
func DeleteIDPMapping(ctx context.Context, db DBTX, idpGroup string) error {
	tag, err := db.Exec(ctx, "DELETE FROM idp_group_mappings WHERE idp_group = $1", idpGroup)
	if err != nil {
		return fmt.Errorf("delete idp mapping: %w", mapError(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("delete idp mapping: %w", ErrNotFound)
	}
	return nil
}

// ListIDPMappings returns identity-provider group name to group id.
func ListIDPMappings(ctx context.Context, db DBTX) (map[string]int64, error) {
	rows, err := db.Query(ctx, "SELECT idp_group, group_id FROM idp_group_mappings")
	if err != nil {
		return nil, fmt.Errorf("list idp mappings: %w", err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var name string
		var id int64
		if err := rows.Scan(&name, &id); err != nil {
			return nil, fmt.Errorf("scan idp mapping: %w", err)
		}
		out[name] = id
	}
	return out, rows.Err()
}
