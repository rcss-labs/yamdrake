//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/yamdrake/internal/store"
)

func TestTokenPlaintextReturnedOnceAndListNeverShowsIt(t *testing.T) {
	h := newHarness(t)
	user := h.newUser("tokenowner")
	sessionID := h.newSession(user.ID)

	// A session creates a token (the session path needs the Origin header).
	rec := h.send(sent{method: "POST", path: "/api/v1/tokens", sessionID: sessionID, origin: publicURL, body: map[string]any{"name": "laptop", "ttl_days": 30}})
	h.expect(rec, http.StatusCreated, "")
	created := decodeObject(t, rec)
	plaintext, _ := created["plaintext"].(string)
	if !strings.HasPrefix(plaintext, "ymd_") || len(plaintext) != 4+43 {
		t.Fatalf("the created token does not look like ymd_ plus 32 bytes of base64url")
	}
	if created["expires_at"] == nil || created["name"] != "laptop" {
		t.Errorf("created token = name %v, expires_at %v", created["name"], created["expires_at"])
	}
	// The plaintext works.
	h.expect(h.send(sent{method: "GET", path: "/api/v1/me", bearer: plaintext}), http.StatusOK, "")

	// A bearer token may list its owner's tokens (but not mint or revoke any).
	rec = h.send(sent{method: "GET", path: "/api/v1/tokens", bearer: plaintext})
	h.expect(rec, http.StatusOK, "")
	if strings.Contains(rec.Body.String(), plaintext) || strings.Contains(rec.Body.String(), plaintext[4:]) {
		t.Fatalf("the token list shows the plaintext")
	}
	body := rec.Body.String()
	if strings.Contains(strings.ToLower(body), "hash") {
		t.Errorf("the token list mentions a hash")
	}
	if tokens := decodeObject(t, rec)["tokens"].([]any); len(tokens) != 1 || tokens[0].(map[string]any)["name"] != "laptop" {
		t.Errorf("token list = %v, want the one token", tokens)
	}
	// Bad requests.
	for _, body := range []map[string]any{{"name": ""}, {"name": "x", "ttl_days": -1}, {"name": strings.Repeat("n", 101)}} {
		h.expect(h.send(sent{method: "POST", path: "/api/v1/tokens", sessionID: sessionID, origin: publicURL, body: body}), http.StatusBadRequest, "invalid")
	}
	// Revoking ends it.
	id := itoa(int64(created["id"].(float64)))
	h.expect(h.send(sent{method: "DELETE", path: "/api/v1/tokens/" + id, sessionID: sessionID, origin: publicURL}), http.StatusNoContent, "")
	h.expect(h.send(sent{method: "GET", path: "/api/v1/me", bearer: plaintext}), http.StatusUnauthorized, "unauthorized")
}

func TestUserCannotRevokeAnotherUsersToken(t *testing.T) {
	h := newHarness(t)
	victim := h.newUser("victim")
	_, victimToken := h.newToken(victim.ID)
	attacker := h.newUser("attacker")
	attackerSession := h.newSession(attacker.ID)
	adminSession := h.newSession(h.admin.ID)

	rec := h.send(sent{method: "DELETE", path: "/api/v1/tokens/" + itoa(victimToken.ID), sessionID: attackerSession, origin: publicURL})
	h.expect(rec, http.StatusNotFound, "not_found")
	// Even an administrator manages only their own tokens here.
	h.expect(h.send(sent{method: "DELETE", path: "/api/v1/tokens/" + itoa(victimToken.ID), sessionID: adminSession, origin: publicURL}), http.StatusNotFound, "not_found")
	tokens, err := store.ListTokens(context.Background(), h.pool, victim.ID)
	if err != nil || len(tokens) != 1 || tokens[0].RevokedAt != nil {
		t.Fatalf("the victim's token was touched: %v, %v", tokens, err)
	}
	h.expect(h.send(sent{method: "DELETE", path: "/api/v1/tokens/abc", sessionID: attackerSession, origin: publicURL}), http.StatusNotFound, "not_found")
}

