//go:build integration

package api

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/rcss-labs/yamdrake/internal/auth"
	"github.com/rcss-labs/yamdrake/internal/store"
)

// managerHarness has a non-admin who holds manage_access, the admin group, and
// a second administrator.
type managerHarness struct {
	*harness
	manager      store.User
	managerToken string
	adminGroup   int64
	otherAdmin   store.User
}

func newManagerHarness(t *testing.T) *managerHarness {
	h := newHarness(t)
	groups, err := store.GroupsOfUser(context.Background(), h.pool, h.admin.ID)
	if err != nil || len(groups) != 1 {
		t.Fatalf("admin groups = %v, %v", groups, err)
	}
	m := &managerHarness{harness: h, adminGroup: groups[0].ID}
	m.manager = h.newUser("manager")
	h.grant(m.manager.ID, "manage_access")
	m.managerToken, _ = h.newToken(m.manager.ID)
	m.otherAdmin = h.newUser("otheradmin")
	h.expect(h.asAdmin("PATCH", "/api/v1/admin/users/"+itoa(m.otherAdmin.ID), map[string]any{"group_ids": []int64{m.adminGroup}}), http.StatusOK, "")
	return m
}

func (m *managerHarness) asManager(method, path string, body any) int {
	m.t.Helper()
	rec := m.send(sent{method: method, path: path, bearer: m.managerToken, body: body})
	if rec.Code == http.StatusForbidden && errorCodeOf(rec) != "forbidden" {
		m.t.Errorf("%s %s: 403 with code %q, want forbidden", method, path, errorCodeOf(rec))
	}
	return rec.Code
}

func (m *managerHarness) memberOf(userID int64) map[int64]bool {
	m.t.Helper()
	groups, err := store.GroupsOfUser(context.Background(), m.pool, userID)
	if err != nil {
		m.t.Fatalf("GroupsOfUser: %v", err)
	}
	out := map[int64]bool{}
	for _, g := range groups {
		out[g.ID] = true
	}
	return out
}

// manage_access must not be a way to become an administrator or to act on one.
func TestManageAccessAloneCannotTouchAdminPower(t *testing.T) {
	m := newManagerHarness(t)
	forbidden := func(name string, code int) {
		t.Helper()
		if code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, code)
		}
	}
	before := len(m.auditEntries())

	forbidden("create an admin group", m.asManager("POST", "/api/v1/admin/groups", map[string]any{"name": "sneaky-admins", "admin": true}))
	forbidden("join the admin group", m.asManager("PATCH", "/api/v1/admin/users/"+itoa(m.manager.ID), map[string]any{"group_ids": []int64{m.adminGroup}}))
	forbidden("remove an administrator from the admin group", m.asManager("PATCH", "/api/v1/admin/users/"+itoa(m.otherAdmin.ID), map[string]any{"group_ids": []int64{}}))
	forbidden("clear the admin flag", m.asManager("PATCH", "/api/v1/admin/groups/"+itoa(m.adminGroup), map[string]any{"admin": false}))
	forbidden("disable an administrator", m.asManager("PATCH", "/api/v1/admin/users/"+itoa(m.otherAdmin.ID), map[string]any{"disabled": true}))
	forbidden("map a provider group to the admin group", m.asManager("PUT", "/api/v1/admin/idp-mappings/dir-admins", map[string]any{"group_id": m.adminGroup}))

	if m.memberOf(m.manager.ID)[m.adminGroup] {
		t.Fatal("the manager became an administrator")
	}
	if got := len(m.auditEntries()); got != before {
		t.Errorf("refused changes left %d audit entries", got-before)
	}

	// Disabled administrators cannot be brought back by a manager either.
	m.expect(m.asAdmin("PATCH", "/api/v1/admin/users/"+itoa(m.otherAdmin.ID), map[string]any{"disabled": true}), http.StatusOK, "")
	forbidden("enable an administrator", m.asManager("PATCH", "/api/v1/admin/users/"+itoa(m.otherAdmin.ID), map[string]any{"disabled": false}))

	// An existing mapping to the admin group is not the manager's to change or remove.
	m.expect(m.asAdmin("PUT", "/api/v1/admin/idp-mappings/dir-admins", map[string]any{"group_id": m.adminGroup}), http.StatusOK, "")
	plain := m.createGroupVia("plain-"+uniqueEmail(t, "g"), false)
	forbidden("repoint a mapping of the admin group", m.asManager("PUT", "/api/v1/admin/idp-mappings/dir-admins", map[string]any{"group_id": plain}))
	forbidden("delete a mapping of the admin group", m.asManager("DELETE", "/api/v1/admin/idp-mappings/dir-admins", nil))
}

