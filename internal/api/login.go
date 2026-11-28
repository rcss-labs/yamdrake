package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	valkey "github.com/valkey-io/valkey-go"

	"github.com/rcss-labs/yamdrake/internal/audit"
	"github.com/rcss-labs/yamdrake/internal/auth"
	"github.com/rcss-labs/yamdrake/internal/session"
	"github.com/rcss-labs/yamdrake/internal/store"
)

const (
	// maxLoginFailures failed sign-ins for one email within lockoutWindow lock it.
	maxLoginFailures     = 5
	defaultLockoutWindow = 15 * time.Minute
	// defaultConcurrentLogins password checks at once use at most 4 x 64 MiB.
	defaultConcurrentLogins = 4
	defaultVerifyWait       = time.Second
	maxEmailLength          = 254
	maxPasswordLength       = 1024
)

type grantView struct {
	Action      string `json:"action"`
	Label       string `json:"label"`
	Environment string `json:"environment"`
	FolderID    int64  `json:"folder_id"`
}

type principalResponse struct {
	ID     int64       `json:"id"`
	Email  string      `json:"email"`
	Name   string      `json:"name"`
	Admin  bool        `json:"admin"`
	Grants []grantView `json:"grants"`
}

func principalView(p auth.Principal) principalResponse {
	out := principalResponse{ID: p.UserID, Email: p.Email, Name: p.Name, Admin: p.Admin, Grants: []grantView{}}
	for _, g := range p.Grants {
		out.Grants = append(out.Grants, grantView{Action: g.Action, Label: g.Label, Environment: g.Environment, FolderID: g.FolderID})
	}
	return out
}

func (s *server) me(w http.ResponseWriter, r *http.Request) {
	principal, _ := PrincipalFrom(r.Context())
	writeJSON(w, http.StatusOK, principalView(principal))
}

// lockoutKey names the failure counter of an email. The email is hashed so the
// address itself never sits in Valkey.
func (s *server) lockoutKey(email string) string { return LoginFailureKey(s.KeyPrefix, email) }

// LoginFailureKey is the Valkey key of an email's failed sign-in counter under
// the given key prefix. The orchestrator's unlock command deletes it, so the
// way the email is normalised lives here, once.
func LoginFailureKey(prefix, email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return prefix + "login-failures:" + hex.EncodeToString(sum[:])
}

// reserveScript counts one sign-in attempt for the key atomically, before the
// password is checked. The expiry is (re)set only while the count is within
// the limit, so attempts made while locked never push the unlock time out.
// It returns the count and the remaining lifetime in milliseconds.
var reserveScript = valkey.NewLuaScript(`
local n = redis.call('INCR', KEYS[1])
if n <= tonumber(ARGV[2]) or redis.call('PTTL', KEYS[1]) < 0 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return {n, redis.call('PTTL', KEYS[1])}
`)

// reserveAttempt counts the attempt and reports whether it may go on. When it
// may not, wait is how long the lock lasts.
func (s *server) reserveAttempt(ctx context.Context, key string) (allowed bool, wait time.Duration, err error) {
	reply, err := reserveScript.Exec(ctx, s.Valkey, []string{key},
		[]string{strconv.FormatInt(s.LockoutWindow.Milliseconds(), 10), strconv.Itoa(maxLoginFailures)}).AsIntSlice()
	if err != nil {
		return false, 0, fmt.Errorf("reserve sign-in attempt: %w", err)
	}
	if len(reply) != 2 {
		return false, 0, errors.New("reserve sign-in attempt: unexpected reply")
	}
	if reply[0] <= maxLoginFailures {
		return true, 0, nil
	}
	return false, max(time.Duration(reply[1])*time.Millisecond, time.Second), nil
}

// refundScript decrements only a counter that still exists and is above zero:
// a plain DECR would create a key of -1 with no expiry if the counter was
// deleted (sign-in success) or expired in the meantime.
var refundScript = valkey.NewLuaScript(`
local n = tonumber(redis.call('GET', KEYS[1]))
if n and n > 0 then return redis.call('DECR', KEYS[1]) end
return 0
`)

// refundAttempt takes back a reserved attempt that was never checked (the
// request was refused for lack of capacity), best effort. It does not depend
// on the request context, which ends when the client gives up waiting.
func (s *server) refundAttempt(r *http.Request, key string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), valkeyTimeout)
	defer cancel()
	if err := refundScript.Exec(ctx, s.Valkey, []string{key}, nil).Error(); err != nil {
		s.Log.Error("refund sign-in attempt", "error", err.Error())
	}
}

