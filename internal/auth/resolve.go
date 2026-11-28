package auth

import (
	"context"
	"fmt"

	"github.com/rcss-labs/yumdrake/internal/store"
)

// Resolve builds the Principal for a user from the database: admin if any of
// the user's groups (manual or from the identity provider) is an admin group,
// and the union of the grants of all those groups. It returns an error
// wrapping store.ErrNotFound for an unknown user and ErrDisabled for a
// disabled one. via records how the caller authenticated ("session"/"token").
func Resolve(ctx context.Context, db store.DBTX, userID int64, via string) (Principal, error) {
	user, err := store.UserByID(ctx, db, userID)
	if err != nil {
		return Principal{}, fmt.Errorf("resolve principal: %w", err)
	}
	if user.Disabled {
		return Principal{}, ErrDisabled
	}
	groups, err := store.GroupsOfUser(ctx, db, userID)
	if err != nil {
		return Principal{}, fmt.Errorf("resolve principal: %w", err)
	}
	grants, err := store.GrantsOfUser(ctx, db, userID)
	if err != nil {
		return Principal{}, fmt.Errorf("resolve principal: %w", err)
	}
	p := Principal{UserID: user.ID, Email: user.Email, Name: user.Name, Grants: grants, Via: via,
		Source: user.Source, LastSignInAt: user.LastLoginAt}
	for _, g := range groups {
		if g.Admin {
			p.Admin = true
			break
		}
	}
	return p, nil
}