// A token cannot mint, renew or revoke tokens: a stolen token (or one whose
// owner was removed from the identity provider) must not be able to outlive
// its own expiry or the SSO rule by creating a successor.
func TestTokenCannotManageTokens(t *testing.T) {
	h := newHarness(t)
	user := h.newUser("tokenmanager")
	bearer, own := h.newToken(user.ID)
	sessionID := h.newSession(user.ID)
	const message = "Sign in with your browser to manage tokens"

	rec := h.send(sent{method: "POST", path: "/api/v1/tokens", bearer: bearer, body: map[string]any{"name": "successor", "ttl_days": 365}})
	h.expect(rec, http.StatusForbidden, "forbidden")
	if got := errorMessage(decodeObject(t, rec)); got != message {
		t.Errorf("message = %q, want %q", got, message)
	}
	// A token may give up its own power, but not touch another token of its owner.
	_, sibling := h.newToken(user.ID)
	rec = h.send(sent{method: "DELETE", path: "/api/v1/tokens/" + itoa(sibling.ID), bearer: bearer})
	h.expect(rec, http.StatusForbidden, "forbidden")
	// The bypass to try: a token request that also carries a valid session
	// cookie and origin. The bearer header decides who the caller is, so it is
	// still a token.
	both := sent{method: "POST", path: "/api/v1/tokens", bearer: bearer, sessionID: sessionID, origin: publicURL, body: map[string]any{"name": "sneaky"}}
	h.expect(h.send(both), http.StatusForbidden, "forbidden")
	both = sent{method: "DELETE", path: "/api/v1/tokens/" + itoa(sibling.ID), bearer: bearer, sessionID: sessionID, origin: publicURL}
	h.expect(h.send(both), http.StatusForbidden, "forbidden")
	// Confirming a CLI sign-in is refused to a token alike.
	h.expect(h.send(sent{method: "POST", path: "/api/v1/cli/login/confirm", bearer: bearer, sessionID: sessionID, origin: publicURL,
		body: map[string]any{"user_code": "ABCDEFGH"}}), http.StatusForbidden, "forbidden")

	tokens, err := store.ListTokens(context.Background(), h.pool, user.ID)
	if err != nil || len(tokens) != 2 || tokens[0].RevokedAt != nil || tokens[1].RevokedAt != nil {
		t.Fatalf("tokens after the refused requests = %v, %v; want the two originals, not revoked", tokens, err)
	}
	// A session still can.
	h.expect(h.send(sent{method: "POST", path: "/api/v1/tokens", sessionID: sessionID, origin: publicURL, body: map[string]any{"name": "from the browser"}}), http.StatusCreated, "")
	h.expect(h.send(sent{method: "DELETE", path: "/api/v1/tokens/" + itoa(sibling.ID), sessionID: sessionID, origin: publicURL}), http.StatusNoContent, "")
	_ = own
}

