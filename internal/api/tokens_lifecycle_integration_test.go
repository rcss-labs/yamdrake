//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/yamdrake/internal/auth"
	"github.com/rcss-labs/yamdrake/internal/store"
)

// clockHarness is a harness whose API clock the test moves.
func newClockHarness(t *testing.T, start time.Time) (*harness, func(time.Time)) {
	h := newHarness(t)
	var now atomic.Int64
	now.Store(start.UnixNano())
	h.rebuild(func(d *Deps) { d.Now = func() time.Time { return time.Unix(0, now.Load()) } })
	return h, func(to time.Time) { now.Store(to.UnixNano()) }
}

func (h *harness) tokenAt(userID int64, at time.Time) string {
	h.t.Helper()
	plaintext, _, err := auth.NewToken(context.Background(), h.pool, userID, "cli", 365*24*time.Hour, at)
	if err != nil {
		h.t.Fatalf("NewToken: %v", err)
	}
	return plaintext
}

// A token of a SAML user lives only as long as the last SSO sign-in is recent:
// group memberships are refreshed at sign-in only, so a person removed from the
// identity provider must lose a token too.
func TestSAMLUserTokenNeedsARecentSSOSignIn(t *testing.T) {
	t0 := time.Now().Truncate(time.Second)
	h, setNow := newClockHarness(t, t0)
	ctx := context.Background()
	samlUser, err := store.UpsertSAMLUser(ctx, h.pool, uniqueEmail(t, "sso"), "Sso Person", t0)
	if err != nil {
		t.Fatalf("UpsertSAMLUser: %v", err)
	}
	token := h.tokenAt(samlUser.ID, t0)
	const maxAge = 168 * time.Hour // the default

	me := func(bearer string) *http.Response {
		return h.send(sent{method: "GET", path: "/api/v1/me", bearer: bearer}).Result()
	}
	for name, age := range map[string]time.Duration{"fresh": 0, "just inside": maxAge - time.Second, "exactly the limit": maxAge} {
		setNow(t0.Add(age))
		if got := me(token).StatusCode; got != http.StatusOK {
			t.Errorf("%s: status %d, want 200", name, got)
		}
	}

	setNow(t0.Add(maxAge + time.Second))
	rec := h.send(sent{method: "GET", path: "/api/v1/me", bearer: token})
	h.expect(rec, http.StatusUnauthorized, "unauthorized")
	if got := errorMessage(decodeObject(t, rec)); got != "Sign in again with SSO: yumdrake login" {
		t.Errorf("message = %q, want the SSO hint", got)
	}
	// The rule lives in authentication, not in one route.
	for _, path := range []string{"/api/v1/tokens", "/api/v1/inventory", "/api/v1/folders"} {
		h.expect(h.send(sent{method: "GET", path: path, bearer: token}), http.StatusUnauthorized, "unauthorized")
	}

	// Signing in with SSO again (which refreshes the groups) revives the same token.
	if _, err := store.UpsertSAMLUser(ctx, h.pool, samlUser.Email, "Sso Person", t0.Add(maxAge+time.Minute)); err != nil {
		t.Fatalf("UpsertSAMLUser: %v", err)
	}
	setNow(t0.Add(maxAge + 2*time.Minute))
	if got := me(token).StatusCode; got != http.StatusOK {
		t.Errorf("after a new SSO sign-in: status %d, want 200", got)
	}
}

// Group changes at the identity provider reach a token holder at the next SSO
// sign-in at the latest: administrator power does not outlive the window.
func TestRemovedSAMLAdministratorLosesTheTokenAfterTheWindow(t *testing.T) {
	t0 := time.Now().Truncate(time.Second)
	h, setNow := newClockHarness(t, t0)
	ctx := context.Background()
	samlAdmin, err := store.UpsertSAMLUser(ctx, h.pool, uniqueEmail(t, "ssoadmin"), "Sso Admin", t0)
	if err != nil {
		t.Fatalf("UpsertSAMLUser: %v", err)
	}
	groups, _ := store.GroupsOfUser(ctx, h.pool, h.admin.ID)
	if err := store.ReplaceIDPMemberships(ctx, h.pool, samlAdmin.ID, []int64{groups[0].ID}); err != nil {
		t.Fatalf("ReplaceIDPMemberships: %v", err)
	}
	token := h.tokenAt(samlAdmin.ID, t0)
	h.expect(h.send(sent{method: "GET", path: "/api/v1/admin/users", bearer: token}), http.StatusOK, "")
	setNow(t0.Add(169 * time.Hour))
	h.expect(h.send(sent{method: "GET", path: "/api/v1/admin/users", bearer: token}), http.StatusUnauthorized, "unauthorized")
}