// valkeyContext gives each Valkey call its own timeout, so a slow password
// check cannot use up the budget of the calls that follow it.
func (s *server) valkeyContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), valkeyTimeout)
}

func (s *server) valkeyFailed(w http.ResponseWriter, what string, err error) {
	s.Log.Error(what, "error", err.Error())
	signInUnavailable(w)
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	if !session.SameOrigin(r, s.PublicURL) {
		writeError(w, http.StatusForbidden, codeForbidden, "cross-origin request refused")
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeBodyUnchecked(w, r, &req) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || req.Password == "" || len(email) > maxEmailLength || len(req.Password) > maxPasswordLength {
		invalid(w, "email and password are required")
		return
	}

	key := s.lockoutKey(email)
	ctx, cancel := s.valkeyContext(r)
	allowed, wait, err := s.reserveAttempt(ctx, key)
	cancel()
	if err != nil {
		s.valkeyFailed(w, "count sign-in attempt", err)
		return
	}
	if !allowed {
		seconds := int64((wait + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		writeError(w, http.StatusTooManyRequests, codeTooMany, "too many failed sign-ins; try again later")
		return
	}

	// Only a few password checks run at once. A request that cannot get a slot
	// says nothing about the account and is not counted.
	release, ok := s.acquireVerifySlot(r.Context())
	if !ok {
		s.refundAttempt(r, key)
		w.Header().Set("Retry-After", "1")
		signInUnavailable(w)
		return
	}
	user, ok, reason, exists, err := s.checkPasswordHoldingSlot(r.Context(), release, email, req.Password)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !ok {
		s.recordFailedLogin(r, email, reason, exists)
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "invalid email or password")
		return
	}

	ctx, cancel = s.valkeyContext(r)
	err = s.Valkey.Do(ctx, s.Valkey.B().Del().Key(key).Build()).Error()
	cancel()
	if err != nil {
		s.valkeyFailed(w, "reset sign-in failure counter", err)
		return
	}
	principal, err := s.startSession(w, r, user.ID, map[string]any{"method": "password"})
	switch {
	case errors.Is(err, session.ErrUnavailable):
		s.valkeyFailed(w, "start session", err)
		return
	case errors.Is(err, auth.ErrDisabled):
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "invalid email or password")
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, principalView(principal))
}

// recordFailedLogin audits a failed sign-in of an account that exists. Anyone
// can make unlimited failures with made-up addresses and the audit log cannot
// be pruned, so those are only logged. The answer and its timing are the same
// either way: the unknown case makes one database round trip too, and both are
// dwarfed by the password check.
func (s *server) recordFailedLogin(r *http.Request, email, reason string, accountExists bool) {
	ctx := context.WithoutCancel(r.Context())
	if !accountExists {
		s.Log.Warn("failed sign-in for an unknown email", "reason", reason)
		_, _ = s.Pool.Exec(ctx, "SELECT 1")
		return
	}
	detail := map[string]any{"email": truncate(email, maxEmailLength), "reason": reason}
	if err := audit.Record(ctx, s.Pool, nil, s.Now(), audit.Event{
		Action: "auth.login_failed", TargetType: "user", TargetID: "", Detail: detail}); err != nil {
		s.Log.Error("audit failed sign-in", "error", err.Error())
	}
}

// startSession signs the user in whichever way they proved who they are: the
// session the client already holds ends, a new one with an id of our own making
// is created (a planted session id is never adopted), the sign-in is audited
// with detail and the cookie is set. It writes nothing but that cookie; the
// caller answers. Errors wrap session.ErrUnavailable (Valkey), auth.ErrDisabled
// or are internal.
func (s *server) startSession(w http.ResponseWriter, r *http.Request, userID int64, detail map[string]any) (auth.Principal, error) {
	ctx, cancel := context.WithTimeout(r.Context(), valkeyTimeout)
	defer cancel()
	if old, has := session.IDFromRequest(r); has {
		if err := s.Sessions.Delete(ctx, old); err != nil {
			return auth.Principal{}, err
		}
	}
	created, err := s.Sessions.Create(ctx, userID)
	if err != nil {
		return auth.Principal{}, err
	}
	principal, err := auth.Resolve(r.Context(), s.Pool, userID, viaSession)
	if err == nil {
		err = audit.Record(r.Context(), s.Pool, &principal, s.Now(), audit.Event{
			Action: "auth.login", TargetType: "user", TargetID: strconv.FormatInt(userID, 10), Detail: detail})
	}
	if err != nil {
		s.deleteSession(r, created.ID)
		return auth.Principal{}, err
	}
	session.SetCookie(w, created)
	return principal, nil
}

// checkPassword reports whether email and password name an enabled user who
// signs in with a password. Every path, including an unknown email, a user
// without a password and a SAML user, verifies one argon2id hash (the dummy one
// when there is nothing real to check), so the cost does not reveal which case
// it was. reason is for the audit log only; accountExists tells whether the
// email belongs to a user at all (of any source), which decides if a failure is audited.
func (s *server) checkPassword(ctx context.Context, email, password string) (user store.User, ok bool, reason string, accountExists bool, err error) {
	hash, known := s.dummyHash, false
	if hasControlText(email, false) {
		// Such an address cannot exist (and PostgreSQL refuses to look for it):
		// it is an unknown email, verified against the dummy hash like one.
		err = store.ErrNotFound
	} else {
		user, err = store.UserByEmail(ctx, s.Pool, email)
	}
	switch {
	case err == nil:
		stored, hashErr := store.PasswordHash(ctx, s.Pool, user.ID)
		switch {
		case hashErr == nil:
			hash, known = stored, true
		case !errors.Is(hashErr, store.ErrNotFound):
			return store.User{}, false, "", false, hashErr
		}
	case !errors.Is(err, store.ErrNotFound):
		return store.User{}, false, "", false, err
	}
	accountExists = err == nil
	matches, verifyErr := auth.VerifyPassword(hash, password)
	if verifyErr != nil {
		// A stored hash that cannot be read: refuse, and say so in the log (the
		// error names no hash content).
		s.Log.Error("stored password hash is unusable", "user_id", user.ID, "error", verifyErr.Error())
		matches = false
	}
	switch {
	case !known || !matches:
		return store.User{}, false, "bad_credentials", accountExists, nil
	case user.Disabled:
		return store.User{}, false, "disabled", accountExists, nil
	}
	return user, true, "", true, nil
}

// checkPasswordHoldingSlot runs checkPassword and always gives the slot back,
// even if the check panics.
func (s *server) checkPasswordHoldingSlot(ctx context.Context, release func(), email, password string) (store.User, bool, string, bool, error) {
	defer release()
	return s.checkPassword(ctx, email, password)
}

// acquireVerifySlot waits briefly for permission to run a password check.
func (s *server) acquireVerifySlot(ctx context.Context) (release func(), ok bool) {
	timer := time.NewTimer(s.VerifyWait)
	defer timer.Stop()
	select {
	case s.verifySlots <- struct{}{}:
		return func() { <-s.verifySlots }, true
	case <-timer.C:
	case <-ctx.Done():
	}
	return nil, false
}

// truncate cuts s to at most n bytes without splitting a character (a split
// one is invalid UTF-8, which PostgreSQL refuses).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	principal, _ := PrincipalFrom(r.Context())
	if principal.Via == viaToken {
		// A token logs out by revoking itself, and only itself.
		err := s.auditedWrite(r, func(ctx context.Context, tx pgx.Tx) (audit.Event, error) {
			err := store.RevokeToken(ctx, tx, principal.TokenID, principal.UserID, s.Now())
			return audit.Event{Action: "token.revoke", TargetType: "token", TargetID: idString(principal.TokenID),
				Detail: map[string]any{"via": "logout"}}, err
		})
		if err != nil {
			s.writeAccessError(w, r, err, "token")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if principal.Via == viaSession {
		if id, ok := session.IDFromRequest(r); ok {
			ctx, cancel := context.WithTimeout(r.Context(), valkeyTimeout)
			defer cancel()
			if err := s.Sessions.Delete(ctx, id); err != nil {
				s.valkeyFailed(w, "delete session", err)
				return
			}
		}
		session.ClearCookie(w)
		if err := audit.Record(r.Context(), s.Pool, &principal, s.Now(), audit.Event{
			Action: "auth.logout", TargetType: "user", TargetID: strconv.FormatInt(principal.UserID, 10)}); err != nil {
			s.Log.Error("audit sign-out", "error", err.Error())
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
