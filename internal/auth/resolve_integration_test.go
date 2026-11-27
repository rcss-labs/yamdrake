//go:build integration

package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/rcss-labs/yamdrake/internal/db"
	"github.com/rcss-labs/yamdrake/internal/store"
	"github.com/rcss-labs/yamdrake/internal/testutil/testdb"
)

func newPool(t *testing.T) store.DBTX {
	t.Helper()
	ctx := context.Background()
	url := testdb.New(t)
	if err := db.Migrate(ctx, url); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestResolve(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)

	mustUser := func(email string) store.User {
		t.Helper()
		u, err := store.CreateLocalUser(ctx, pool, email, email, "local", "hash-not-for-principal")
		if err != nil {
			t.Fatalf("CreateLocalUser: %v", err)
		}
		return u
	}
	mustGroup := func(name string, admin bool) store.Group {
		t.Helper()
		g, err := store.CreateGroup(ctx, pool, name, admin)
		if err != nil {
			t.Fatalf("CreateGroup: %v", err)
		}
		return g
	}
	mustGrant := func(g store.Grant) {
		t.Helper()
		if _, err := store.CreateGrant(ctx, pool, g); err != nil {
			t.Fatalf("CreateGrant: %v", err)
		}
	}

	t.Run("unions grants from two groups", func(t *testing.T) {
		u := mustUser("two-groups@example.com")
		runners := mustGroup("runners", false)
		cancellers := mustGroup("cancellers", false)
		mustGrant(store.Grant{GroupID: runners.ID, Action: "run", Label: "dev"})
		mustGrant(store.Grant{GroupID: cancellers.ID, Action: "cancel"})
		if err := store.SetManualMembership(ctx, pool, u.ID, runners.ID, true); err != nil {
			t.Fatal(err)
		}
		// An IdP membership counts like a manual one.
		if err := store.ReplaceIDPMemberships(ctx, pool, u.ID, []int64{cancellers.ID}); err != nil {
			t.Fatal(err)
		}

		p, err := Resolve(ctx, pool, u.ID, "session")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if p.UserID != u.ID || p.Email != u.Email || p.Name != u.Name || p.Via != "session" || p.Admin {
			t.Fatalf("principal = %+v", p)
		}
		dev := Target{Label: "dev", Environment: "e", FolderID: 1}
		if !p.Can(Run, dev) || !p.Can(Cancel, dev) || p.Can(Approve, dev) || p.Can(Run, Target{Label: "prod"}) {
			t.Fatalf("grants not unioned correctly: %+v", p.Grants)
		}
	})

	t.Run("admin through an admin group", func(t *testing.T) {
		u := mustUser("admin@example.com")
		admins := mustGroup("admins", true)
		if err := store.SetManualMembership(ctx, pool, u.ID, admins.ID, true); err != nil {
			t.Fatal(err)
		}
		p, err := Resolve(ctx, pool, u.ID, "token")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if !p.Admin || p.Via != "token" || !p.Can(ManageAccess, Target{}) {
			t.Fatalf("principal = %+v, want admin", p)
		}
	})

	t.Run("disabled user", func(t *testing.T) {
		u := mustUser("disabled@example.com")
		if err := store.SetDisabled(ctx, pool, u.ID, true); err != nil {
			t.Fatal(err)
		}
		if _, err := Resolve(ctx, pool, u.ID, "session"); !errors.Is(err, ErrDisabled) {
			t.Fatalf("error = %v, want ErrDisabled", err)
		}
	})

	t.Run("unknown user", func(t *testing.T) {
		if _, err := Resolve(ctx, pool, 999999999, "session"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("error = %v, want store.ErrNotFound", err)
		}
	})

	t.Run("a user with no groups only views", func(t *testing.T) {
		u := mustUser(fmt.Sprintf("plain-%d@example.com", time.Now().UnixNano()))
		p, err := Resolve(ctx, pool, u.ID, "session")
		if err != nil || p.Admin || len(p.Grants) != 0 || !p.Can(View, Target{}) || p.Can(Run, Target{}) {
			t.Fatalf("principal = %+v, err = %v", p, err)
		}
	})
}
