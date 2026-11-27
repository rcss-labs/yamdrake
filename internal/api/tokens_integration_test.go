//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
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

	// A bearer token may list and create too ("signed in").
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
		h.expect(h.send(sent{method: "POST", path: "/api/v1/tokens", bearer: plaintext, body: body}), http.StatusBadRequest, "invalid")
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
	attackerToken, _ := h.newToken(attacker.ID)

	rec := h.send(sent{method: "DELETE", path: "/api/v1/tokens/" + itoa(victimToken.ID), bearer: attackerToken})
	h.expect(rec, http.StatusNotFound, "not_found")
	// Even an administrator manages only their own tokens here.
	h.expect(h.asAdmin("DELETE", "/api/v1/tokens/"+itoa(victimToken.ID), nil), http.StatusNotFound, "not_found")
	tokens, err := store.ListTokens(context.Background(), h.pool, victim.ID)
	if err != nil || len(tokens) != 1 || tokens[0].RevokedAt != nil {
		t.Fatalf("the victim's token was touched: %v, %v", tokens, err)
	}
	h.expect(h.send(sent{method: "DELETE", path: "/api/v1/tokens/abc", bearer: attackerToken}), http.StatusNotFound, "not_found")
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
	key := deviceKey(deviceHash(racing["device_code"].(string)))
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
	keys, err := h.valkey.Do(context.Background(), h.valkey.B().Keys().Pattern("device:*").Build()).AsStrSlice()
	if err != nil {
		t.Fatalf("list device keys: %v", err)
	}
	for _, k := range keys {
		if strings.Contains(k, deviceCode) {
			t.Fatalf("a Valkey key contains the device code itself")
		}
	}
	// TTL is about ten minutes on the code's keys.
	ttl := h.valkey.Do(context.Background(), h.valkey.B().Ttl().Key("device-user:"+userCode).Build())
	if secs, err := ttl.AsInt64(); err != nil || secs < 590 || secs > 600 {
		t.Errorf("TTL of the user code = %d, %v; want about 600 seconds", secs, err)
	}
	if err := h.valkey.Do(context.Background(), h.valkey.B().Del().Key("device-user:"+userCode).Build()).Error(); err != nil {
		t.Fatalf("expire user code: %v", err)
	}
	h.expect(h.confirmDevice(sessionID, userCode), http.StatusNotFound, "not_found")
	h.expect(h.confirmDevice(sessionID, "AAAAAAAA"), http.StatusNotFound, "not_found")
	h.expect(h.confirmDevice(sessionID, "ZZ"), http.StatusBadRequest, "invalid")
	h.expect(h.confirmDevice(sessionID, "0OIL1111"), http.StatusBadRequest, "invalid")

	// An unknown device code and an expired one answer alike.
	h.expect(h.pollDevice(strings.Repeat("A", 43)), http.StatusBadRequest, "expired_token")
	for _, key := range keys {
		if strings.HasPrefix(key, "device:") {
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