func TestLocalAndServiceTokensIgnoreTheSSOWindow(t *testing.T) {
	t0 := time.Now().Truncate(time.Second)
	h, setNow := newClockHarness(t, t0)
	local := h.newUser("localcli")
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	service, err := store.CreateLocalUser(context.Background(), h.pool, uniqueEmail(t, "svc"), "svc", "service", hash)
	if err != nil {
		t.Fatalf("CreateLocalUser: %v", err)
	}
	tokens := []string{h.tokenAt(local.ID, t0), h.tokenAt(service.ID, t0)}
	setNow(t0.Add(30 * 24 * time.Hour))
	for i, token := range tokens {
		h.expect(h.send(sent{method: "GET", path: "/api/v1/me", bearer: token}), http.StatusOK, "")
		_ = i
	}
}

// Disabling a user revokes their tokens for good: enabling the user again must
// not bring them back.
func TestDisablingAUserRevokesTheirTokensForGood(t *testing.T) {
	h := newHarness(t)
	victim := h.newUser("tokenvictim")
	first, _ := h.newToken(victim.ID)
	second, _ := h.newToken(victim.ID)
	path := "/api/v1/admin/users/" + itoa(victim.ID)
	h.expect(h.send(sent{method: "GET", path: "/api/v1/me", bearer: first}), http.StatusOK, "")

	h.expect(h.asAdmin("PATCH", path, map[string]any{"disabled": true}), http.StatusOK, "")
	h.expect(h.asAdmin("PATCH", path, map[string]any{"disabled": false}), http.StatusOK, "")
	for _, token := range []string{first, second} {
		h.expect(h.send(sent{method: "GET", path: "/api/v1/me", bearer: token}), http.StatusUnauthorized, "unauthorized")
	}
	tokens, err := store.ListTokens(context.Background(), h.pool, victim.ID)
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	for _, tok := range tokens {
		if tok.RevokedAt == nil {
			t.Errorf("token %d is not revoked", tok.ID)
		}
	}
	// A token made after the user is enabled again is a new one and works.
	fresh, _ := h.newToken(victim.ID)
	h.expect(h.send(sent{method: "GET", path: "/api/v1/me", bearer: fresh}), http.StatusOK, "")
}

// The same when the disable arrives together with a group change.
func TestDisablingTogetherWithGroupChangeRevokesTokensToo(t *testing.T) {
	h := newHarness(t)
	victim := h.newUser("tokenvictim2")
	token, _ := h.newToken(victim.ID)
	group := h.createGroupVia("with-disable-"+uniqueEmail(t, "g"), false)
	h.expect(h.asAdmin("PATCH", "/api/v1/admin/users/"+itoa(victim.ID), map[string]any{"disabled": true, "group_ids": []int64{group}}), http.StatusOK, "")
	h.expect(h.asAdmin("PATCH", "/api/v1/admin/users/"+itoa(victim.ID), map[string]any{"disabled": false}), http.StatusOK, "")
	h.expect(h.send(sent{method: "GET", path: "/api/v1/me", bearer: token}), http.StatusUnauthorized, "unauthorized")
	// Enabling a user who was never disabled revokes nothing.
	other := h.newUser("untouched")
	otherToken, _ := h.newToken(other.ID)
	h.expect(h.asAdmin("PATCH", "/api/v1/admin/users/"+itoa(other.ID), map[string]any{"disabled": false}), http.StatusOK, "")
	h.expect(h.send(sent{method: "GET", path: "/api/v1/me", bearer: otherToken}), http.StatusOK, "")
}

func (h *harness) newServiceUser(label string) store.User {
	h.t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		h.t.Fatalf("HashPassword: %v", err)
	}
	u, err := store.CreateLocalUser(context.Background(), h.pool, uniqueEmail(h.t, label), label, "service", hash)
	if err != nil {
		h.t.Fatalf("CreateLocalUser: %v", err)
	}
	return u
}