// A token can give its own power up: DELETE of itself, or logout.
func TestTokenCanRevokeItselfAndNothingElse(t *testing.T) {
	h := newHarness(t)
	user := h.newUser("selfrevoke")
	first, firstTok := h.newToken(user.ID)
	second, secondTok := h.newToken(user.ID)
	stranger := h.newUser("stranger")
	_, strangerTok := h.newToken(stranger.ID)
	me := func(token string) int { return h.send(sent{method: "GET", path: "/api/v1/me", bearer: token}).Code }

	// Not another token of the same user, not another user's, not an unknown id: all 403.
	for name, id := range map[string]string{"sibling": itoa(secondTok.ID), "another user's": itoa(strangerTok.ID), "unknown": "999999"} {
		if code := h.send(sent{method: "DELETE", path: "/api/v1/tokens/" + id, bearer: first}).Code; code != http.StatusForbidden {
			t.Errorf("a token revoking a %s token: %d, want 403", name, code)
		}
	}
	if me(first) != http.StatusOK || me(second) != http.StatusOK {
		t.Fatal("a refused revocation changed a token")
	}
	h.expect(h.send(sent{method: "DELETE", path: "/api/v1/tokens/" + itoa(firstTok.ID), bearer: first}), http.StatusNoContent, "")
	if me(first) != http.StatusUnauthorized || me(second) != http.StatusOK {
		t.Errorf("after DELETE of itself: first %d (want 401), second %d (want 200)", me(first), me(second))
	}

	// Logout with a token revokes that token and only that token, whatever else the request carries.
	third, _ := h.newToken(user.ID)
	sessionID := h.newSession(user.ID)
	rec := h.send(sent{method: "POST", path: "/api/v1/auth/logout", bearer: third, sessionID: sessionID, origin: publicURL})
	h.expect(rec, http.StatusNoContent, "")
	if me(third) != http.StatusUnauthorized {
		t.Error("the token that logged out still works")
	}
	if me(second) != http.StatusOK {
		t.Error("logging out with one token revoked another")
	}
	if h.send(sent{method: "GET", path: "/api/v1/me", sessionID: sessionID}).Code != http.StatusOK {
		t.Error("logging out with a token ended the browser session")
	}
	var audited bool
	for _, e := range h.auditEntries() {
		audited = audited || e.Action == "token.revoke"
	}
	if !audited {
		t.Errorf("revoking a token by logout is not audited: %v", h.auditActions())
	}
}

// deviceHarness is a harness whose poll interval is short enough to wait out.
func newDeviceHarness(t *testing.T) *harness {
	h := newHarness(t)
	h.rebuild(func(d *Deps) { d.DevicePollInterval = 300 * time.Millisecond })
	return h
}

func (h *harness) startDeviceLogin() map[string]any {
	h.t.Helper()
	rec := h.send(sent{method: "POST", path: "/api/v1/cli/login"})
	h.expect(rec, http.StatusOK, "")
	return decodeObject(h.t, rec)
}

func (h *harness) pollDevice(deviceCode string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.send(sent{method: "POST", path: "/api/v1/cli/login/token", body: map[string]any{"device_code": deviceCode}})
}

func (h *harness) confirmDevice(sessionID, userCode string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.send(sent{method: "POST", path: "/api/v1/cli/login/confirm", sessionID: sessionID, origin: publicURL, body: map[string]any{"user_code": userCode}})
}

var userCodePattern = regexp.MustCompile(`^[ABCDEFGHJKMNPQRSTUVWXYZ23456789]{8}$`)

func TestDeviceLoginVerifyURLOpensConfirmPage(t *testing.T) {
	h := newDeviceHarness(t)
	start := h.startDeviceLogin()
	want := publicURL + "/cli/confirm?code=" + url.QueryEscape(start["user_code"].(string))
	if got, _ := start["verify_url"].(string); got != want {
		t.Errorf("verify_url = %q, want %q (the page the web application serves)", got, want)
	}
}

