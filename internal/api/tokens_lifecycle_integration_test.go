//go:build integration

package api

import (
	"context"
	"net/http"
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
