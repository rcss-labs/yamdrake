package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	maxEmailLength       = 254
	maxPasswordLength    = 1024
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
func lockoutKey(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return "login-failures:" + hex.EncodeToString(sum[:])
}

// lockedFor returns how long sign-in for the counter stays locked (zero when
// it is not locked).
func (s *server) lockedFor(ctx context.Context, key string) (time.Duration, error) {
	b := s.Valkey.B()
	count, err := s.Valkey.Do(ctx, b.Get().Key(key).Build()).AsInt64()
	if err != nil {
		if isNil(err) {
			return 0, nil
		}
		return 0, err
	}
	if count < maxLoginFailures {
		return 0, nil
	}
	remaining, err := s.Valkey.Do(ctx, b.Pttl().Key(key).Build()).AsInt64()
	if err != nil {
		return 0, err
	}
	if remaining <= 0 {
		return time.Second, nil
	}
	return time.Duration(remaining) * time.Millisecond, nil
}

// recordFailure counts one failed sign-in and restarts the window: the counter
// goes away LockoutWindow after the latest failure, so five failures less than
// a window apart lock the email until a full window passes without an attempt.
// SET NX comes first so a counter can never exist without an expiry.
func (s *server) recordFailure(ctx context.Context, key string) error {
	b := s.Valkey.B()
	for _, res := range s.Valkey.DoMulti(ctx,
		b.Set().Key(key).Value("0").Nx().Px(s.LockoutWindow).Build(),
		b.Incr().Key(key).Build(),
		b.Pexpire().Key(key).Milliseconds(s.LockoutWindow.Milliseconds()).Build(),
	) {
		if err := res.Error(); err != nil && !isNil(err) {
			return err
		}
	}
	return nil
}

func isNil(err error) bool { return valkey.IsValkeyNil(err) }

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
	if !decodeBody(w, r, &req) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || req.Password == "" || len(email) > maxEmailLength || len(req.Password) > maxPasswordLength {
		invalid(w, "email and password are required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), valkeyTimeout)
	defer cancel()
	key := lockoutKey(email)
	wait, err := s.lockedFor(ctx, key)
	if err != nil {
		s.valkeyFailed(w, "read sign-in failure counter", err)
		return
	}
	if wait > 0 {
		seconds := int64((wait + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		writeError(w, http.StatusTooManyRequests, codeTooMany, "too many failed sign-ins; try again later")
		return
	}

	user, ok, reason, err := s.checkPassword(r.Context(), email, req.Password)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !ok {
		if err := s.recordFailure(ctx, key); err != nil {
			s.valkeyFailed(w, "count failed sign-in", err)
			return
		}
		detail := map[string]any{"email": truncate(email, maxEmailLength), "reason": reason}
		if err := audit.Record(context.WithoutCancel(r.Context()), s.Pool, nil, s.Now(), audit.Event{
			Action: "auth.login_failed", TargetType: "user", TargetID: "", Detail: detail}); err != nil {
			s.Log.Error("audit failed sign-in", "error", err.Error())
		}
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "invalid email or password")
		return
	}

	if err := s.Valkey.Do(ctx, s.Valkey.B().Del().Key(key).Build()).Error(); err != nil {
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
// it was. reason is for the audit log only.
func (s *server) checkPassword(ctx context.Context, email, password string) (user store.User, ok bool, reason string, err error) {
	hash, known := s.dummyHash, false
	user, err = store.UserByEmail(ctx, s.Pool, email)
	switch {
	case err == nil:
		stored, hashErr := store.PasswordHash(ctx, s.Pool, user.ID)
		switch {
		case hashErr == nil:
			hash, known = stored, true
		case !errors.Is(hashErr, store.ErrNotFound):
			return store.User{}, false, "", hashErr
		}
	case !errors.Is(err, store.ErrNotFound):
		return store.User{}, false, "", err
	}
	matches, verifyErr := auth.VerifyPassword(hash, password)
	if verifyErr != nil {
		// A stored hash that cannot be read: refuse, and say so in the log (the
		// error names no hash content).
		s.Log.Error("stored password hash is unusable", "user_id", user.ID, "error", verifyErr.Error())
		matches = false
	}
	switch {
	case !known || !matches:
		return store.User{}, false, "bad_credentials", nil
	case user.Disabled:
		return store.User{}, false, "disabled", nil
	}
	return user, true, "", nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	principal, _ := PrincipalFrom(r.Context())
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
