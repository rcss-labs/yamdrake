// Package session implements server-side sign-in sessions stored in Valkey,
// the session cookie, and the same-origin check for state-changing requests.
//
// Session ids are bearer secrets: they are never logged or placed in errors.
package session

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	valkey "github.com/valkey-io/valkey-go"
)

const (
	// CookieName is the name of the session cookie.
	CookieName = "yumdrake_session"
	// IdleTimeout is how long a session survives without being used.
	IdleTimeout = 8 * time.Hour
	// MaxAge is the longest a session lives, regardless of activity.
	MaxAge = 24 * time.Hour

	idBytes  = 32
	idLength = 43 // base64url (unpadded) length of idBytes
)

var (
	// ErrNoSession means the id is unknown, expired (idle or maximum age) or malformed.
	ErrNoSession = errors.New("session: no such session")
	// ErrUnavailable means Valkey could not be reached or answered with an error.
	// Callers must treat it as neither "signed in" nor "signed out".
	ErrUnavailable = errors.New("session: store unavailable")
)

// Session is a signed-in browser session.
type Session struct {
	ID         string
	UserID     int64
	CreatedAt  time.Time
	LastSeenAt time.Time
}

// stored is the JSON document kept under session:<id>. The id is the key, not part of the value.
type stored struct {
	UserID     int64     `json:"user_id"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

// Manager creates, reads and deletes sessions.
type Manager struct {
	client valkey.Client
	now    func() time.Time
	prefix string // in front of every key; empty in production

	// afterListingUserSessions is a test seam called between reading a user's
	// session ids and deleting them, to interleave a concurrent Create.
	afterListingUserSessions func()
}

// Option adjusts a Manager.
type Option func(*Manager)

// WithKeyPrefix puts prefix in front of every Valkey key the Manager uses.
func WithKeyPrefix(prefix string) Option { return func(m *Manager) { m.prefix = prefix } }

// NewManager returns a Manager using c. now is injectable for tests.
func NewManager(c valkey.Client, now func() time.Time, opts ...Option) *Manager {
	m := &Manager{client: c, now: now}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func (m *Manager) sessionKey(id string) string { return m.prefix + "session:" + id }
func (m *Manager) userSessionsKey(userID int64) string {
	return m.prefix + "user-sessions:" + strconv.FormatInt(userID, 10)
}

func unavailable(err error) error {
	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}

func validID(id string) bool {
	if len(id) != idLength {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(id)
	return err == nil && len(raw) == idBytes
}

// Create starts a session for userID.
func (m *Manager) Create(ctx context.Context, userID int64) (Session, error) {
	raw := make([]byte, idBytes)
	if _, err := rand.Read(raw); err != nil {
		return Session{}, fmt.Errorf("session: generate id: %w", err)
	}
	now := m.now()
	s := Session{
		ID:         base64.RawURLEncoding.EncodeToString(raw),
		UserID:     userID,
		CreatedAt:  now,
		LastSeenAt: now,
	}
	doc, err := json.Marshal(stored{UserID: userID, CreatedAt: now, LastSeenAt: now})
	if err != nil {
		return Session{}, fmt.Errorf("session: encode: %w", err)
	}
	b := m.client.B()
	setKey := m.userSessionsKey(userID)
	results := m.client.DoMulti(ctx,
		b.Set().Key(m.sessionKey(s.ID)).Value(string(doc)).Px(IdleTimeout).Build(),
		b.Sadd().Key(setKey).Member(s.ID).Build(),
		// No session outlives MaxAge, so the set may expire that long after its latest addition.
		b.Pexpire().Key(setKey).Milliseconds(MaxAge.Milliseconds()).Build(),
	)
	for _, res := range results {
		if err := res.Error(); err != nil {
			return Session{}, unavailable(err)
		}
	}
	return s, nil
}

// Get returns the session and refreshes its last-seen time. It returns
// ErrNoSession when the id is malformed, unknown, idle for 8h or older than 24h.
func (m *Manager) Get(ctx context.Context, id string) (Session, error) {
	if !validID(id) {
		return Session{}, ErrNoSession
	}
	key := m.sessionKey(id)
	doc, err := m.client.Do(ctx, m.client.B().Get().Key(key).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return Session{}, ErrNoSession
	}
	if err != nil {
		return Session{}, unavailable(err)
	}
	var st stored
	if err := json.Unmarshal([]byte(doc), &st); err != nil {
		// A corrupt record cannot authenticate anyone.
		return Session{}, ErrNoSession
	}
	now := m.now()
	if now.Sub(st.LastSeenAt) > IdleTimeout || now.Sub(st.CreatedAt) > MaxAge {
		return Session{}, ErrNoSession
	}

	st.LastSeenAt = now
	updated, err := json.Marshal(st)
	if err != nil {
		return Session{}, fmt.Errorf("session: encode: %w", err)
	}
	// The key must not outlive the maximum age.
	ttl := min(IdleTimeout, st.CreatedAt.Add(MaxAge).Sub(now))
	if ttl < time.Millisecond {
		return Session{}, ErrNoSession
	}
	// XX: do not resurrect a session deleted since the read above.
	setErr := m.client.Do(ctx, m.client.B().Set().Key(key).Value(string(updated)).Xx().Px(ttl).Build()).Error()
	if valkey.IsValkeyNil(setErr) {
		return Session{}, ErrNoSession
	}
	if setErr != nil {
		return Session{}, unavailable(setErr)
	}
	return Session{
		ID:         id,
		UserID:     st.UserID,
		CreatedAt:  st.CreatedAt,
		LastSeenAt: now,
	}, nil
}

// Delete removes one session. Deleting an absent session is not an error.
func (m *Manager) Delete(ctx context.Context, id string) error {
	if !validID(id) {
		return nil
	}
	key := m.sessionKey(id)
	doc, err := m.client.Do(ctx, m.client.B().Get().Key(key).Build()).ToString()
	if err != nil && !valkey.IsValkeyNil(err) {
		return unavailable(err)
	}
	b := m.client.B()
	cmds := valkey.Commands{b.Del().Key(key).Build()}
	if err == nil {
		var st stored
		if json.Unmarshal([]byte(doc), &st) == nil {
			cmds = append(cmds, b.Srem().Key(m.userSessionsKey(st.UserID)).Member(id).Build())
		}
	}
	for _, res := range m.client.DoMulti(ctx, cmds...) {
		if err := res.Error(); err != nil {
			return unavailable(err)
		}
	}
	return nil
}

// DeleteAllForUser removes every session of userID. The user's set may list
// sessions that already expired; those are harmless to delete again.
func (m *Manager) DeleteAllForUser(ctx context.Context, userID int64) error {
	setKey := m.userSessionsKey(userID)
	ids, err := m.client.Do(ctx, m.client.B().Smembers().Key(setKey).Build()).AsStrSlice()
	if err != nil && !valkey.IsValkeyNil(err) {
		return unavailable(err)
	}
	if m.afterListingUserSessions != nil {
		m.afterListingUserSessions()
	}
	b := m.client.B()
	cmds := make(valkey.Commands, 0, len(ids)+1)
	for _, id := range ids {
		if validID(id) {
			cmds = append(cmds, b.Del().Key(m.sessionKey(id)).Build())
		}
	}
	// Remove only the ids read above rather than deleting the whole set: a
	// session created after SMEMBERS must stay listed so a later call finds it.
	// An emptied set disappears on its own.
	if len(ids) > 0 {
		cmds = append(cmds, b.Srem().Key(setKey).Member(ids...).Build())
	}
	for _, res := range m.client.DoMulti(ctx, cmds...) {
		if err := res.Error(); err != nil {
			return unavailable(err)
		}
	}
	return nil
}

// SetCookie sends the session cookie for s.
func SetCookie(w http.ResponseWriter, s Session) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    s.ID,
		Path:     "/",
		Expires:  s.CreatedAt.Add(MaxAge),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearCookie tells the browser to drop the session cookie.
func ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// IDFromRequest returns the session id from the cookie. A missing or
// malformed value reports false, so no Valkey call is wasted on it.
func IDFromRequest(r *http.Request) (string, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil || !validID(c.Value) {
		return "", false
	}
	return c.Value, true
}

// SameOrigin reports whether r may change state. Safe methods always pass.
// Otherwise the Origin header must match publicURL's scheme and host (port
// included); with no Origin, Sec-Fetch-Site must be "same-origin".
func SameOrigin(r *http.Request, publicURL string) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		want, err := url.Parse(publicURL)
		if err != nil || want.Scheme == "" || want.Host == "" {
			return false
		}
		got, err := url.Parse(origin) // "null" parses to an empty host and never matches
		if err != nil || got.Host == "" {
			return false
		}
		return strings.EqualFold(got.Scheme, want.Scheme) && strings.EqualFold(got.Host, want.Host)
	}
	return r.Header.Get("Sec-Fetch-Site") == "same-origin"
}
