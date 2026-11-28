package store

import (
	"context"
	"fmt"
)

// accessLockKey is the advisory-lock key that serialises every change that can
// remove an administrator. It is an arbitrary constant, unique in this program.
const accessLockKey int64 = 0x79756d_41444d // "yum" "ADM"

// AdminLockKey is the advisory-lock key LockAdminChanges takes (tests read
// pg_locks for it).
func AdminLockKey() int64 { return accessLockKey }

// LockAdminChanges takes a transaction-scoped advisory lock. Callers that may
// reduce the set of administrators (disable a user, clear a group's admin
// flag, remove a membership) take it first, then count with
// CountEnabledAdmins, so two such changes made at once cannot each see the
// other's administrator still present. The lock ends with the transaction.
func LockAdminChanges(ctx context.Context, tx DBTX) error {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", accessLockKey); err != nil {
		return fmt.Errorf("lock admin changes: %w", err)
	}
	return nil
}

// CountEnabledAdmins returns how many enabled users belong to an admin group.
func CountEnabledAdmins(ctx context.Context, db DBTX) (int, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT count(DISTINCT u.id) FROM users u
JOIN group_members gm ON gm.user_id = u.id
JOIN groups g ON g.id = gm.group_id
WHERE g.admin AND NOT u.disabled`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count enabled admins: %w", err)
	}
	return n, nil
}

// ManualGroupIDs returns the groups an administrator put the user in by hand,
// in id order.
func ManualGroupIDs(ctx context.Context, db DBTX, userID int64) ([]int64, error) {
	rows, err := db.Query(ctx, "SELECT group_id FROM group_members WHERE user_id = $1 AND source = 'manual' ORDER BY group_id", userID)
	if err != nil {
		return nil, fmt.Errorf("manual group ids: %w", err)
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("manual group ids: scan: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GroupMemberCounts returns how many distinct users are in each group (groups
// without members are absent).
func GroupMemberCounts(ctx context.Context, db DBTX) (map[int64]int, error) {
	rows, err := db.Query(ctx, "SELECT group_id, count(DISTINCT user_id) FROM group_members GROUP BY group_id")
	if err != nil {
		return nil, fmt.Errorf("group member counts: %w", err)
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("group member counts: scan: %w", err)
		}
		out[id] = n
	}
	return out, rows.Err()
}