func TestDeviceLoginHappyPath(t *testing.T) {
	h := newDeviceHarness(t)
	user := h.newUser("deviceuser")
	sessionID := h.newSession(user.ID)

	start := h.startDeviceLogin()
	deviceCode, userCode := start["device_code"].(string), start["user_code"].(string)
	if len(deviceCode) != 43 {
		t.Errorf("device_code length = %d, want 43 (32 bytes, base64url)", len(deviceCode))
	}
	if !userCodePattern.MatchString(userCode) {
		t.Errorf("user_code is not 8 characters of the unambiguous alphabet")
	}
	if verify, _ := start["verify_url"].(string); !strings.HasPrefix(verify, publicURL+"/") {
		t.Errorf("verify_url = %v, want it under %s", start["verify_url"], publicURL)
	}
	if start["interval"] == nil {
		t.Errorf("no interval in the start response")
	}

	pending := h.pollDevice(deviceCode)
	h.expect(pending, http.StatusOK, "")
	if got := decodeObject(t, pending)["status"]; got != "pending" {
		t.Fatalf("poll before confirmation: status %v, want pending", got)
	}
	// A bearer token cannot confirm; a session of someone can, in lower case too.
	bearer, _ := h.newToken(user.ID)
	h.expect(h.send(sent{method: "POST", path: "/api/v1/cli/login/confirm", bearer: bearer, body: map[string]any{"user_code": userCode}}), http.StatusForbidden, "forbidden")
	h.expect(h.confirmDevice(sessionID, strings.ToLower(userCode)), http.StatusNoContent, "")
	time.Sleep(350 * time.Millisecond)

	done := h.pollDevice(deviceCode)
	h.expect(done, http.StatusOK, "")
	token, _ := decodeObject(t, done)["token"].(string)
	if !strings.HasPrefix(token, "ymd_") {
		t.Fatalf("the confirmed poll did not return a token")
	}
	// The token belongs to the user who confirmed.
	rec := h.send(sent{method: "GET", path: "/api/v1/me", bearer: token})
	h.expect(rec, http.StatusOK, "")
	if decodeObject(t, rec)["email"] != user.Email {
		t.Errorf("the token belongs to someone else than the confirming user")
	}
	// The audit trail names the user and no secret.
	for _, e := range h.auditEntries() {
		if strings.Contains(string(e.Detail), deviceCode) || strings.Contains(string(e.Detail), token) || strings.Contains(string(e.Detail), userCode) {
			t.Fatalf("audit entry %s holds a device or token secret", e.Action)
		}
	}
	actions := strings.Join(h.auditActions(), ",")
	if !strings.Contains(actions, "cli_login.confirm") || !strings.Contains(actions, "token.create") {
		t.Errorf("audit actions %s lack cli_login.confirm or token.create", actions)
	}
}

func TestDeviceCodeYieldsTokenOnce(t *testing.T) {
	h := newDeviceHarness(t)
	user := h.newUser("once")
	sessionID := h.newSession(user.ID)

	// The exactly-once step itself: many readers of one confirmed device state
	// at the same time (the poll limiter is not in the way here). Exactly one
	// may see "confirmed:...".
	racing := h.startDeviceLogin()
	h.expect(h.confirmDevice(sessionID, racing["user_code"].(string)), http.StatusNoContent, "")
	key := (&server{Deps: h.deps}).deviceKey(deviceHash(racing["device_code"].(string)))
	const readers = 16
	results := make(chan bool, readers)
	for range readers {
		go func() {
			state, err := readDeviceState(context.Background(), h.valkey, key)
			results <- err == nil && strings.HasPrefix(state, confirmedPrefix)
		}()
	}
	sawConfirmed := 0
	for range readers {
		if <-results {
			sawConfirmed++
		}
	}
	if sawConfirmed != 1 {
		t.Fatalf("%d of %d concurrent readers saw the confirmed state, want exactly 1", sawConfirmed, readers)
	}

	// Through the API: the code is confirmed once, gives one token, then is gone.
	start := h.startDeviceLogin()
	deviceCode, userCode := start["device_code"].(string), start["user_code"].(string)
	h.expect(h.confirmDevice(sessionID, userCode), http.StatusNoContent, "")
	h.expect(h.confirmDevice(sessionID, userCode), http.StatusNotFound, "not_found")
	done := h.pollDevice(deviceCode)
	h.expect(done, http.StatusOK, "")
	if token, _ := decodeObject(t, done)["token"].(string); !strings.HasPrefix(token, "ymd_") {
		t.Fatalf("the confirmed poll did not return a token")
	}
	time.Sleep(350 * time.Millisecond)
	h.expect(h.pollDevice(deviceCode), http.StatusBadRequest, "expired_token")
	tokens, err := store.ListTokens(context.Background(), h.pool, user.ID)
	if err != nil || len(tokens) != 1 {
		t.Fatalf("tokens of the user = %d, %v; want exactly 1", len(tokens), err)
	}
}

