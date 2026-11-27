//go:build integration

package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/yamdrake/internal/auth"
	"github.com/rcss-labs/yamdrake/internal/session"
	"github.com/rcss-labs/yamdrake/internal/store"
)

func (h *harness) login(email, password string, extra ...func(*sent)) *httptest.ResponseRecorder {
	h.t.Helper()
	r := sent{method: "POST", path: "/api/v1/auth/login", origin: publicURL,
		body: map[string]string{"email": email, "password": password}}
	for _, change := range extra {
		change(&r)
	}
	return h.send(r)
}

// sessionCookie returns the session cookie set by rec, if any.
func sessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == session.CookieName {
			return c
		}
	}
	return nil
}

func TestLoginSuccessSetsCookieAndAudits(t *testing.T) {
	h := newHarness(t)
	user := h.newUser("login")
	rec := h.login(strings.ToUpper(user.Email), testPassword) // email case does not matter
	h.expect(rec, http.StatusOK, "")

	cookie := sessionCookie(rec)
	if cookie == nil || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.Value == "" {
		t.Fatalf("session cookie = %+v, want Secure HttpOnly SameSite=Lax at /", cookie)
	}
	s, err := h.sessions.Get(context.Background(), cookie.Value)
	if err != nil || s.UserID != user.ID {
		t.Fatalf("session from the cookie = %+v, %v; want one of the user", s, err)
	}
	if !strings.Contains(rec.Body.String(), user.Email) || strings.Contains(rec.Body.String(), "argon2") ||
		strings.Contains(rec.Body.String(), cookie.Value) {
		t.Errorf("response body must hold the principal and no hash or session id")
	}
	// The new session works.
	h.expect(h.send(sent{method: "GET", path: "/api/v1/me", sessionID: cookie.Value}), http.StatusOK, "")

	var found bool
	for _, e := range h.auditEntries() {
		if e.Action == "auth.login" && e.Actor == user.Email && e.ActorID != nil && *e.ActorID == user.ID {
			found = true
		}
		if strings.Contains(string(e.Detail), testPassword) || strings.Contains(string(e.Detail), cookie.Value) {
			t.Errorf("audit entry %s holds a secret", e.Action)
		}
	}
	if !found {
		t.Fatalf("no auth.login audit entry for the user: %v", h.auditActions())
	}
}

func TestLoginAlwaysStartsAFreshSession(t *testing.T) {
	h := newHarness(t)
	user := h.newUser("fixation")
	old := h.newSession(user.ID)
	withCookie := func(id string) func(*sent) { return func(r *sent) { r.sessionID = id } }

	rec := h.login(user.Email, testPassword, withCookie(old))
	h.expect(rec, http.StatusOK, "")
	cookie := sessionCookie(rec)
	if cookie == nil || cookie.Value == old {
		t.Fatalf("sign-in reused the session id sent by the client")
	}
	if _, err := h.sessions.Get(context.Background(), old); !errors.Is(err, session.ErrNoSession) {
		t.Errorf("old session after sign-in: Get = %v, want ErrNoSession", err)
	}

	// An id the client made up is never adopted either.
	planted := validLookingSessionID()
	rec = h.login(user.Email, testPassword, withCookie(planted))
	h.expect(rec, http.StatusOK, "")
	if cookie := sessionCookie(rec); cookie == nil || cookie.Value == planted {
		t.Fatalf("sign-in adopted a client-chosen session id")
	}
}

func TestLoginFromOtherOriginIsForbidden(t *testing.T) {
	h := newHarness(t)
	user := h.newUser("loginorigin")
	for _, origin := range []string{"https://evil.test", ""} {
		rec := h.login(user.Email, testPassword, func(r *sent) { r.origin = origin })
		h.expect(rec, http.StatusForbidden, "forbidden")
		if sessionCookie(rec) != nil {
			t.Errorf("a refused sign-in set a cookie")
		}
	}
}

