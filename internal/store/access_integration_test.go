//go:build integration

package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func groupNames(groups []Group) map[string]bool {
	out := map[string]bool{}
	for _, g := range groups {
		out[g.Name] = true
	}
	return out
}

func TestUserEmailIsCaseInsensitivelyUnique(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)

	u, err := CreateLocalUser(ctx, pool, "Alice@Example.COM", "Alice", "local", "hash")
	if err != nil {
		t.Fatalf("CreateLocalUser: %v", err)
	}
	if u.Email != "alice@example.com" || u.Source != "local" {
		t.Fatalf("user = %+v, want lower-case email", u)
	}
	if _, err := CreateLocalUser(ctx, pool, "ALICE@example.com", "Other", "local", "hash"); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second create error = %v, want ErrDuplicate", err)
	}
	if _, err := UpsertSAMLUser(ctx, pool, "alice@EXAMPLE.com", "SAML Alice", time.Now()); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("SAML upsert over a local user error = %v, want ErrDuplicate", err)
	}
	if _, err := CreateLocalUser(ctx, pool, "x@example.com", "X", "saml", "hash"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("source saml error = %v, want ErrInvalid", err)
	}
	// The CHECK constraint refuses a mixed-case email even around the store.
	if _, err := pool.Exec(ctx, "INSERT INTO users (email, source) VALUES ('Mixed@Example.com', 'local')"); err == nil {
		t.Fatal("raw insert of a mixed-case email succeeded")
	}
	got, err := UserByEmail(ctx, pool, "ALICE@example.com")
	if err != nil || got.ID != u.ID {
		t.Fatalf("UserByEmail = %+v, %v", got, err)
	}
	if hash, err := PasswordHash(ctx, pool, u.ID); err != nil || hash != "hash" {
		t.Fatalf("PasswordHash = %q, %v", hash, err)
	}
}

func TestUpsertSAMLUserUpdatesNameAndLastLogin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)

	first := nanosecondNow()
	u, err := UpsertSAMLUser(ctx, pool, "Bob@Example.com", "Bob", first)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if u.Source != "saml" || u.LastLoginAt == nil || !u.LastLoginAt.Equal(micro(first)) {
		t.Fatalf("first user = %+v", u)
	}
	second := first.Add(time.Hour)
	again, err := UpsertSAMLUser(ctx, pool, "bob@example.com", "Robert", second)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if again.ID != u.ID || again.Name != "Robert" || !again.LastLoginAt.Equal(micro(second)) {
		t.Fatalf("second user = %+v", again)
	}
	if !again.CreatedAt.Equal(u.CreatedAt) {
		t.Fatalf("created_at changed: %v vs %v", again.CreatedAt, u.CreatedAt)
	}
	if _, err := PasswordHash(ctx, pool, u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("PasswordHash of SAML user = %v, want ErrNotFound", err)
	}
	if err := SetDisabled(ctx, pool, u.ID, true); err != nil {
		t.Fatalf("SetDisabled: %v", err)
	}
	// A disabled flag survives later logins.
	again, err = UpsertSAMLUser(ctx, pool, "bob@example.com", "Robert", second.Add(time.Hour))
	if err != nil || !again.Disabled {
		t.Fatalf("after login: %+v, %v", again, err)
	}
	if err := SetDisabled(ctx, pool, 999999, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetDisabled unknown = %v, want ErrNotFound", err)
	}
	users, err := ListUsers(ctx, pool)
	if err != nil || len(users) != 1 {
		t.Fatalf("ListUsers = %+v, %v", users, err)
	}
}