func TestDeviceLoginExpires(t *testing.T) {
	h := newDeviceHarness(t)
	user := h.newUser("expire")
	sessionID := h.newSession(user.ID)
	start := h.startDeviceLogin()
	deviceCode, userCode := start["device_code"].(string), start["user_code"].(string)

	// Remove the state the way its 10 minute expiry does.
	keys, err := h.valkey.Do(context.Background(), h.valkey.B().Keys().Pattern(h.deps.KeyPrefix+"device:*").Build()).AsStrSlice()
	if err != nil {
		t.Fatalf("list device keys: %v", err)
	}
	for _, k := range keys {
		if strings.Contains(k, deviceCode) {
			t.Fatalf("a Valkey key contains the device code itself")
		}
	}
	// TTL is about ten minutes on the code's keys.
	ttl := h.valkey.Do(context.Background(), h.valkey.B().Ttl().Key(h.deps.KeyPrefix+"device-user:"+userCode).Build())
	if secs, err := ttl.AsInt64(); err != nil || secs < 590 || secs > 600 {
		t.Errorf("TTL of the user code = %d, %v; want about 600 seconds", secs, err)
	}
	if err := h.valkey.Do(context.Background(), h.valkey.B().Del().Key(h.deps.KeyPrefix+"device-user:"+userCode).Build()).Error(); err != nil {
		t.Fatalf("expire user code: %v", err)
	}
	h.expect(h.confirmDevice(sessionID, userCode), http.StatusNotFound, "not_found")
	h.expect(h.confirmDevice(sessionID, "AAAAAAAA"), http.StatusNotFound, "not_found")
	h.expect(h.confirmDevice(sessionID, "ZZ"), http.StatusBadRequest, "invalid")
	h.expect(h.confirmDevice(sessionID, "0OIL1111"), http.StatusBadRequest, "invalid")

	// An unknown device code and an expired one answer alike.
	h.expect(h.pollDevice(strings.Repeat("A", 43)), http.StatusBadRequest, "expired_token")
	for _, key := range keys {
		if strings.HasPrefix(key, h.deps.KeyPrefix+"device:") {
			_ = h.valkey.Do(context.Background(), h.valkey.B().Del().Key(key).Build()).Error()
		}
	}
	time.Sleep(350 * time.Millisecond)
	h.expect(h.pollDevice(deviceCode), http.StatusBadRequest, "expired_token")
	h.expect(h.send(sent{method: "POST", path: "/api/v1/cli/login/token", body: map[string]any{"device_code": "short"}}), http.StatusBadRequest, "invalid")
}

func TestDevicePollTooFastIs429(t *testing.T) {
	h := newDeviceHarness(t)
	start := h.startDeviceLogin()
	deviceCode := start["device_code"].(string)
	h.expect(h.pollDevice(deviceCode), http.StatusOK, "")
	rec := h.pollDevice(deviceCode)
	h.expect(rec, http.StatusTooManyRequests, "too_many_requests")
	if rec.Header().Get("Retry-After") == "" {
		t.Errorf("a 429 without Retry-After")
	}
	// The limit is per device code: another device is not slowed down.
	other := h.startDeviceLogin()["device_code"].(string)
	h.expect(h.pollDevice(other), http.StatusOK, "")
	time.Sleep(350 * time.Millisecond)
	h.expect(h.pollDevice(deviceCode), http.StatusOK, "")
}

