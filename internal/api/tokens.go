package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	valkey "github.com/valkey-io/valkey-go"

	"github.com/rcss-labs/yamdrake/internal/audit"
	"github.com/rcss-labs/yamdrake/internal/auth"
	"github.com/rcss-labs/yamdrake/internal/session"
	"github.com/rcss-labs/yamdrake/internal/store"
)

const (
	maxTokenNameLength        = 100
	maxTokenDays              = 365
	defaultDevicePollInterval = 5 * time.Second

	// deviceTTL is how long a device sign-in can be completed.
	deviceTTL = 10 * time.Minute
	// userCodeAlphabet leaves out 0, O, 1, I and L, which are easily mixed up.
	userCodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	userCodeLength   = 8
	deviceCodeBytes  = 32
	deviceCodeLength = 43 // base64url (unpadded) length of deviceCodeBytes

	confirmedPrefix = "confirmed:"
)

// ---- the caller's own tokens ----

type tokenResponse struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
}

func tokenView(t store.Token) tokenResponse {
	return tokenResponse{ID: t.ID, Name: t.Name, CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt, ExpiresAt: t.ExpiresAt}
}

func (s *server) listTokens(w http.ResponseWriter, r *http.Request) {
	principal, _ := PrincipalFrom(r.Context())
	tokens, err := store.ListTokens(r.Context(), s.Pool, principal.UserID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]tokenResponse, 0, len(tokens))
	for _, t := range tokens {
		if t.RevokedAt != nil {
			continue // a revoked token is gone as far as the owner is concerned
		}
		out = append(out, tokenView(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

// createToken makes a token for the caller. Its plaintext is in this response
// and nowhere else. Only a browser session may do it: a token that could mint
// tokens would renew itself for ever and outlive its owner's removal.
func (s *server) createToken(w http.ResponseWriter, r *http.Request) {
	principal, _ := PrincipalFrom(r.Context())
	if principal.Via != viaSession {
		writeError(w, http.StatusForbidden, codeForbidden, messageTokensNeedSession)
		return
	}
	var req struct {
		Name          string `json:"name"`
		ExpiresInDays int    `json:"ttl_days"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > maxTokenNameLength {
		invalid(w, fmt.Sprintf("name is required and at most %d characters", maxTokenNameLength))
		return
	}
	if req.ExpiresInDays < 0 || req.ExpiresInDays > maxTokenDays {
		invalid(w, fmt.Sprintf("ttl_days must be between 0 (the default, 90) and %d", maxTokenDays))
		return
	}
	plaintext, created, err := s.issueToken(r, principal, name, time.Duration(req.ExpiresInDays)*24*time.Hour)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	body := struct {
		tokenResponse
		Plaintext string `json:"plaintext"`
	}{tokenView(created), plaintext}
	writeJSON(w, http.StatusCreated, body)
}

// createServiceToken mints a token for a service account, which has no browser
// to do it from. Global admins only, and only from a browser session (a token
// that could mint tokens could renew admin power for ever); the audit entry
// names the token and never its value. The plaintext is in this response only.
func (s *server) createServiceToken(w http.ResponseWriter, r *http.Request) {
	principal, _ := PrincipalFrom(r.Context())
	if principal.Via != viaSession {
		writeError(w, http.StatusForbidden, codeForbidden, messageTokensNeedSession)
		return
	}
	if !principal.Admin {
		s.writeAccessError(w, r, errAdminRequired, "token")
		return
	}
	id, ok := pathID(r)
	if !ok {
		notFound(w, "user")
		return
	}
	var req struct {
		Name          string `json:"name"`
		ExpiresInDays int    `json:"ttl_days"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > maxTokenNameLength {
		invalid(w, fmt.Sprintf("name is required and at most %d characters", maxTokenNameLength))
		return
	}
	if req.ExpiresInDays < 0 || req.ExpiresInDays > maxTokenDays {
		invalid(w, fmt.Sprintf("ttl_days must be between 0 (the default, 90) and %d", maxTokenDays))
		return
	}
	var plaintext string
	var created store.Token
	err := s.auditedWrite(r, func(ctx context.Context, tx pgx.Tx) (audit.Event, error) {
		user, err := store.UserByID(ctx, tx, id)
		if err != nil {
			return audit.Event{}, err
		}
		if user.Source != "service" {
			return audit.Event{}, fmt.Errorf("%w: tokens are minted here only for service accounts", store.ErrInvalid)
		}
		plaintext, created, err = auth.NewToken(ctx, tx, id, name, time.Duration(req.ExpiresInDays)*24*time.Hour, s.Now())
		if err != nil {
			return audit.Event{}, err
		}
		detail := map[string]any{"name": name, "user_id": id, "email": user.Email}
		if created.ExpiresAt != nil {
			detail["expires_at"] = created.ExpiresAt.UTC().Format(time.RFC3339)
		}
		return audit.Event{Action: "token.create", TargetType: "token", TargetID: idString(created.ID), Detail: detail}, nil
	})
	if errors.Is(err, store.ErrInvalid) {
		writeError(w, http.StatusUnprocessableEntity, codeInvalid, "tokens are minted here only for service accounts")
		return
	}
	if err != nil {
		s.writeAccessError(w, r, err, "user")
		return
	}
	body := struct {
		tokenResponse
		Plaintext string `json:"plaintext"`
	}{tokenView(created), plaintext}
	writeJSON(w, http.StatusCreated, body)
}

// issueToken creates a token for principal and its audit entry in one
// transaction, attributed to principal (who need not be the request's caller:
// the device sign-in issues it for the user who confirmed).
func (s *server) issueToken(r *http.Request, principal auth.Principal, name string, ttl time.Duration) (string, store.Token, error) {
	ctx := r.Context()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", store.Token{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := s.Now()
	plaintext, created, err := auth.NewToken(ctx, tx, principal.UserID, name, ttl, now)
	if err != nil {
		return "", store.Token{}, err
	}
	// The detail names the token, never its secret.
	detail := map[string]any{"name": name}
	if created.ExpiresAt != nil {
		detail["expires_at"] = created.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if err := audit.Record(ctx, tx, &principal, now, audit.Event{
		Action: "token.create", TargetType: "token", TargetID: idString(created.ID), Detail: detail}); err != nil {
		return "", store.Token{}, fmt.Errorf("record audit entry: %v", err) //nolint:errorlint // keep the chain short
	}
	if err := tx.Commit(ctx); err != nil {
		return "", store.Token{}, fmt.Errorf("commit: %w", err)
	}
	return plaintext, created, nil
}

// revokeToken revokes one of the caller's own tokens (from a browser session),
// or the token that makes the request. A token of someone else is a 404, the same as an
// unknown id, so its existence is not revealed.
func (s *server) revokeToken(w http.ResponseWriter, r *http.Request) {
	principal, _ := PrincipalFrom(r.Context())
	id, ok := pathID(r)
	if !ok && principal.Via == viaSession {
		notFound(w, "token")
		return
	}
	// A token may give up its own power and nothing else; any other id (a
	// sibling, a stranger's, an unknown one) is refused alike.
	if principal.Via != viaSession && (!ok || id != principal.TokenID) {
		writeError(w, http.StatusForbidden, codeForbidden, messageTokenRevokesItself)
		return
	}
	err := s.auditedWrite(r, func(ctx context.Context, tx pgx.Tx) (audit.Event, error) {
		err := store.RevokeToken(ctx, tx, id, principal.UserID, s.Now())
		return audit.Event{Action: "token.revoke", TargetType: "token", TargetID: idString(id)}, err
	})
	if err != nil {
		s.writeAccessError(w, r, err, "token")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- device sign-in for the CLI ----
//
// State lives in Valkey for ten minutes under keys that never contain the
// device code itself (only its SHA-256), since the device code is the secret
// the CLI trades for a token:
//
//	device:<sha256(device_code)>       "pending" or "confirmed:<user id>"
//	device-user:<USER CODE>            sha256(device_code), consumed by the first confirmation
//	device-poll:<sha256(device_code)>  exists for one poll interval after a poll
//
// POST /cli/login and POST /cli/login/token are open routes. They read no
// cookie and so cannot be made to act with a victim's session from another
// site; confirm, the only route that acts for a signed-in person, needs a
// session cookie and so goes through the same-origin check of authenticate.

func deviceHash(deviceCode string) string {
	sum := sha256.Sum256([]byte(deviceCode))
	return hex.EncodeToString(sum[:])
}

func (s *server) deviceKey(hash string) string     { return s.KeyPrefix + "device:" + hash }
func (s *server) deviceUserKey(code string) string { return s.KeyPrefix + "device-user:" + code }
func (s *server) devicePollKey(hash string) string { return s.KeyPrefix + "device-poll:" + hash }

func validDeviceCode(code string) bool {
	if len(code) != deviceCodeLength {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(code)
	return err == nil && len(raw) == deviceCodeBytes
}

// newUserCode returns 8 random characters of userCodeAlphabet.
func newUserCode() (string, error) {
	out := make([]byte, userCodeLength)
	limit := big.NewInt(int64(len(userCodeAlphabet)))
	for i := range out {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		out[i] = userCodeAlphabet[n.Int64()]
	}
	return string(out), nil
}

// normalizeUserCode accepts any letter case and ignores spaces and hyphens
// ("abcd-efgh"); ok is false for anything that cannot be a user code.
func normalizeUserCode(raw string) (string, bool) {
	code := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(raw))
	if len(code) != userCodeLength {
		return "", false
	}
	for _, c := range code {
		if !strings.ContainsRune(userCodeAlphabet, c) {
			return "", false
		}
	}
	return code, true
}

func (s *server) pollSeconds() int64 {
	return int64((s.DevicePollInterval + time.Second - 1) / time.Second)
}

func (s *server) startDeviceLogin(w http.ResponseWriter, r *http.Request) {
	raw := make([]byte, deviceCodeBytes)
	if _, err := rand.Read(raw); err != nil {
		s.internalError(w, r, err)
		return
	}
	deviceCode := base64.RawURLEncoding.EncodeToString(raw)
	hash := deviceHash(deviceCode)

	ctx, cancel := context.WithTimeout(r.Context(), valkeyTimeout)
	defer cancel()
	b := s.Valkey.B()
	if err := s.Valkey.Do(ctx, b.Set().Key(s.deviceKey(hash)).Value("pending").Ex(deviceTTL).Build()).Error(); err != nil {
		s.valkeyFailed(w, "store device sign-in", err)
		return
	}
	// A user code that is already in use is replaced by a fresh one.
	for range 8 {
		userCode, err := newUserCode()
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		err = s.Valkey.Do(ctx, b.Set().Key(s.deviceUserKey(userCode)).Value(hash).Nx().Ex(deviceTTL).Build()).Error()
		if isNil(err) {
			continue
		}
		if err != nil {
			s.valkeyFailed(w, "store device user code", err)
			return
		}
		verifyURL := strings.TrimRight(s.PublicURL, "/") + "/device?user_code=" + url.QueryEscape(userCode)
		writeJSON(w, http.StatusOK, map[string]any{
			"device_code": deviceCode, "user_code": userCode, "verify_url": verifyURL,
			"interval": s.pollSeconds(), "expires_in": int64(deviceTTL / time.Second),
		})
		return
	}
	s.internalError(w, r, errors.New("could not find a free user code"))
}

// confirmDeviceLogin binds a user code to the signed-in browser user. Only a
// session may do it: a token (or anyone holding one) cannot approve a CLI.
func (s *server) confirmDeviceLogin(w http.ResponseWriter, r *http.Request) {
	principal, _ := PrincipalFrom(r.Context())
	if principal.Via != viaSession {
		writeError(w, http.StatusForbidden, codeForbidden, "a device sign-in is confirmed from a browser session")
		return
	}
	if !s.allowConfirmAttempt(w, r) {
		return
	}
	var req struct {
		UserCode string `json:"user_code"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	userCode, ok := normalizeUserCode(req.UserCode)
	if !ok {
		invalid(w, "user_code must be 8 letters and digits")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), valkeyTimeout)
	defer cancel()
	b := s.Valkey.B()
	// GETDEL: the first confirmation consumes the user code, so it cannot be
	// confirmed again (by someone else) afterwards.
	hash, err := s.Valkey.Do(ctx, b.Getdel().Key(s.deviceUserKey(userCode)).Build()).ToString()
	if isNil(err) {
		notFound(w, "device code")
		return
	}
	if err != nil {
		s.valkeyFailed(w, "read device user code", err)
		return
	}
	// XX KEEPTTL: only a still-pending sign-in changes, with its remaining time.
	err = s.Valkey.Do(ctx, b.Set().Key(s.deviceKey(hash)).Value(confirmedPrefix+idString(principal.UserID)).Xx().Keepttl().Build()).Error()
	if isNil(err) {
		notFound(w, "device code")
		return
	}
	if err != nil {
		s.valkeyFailed(w, "confirm device sign-in", err)
		return
	}
	if err := s.recordAudit(r.Context(), s.Pool, r, audit.Event{
		Action: "cli_login.confirm", TargetType: "user", TargetID: idString(principal.UserID)}); err != nil {
		// Do not leave an approval nobody can see in the log.
		_ = s.Valkey.Do(ctx, b.Del().Key(s.deviceKey(hash)).Build()).Error()
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

const (
	maxConfirmAttempts = 10
	confirmWindow      = 10 * time.Minute
)

// confirmCountScript counts one attempt and starts the window with the first.
var confirmCountScript = valkey.NewLuaScript(`
local n = redis.call('INCR', KEYS[1])
if n == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
return {n, redis.call('PTTL', KEYS[1])}`)

// allowConfirmAttempt counts a confirm attempt of this session (every attempt,
// whatever the code) and answers 429 once there are more than
// maxConfirmAttempts in confirmWindow, so a user code cannot be guessed. The
// key holds a hash of the session id, never the id.
func (s *server) allowConfirmAttempt(w http.ResponseWriter, r *http.Request) bool {
	id, _ := session.IDFromRequest(r)
	sum := sha256.Sum256([]byte(id))
	key := s.KeyPrefix + "device-confirm:" + hex.EncodeToString(sum[:])
	ctx, cancel := context.WithTimeout(r.Context(), valkeyTimeout)
	defer cancel()
	reply, err := confirmCountScript.Exec(ctx, s.Valkey, []string{key}, []string{strconv.FormatInt(confirmWindow.Milliseconds(), 10)}).AsIntSlice()
	if err != nil || len(reply) != 2 {
		s.valkeyFailed(w, "count device confirm attempt", errors.Join(err, errors.New("confirm counter")))
		return false
	}
	if reply[0] > maxConfirmAttempts {
		wait := max(time.Duration(reply[1])*time.Millisecond, time.Second)
		w.Header().Set("Retry-After", strconv.FormatInt(int64((wait+time.Second-1)/time.Second), 10))
		writeError(w, http.StatusTooManyRequests, codeTooMany, "too many attempts; try again later")
		return false
	}
	return true
}

// pollScript returns the device state and, when it is confirmed, deletes it in
// the same step: of any number of simultaneous polls exactly one sees
// "confirmed:...". (GET then DEL would let two of them both see it.)
var pollScript = valkey.NewLuaScript(`
local v = redis.call('GET', KEYS[1])
if not v then return false end
if string.sub(v, 1, 10) == 'confirmed:' then redis.call('DEL', KEYS[1]) end
return v`)

// readDeviceState returns the state under key, deleting it atomically when it
// is confirmed.
func readDeviceState(ctx context.Context, client valkey.Client, key string) (string, error) {
	return pollScript.Exec(ctx, client, []string{key}, nil).ToString()
}

// pollDeviceLogin answers {"status":"pending"} until the user confirmed, then
// once {"status":"complete","token":...}. An unknown, expired or already
// collected device code is 400 "expired_token" (the cases are not told apart).
// Polling again within the interval is 429.
func (s *server) pollDeviceLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceCode string `json:"device_code"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if !validDeviceCode(req.DeviceCode) {
		invalid(w, "device_code is malformed")
		return
	}
	hash := deviceHash(req.DeviceCode)
	ctx, cancel := context.WithTimeout(r.Context(), valkeyTimeout)
	defer cancel()
	b := s.Valkey.B()
	err := s.Valkey.Do(ctx, b.Set().Key(s.devicePollKey(hash)).Value("1").Nx().Px(s.DevicePollInterval).Build()).Error()
	if isNil(err) {
		w.Header().Set("Retry-After", strconv.FormatInt(s.pollSeconds(), 10))
		writeError(w, http.StatusTooManyRequests, codeTooMany, "polling too fast; wait for the interval")
		return
	}
	if err != nil {
		s.valkeyFailed(w, "rate-limit device poll", err)
		return
	}
	state, err := readDeviceState(ctx, s.Valkey, s.deviceKey(hash))
	if isNil(err) {
		writeError(w, http.StatusBadRequest, "expired_token", "the device code is unknown or has expired")
		return
	}
	if err != nil {
		s.valkeyFailed(w, "read device sign-in", err)
		return
	}
	rest, confirmed := strings.CutPrefix(state, confirmedPrefix)
	if !confirmed {
		writeJSON(w, http.StatusOK, map[string]any{"status": "pending", "interval": s.pollSeconds()})
		return
	}
	userID, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		s.internalError(w, r, errors.New("device sign-in state is corrupt"))
		return
	}
	principal, err := auth.Resolve(r.Context(), s.Pool, userID, viaToken)
	if errors.Is(err, auth.ErrDisabled) || errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusBadRequest, "expired_token", "the device code is unknown or has expired")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	plaintext, created, err := s.issueToken(r, principal, "yumdrake CLI", 0)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "complete", "token": plaintext, "token_id": created.ID, "expires_at": created.ExpiresAt,
	})
}

func isNil(err error) bool { return valkey.IsValkeyNil(err) }
