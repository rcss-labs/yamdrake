//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/yamdrake/internal/session"
	"github.com/rcss-labs/yamdrake/internal/store"
)

// shortPassword is the shortest valid password: argon2 dominates test time.
const shortPassword = "twelve-chars"

func decodeObject(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not a JSON object (status %d): %v", rec.Code, err)
	}
	return out
}

// asAdmin sends a request with the harness admin's bearer token.
func (h *harness) asAdmin(method, path string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.send(sent{method: method, path: path, bearer: h.adminToken, body: body})
}

func (h *harness) createGroupVia(name string, admin bool) int64 {
	h.t.Helper()
	rec := h.asAdmin("POST", "/api/v1/admin/groups", map[string]any{"name": name, "admin": admin})
	h.expect(rec, http.StatusCreated, "")
	return int64(decodeObject(h.t, rec)["id"].(float64))
}

func TestNonAdminCannotManageAccess(t *testing.T) {
	h := newHarness(t)
	user := h.newUser("plain")
	token, _ := h.newToken(user.ID)
	routes := []struct{ method, path string }{
		{"GET", "/api/v1/admin/users"}, {"POST", "/api/v1/admin/users"}, {"PATCH", "/api/v1/admin/users/1"},
		{"GET", "/api/v1/admin/groups"}, {"POST", "/api/v1/admin/groups"}, {"PATCH", "/api/v1/admin/groups/1"},
		{"GET", "/api/v1/admin/idp-mappings"}, {"GET", "/api/v1/admin/idp-mappings/x"},
		{"PUT", "/api/v1/admin/idp-mappings/x"}, {"DELETE", "/api/v1/admin/idp-mappings/x"},
		{"GET", "/api/v1/admin/grants"}, {"POST", "/api/v1/admin/grants"}, {"DELETE", "/api/v1/admin/grants/1"},
		{"GET", "/api/v1/admin/audit"},
	}
	for _, route := range routes {
		rec := h.send(sent{method: route.method, path: route.path, bearer: token, body: map[string]any{}})
		if rec.Code != http.StatusForbidden || errorCodeOf(rec) != "forbidden" {
			t.Errorf("%s %s as a plain user = %s, want 403 forbidden", route.method, route.path, failure(rec))
		}
		if rec := h.send(sent{method: route.method, path: route.path}); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without credentials = %s, want 401", route.method, route.path, failure(rec))
		}
	}
	// view_audit alone opens the audit log and nothing else.
	h.grant(user.ID, "view_audit")
	h.expect(h.send(sent{method: "GET", path: "/api/v1/admin/audit", bearer: token}), http.StatusOK, "")
	h.expect(h.send(sent{method: "GET", path: "/api/v1/admin/users", bearer: token}), http.StatusForbidden, "forbidden")
}