// The user code is 8 characters of a 31-letter alphabet: guessing is possible
// only without a limit, so a session gets 10 confirm attempts per 10 minutes.
func TestDeviceConfirmAttemptsAreLimitedPerSession(t *testing.T) {
	h := newDeviceHarness(t)
	user := h.newUser("guesser")
	sessionID := h.newSession(user.ID)
	start := h.startDeviceLogin()
	userCode, deviceCode := start["user_code"].(string), start["device_code"].(string)

	// Wrong guesses, and malformed ones (which an attacker could use to probe
	// for free), all count.
	for i := range 10 {
		code := "AAAAAAAA"
		if i%2 == 1 {
			code = "not a code"
		}
		rec := h.confirmDevice(sessionID, code)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d: %s, want 404 or 400", i+1, failure(rec))
		}
	}
	// The 11th is refused, even with the right code, and the code stays unused.
	rec := h.confirmDevice(sessionID, userCode)
	h.expect(rec, http.StatusTooManyRequests, "too_many_requests")
	if rec.Header().Get("Retry-After") == "" {
		t.Errorf("a 429 without Retry-After")
	}
	// Spelling the code differently does not get a fresh allowance.
	h.expect(h.confirmDevice(sessionID, strings.ToLower(userCode[:4]+"-"+userCode[4:])), http.StatusTooManyRequests, "too_many_requests")
	// Another session has its own allowance, and the code was never consumed.
	other := h.newSession(user.ID)
	h.expect(h.confirmDevice(other, userCode), http.StatusNoContent, "")
	time.Sleep(350 * time.Millisecond)
	h.expect(h.pollDevice(deviceCode), http.StatusOK, "")

	// The counter lives about ten minutes and its key uses the prefix.
	keys, err := h.valkey.Do(context.Background(), h.valkey.B().Keys().Pattern(h.deps.KeyPrefix+"device-confirm:*").Build()).AsStrSlice()
	if err != nil || len(keys) != 2 {
		t.Fatalf("confirm counters = %v, %v; want one per session", keys, err)
	}
	for _, key := range keys {
		if strings.Contains(key, sessionID) {
			t.Errorf("a Valkey key contains the session id")
		}
		if secs, err := h.valkey.Do(context.Background(), h.valkey.B().Ttl().Key(key).Build()).AsInt64(); err != nil || secs < 590 || secs > 600 {
			t.Errorf("TTL of %s = %d, %v; want about 600", key, secs, err)
		}
	}
}

// A device sign-in confirmed before the user was disabled must not yield a
// token after the user is enabled again.
func TestDisablingDropsDeviceSignInsConfirmedByTheUser(t *testing.T) {
	h := newDeviceHarness(t)
	user := h.newUser("devicedisabled")
	bystander := h.newUser("devicebystander")
	userSession, bystanderSession := h.newSession(user.ID), h.newSession(bystander.ID)

	confirmed := h.startDeviceLogin()
	h.expect(h.confirmDevice(userSession, confirmed["user_code"].(string)), http.StatusNoContent, "")
	pending := h.startDeviceLogin() // nobody confirmed it yet
	others := h.startDeviceLogin()  // confirmed by someone else
	h.expect(h.confirmDevice(bystanderSession, others["user_code"].(string)), http.StatusNoContent, "")

	path := "/api/v1/admin/users/" + itoa(user.ID)
	h.expect(h.asAdmin("PATCH", path, map[string]any{"disabled": true}), http.StatusOK, "")
	h.expect(h.asAdmin("PATCH", path, map[string]any{"disabled": false}), http.StatusOK, "")
	time.Sleep(350 * time.Millisecond)

	h.expect(h.pollDevice(confirmed["device_code"].(string)), http.StatusBadRequest, "expired_token")
	// Other people's sign-ins are not touched.
	rec := h.pollDevice(others["device_code"].(string))
	h.expect(rec, http.StatusOK, "")
	if token, _ := decodeObject(t, rec)["token"].(string); !strings.HasPrefix(token, "ymd_") {
		t.Error("another user's confirmed sign-in was dropped")
	}
	// A sign-in nobody has confirmed is not attributed to anyone and stays pending.
	rec = h.pollDevice(pending["device_code"].(string))
	h.expect(rec, http.StatusOK, "")
	if decodeObject(t, rec)["status"] != "pending" {
		t.Error("a pending sign-in was dropped")
	}
}