func TestLoginWrongPasswordAndUnknownEmailLookTheSame(t *testing.T) {
	h := newHarness(t)
	known := h.newUser("known")
	samlUser, err := store.UpsertSAMLUser(context.Background(), h.pool, uniqueEmail(t, "saml"), "Sam", time.Now())
	if err != nil {
		t.Fatalf("UpsertSAMLUser: %v", err)
	}
	noPassword, err := store.CreateLocalUser(context.Background(), h.pool, uniqueEmail(t, "nopw"), "No Pw", "local", "")
	if err != nil {
		t.Fatalf("CreateLocalUser: %v", err)
	}

	const tries = 5
	var wrongTotal, unknownTotal time.Duration
	var wantStatus int
	var wantBody string
	for i := range tries {
		start := time.Now()
		wrong := h.login(known.Email, "not the password at all")
		wrongTotal += time.Since(start)
		start = time.Now()
		unknown := h.login(uniqueEmail(t, "nobody"), "not the password at all")
		unknownTotal += time.Since(start)
		if wrong.Code != http.StatusUnauthorized || unknown.Code != http.StatusUnauthorized {
			t.Fatalf("try %d: statuses %d and %d, want 401 for both", i, wrong.Code, unknown.Code)
		}
		if wrong.Body.String() != unknown.Body.String() {
			t.Fatalf("try %d: a wrong password and an unknown email answer differently", i)
		}
		wantStatus, wantBody = wrong.Code, wrong.Body.String()
	}
	// SAML users and users without a password answer exactly the same.
	for _, email := range []string{samlUser.Email, noPassword.Email} {
		rec := h.login(email, testPassword)
		if rec.Code != wantStatus || rec.Body.String() != wantBody {
			t.Errorf("%s: %s, want the same answer as a wrong password", email, failure(rec))
		}
	}
	hi, lo := wrongTotal, unknownTotal
	if lo > hi {
		hi, lo = lo, hi
	}
	if float64(hi-lo) > 0.5*float64(hi) {
		t.Errorf("sign-in takes %v for a wrong password and %v for an unknown email (%d tries each); they must be within 50%%", wrongTotal, unknownTotal, tries)
	}
}

func TestLoginLocksAfterFiveFailures(t *testing.T) {
	h := newHarness(t)
	user := h.newUser("lock")
	for i := range 5 {
		h.expect(h.login(user.Email, "wrong password "+strconv.Itoa(i)), http.StatusUnauthorized, "unauthorized")
	}
	rec := h.login(user.Email, "one more wrong password")
	h.expect(rec, http.StatusTooManyRequests, "")
	retry, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || retry < 1 || retry > int(testLockoutWindow/time.Second) {
		t.Fatalf("Retry-After = %q, want at most the window", rec.Header().Get("Retry-After"))
	}
	// The right password does not get through while locked, in any letter case.
	locked := h.login(strings.ToUpper(user.Email), testPassword)
	h.expect(locked, http.StatusTooManyRequests, "")
	if sessionCookie(locked) != nil {
		t.Error("a locked sign-in set a cookie")
	}
	// Other accounts are not affected.
	other := h.newUser("unlocked")
	h.expect(h.login(other.Email, testPassword), http.StatusOK, "")

	time.Sleep(testLockoutWindow + 300*time.Millisecond)
	h.expect(h.login(user.Email, testPassword), http.StatusOK, "")
}

func TestLoginLockoutAlsoCoversUnknownEmails(t *testing.T) {
	h := newHarness(t)
	email := uniqueEmail(t, "ghost")
	for range 5 {
		h.expect(h.login(email, "whatever password"), http.StatusUnauthorized, "unauthorized")
	}
	h.expect(h.login(email, "whatever password"), http.StatusTooManyRequests, "")

	// The counter key is a hash: the address itself is not in Valkey.
	local, _, _ := strings.Cut(email, "@")
	keys, err := h.valkey.Do(context.Background(), h.valkey.B().Keys().Pattern("*"+local+"*").Build()).AsStrSlice()
	if err != nil || len(keys) != 0 {
		t.Errorf("Valkey keys containing the email = %v (error %v), want none", keys, err)
	}
}

func TestSuccessfulLoginResetsTheFailureCount(t *testing.T) {
	h := newHarness(t)
	user := h.newUser("reset")
	for range 4 {
		h.expect(h.login(user.Email, "wrong password"), http.StatusUnauthorized, "unauthorized")
	}
	h.expect(h.login(user.Email, testPassword), http.StatusOK, "")
	for range 4 {
		h.expect(h.login(user.Email, "wrong password"), http.StatusUnauthorized, "unauthorized")
	}
	h.expect(h.login(user.Email, testPassword), http.StatusOK, "")
}