func TestCreateGroupGrantAndMembershipThenUserGainsPermission(t *testing.T) {
	h := newHarness(t)
	user := h.newUser("member")
	token, _ := h.newToken(user.ID)
	meGrants := func() int {
		rec := h.send(sent{method: "GET", path: "/api/v1/me", bearer: token})
		h.expect(rec, http.StatusOK, "")
		return len(decodeObject(t, rec)["grants"].([]any))
	}
	if meGrants() != 0 {
		t.Fatalf("a new user already has grants")
	}
	groupID := h.createGroupVia("deployers-"+uniqueEmail(t, "g"), false)
	h.expect(h.asAdmin("POST", "/api/v1/admin/grants", map[string]any{"group_id": groupID, "action": "run", "label": "dev"}), http.StatusCreated, "")
	h.expect(h.asAdmin("POST", "/api/v1/admin/grants", map[string]any{"group_id": groupID, "action": "run", "label": "dev"}), http.StatusConflict, "conflict")
	h.expect(h.asAdmin("POST", "/api/v1/admin/grants", map[string]any{"group_id": groupID, "action": "fly"}), http.StatusBadRequest, "invalid")
	h.expect(h.asAdmin("POST", "/api/v1/admin/grants", map[string]any{"group_id": groupID + 999, "action": "run"}), http.StatusBadRequest, "invalid")

	rec := h.asAdmin("PATCH", "/api/v1/admin/users/"+itoa(user.ID), map[string]any{"group_ids": []int64{groupID}})
	h.expect(rec, http.StatusOK, "")
	if got := meGrants(); got != 1 {
		t.Fatalf("grants after joining the group = %d, want 1", got)
	}
	// The list shows the membership.
	list := decodeObject(t, h.asAdmin("GET", "/api/v1/admin/users", nil))["users"].([]any)
	var seen bool
	for _, entry := range list {
		u := entry.(map[string]any)
		if int64(u["id"].(float64)) == user.ID {
			seen = len(u["groups"].([]any)) == 1
		}
	}
	if !seen {
		t.Errorf("the user list does not show the new membership")
	}
	// Leaving the group takes the permission away again.
	h.expect(h.asAdmin("PATCH", "/api/v1/admin/users/"+itoa(user.ID), map[string]any{"group_ids": []int64{}}), http.StatusOK, "")
	if meGrants() != 0 {
		t.Errorf("the grant outlived the membership")
	}
	h.expect(h.asAdmin("PATCH", "/api/v1/admin/users/"+itoa(user.ID), map[string]any{"group_ids": []int64{groupID + 999}}), http.StatusBadRequest, "invalid")
	h.expect(h.asAdmin("PATCH", "/api/v1/admin/users/"+itoa(user.ID+999), map[string]any{"disabled": true}), http.StatusNotFound, "not_found")
}

func TestCreateUserLocalAndService(t *testing.T) {
	h := newHarness(t)
	email := uniqueEmail(t, "created")
	rec := h.asAdmin("POST", "/api/v1/admin/users", map[string]any{"email": strings.ToUpper(email), "name": "Created", "source": "local", "password": shortPassword})
	h.expect(rec, http.StatusCreated, "")
	if strings.Contains(rec.Body.String(), shortPassword) || strings.Contains(rec.Body.String(), "argon2") {
		t.Errorf("the response holds the password or its hash")
	}
	hash, err := store.PasswordHash(context.Background(), h.pool, int64(decodeObject(t, rec)["id"].(float64)))
	if err != nil || !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("stored hash = %q, %v; want an argon2id hash", hash[:min(len(hash), 9)], err)
	}
	h.expect(h.asAdmin("POST", "/api/v1/admin/users", map[string]any{"email": email, "name": "Again", "source": "service", "password": shortPassword}), http.StatusConflict, "conflict")
	h.expect(h.asAdmin("POST", "/api/v1/admin/users", map[string]any{"email": uniqueEmail(t, "weak"), "source": "local", "password": "short"}), http.StatusBadRequest, "invalid")
	h.expect(h.asAdmin("POST", "/api/v1/admin/users", map[string]any{"email": uniqueEmail(t, "saml"), "source": "saml", "password": shortPassword}), http.StatusBadRequest, "invalid")
	h.expect(h.asAdmin("POST", "/api/v1/admin/users", map[string]any{"email": "not-an-email", "source": "local", "password": shortPassword}), http.StatusBadRequest, "invalid")
	h.expect(h.asAdmin("POST", "/api/v1/admin/users", map[string]any{"email": uniqueEmail(t, "svc"), "name": "Bot", "source": "service", "password": shortPassword}), http.StatusCreated, "")
}