// The bypasses a manager would try instead.
func TestManageAccessBypassesOfTheAdminRule(t *testing.T) {
	m := newManagerHarness(t)

	// A group made harmless and promoted afterwards.
	plain := m.createGroupVia("promote-me-"+uniqueEmail(t, "g"), false)
	if code := m.asManager("PATCH", "/api/v1/admin/groups/"+itoa(plain), map[string]any{"admin": true}); code != http.StatusForbidden {
		t.Errorf("promote a plain group: status %d, want 403", code)
	}
	// The same through a different group the manager made itself.
	own := decodeObject(t, m.send(sent{method: "POST", path: "/api/v1/admin/groups", bearer: m.managerToken, body: map[string]any{"name": "mine-" + uniqueEmail(t, "g")}}))
	if own["admin"] != false {
		t.Fatalf("a manager could not make a plain group: %v", own)
	}
	if code := m.asManager("PATCH", "/api/v1/admin/groups/"+itoa(int64(own["id"].(float64))), map[string]any{"admin": true}); code != http.StatusForbidden {
		t.Errorf("promote the manager's own group: status %d, want 403", code)
	}

	// A mixed list: the harmless group and the admin group together. Nothing is applied.
	if code := m.asManager("PATCH", "/api/v1/admin/users/"+itoa(m.manager.ID), map[string]any{"group_ids": []int64{plain, m.adminGroup}}); code != http.StatusForbidden {
		t.Errorf("mixed group list: status %d, want 403", code)
	}
	if m.memberOf(m.manager.ID)[plain] {
		t.Error("the harmless half of a refused request was applied")
	}

	// An administrator through the identity provider only (no manual membership).
	idpAdmin := m.newUser("idpadmin")
	if err := store.ReplaceIDPMemberships(context.Background(), m.pool, idpAdmin.ID, []int64{m.adminGroup}); err != nil {
		t.Fatalf("ReplaceIDPMemberships: %v", err)
	}
	if code := m.asManager("PATCH", "/api/v1/admin/users/"+itoa(idpAdmin.ID), map[string]any{"disabled": true}); code != http.StatusForbidden {
		t.Errorf("disable an administrator by provider group: status %d, want 403", code)
	}

	// A mapping name that differs only in how it is written still names the same row.
	m.expect(m.asAdmin("PUT", "/api/v1/admin/idp-mappings/"+url.PathEscape("Dir Admins"), map[string]any{"group_id": m.adminGroup}), http.StatusOK, "")
	if code := m.asManager("DELETE", "/api/v1/admin/idp-mappings/"+url.PathEscape("Dir Admins"), nil); code != http.StatusForbidden {
		t.Errorf("delete an escaped mapping name: status %d, want 403", code)
	}
}

// What a manager is meant to do still works.
func TestManageAccessStillManagesOrdinaryAccess(t *testing.T) {
	m := newManagerHarness(t)
	plainUser := m.newUser("plainuser")

	rec := m.send(sent{method: "POST", path: "/api/v1/admin/groups", bearer: m.managerToken, body: map[string]any{"name": "deployers-" + uniqueEmail(t, "g")}})
	m.expect(rec, http.StatusCreated, "")
	group := int64(decodeObject(t, rec)["id"].(float64))
	if code := m.asManager("POST", "/api/v1/admin/grants", map[string]any{"group_id": group, "action": "run", "label": "dev"}); code != http.StatusCreated {
		t.Errorf("create a grant: status %d, want 201", code)
	}
	if code := m.asManager("PATCH", "/api/v1/admin/users/"+itoa(plainUser.ID), map[string]any{"group_ids": []int64{group}}); code != http.StatusOK {
		t.Errorf("add a user to a plain group: status %d, want 200", code)
	}
	if code := m.asManager("PATCH", "/api/v1/admin/users/"+itoa(plainUser.ID), map[string]any{"disabled": true}); code != http.StatusOK {
		t.Errorf("disable a plain user: status %d, want 200", code)
	}
	if code := m.asManager("PATCH", "/api/v1/admin/users/"+itoa(plainUser.ID), map[string]any{"disabled": false}); code != http.StatusOK {
		t.Errorf("enable a plain user: status %d, want 200", code)
	}
	if code := m.asManager("PUT", "/api/v1/admin/idp-mappings/dir-devs", map[string]any{"group_id": group}); code != http.StatusOK {
		t.Errorf("map a provider group to a plain group: status %d, want 200", code)
	}
	if code := m.asManager("DELETE", "/api/v1/admin/idp-mappings/dir-devs", nil); code != http.StatusNoContent {
		t.Errorf("delete that mapping: status %d, want 204", code)
	}
	if code := m.asManager("PATCH", "/api/v1/admin/groups/"+itoa(group), map[string]any{"admin": false}); code != http.StatusOK {
		t.Errorf("a no-change patch of a plain group: status %d, want 200", code)
	}
	// Administrators can still do all of it.
	m.expect(m.asAdmin("POST", "/api/v1/admin/groups", map[string]any{"name": "more-admins-" + uniqueEmail(t, "g"), "admin": true}), http.StatusCreated, "")
}