func TestLoginWhenValkeyIsDownIs503(t *testing.T) {
	h := newHarness(t)
	shortenValkeyTimeout(t)
	user := h.newUser("valkeydown")
	dead := openValkey(t, "127.0.0.1:1")

	// Counters and sessions both down.
	h.rebuild(func(d *Deps) { d.Sessions = session.NewManager(dead, time.Now); d.Valkey = dead })
	rec := h.login(user.Email, testPassword)
	h.expect(rec, http.StatusServiceUnavailable, "unavailable")
	if sessionCookie(rec) != nil {
		t.Error("a 503 sign-in set a cookie")
	}

	// Only the session store down: the password is right but no session can be made.
	h.rebuild(func(d *Deps) { d.Sessions = session.NewManager(dead, time.Now) })
	rec = h.login(user.Email, testPassword)
	h.expect(rec, http.StatusServiceUnavailable, "unavailable")
	if sessionCookie(rec) != nil {
		t.Error("a 503 sign-in set a cookie")
	}
	if !strings.Contains(rec.Body.String(), "Sign-in is temporarily unavailable") {
		t.Errorf("message of the 503 = %s", failure(rec))
	}
}

func TestSAMLUserCannotPasswordLogin(t *testing.T) {
	h := newHarness(t)
	saml, err := store.UpsertSAMLUser(context.Background(), h.pool, uniqueEmail(t, "samlonly"), "Sam", time.Now())
	if err != nil {
		t.Fatalf("UpsertSAMLUser: %v", err)
	}
	rec := h.login(saml.Email, testPassword)
	h.expect(rec, http.StatusUnauthorized, "unauthorized")
	if sessionCookie(rec) != nil {
		t.Error("a SAML user got a cookie from password sign-in")
	}
}

func TestDisabledUserCannotLogin(t *testing.T) {
	h := newHarness(t)
	user := h.newUser("offboarded")
	if err := store.SetDisabled(context.Background(), h.pool, user.ID, true); err != nil {
		t.Fatalf("SetDisabled: %v", err)
	}
	rec := h.login(user.Email, testPassword)
	h.expect(rec, http.StatusUnauthorized, "unauthorized")
	if sessionCookie(rec) != nil {
		t.Error("a disabled user got a cookie")
	}
}

func TestFailedLoginIsAuditedWithEmailNotPassword(t *testing.T) {
	h := newHarness(t)
	email := h.newUser("audited").Email // only existing accounts are audited
	const attempt = "the-password-that-was-tried"
	h.expect(h.login(email, attempt), http.StatusUnauthorized, "unauthorized")
	var found bool
	for _, e := range h.auditEntries() {
		if e.Action != "auth.login_failed" {
			continue
		}
		found = true
		if !strings.Contains(string(e.Detail), email) || strings.Contains(string(e.Detail), attempt) || e.ActorID != nil {
			t.Errorf("failed-login entry = %+v, want the email in the detail, no password, no actor id", e)
		}
	}
	if !found {
		t.Fatalf("no auth.login_failed entry: %v", h.auditActions())
	}
}

func TestLogoutDeletesSession(t *testing.T) {
	h := newHarness(t)
	user := h.newUser("logout")
	id := h.newSession(user.ID)

	// Cross-origin logout is a cookie POST like any other.
	h.expect(h.send(sent{method: "POST", path: "/api/v1/auth/logout", sessionID: id, origin: "https://evil.test"}), http.StatusForbidden, "forbidden")

	rec := h.send(sent{method: "POST", path: "/api/v1/auth/logout", sessionID: id, origin: publicURL})
	h.expect(rec, http.StatusNoContent, "")
	if cookie := sessionCookie(rec); cookie == nil || cookie.MaxAge >= 0 || cookie.Value != "" {
		t.Errorf("logout cookie = %+v, want it cleared", cookie)
	}
	if _, err := h.sessions.Get(context.Background(), id); !errors.Is(err, session.ErrNoSession) {
		t.Fatalf("session after logout: Get = %v, want ErrNoSession", err)
	}
	h.expect(h.send(sent{method: "GET", path: "/api/v1/me", sessionID: id}), http.StatusUnauthorized, "unauthorized")
	var found bool
	for _, e := range h.auditEntries() {
		found = found || (e.Action == "auth.logout" && e.Actor == user.Email)
	}
	if !found {
		t.Errorf("no auth.logout entry: %v", h.auditActions())
	}
}

func TestServiceAccountSignsInWithItsPassword(t *testing.T) {
	h := newHarness(t)
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	svc, err := store.CreateLocalUser(context.Background(), h.pool, uniqueEmail(t, "svc"), "svc", "service", hash)
	if err != nil {
		t.Fatalf("CreateLocalUser: %v", err)
	}
	rec := h.login(svc.Email, testPassword)
	h.expect(rec, http.StatusOK, "")
	if sessionCookie(rec) == nil {
		t.Error("a service account got no session cookie")
	}
}