func TestDisableUserDeletesSessions(t *testing.T) {
	h := newHarness(t)
	user := h.newUser("todisable")
	first, second := h.newSession(user.ID), h.newSession(user.ID)
	token, _ := h.newToken(user.ID)
	h.expect(h.send(sent{method: "GET", path: "/api/v1/me", sessionID: first}), http.StatusOK, "")

	rec := h.asAdmin("PATCH", "/api/v1/admin/users/"+itoa(user.ID), map[string]any{"disabled": true})
	h.expect(rec, http.StatusOK, "")
	if decodeObject(t, rec)["disabled"] != true {
		t.Errorf("the response does not show the user as disabled")
	}
	for _, id := range []string{first, second} {
		if _, err := h.sessions.Get(context.Background(), id); err == nil {
			t.Errorf("a session of the disabled user is still stored")
		}
	}
	h.expect(h.send(sent{method: "GET", path: "/api/v1/me", bearer: token}), http.StatusUnauthorized, "unauthorized")

	// Enabling brings back the account, but neither the sessions nor the tokens
	// (disabling revoked them for good).
	h.expect(h.asAdmin("PATCH", "/api/v1/admin/users/"+itoa(user.ID), map[string]any{"disabled": false}), http.StatusOK, "")
	h.expect(h.send(sent{method: "GET", path: "/api/v1/me", bearer: token}), http.StatusUnauthorized, "unauthorized")
}

func TestDisableStandsWhenValkeyIsDown(t *testing.T) {
	h := newHarness(t)
	user := h.newUser("valkeydown")
	shortenValkeyTimeout(t)
	dead := openValkey(t, "127.0.0.1:1")
	h.rebuild(func(d *Deps) { d.Sessions = session.NewManager(dead, time.Now) })
	rec := h.asAdmin("PATCH", "/api/v1/admin/users/"+itoa(user.ID), map[string]any{"disabled": true})
	h.expect(rec, http.StatusOK, "")
	if decodeObject(t, rec)["sessions_revoked"] != false {
		t.Errorf("the response must say the sessions could not be deleted")
	}
	stored, err := store.UserByID(context.Background(), h.pool, user.ID)
	if err != nil || !stored.Disabled {
		t.Fatalf("the user is not disabled after the request: %v", err)
	}
}

func TestCannotRemoveLastAdmin(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	adminPath := "/api/v1/admin/users/" + itoa(h.admin.ID)
	groups, err := store.GroupsOfUser(ctx, h.pool, h.admin.ID)
	if err != nil || len(groups) != 1 {
		t.Fatalf("GroupsOfUser = %v, %v", groups, err)
	}
	adminGroup := groups[0].ID

	// Disable path.
	h.expect(h.asAdmin("PATCH", adminPath, map[string]any{"disabled": true}), http.StatusConflict, "conflict")
	// Un-admin path.
	h.expect(h.asAdmin("PATCH", "/api/v1/admin/groups/"+itoa(adminGroup), map[string]any{"admin": false}), http.StatusConflict, "conflict")
	// Leaving the admin group.
	h.expect(h.asAdmin("PATCH", adminPath, map[string]any{"group_ids": []int64{}}), http.StatusConflict, "conflict")
	if u, _ := store.UserByID(ctx, h.pool, h.admin.ID); u.Disabled {
		t.Fatalf("a refused change was applied")
	}
	if g, _ := store.GroupsOfUser(ctx, h.pool, h.admin.ID); len(g) != 1 || !g[0].Admin {
		t.Fatalf("a refused change was applied to the group")
	}

	// With a second admin the first can go.
	second := h.newUser("second")
	h.expect(h.asAdmin("PATCH", "/api/v1/admin/users/"+itoa(second.ID), map[string]any{"group_ids": []int64{adminGroup}}), http.StatusOK, "")
	h.expect(h.asAdmin("PATCH", adminPath, map[string]any{"disabled": true}), http.StatusOK, "")
	// ... and now the second is the last one.
	secondToken, _ := h.newToken(second.ID)
	rec := h.send(sent{method: "PATCH", path: "/api/v1/admin/users/" + itoa(second.ID), bearer: secondToken, body: map[string]any{"disabled": true}})
	h.expect(rec, http.StatusConflict, "conflict")
}