func TestReplaceIDPMembershipsKeepsManualOnes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)

	u, err := UpsertSAMLUser(ctx, pool, "carol@example.com", "Carol", time.Now())
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	var ids = map[string]int64{}
	for _, n := range []string{"A", "B", "C"} {
		g, err := CreateGroup(ctx, pool, n, false)
		if err != nil {
			t.Fatalf("CreateGroup %s: %v", n, err)
		}
		ids[n] = g.ID
	}
	if _, err := CreateGroup(ctx, pool, "a", false); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("group name case-insensitive duplicate = %v, want ErrDuplicate", err)
	}
	if err := SetManualMembership(ctx, pool, u.ID, ids["A"], true); err != nil {
		t.Fatalf("manual A: %v", err)
	}
	if err := ReplaceIDPMemberships(ctx, pool, u.ID, []int64{ids["B"]}); err != nil {
		t.Fatalf("idp B: %v", err)
	}
	// Manual and idp membership of the same group coexist.
	if err := SetManualMembership(ctx, pool, u.ID, ids["B"], true); err != nil {
		t.Fatalf("manual B: %v", err)
	}
	if err := ReplaceIDPMemberships(ctx, pool, u.ID, []int64{ids["C"], ids["C"]}); err != nil {
		t.Fatalf("idp C: %v", err)
	}
	groups, err := GroupsOfUser(ctx, pool, u.ID)
	if err != nil {
		t.Fatalf("GroupsOfUser: %v", err)
	}
	got := groupNames(groups)
	if len(groups) != 3 || !got["A"] || !got["B"] || !got["C"] {
		t.Fatalf("groups = %+v, want A (manual), B (manual) and C (idp)", groups)
	}
	// Removing the manual B membership leaves no B because the idp one was replaced.
	if err := SetManualMembership(ctx, pool, u.ID, ids["B"], false); err != nil {
		t.Fatalf("unset manual B: %v", err)
	}
	if err := ReplaceIDPMemberships(ctx, pool, u.ID, nil); err != nil {
		t.Fatalf("idp none: %v", err)
	}
	groups, _ = GroupsOfUser(ctx, pool, u.ID)
	if got := groupNames(groups); len(groups) != 1 || !got["A"] {
		t.Fatalf("groups = %+v, want only A", groups)
	}
	if err := ReplaceIDPMemberships(ctx, pool, u.ID, []int64{424242}); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("unknown group = %v, want ErrInvalidReference", err)
	}

	if err := SetIDPMapping(ctx, pool, "idp-admins", ids["A"]); err != nil {
		t.Fatalf("SetIDPMapping: %v", err)
	}
	if err := SetIDPMapping(ctx, pool, "idp-admins", ids["B"]); err != nil {
		t.Fatalf("SetIDPMapping again: %v", err)
	}
	m, err := ListIDPMappings(ctx, pool)
	if err != nil || len(m) != 1 || m["idp-admins"] != ids["B"] {
		t.Fatalf("mappings = %v, %v", m, err)
	}
	if err := DeleteIDPMapping(ctx, pool, "idp-admins"); err != nil {
		t.Fatalf("DeleteIDPMapping: %v", err)
	}
	if err := DeleteIDPMapping(ctx, pool, "idp-admins"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
	if err := SetGroupAdmin(ctx, pool, ids["A"], true); err != nil {
		t.Fatalf("SetGroupAdmin: %v", err)
	}
	all, _ := ListGroups(ctx, pool)
	if len(all) != 3 || !all[0].Admin || all[0].Name != "A" {
		t.Fatalf("ListGroups = %+v", all)
	}
}

func TestGrantValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	g, err := CreateGroup(ctx, pool, "ops", false)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	f, err := CreateFolder(ctx, pool, "payments")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}

	invalid := map[string]Grant{
		"unknown action":         {GroupID: g.ID, Action: "destroy"},
		"unknown label":          {GroupID: g.ID, Action: "run", Label: "staging"},
		"label and environment":  {GroupID: g.ID, Action: "run", Label: "dev", Environment: "eu"},
		"scoped manage_access":   {GroupID: g.ID, Action: "manage_access", Label: "dev"},
		"foldered manage_jobs":   {GroupID: g.ID, Action: "manage_jobs", FolderID: f.ID},
		"environment view_audit": {GroupID: g.ID, Action: "view_audit", Environment: "eu"},
		"view is never a grant":  {GroupID: g.ID, Action: "view"},
		"empty action":           {GroupID: g.ID},
		"negative folder":        {GroupID: g.ID, Action: "run", FolderID: -1},
	}
	for name, grant := range invalid {
		if _, err := CreateGrant(ctx, pool, grant); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: error = %v, want ErrInvalid", name, err)
		}
	}
	// The CHECK constraints refuse the same rows when Go validation is bypassed.
	if _, err := pool.Exec(ctx, "INSERT INTO grants (group_id, action, label) VALUES ($1, 'manage_access', 'dev')", g.ID); err == nil {
		t.Error("raw scoped manage_access insert succeeded")
	}
	if _, err := pool.Exec(ctx, "INSERT INTO grants (group_id, action) VALUES ($1, 'view')", g.ID); err == nil {
		t.Error("raw view grant insert succeeded")
	}

	valid, err := CreateGrant(ctx, pool, Grant{GroupID: g.ID, Action: "run", Label: "dev", FolderID: f.ID})
	if err != nil {
		t.Fatalf("valid grant: %v", err)
	}
	if valid.ID == 0 || valid.Label != "dev" || valid.FolderID != f.ID || valid.Environment != "" {
		t.Fatalf("stored grant = %+v", valid)
	}
	if _, err := CreateGrant(ctx, pool, Grant{GroupID: g.ID, Action: "run", Label: "dev", FolderID: f.ID}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate grant = %v, want ErrDuplicate", err)
	}
	// Two unscoped duplicates are also caught (NULL folder).
	if _, err := CreateGrant(ctx, pool, Grant{GroupID: g.ID, Action: "manage_jobs"}); err != nil {
		t.Fatalf("manage_jobs: %v", err)
	}
	if _, err := CreateGrant(ctx, pool, Grant{GroupID: g.ID, Action: "manage_jobs"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate unscoped grant = %v, want ErrDuplicate", err)
	}
	if _, err := CreateGrant(ctx, pool, Grant{GroupID: g.ID, Action: "run", FolderID: 99999}); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("unknown folder = %v, want ErrInvalidReference", err)
	}
	if _, err := CreateGrant(ctx, pool, Grant{GroupID: 99999, Action: "run"}); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("unknown group = %v, want ErrInvalidReference", err)
	}
	if err := DeleteGrant(ctx, pool, valid.ID); err != nil {
		t.Fatalf("DeleteGrant: %v", err)
	}
	if err := DeleteGrant(ctx, pool, valid.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
	if list, err := ListGrants(ctx, pool); err != nil || len(list) != 1 {
		t.Fatalf("ListGrants = %+v, %v", list, err)
	}
}

func TestGrantsOfUserUnionsGroupGrants(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	u, _ := UpsertSAMLUser(ctx, pool, "dan@example.com", "Dan", time.Now())
	other, _ := UpsertSAMLUser(ctx, pool, "eve@example.com", "Eve", time.Now())
	a, _ := CreateGroup(ctx, pool, "a", false)
	b, _ := CreateGroup(ctx, pool, "b", false)
	c, _ := CreateGroup(ctx, pool, "c", false)
	for _, g := range []Grant{
		{GroupID: a.ID, Action: "run", Label: "dev"},
		{GroupID: b.ID, Action: "approve", Environment: "eu-prod"},
		{GroupID: b.ID, Action: "run", Label: "dev"}, // same rule as A's, different group
		{GroupID: c.ID, Action: "cancel"},
	} {
		if _, err := CreateGrant(ctx, pool, g); err != nil {
			t.Fatalf("CreateGrant %+v: %v", g, err)
		}
	}
	// u: manual in A, idp in B (and both in B manually, to prove no double rows).
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(SetManualMembership(ctx, pool, u.ID, a.ID, true))
	must(SetManualMembership(ctx, pool, u.ID, b.ID, true))
	must(ReplaceIDPMemberships(ctx, pool, u.ID, []int64{b.ID}))
	must(SetManualMembership(ctx, pool, other.ID, c.ID, true))

	grants, err := GrantsOfUser(ctx, pool, u.ID)
	if err != nil {
		t.Fatalf("GrantsOfUser: %v", err)
	}
	if len(grants) != 3 {
		t.Fatalf("grants = %+v, want 3 (A run, B approve, B run)", grants)
	}
	for _, g := range grants {
		if g.Action == "cancel" {
			t.Fatalf("got a grant of a group the user is not in: %+v", g)
		}
	}
	none, err := GrantsOfUser(ctx, pool, 12345)
	if err != nil || len(none) != 0 {
		t.Fatalf("unknown user grants = %+v, %v", none, err)
	}
}

func TestTokenLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	u, _ := CreateLocalUser(ctx, pool, "svc@example.com", "svc", "service", "")
	other, _ := CreateLocalUser(ctx, pool, "o@example.com", "o", "local", "h")
	if _, err := PasswordHash(ctx, pool, u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("PasswordHash of service user without a password = %v, want ErrNotFound", err)
	}
	withPassword, _ := CreateLocalUser(ctx, pool, "svcpw@example.com", "svcpw", "service", "svc-hash")
	if hash, err := PasswordHash(ctx, pool, withPassword.ID); err != nil || hash != "svc-hash" {
		t.Fatalf("PasswordHash of service user with a password = %q, %v", hash, err)
	}

	hash := []byte{1, 2, 3, 4}
	expires := nanosecondNow().Add(24 * time.Hour)
	tok, err := CreateToken(ctx, pool, u.ID, "ci", hash, expires)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if tok.ID == 0 || tok.ExpiresAt == nil || !tok.ExpiresAt.Equal(micro(expires)) || tok.RevokedAt != nil || tok.LastUsedAt != nil {
		t.Fatalf("token = %+v", tok)
	}
	if _, err := CreateToken(ctx, pool, u.ID, "dup", hash, expires); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate hash = %v, want ErrDuplicate", err)
	}
	found, err := TokenByHash(ctx, pool, hash)
	if err != nil || found.ID != tok.ID || found.UserID != u.ID {
		t.Fatalf("TokenByHash = %+v, %v", found, err)
	}
	if _, err := TokenByHash(ctx, pool, []byte{9}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown hash = %v, want ErrNotFound", err)
	}
	used := nanosecondNow()
	if err := TouchToken(ctx, pool, tok.ID, used); err != nil {
		t.Fatalf("TouchToken: %v", err)
	}
	if err := RevokeToken(ctx, pool, tok.ID, other.ID, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoke by another user = %v, want ErrNotFound", err)
	}
	if again, _ := TokenByHash(ctx, pool, hash); again.RevokedAt != nil {
		t.Fatal("another user's revoke took effect")
	}
	revoked := nanosecondNow()
	if err := RevokeToken(ctx, pool, tok.ID, u.ID, revoked); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	if err := RevokeToken(ctx, pool, tok.ID, u.ID, revoked.Add(time.Hour)); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	after, err := TokenByHash(ctx, pool, hash)
	if err != nil {
		t.Fatalf("TokenByHash after revoke: %v", err)
	}
	if after.RevokedAt == nil || !after.RevokedAt.Equal(micro(revoked)) {
		t.Fatalf("RevokedAt = %v, want first revoke time %v", after.RevokedAt, micro(revoked))
	}
	if after.LastUsedAt == nil || !after.LastUsedAt.Equal(micro(used)) {
		t.Fatalf("LastUsedAt = %v, want %v", after.LastUsedAt, micro(used))
	}
	list, err := ListTokens(ctx, pool, u.ID)
	if err != nil || len(list) != 1 || list[0].ID != tok.ID {
		t.Fatalf("ListTokens = %+v, %v", list, err)
	}
	if list, _ := ListTokens(ctx, pool, other.ID); len(list) != 0 {
		t.Fatalf("other user's tokens = %+v", list)
	}
}