// Attempts made while locked must not push the unlock time out, or anyone
// could keep an account locked for ever by retrying.
func TestAttemptsWhileLockedDoNotExtendTheLock(t *testing.T) {
	h := newHarness(t)
	user := h.newUser("nolockextend")
	for range 5 {
		h.expect(h.login(user.Email, "wrong password"), http.StatusUnauthorized, "unauthorized")
	}
	locked := time.Now()
	for time.Since(locked) < 1500*time.Millisecond {
		h.expect(h.login(user.Email, "wrong password"), http.StatusTooManyRequests, "")
		time.Sleep(200 * time.Millisecond)
	}
	// The lock ends one window after the fifth attempt however often it was hit.
	time.Sleep(time.Until(locked.Add(testLockoutWindow + 300*time.Millisecond)))
	h.expect(h.login(user.Email, testPassword), http.StatusOK, "")
}

// Parallel attempts must not get around the lockout: the attempt is counted
// before the password is checked, so at most five of them are ever checked.
func TestConcurrentWrongPasswordsAreLimitedToFive(t *testing.T) {
	h := newHarness(t)
	// A limiter that cannot fill, so only the lockout decides each answer.
	h.rebuild(func(d *Deps) { d.MaxConcurrentLogins = 10; d.VerifyWait = time.Minute })
	user := h.newUser("concurrent")
	const parallel = 10
	statuses := make(chan int, parallel)
	start := make(chan struct{})
	for range parallel {
		go func() {
			<-start
			statuses <- h.login(user.Email, "wrong password").Code
		}()
	}
	close(start)
	counts := map[int]int{}
	for range parallel {
		counts[<-statuses]++
	}
	if counts[http.StatusUnauthorized] > maxLoginFailures || counts[http.StatusUnauthorized]+counts[http.StatusTooManyRequests] != parallel {
		t.Fatalf("statuses = %v; want at most %d x 401 and the rest 429", counts, maxLoginFailures)
	}
}

// Password checks cost 64 MiB each, so only a few run at once; a request that
// cannot get a slot is told to retry and is not counted as a failed attempt.
func TestFullVerificationLimiterAnswers503AndIsNotCounted(t *testing.T) {
	h := newHarness(t)
	h.rebuild(func(d *Deps) { d.MaxConcurrentLogins = 1; d.VerifyWait = 20 * time.Millisecond })
	user := h.newUser("limiter")
	const parallel = 4
	recs := make(chan *httptest.ResponseRecorder, parallel)
	start := make(chan struct{})
	for range parallel {
		go func() {
			<-start
			recs <- h.login(user.Email, "wrong password")
		}()
	}
	close(start)
	var unauthorized, unavailable int
	for range parallel {
		rec := <-recs
		switch rec.Code {
		case http.StatusUnauthorized:
			unauthorized++
		case http.StatusServiceUnavailable:
			unavailable++
			if errorCodeOf(rec) != "unavailable" || rec.Header().Get("Retry-After") == "" {
				t.Errorf("503 = %s with Retry-After %q, want code unavailable and a Retry-After", failure(rec), rec.Header().Get("Retry-After"))
			}
		default:
			t.Errorf("unexpected status %d", rec.Code)
		}
	}
	if unavailable == 0 || unauthorized == 0 {
		t.Fatalf("got %d x 401 and %d x 503; want some of each", unauthorized, unavailable)
	}
	count, err := h.valkey.Do(context.Background(), h.valkey.B().Get().Key((&server{Deps: h.deps}).lockoutKey(user.Email)).Build()).AsInt64()
	if err != nil || int(count) != unauthorized {
		t.Fatalf("failure counter = %d (%v), want %d: refused requests must not count", count, err, unauthorized)
	}
}

// The operator's unlock command clears the counter by the key LoginFailureKey
// names: it must be the key the sign-in really counts in.
func TestLoginFailureKeyIsTheCounterSignInUses(t *testing.T) {
	h := newHarness(t)
	user := h.newUser("unlockable")
	for i := range 5 {
		h.expect(h.login(user.Email, "wrong password "+strconv.Itoa(i)), http.StatusUnauthorized, "unauthorized")
	}
	h.expect(h.login(user.Email, testPassword), http.StatusTooManyRequests, "")
	key := LoginFailureKey(h.deps.KeyPrefix, "  "+strings.ToUpper(user.Email)+" ")
	if err := h.valkey.Do(context.Background(), h.valkey.B().Del().Key(key).Build()).Error(); err != nil {
		t.Fatalf("DEL: %v", err)
	}
	h.expect(h.login(user.Email, testPassword), http.StatusOK, "")
}