// holdAdminLock takes the admin lock in a transaction of its own and returns
// it with the PostgreSQL backend pid that holds it.
func (h *harness) holdAdminLock() (pgx.Tx, int) {
	h.t.Helper()
	ctx := context.Background()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.t.Fatalf("begin: %v", err)
	}
	if err := store.LockAdminChanges(ctx, tx); err != nil {
		h.t.Fatalf("LockAdminChanges: %v", err)
	}
	var pid int
	if err := tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		h.t.Fatalf("pg_backend_pid: %v", err)
	}
	return tx, pid
}

// waitForAdminLockWaiters waits (up to 10 seconds) until at least n backends
// other than holderPID wait for the admin lock itself: the advisory lock with
// its key, ungranted, in this database, so other locks and other tests on a
// shared server are not counted.
func (h *harness) waitForAdminLockWaiters(holderPID, n int) bool {
	h.t.Helper()
	key := store.AdminLockKey()
	classID, objID := int64(uint32(uint64(key)>>32)), int64(uint32(uint64(key)))
	deadline := time.Now().Add(10 * time.Second)
	for {
		var got int
		err := h.pool.QueryRow(context.Background(), `SELECT count(DISTINCT pid) FROM pg_locks
WHERE locktype = 'advisory' AND NOT granted AND objsubid = 1
AND classid::bigint = $1 AND objid::bigint = $2 AND pid <> $3
AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`, classID, objID, holderPID).Scan(&got)
		if err != nil {
			h.t.Fatalf("read pg_locks: %v", err)
		}
		if got >= n {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The admin-affecting writes of identity-provider mappings take the same lock
// as the other writes that can change who is an administrator.
func TestIDPMappingChangesTakeTheAdminLock(t *testing.T) {
	h := newHarness(t)
	group := h.createGroupVia("locked-"+uniqueEmail(t, "g"), false)
	h.expect(h.asAdmin("PUT", "/api/v1/admin/idp-mappings/to-delete", map[string]any{"group_id": group}), http.StatusOK, "")
	for name, request := range map[string]struct {
		method, path string
		body         any
		want         int
	}{
		"put":    {"PUT", "/api/v1/admin/idp-mappings/locked-one", map[string]any{"group_id": group}, http.StatusOK},
		"delete": {"DELETE", "/api/v1/admin/idp-mappings/to-delete", nil, http.StatusNoContent},
	} {
		holder, pid := h.holdAdminLock()
		done := make(chan int, 1)
		go func() {
			done <- h.send(sent{method: request.method, path: request.path, bearer: h.adminToken, body: request.body}).Code
		}()
		if !h.waitForAdminLockWaiters(pid, 1) {
			_ = holder.Rollback(context.Background())
			t.Fatalf("%s: the request did not wait for the admin lock", name)
		}
		_ = holder.Rollback(context.Background())
		if code := <-done; code != request.want {
			t.Errorf("%s: status %d, want %d", name, code, request.want)
		}
	}
}

// Two administrators each disable the other. Left alone the two requests could
// both read "two administrators" before either writes; the admin lock makes the
// second one count afresh. The test holds the lock itself, so both requests are
// certainly waiting on it (checked in pg_locks) before either may go on, and the
// outcome cannot depend on timing: one succeeds, the other is refused as the
// removal of the last administrator.
func TestTwoConcurrentRemovalsOfDifferentAdminsCannotBothSucceed(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	second := h.newUser("second")
	groups, _ := store.GroupsOfUser(ctx, h.pool, h.admin.ID)
	h.expect(h.asAdmin("PATCH", "/api/v1/admin/users/"+itoa(second.ID), map[string]any{"group_ids": []int64{groups[0].ID}}), http.StatusOK, "")
	secondToken, _ := h.newToken(second.ID)

	holder, holderPID := h.holdAdminLock()
	defer func() { _ = holder.Rollback(ctx) }()

	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i, request := range []struct{ actor, victim string }{
		{h.adminToken, itoa(second.ID)}, // the first administrator disables the second ...
		{secondToken, itoa(h.admin.ID)}, // ... and the second disables the first
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := h.send(sent{method: "PATCH", path: "/api/v1/admin/users/" + request.victim, bearer: request.actor, body: map[string]any{"disabled": true}})
			codes[i] = rec.Code
		}()
	}

	// Both requests must be queued on the advisory lock before it is released.
	if !h.waitForAdminLockWaiters(holderPID, 2) {
		_ = holder.Rollback(ctx)
		wg.Wait()
		t.Fatalf("fewer than 2 requests waited on the admin lock (answers %v); both must take it before changing anything", codes)
	}
	if err := holder.Rollback(ctx); err != nil {
		t.Fatalf("release the lock: %v", err)
	}
	wg.Wait()

	slices.Sort(codes)
	if codes[0] != http.StatusOK || codes[1] != http.StatusConflict {
		t.Fatalf("answers = %v, want exactly one 200 and one 409", codes)
	}
	admins, err := store.CountEnabledAdmins(ctx, h.pool)
	if err != nil || admins != 1 {
		t.Fatalf("enabled administrators = %d, %v; want exactly 1", admins, err)
	}
}

func TestIDPMappingCRUD(t *testing.T) {
	h := newHarness(t)
	groupID := h.createGroupVia("mapped-"+uniqueEmail(t, "g"), false)
	other := h.createGroupVia("other-"+uniqueEmail(t, "g"), false)
	name := "Engineers & Co/" + uniqueEmail(t, "idp") // needs URL escaping
	path := "/api/v1/admin/idp-mappings/" + url.PathEscape(name)

	h.expect(h.asAdmin("GET", path, nil), http.StatusNotFound, "not_found")
	h.expect(h.asAdmin("PUT", path, map[string]any{"group_id": groupID}), http.StatusOK, "")
	rec := h.asAdmin("GET", path, nil)
	h.expect(rec, http.StatusOK, "")
	if got := decodeObject(t, rec); got["idp_group"] != name || int64(got["group_id"].(float64)) != groupID {
		t.Fatalf("mapping = %v, want %q -> %d", got, name, groupID)
	}
	h.expect(h.asAdmin("PUT", path, map[string]any{"group_id": other}), http.StatusOK, "") // replaces
	list := decodeObject(t, h.asAdmin("GET", "/api/v1/admin/idp-mappings", nil))["mappings"].([]any)
	var found bool
	for _, m := range list {
		entry := m.(map[string]any)
		if entry["idp_group"] == name {
			found = int64(entry["group_id"].(float64)) == other
		}
	}
	if !found {
		t.Errorf("the list does not show the replaced mapping")
	}
	h.expect(h.asAdmin("PUT", path, map[string]any{"group_id": other + 999}), http.StatusBadRequest, "invalid")
	h.expect(h.asAdmin("DELETE", path, nil), http.StatusNoContent, "")
	h.expect(h.asAdmin("DELETE", path, nil), http.StatusNotFound, "not_found")
}

func TestAuditPagingAndPermission(t *testing.T) {
	h := newHarness(t)
	for i := range 5 {
		h.createGroupVia("audit-"+strconv.Itoa(i)+"-"+uniqueEmail(t, "g"), false)
	}
	rec := h.asAdmin("GET", "/api/v1/admin/audit?limit=2", nil)
	h.expect(rec, http.StatusOK, "")
	page := decodeObject(t, rec)
	entries := page["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("page size = %d, want 2", len(entries))
	}
	first := entries[0].(map[string]any)
	second := entries[1].(map[string]any)
	if first["action"] != "group.create" || first["actor"] != h.admin.Email || first["target_type"] != "group" {
		t.Errorf("newest entry = %v, want a group.create by the admin", first)
	}
	if first["id"].(float64) <= second["id"].(float64) {
		t.Errorf("entries are not newest first")
	}
	next, ok := page["next_before"].(float64)
	if !ok || next != second["id"].(float64) {
		t.Fatalf("next_before = %v, want the id of the last entry", page["next_before"])
	}
	older := decodeObject(t, h.asAdmin("GET", "/api/v1/admin/audit?limit=2&before="+strconv.FormatInt(int64(next), 10), nil))["entries"].([]any)
	if len(older) != 2 || older[0].(map[string]any)["id"].(float64) >= next {
		t.Errorf("the second page does not continue below the first")
	}
	// Bad parameters.
	for _, q := range []string{"limit=0", "limit=x", "before=-1", "before=x", "limit=-3"} {
		h.expect(h.asAdmin("GET", "/api/v1/admin/audit?"+q, nil), http.StatusBadRequest, "invalid")
	}
	// A limit above 200 is capped, not refused.
	capped := decodeObject(t, h.asAdmin("GET", "/api/v1/admin/audit?limit=100000", nil))["entries"].([]any)
	if len(capped) > 200 {
		t.Errorf("limit above 200 returned %d entries", len(capped))
	}
	// Permission is view_audit, not manage_access.
	user := h.newUser("manager")
	h.grant(user.ID, "manage_access")
	token, _ := h.newToken(user.ID)
	h.expect(h.send(sent{method: "GET", path: "/api/v1/admin/audit", bearer: token}), http.StatusForbidden, "forbidden")
}

func TestUserGroupsReportTheirSource(t *testing.T) {
	h := newHarness(t)
	user := h.newUser("mixed")
	manualOnly := h.createGroupVia("manual-only-"+uniqueEmail(t, "g"), false)
	idpOnly := h.createGroupVia("idp-only-"+uniqueEmail(t, "g"), false)
	both := h.createGroupVia("both-"+uniqueEmail(t, "g"), false)
	h.expect(h.asAdmin("PATCH", "/api/v1/admin/users/"+itoa(user.ID), map[string]any{"group_ids": []int64{manualOnly, both}}), http.StatusOK, "")
	if err := store.ReplaceIDPMemberships(context.Background(), h.pool, user.ID, []int64{idpOnly, both}); err != nil {
		t.Fatal(err)
	}
	want := map[int64]string{manualOnly: "manual", idpOnly: "idp", both: "manual"}
	for _, entry := range decodeObject(t, h.asAdmin("GET", "/api/v1/admin/users", nil))["users"].([]any) {
		u := entry.(map[string]any)
		if int64(u["id"].(float64)) != user.ID {
			continue
		}
		got := map[int64]string{}
		for _, g := range u["groups"].([]any) {
			ref := g.(map[string]any)
			got[int64(ref["id"].(float64))], _ = ref["source"].(string)
		}
		if len(got) != len(want) {
			t.Fatalf("groups = %v, want %v", got, want)
		}
		for id, source := range want {
			if got[id] != source {
				t.Errorf("group %d source = %q, want %q (manual wins when both)", id, got[id], source)
			}
		}
		return
	}
	t.Fatal("user not listed")
}

func TestAuditNextBeforeOnlyWhenOlderRowsExist(t *testing.T) {
	h := newHarness(t)
	for i := range 3 {
		h.createGroupVia("audit-edge-"+strconv.Itoa(i)+"-"+uniqueEmail(t, "g"), false)
	}
	all := decodeObject(t, h.asAdmin("GET", "/api/v1/admin/audit?limit=200", nil))["entries"].([]any)
	total := len(all)
	if total < 3 || total >= 200 {
		t.Fatalf("test needs a log of 3 to 199 entries, got %d", total)
	}
	// Exactly as many rows as the limit: nothing older, so no next page.
	exact := decodeObject(t, h.asAdmin("GET", "/api/v1/admin/audit?limit="+strconv.Itoa(total), nil))
	if len(exact["entries"].([]any)) != total || exact["next_before"] != nil {
		t.Errorf("a full last page: %d entries, next_before = %v, want %d and null", len(exact["entries"].([]any)), exact["next_before"], total)
	}
	// One row fewer than the log: an older row exists.
	short := decodeObject(t, h.asAdmin("GET", "/api/v1/admin/audit?limit="+strconv.Itoa(total-1), nil))
	if len(short["entries"].([]any)) != total-1 || short["next_before"] == nil {
		t.Errorf("a page with an older row: %d entries, next_before = %v, want %d and a cursor", len(short["entries"].([]any)), short["next_before"], total-1)
	}
}

// TestEveryChangeIsAudited makes one change per mutating route of this task
// and checks the entry it leaves, in the same step.
func TestEveryChangeIsAudited(t *testing.T) {
	h := newHarness(t)
	wantAction := func(action string, run func()) store.AuditEntry {
		t.Helper()
		before := len(h.auditEntries())
		run()
		entries := h.auditEntries()
		if len(entries) <= before {
			t.Fatalf("%s: no audit entry was written", action)
		}
		e := entries[0]
		if e.Action != action || e.ActorID == nil || *e.ActorID != h.admin.ID {
			t.Fatalf("%s: newest entry is %q by %v", action, e.Action, e.ActorID)
		}
		for _, secret := range []string{shortPassword, "argon2", "ymd_", h.adminToken} {
			if strings.Contains(string(e.Detail), secret) {
				t.Fatalf("%s: the audit detail holds a secret", action)
			}
		}
		return e
	}
	var userID, groupID int64
	wantAction("user.create", func() {
		rec := h.asAdmin("POST", "/api/v1/admin/users", map[string]any{"email": uniqueEmail(t, "aud"), "name": "Aud", "source": "service", "password": shortPassword})
		h.expect(rec, http.StatusCreated, "")
		userID = int64(decodeObject(t, rec)["id"].(float64))
	})
	wantAction("group.create", func() { groupID = h.createGroupVia("aud-"+uniqueEmail(t, "g"), false) })
	e := wantAction("group.update", func() {
		h.expect(h.asAdmin("PATCH", "/api/v1/admin/groups/"+itoa(groupID), map[string]any{"admin": true}), http.StatusOK, "")
	})
	if !strings.Contains(string(e.Detail), `"before"`) || !strings.Contains(string(e.Detail), `"after"`) {
		t.Errorf("group.update detail %s lacks before and after", e.Detail)
	}
	wantAction("user.update", func() {
		h.expect(h.asAdmin("PATCH", "/api/v1/admin/users/"+itoa(userID), map[string]any{"disabled": true, "group_ids": []int64{groupID}}), http.StatusOK, "")
	})
	var grantID int64
	wantAction("grant.create", func() {
		rec := h.asAdmin("POST", "/api/v1/admin/grants", map[string]any{"group_id": groupID, "action": "run", "environment": "dev"})
		h.expect(rec, http.StatusCreated, "")
		grantID = int64(decodeObject(t, rec)["id"].(float64))
	})
	wantAction("grant.delete", func() {
		h.expect(h.asAdmin("DELETE", "/api/v1/admin/grants/"+itoa(grantID), nil), http.StatusNoContent, "")
	})
	wantAction("idp_mapping.set", func() {
		h.expect(h.asAdmin("PUT", "/api/v1/admin/idp-mappings/aud-idp", map[string]any{"group_id": groupID}), http.StatusOK, "")
	})
	wantAction("idp_mapping.delete", func() {
		h.expect(h.asAdmin("DELETE", "/api/v1/admin/idp-mappings/aud-idp", nil), http.StatusNoContent, "")
	})
	var tokenID int64
	adminSession := h.newSession(h.admin.ID)
	e = wantAction("token.create", func() {
		// Tokens are managed from a browser session only.
		rec := h.send(sent{method: "POST", path: "/api/v1/tokens", sessionID: adminSession, origin: publicURL, body: map[string]any{"name": "audited"}})
		h.expect(rec, http.StatusCreated, "")
		tokenID = int64(decodeObject(t, rec)["id"].(float64))
	})
	_ = e
	wantAction("token.revoke", func() {
		h.expect(h.send(sent{method: "DELETE", path: "/api/v1/tokens/" + itoa(tokenID), sessionID: adminSession, origin: publicURL}), http.StatusNoContent, "")
	})
}

// The web application's contract: field names, and lists that are [] never null.
func TestAccessResponsesMatchTheWebContract(t *testing.T) {
	h := newHarness(t)
	// Empty lists are arrays.
	for path, key := range map[string]string{
		"/api/v1/admin/grants": "grants", "/api/v1/admin/idp-mappings": "mappings",
		"/api/v1/admin/audit": "entries",
	} {
		rec := h.asAdmin("GET", path, nil)
		h.expect(rec, http.StatusOK, "")
		if !strings.Contains(rec.Body.String(), `"`+key+`":[]`) {
			t.Errorf("GET %s: empty %s is not []", path, key)
		}
	}
	fresh := h.newUser("notokens")
	rec := h.send(sent{method: "GET", path: "/api/v1/tokens", sessionID: h.newSession(fresh.ID)})
	h.expect(rec, http.StatusOK, "")
	if !strings.Contains(rec.Body.String(), `"tokens":[]`) {
		t.Errorf("GET /api/v1/tokens: empty tokens is not []")
	}
	// A user without groups has "groups": [], and the contract's field names.
	user := h.newUser("contract")
	rec = h.asAdmin("GET", "/api/v1/admin/users", nil)
	h.expect(rec, http.StatusOK, "")
	var found map[string]any
	for _, u := range decodeObject(t, rec)["users"].([]any) {
		if int64(u.(map[string]any)["id"].(float64)) == user.ID {
			found = u.(map[string]any)
		}
	}
	for _, key := range []string{"id", "email", "name", "source", "disabled", "groups", "last_sign_in_at", "created_at"} {
		if _, ok := found[key]; !ok {
			t.Errorf("user lacks %q", key)
		}
	}
	if groups, _ := found["groups"].([]any); groups == nil || len(groups) != 0 {
		t.Errorf("groups of a new user = %v, want []", found["groups"])
	}
	// Groups carry member_count; a group's members are counted once.
	groupID := h.createGroupVia("counted-"+uniqueEmail(t, "g"), false)
	h.expect(h.asAdmin("PATCH", "/api/v1/admin/users/"+idString(user.ID), map[string]any{"group_ids": []int64{groupID}}), http.StatusOK, "")
	counts := map[int64]float64{}
	for _, g := range decodeObject(t, h.asAdmin("GET", "/api/v1/admin/groups", nil))["groups"].([]any) {
		entry := g.(map[string]any)
		count, ok := entry["member_count"].(float64)
		if !ok {
			t.Fatalf("group lacks member_count")
		}
		counts[int64(entry["id"].(float64))] = count
	}
	if counts[groupID] != 1 {
		t.Errorf("member_count of the group = %v, want 1", counts[groupID])
	}
	// Audit pages: next_before is always present (null on the last page).
	last := decodeObject(t, h.asAdmin("GET", "/api/v1/admin/audit?limit=200", nil))
	if v, present := last["next_before"]; !present || v != nil {
		t.Errorf("next_before on the last page = %v (present %v), want null", v, present)
	}
	// A token is listed with the contract's fields and no revoked ones.
	adminSession := h.newSession(h.admin.ID)
	created := decodeObject(t, h.send(sent{method: "POST", path: "/api/v1/tokens", sessionID: adminSession, origin: publicURL, body: map[string]any{"name": "w", "ttl_days": 1}}))
	if p, _ := created["plaintext"].(string); !strings.HasPrefix(p, "ymd_") {
		t.Errorf("created token has no plaintext")
	}
	h.expect(h.send(sent{method: "DELETE", path: "/api/v1/tokens/" + idString(int64(created["id"].(float64))), sessionID: adminSession, origin: publicURL}), http.StatusNoContent, "")
	if left := decodeObject(t, h.asAdmin("GET", "/api/v1/tokens", nil))["tokens"].([]any); len(left) != 1 {
		t.Errorf("tokens listed after revoking one = %d, want only the admin's own", len(left))
	}
}