// Whoever controls the identity provider can sign in as anyone, so changing
// the single sign-on settings is admin power; reading and testing are not.
func TestOnlyAnAdministratorChangesSSOSettings(t *testing.T) {
	m := newManagerHarness(t)
	idp := m.newIDP()
	before := len(m.auditEntries())
	body := map[string]any{"enabled": true, "idp_metadata_xml": idp.metadataXML()}

	rec := m.send(sent{method: "PUT", path: "/api/v1/admin/sso", bearer: m.managerToken, body: body})
	m.expect(rec, http.StatusForbidden, "forbidden")
	if got := len(m.auditEntries()); got != before {
		t.Errorf("a refused change left %d audit entries", got-before)
	}
	settings, err := auth.LoadSAMLSettings(context.Background(), m.pool)
	if err != nil || settings.Enabled || settings.IDPMetadataXML != "" {
		t.Fatalf("stored settings after the refused PUT = %+v, %v; want none", settings, err)
	}
	// Reading and testing stay open to manage_access.
	getRec := m.send(sent{method: "GET", path: "/api/v1/admin/sso", bearer: m.managerToken})
	m.expect(getRec, http.StatusOK, "")
	m.expect(m.send(sent{method: "POST", path: "/api/v1/admin/sso/test", bearer: m.managerToken}), http.StatusOK, "")

	// An administrator can, and the GET shape is the same as before.
	m.expect(m.asAdmin("PUT", "/api/v1/admin/sso", body), http.StatusOK, "")
	adminGet := decodeObject(t, m.asAdmin("GET", "/api/v1/admin/sso", nil))
	for _, key := range []string{"enabled", "idp_metadata_xml", "entity_id", "groups_attribute", "email_attribute", "name_attribute", "metadata_url", "acs_url"} {
		if _, ok := adminGet[key]; !ok {
			t.Errorf("GET /admin/sso lost the field %q", key)
		}
	}
	if len(adminGet) != 8 {
		t.Errorf("GET /admin/sso has %d fields, want the 8 of the web contract", len(adminGet))
	}
	// The manager sees the same document.
	if got := decodeObject(t, m.send(sent{method: "GET", path: "/api/v1/admin/sso", bearer: m.managerToken})); len(got) != 8 {
		t.Errorf("manager GET has %d fields, want 8", len(got))
	}
}

// Other ways a manager might try to reach admin power: a new user placed in an
// admin group in the same request, or a field that sets an existing admin's
// identity or password. None of those exist; the requests are refused.
func TestManagerCannotReachAdminPowerThroughUsers(t *testing.T) {
	m := newManagerHarness(t)
	email := uniqueEmail(t, "planted")
	for name, body := range map[string]map[string]any{
		"new user with group_ids":  {"email": email, "name": "n", "source": "local", "password": shortPassword, "group_ids": []int64{m.adminGroup}},
		"new user with admin flag": {"email": email, "name": "n", "source": "local", "password": shortPassword, "admin": true},
	} {
		if rec := m.send(sent{method: "POST", path: "/api/v1/admin/users", bearer: m.managerToken, body: body}); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %s, want 400", name, failure(rec))
		}
	}
	for name, body := range map[string]map[string]any{
		"change an email":   {"email": "x@example.test"},
		"set a password":    {"password": shortPassword},
		"change the source": {"source": "saml"},
	} {
		if rec := m.send(sent{method: "PATCH", path: "/api/v1/admin/users/" + itoa(m.otherAdmin.ID), bearer: m.managerToken, body: body}); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %s, want 400", name, failure(rec))
		}
	}
	if _, err := store.UserByEmail(context.Background(), m.pool, email); err == nil {
		t.Error("a refused request created the user")
	}
}