func TestAuditIsNewestFirstWithKeysetPaging(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	u, _ := CreateLocalUser(ctx, pool, "admin@example.com", "Admin", "local", "h")
	base := nanosecondNow()

	for i, e := range []AuditEntry{
		{At: base, ActorID: &u.ID, Actor: "admin@example.com", Action: "grant.create", TargetType: "grant", TargetID: "1", Detail: []byte(`{"n":1}`)},
		{At: base.Add(time.Second), Actor: "system", Action: "a2", Detail: nil},
		{At: base.Add(2 * time.Second), Actor: "system", Action: "a3", Detail: []byte{}},
		{At: base.Add(3 * time.Second), Actor: "system", Action: "a4"},
		{At: base.Add(4 * time.Second), Actor: "system", Action: "a5"},
	} {
		if err := InsertAudit(ctx, pool, e); err != nil {
			t.Fatalf("InsertAudit %d: %v", i, err)
		}
	}
	page1, err := ListAudit(ctx, pool, 0, 2)
	if err != nil || len(page1) != 2 || page1[0].Action != "a5" || page1[1].Action != "a4" {
		t.Fatalf("page1 = %+v, %v", page1, err)
	}
	page2, err := ListAudit(ctx, pool, page1[1].ID, 2)
	if err != nil || len(page2) != 2 || page2[0].Action != "a3" || page2[1].Action != "a2" {
		t.Fatalf("page2 = %+v, %v", page2, err)
	}
	if page2[0].Detail != nil {
		t.Fatalf("empty detail read back as %q, want nil", page2[0].Detail)
	}
	page3, err := ListAudit(ctx, pool, page2[1].ID, 2)
	if err != nil || len(page3) != 1 || page3[0].Action != "grant.create" {
		t.Fatalf("page3 = %+v, %v", page3, err)
	}
	first := page3[0]
	if first.ActorID == nil || *first.ActorID != u.ID || first.TargetType != "grant" || string(first.Detail) != `{"n": 1}` || !first.At.Equal(micro(base)) {
		t.Fatalf("first entry = %+v (detail %s)", first, first.Detail)
	}
	if err := InsertAudit(ctx, pool, AuditEntry{At: base, Actor: "x", Action: "bad", Detail: []byte("not json")}); err == nil {
		t.Fatal("invalid JSON detail accepted")
	}
	if _, err := ListAudit(ctx, pool, 0, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("limit 0 = %v, want ErrInvalid", err)
	}
}

func TestAuditIsAppendOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	if err := InsertAudit(ctx, pool, AuditEntry{At: nanosecondNow(), Actor: "system", Action: "keep.me"}); err != nil {
		t.Fatalf("InsertAudit: %v", err)
	}
	for name, statement := range map[string]string{
		"update":   "UPDATE audit_log SET action = 'changed'",
		"delete":   "DELETE FROM audit_log",
		"truncate": "TRUNCATE audit_log",
	} {
		if _, err := pool.Exec(ctx, statement); err == nil {
			t.Errorf("%s on the audit table succeeded, want it refused", name)
		}
	}
	entries, err := ListAudit(ctx, pool, 0, 10)
	if err != nil || len(entries) != 1 || entries[0].Action != "keep.me" {
		t.Fatalf("entries after attempted changes = %+v, %v", entries, err)
	}
}

func TestInsertAuditInvalidJSONIsErrInvalidWithoutServerText(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	err := InsertAudit(ctx, pool, AuditEntry{At: nanosecondNow(), Actor: "x", Action: "bad", Detail: []byte(`{"k": fragment-marker-42`)})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
	if strings.Contains(err.Error(), "fragment-marker-42") || strings.Contains(err.Error(), "SQLSTATE") {
		t.Fatalf("error text carries server message: %q", err.Error())
	}
}

func TestUpsertSAMLUserReportsWhetherItCreated(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	_, created, err := UpsertSAMLUserCreated(ctx, pool, "created-flag@example.com", "A", time.Now())
	if err != nil || !created {
		t.Fatalf("first upsert: created = %v, %v; want true", created, err)
	}
	_, created, err = UpsertSAMLUserCreated(ctx, pool, "Created-Flag@example.com", "B", time.Now())
	if err != nil || created {
		t.Fatalf("second upsert: created = %v, %v; want false", created, err)
	}
}