// A service account has no browser, so an administrator mints its token.
func TestAdminMintsATokenForAServiceAccount(t *testing.T) {
	h := newHarness(t)
	svc := h.newServiceUser("bot")
	adminSession := h.newSession(h.admin.ID)
	path := "/api/v1/admin/users/" + itoa(svc.ID) + "/tokens"
	mint := func(sessionID string, id int64, body any) *httptest.ResponseRecorder {
		return h.send(sent{method: "POST", path: "/api/v1/admin/users/" + itoa(id) + "/tokens", sessionID: sessionID, origin: publicURL, body: body})
	}

	rec := h.send(sent{method: "POST", path: path, sessionID: adminSession, origin: publicURL, body: map[string]any{"name": "ci", "ttl_days": 30}})
	h.expect(rec, http.StatusCreated, "")
	created := decodeObject(t, rec)
	plaintext, _ := created["plaintext"].(string)
	if !strings.HasPrefix(plaintext, "ymd_") || created["name"] != "ci" || created["expires_at"] == nil || created["id"] == nil {
		t.Fatalf("response = %v, want the shape of POST /tokens", created)
	}
	if me := decodeObject(t, h.send(sent{method: "GET", path: "/api/v1/me", bearer: plaintext})); me["email"] != svc.Email {
		t.Errorf("the token belongs to %v, want the service account", me["email"])
	}
	var entry *store.AuditEntry
	for _, e := range h.auditEntries() {
		if e.Action == "token.create" && strings.Contains(string(e.Detail), `"ci"`) {
			entry = &e
			break
		}
	}
	if entry == nil || entry.Actor != h.admin.Email || entry.TargetID != itoa(int64(created["id"].(float64))) {
		t.Fatalf("audit entry = %+v, want token.create by the admin naming the token", entry)
	}
	if strings.Contains(string(entry.Detail), plaintext) || strings.Contains(string(entry.Detail), plaintext[4:]) {
		t.Error("the audit entry holds the token value")
	}

	// Only service accounts: a local user, a SAML user, an unknown one.
	samlUser, _ := store.UpsertSAMLUser(context.Background(), h.pool, uniqueEmail(t, "samlsvc"), "S", time.Now())
	for name, id := range map[string]int64{"local": h.newUser("localsvc").ID, "saml": samlUser.ID, "an administrator": h.admin.ID} {
		rec := mint(adminSession, id, map[string]any{"name": "x"})
		if rec.Code != http.StatusUnprocessableEntity || errorCodeOf(rec) != "invalid" {
			t.Errorf("%s user: %s, want 422 invalid", name, failure(rec))
		}
	}
	h.expect(mint(adminSession, svc.ID+999, map[string]any{"name": "x"}), http.StatusNotFound, "not_found")
	h.expect(mint(adminSession, svc.ID, map[string]any{"name": ""}), http.StatusBadRequest, "invalid")
	h.expect(mint(adminSession, svc.ID, map[string]any{"name": "x", "ttl_days": 9999}), http.StatusBadRequest, "invalid")

	// Admin only: manage_access is not enough, and neither is an admin's token
	// (a token that could mint tokens could renew admin power for ever).
	manager := h.newUser("mgr")
	h.grant(manager.ID, "manage_access")
	h.expect(mint(h.newSession(manager.ID), svc.ID, map[string]any{"name": "x"}), http.StatusForbidden, "forbidden")
	h.expect(h.send(sent{method: "POST", path: path, bearer: h.adminToken, body: map[string]any{"name": "x"}}), http.StatusForbidden, "forbidden")
	h.expect(h.send(sent{method: "POST", path: path, body: map[string]any{"name": "x"}}), http.StatusUnauthorized, "unauthorized")
	tokens, _ := store.ListTokens(context.Background(), h.pool, svc.ID)
	if len(tokens) != 1 {
		t.Errorf("the service account has %d tokens, want only the one minted", len(tokens))
	}
}

// A token minted for a disabled service account would start working the moment
// the account is enabled again, so the mint is refused.
func TestAdminCannotMintATokenForADisabledServiceAccount(t *testing.T) {
	h := newHarness(t)
	svc := h.newServiceUser("disabledbot")
	h.expect(h.asAdmin("PATCH", "/api/v1/admin/users/"+itoa(svc.ID), map[string]any{"disabled": true}), http.StatusOK, "")
	rec := h.send(sent{method: "POST", path: "/api/v1/admin/users/" + itoa(svc.ID) + "/tokens", sessionID: h.newSession(h.admin.ID), origin: publicURL, body: map[string]any{"name": "ci"}})
	if rec.Code != http.StatusUnprocessableEntity || errorCodeOf(rec) != "invalid" {
		t.Errorf("mint for a disabled account: %s, want 422 invalid", failure(rec))
	}
	tokens, _ := store.ListTokens(context.Background(), h.pool, svc.ID)
	if len(tokens) != 0 {
		t.Errorf("the disabled account has %d tokens, want none", len(tokens))
	}
}
