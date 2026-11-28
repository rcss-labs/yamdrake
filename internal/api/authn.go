package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/rcss-labs/yumdrake/internal/auth"
	"github.com/rcss-labs/yumdrake/internal/session"
	"github.com/rcss-labs/yumdrake/internal/store"
)

type principalKey struct{}

// PrincipalFrom returns the principal that authenticate put into ctx.
func PrincipalFrom(ctx context.Context) (auth.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(auth.Principal)
	return p, ok
}

const (
	messageSignInUnavailable  = "Sign-in is temporarily unavailable"
	messageSSOExpired         = "Sign in again with SSO: yumdrake login"
	messageTokenRevokesItself = "A token can only revoke itself"
	messageTokensNeedSession  = "Sign in with your browser to manage tokens"
	viaSession                = "session"
	viaToken                  = "token"
)

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="yumdrake"`)
	writeError(w, http.StatusUnauthorized, codeUnauthorized, "authentication is required")
}

func signInUnavailable(w http.ResponseWriter) {
	writeError(w, http.StatusServiceUnavailable, codeUnavailable, messageSignInUnavailable)
}

// bearerToken returns the token of an "Authorization: Bearer" header.
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	const scheme = "bearer "
	if len(header) < len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return "", false
	}
	return strings.TrimSpace(header[len(scheme):]), true
}

// authenticate puts the caller's principal into the request context, or
// answers 401 (no usable credential), 403 (a cookie request from another
// origin) or 503 (the credential store is down).
//
// A bearer header is tried as a token and nothing else: cookies are ignored
// and no origin check applies, because a CLI sends neither and a browser never
// adds this header on its own. Without one, the session cookie is used, and a
// cookie request that changes state must come from the public URL.
func (s *server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var principal auth.Principal
		var ok bool
		if token, bearer := bearerToken(r); bearer {
			principal, ok = s.authenticateToken(w, r, token)
		} else {
			principal, ok = s.authenticateSession(w, r)
		}
		if !ok {
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, principal)))
	})
}

func (s *server) authenticateToken(w http.ResponseWriter, r *http.Request, token string) (auth.Principal, bool) {
	tok, err := auth.VerifyTokenRecord(r.Context(), s.Pool, token, s.Now())
	switch {
	case errors.Is(err, auth.ErrBadToken):
		unauthorized(w)
		return auth.Principal{}, false
	case err != nil:
		// A database failure is not the caller's fault: never a 401.
		s.Log.Error("token check failed", "error", err.Error())
		writeError(w, http.StatusServiceUnavailable, codeUnavailable, "authentication is temporarily unavailable")
		return auth.Principal{}, false
	}
	principal, err := auth.Resolve(r.Context(), s.Pool, tok.UserID, viaToken)
	switch {
	case errors.Is(err, auth.ErrDisabled), errors.Is(err, store.ErrNotFound):
		unauthorized(w)
		return auth.Principal{}, false
	case err != nil:
		s.internalError(w, r, err)
		return auth.Principal{}, false
	}
	principal.TokenID = tok.ID
	// Memberships from the identity provider are refreshed only at SSO sign-in,
	// so a SAML user's token works only for a while after the last one.
	if principal.Source == "saml" && (principal.LastSignInAt == nil || s.Now().Sub(*principal.LastSignInAt) > s.TokenSSOMaxAge) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="yumdrake"`)
		writeError(w, http.StatusUnauthorized, codeUnauthorized, messageSSOExpired)
		return auth.Principal{}, false
	}
	return principal, true
}

func (s *server) authenticateSession(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	id, ok := session.IDFromRequest(r)
	if !ok {
		unauthorized(w)
		return auth.Principal{}, false
	}
	if !session.SameOrigin(r, s.PublicURL) {
		writeError(w, http.StatusForbidden, codeForbidden, "cross-origin request refused")
		return auth.Principal{}, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), valkeyTimeout)
	defer cancel()
	sess, err := s.Sessions.Get(ctx, id)
	switch {
	case errors.Is(err, session.ErrNoSession):
		session.ClearCookie(w)
		unauthorized(w)
		return auth.Principal{}, false
	case err != nil:
		s.Log.Error("session lookup failed", "error", err.Error())
		signInUnavailable(w)
		return auth.Principal{}, false
	}
	principal, err := auth.Resolve(r.Context(), s.Pool, sess.UserID, viaSession)
	switch {
	case errors.Is(err, auth.ErrDisabled), errors.Is(err, store.ErrNotFound):
		s.deleteSession(r, id)
		session.ClearCookie(w)
		unauthorized(w)
		return auth.Principal{}, false
	case err != nil:
		s.internalError(w, r, err)
		return auth.Principal{}, false
	}
	return principal, true
}

// deleteSession removes a session after the request is otherwise settled. A
// failure is logged only: the session cannot be used again anyway because the
// user is refused on every request.
func (s *server) deleteSession(r *http.Request, id string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), valkeyTimeout)
	defer cancel()
	if err := s.Sessions.Delete(ctx, id); err != nil {
		s.Log.Error("delete session failed", "error", err.Error())
	}
}

// require lets a request through only when its principal may perform action on
// the target. target may be nil for actions that have no scope; an error from
// it is a 404 when it wraps store.ErrNotFound and a 500 otherwise. Without a
// principal (authenticate did not run) the answer is 401.
func (s *server) require(action auth.Action, target func(*http.Request) (auth.Target, error)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFrom(r.Context())
			if !ok {
				unauthorized(w)
				return
			}
			var t auth.Target
			if target != nil {
				var err error
				if t, err = target(r); errors.Is(err, store.ErrNotFound) {
					notFound(w, "resource")
					return
				} else if err != nil {
					s.internalError(w, r, err)
					return
				}
			}
			if !principal.Can(action, t) {
				writeError(w, http.StatusForbidden, codeForbidden, "you do not have permission to do this")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
