//go:build integration

package api

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/yamdrake/internal/secrets"
	"github.com/rcss-labs/yamdrake/internal/store"
)

// lockedBuffer is a log destination that several requests may write at once.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// logTo makes the harness's API log into the returned buffer.
func (h *harness) logTo() *lockedBuffer {
	buf := &lockedBuffer{}
	h.rebuild(func(d *Deps) {
		d.Log = slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	return buf
}

func rawACS(h *harness, contentType, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/saml/acs", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Origin", "https://idp.test")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// The audit log cannot be pruned, so what anyone on the internet can make us
// write to it must be bounded: a response that is not tied to a sign-in this
// server started is logged, not audited.
func TestAnonymousACSPostsAreLoggedNotAudited(t *testing.T) {
	h, idp, _ := ssoSetup(t)
	logs := h.logTo()
	before := h.auditCount("saml.rejected")

	flow := idp.begin() // a real response and relay cookie to damage in different ways
	forged := *flow.relay
	forged.Value = forged.Value[:len(forged.Value)-2] + "AA"
	otherServer := h.rebuildWithOtherRelayKey(t, idp)
	attempts := map[string]*httptest.ResponseRecorder{
		"no cookie, empty body":     rawACS(h, "application/x-www-form-urlencoded", "", nil),
		"no cookie, junk":           rawACS(h, "text/plain", "\x00\xff junk", nil),
		"unparseable form":          rawACS(h, "application/x-www-form-urlencoded", "a=%zz&SAMLResponse=%", nil),
		"binding the server skips":  rawACS(h, "application/x-www-form-urlencoded", "SAMLart=abc", nil),
		"valid response, no cookie": h.postACS(flow, false),
		"forged cookie":             h.postACS(samlFlow{form: flow.form, relay: &forged}, true),
		"cookie of another server":  otherServer,
	}
	for name, rec := range attempts {
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %s, want 400", name, failure(rec))
		}
	}
	// Thousands of them write nothing either.
	for i := range 100 {
		rawACS(h, "application/x-www-form-urlencoded", fmt.Sprintf("SAMLResponse=%d&RelayState=x", i), nil)
	}
	if got := h.auditCount("saml.rejected"); got != before {
		t.Fatalf("anonymous posts wrote %d audit entries, want none", got-before)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "saml response rejected") {
		t.Errorf("the rejections are not in the log at warn level:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "SAMLResponse") {
		t.Errorf("the log holds response material")
	}

	// A rejection after the relay state validated is still audited.
	h.postACS(idp.begin().unsigned(t), true)
	if got := h.auditCount("saml.rejected"); got != before+1 {
		t.Fatalf("a rejected response of a real sign-in wrote %d entries, want 1", got-before)
	}
}

// rebuildWithOtherRelayKey posts a response with a relay cookie that another
// installation (another relay key) sealed.
func (h *harness) rebuildWithOtherRelayKey(t *testing.T, idp *testIDP) *httptest.ResponseRecorder {
	t.Helper()
	nonce := "n-other"
	cookie := &http.Cookie{Name: relayCookieName, Value: sealRelayState([]byte("another key another key another!"), nonce, "id-1", "/", time.Now().Add(time.Minute))}
	flow := idp.begin()
	flow.form.Set("RelayState", nonce)
	return h.postACS(samlFlow{form: flow.form, relay: cookie}, true)
}

// A failed sign-in is audited only for an account that exists: the failures of
// made-up addresses are free for anyone to produce.
func TestFailedLoginIsAuditedOnlyForExistingUsers(t *testing.T) {
	h := newHarness(t)
	logs := h.logTo()
	for range 20 {
		h.expect(h.login(uniqueEmail(t, "nobody"), "not the password at all"), http.StatusUnauthorized, "unauthorized")
	}
	if n := h.auditCount("auth.login_failed"); n != 0 {
		t.Fatalf("20 sign-ins with unknown emails wrote %d audit entries, want 0", n)
	}
	if !strings.Contains(logs.String(), "failed sign-in") {
		t.Errorf("failed sign-ins of unknown emails are not logged:\n%s", logs.String())
	}

	user := h.newUser("exists")
	h.expect(h.login(user.Email, "not the password at all"), http.StatusUnauthorized, "unauthorized")
	if n := h.auditCount("auth.login_failed"); n != 1 {
		t.Fatalf("a failed sign-in of an existing user wrote %d entries, want 1", n)
	}
	// The bypass to try: spell the address differently so it looks unknown.
	h.expect(h.login("  "+strings.ToUpper(user.Email)+" ", "not the password at all"), http.StatusUnauthorized, "unauthorized")
	if n := h.auditCount("auth.login_failed"); n != 2 {
		t.Fatalf("a differently-cased address of an existing user was not audited (%d entries, want 2)", n)
	}
	// A SAML account that someone tries to sign in to with a password exists too.
	samlUser, err := store.UpsertSAMLUser(context.Background(), h.pool, uniqueEmail(t, "samlexists"), "S", time.Now())
	if err != nil {
		t.Fatalf("UpsertSAMLUser: %v", err)
	}
	h.expect(h.login(samlUser.Email, "not the password at all"), http.StatusUnauthorized, "unauthorized")
	if n := h.auditCount("auth.login_failed"); n != 3 {
		t.Fatalf("a failed password sign-in of a SAML user was not audited (%d entries, want 3)", n)
	}
	// Known and unknown answer alike.
	known := h.login(user.Email, "not the password at all")
	unknown := h.login(uniqueEmail(t, "nobody"), "not the password at all")
	if known.Code != unknown.Code || known.Body.String() != unknown.Body.String() {
		t.Errorf("known and unknown emails answer differently: %s / %s", failure(known), failure(unknown))
	}
}

// countingCipher counts decryptions.
type countingCipher struct {
	secrets.Cipher
	decrypts atomic.Int64
}

func (c *countingCipher) Decrypt(ctx context.Context, purpose string, ciphertext []byte) ([]byte, error) {
	c.decrypts.Add(1)
	return c.Cipher.Decrypt(ctx, purpose, ciphertext)
}

// Anonymous requests must not each cost a decryption: the key pair and the
// relay key are decrypted once and kept in memory for five minutes.
func TestSAMLKeysAreDecryptedOnceAndCachedForFiveMinutes(t *testing.T) {
	h, idp, _ := ssoSetup(t)
	cipher := &countingCipher{Cipher: h.cipher}
	var now atomic.Int64
	start := time.Now()
	now.Store(start.UnixNano())
	h.rebuild(func(d *Deps) {
		d.Cipher = cipher
		d.Now = func() time.Time { return time.Unix(0, now.Load()) }
	})
	post := func() { rawACS(h, "application/x-www-form-urlencoded", "SAMLResponse=x&RelayState=y", nil) }

	for range 25 {
		post()
	}
	first := cipher.decrypts.Load()
	if first > 2 {
		t.Fatalf("25 posts caused %d decryptions, want at most 2 (key pair, relay key)", first)
	}
	// A start request needs both keys and shares the cache.
	for range 5 {
		idp.begin()
	}
	if got := cipher.decrypts.Load(); got > 2 {
		t.Fatalf("25 posts and 5 starts caused %d decryptions, want at most 2 in all", got)
	}
	now.Store(start.Add(4*time.Minute + 50*time.Second).UnixNano())
	for range 5 {
		post()
	}
	if got := cipher.decrypts.Load(); got > 2 {
		t.Fatalf("within five minutes the keys were decrypted again (%d)", got)
	}
	now.Store(start.Add(5*time.Minute + time.Second).UnixNano())
	post()
	idp.begin()
	if got := cipher.decrypts.Load(); got < 3 || got > 4 {
		t.Fatalf("after five minutes there were %d decryptions in all, want the keys decrypted afresh (3 or 4)", got)
	}
}
