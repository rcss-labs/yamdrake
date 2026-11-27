package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rcss-labs/yamdrake/internal/store"
)

const (
	tokenPrefix     = "ymd_"
	tokenBytes      = 32
	defaultTokenTTL = 90 * 24 * time.Hour
	maxTokenTTL     = 365 * 24 * time.Hour
	// touchInterval bounds how often a token's last use is written, so a busy
	// client does not turn every request into a database write.
	touchInterval = time.Minute
)

var (
	// ErrBadToken is returned for every token that cannot be used: malformed,
	// unknown, expired or revoked. The cases are deliberately indistinguishable.
	ErrBadToken = errors.New("invalid token")
	// ErrTokenCheckFailed wraps a database failure while checking a token. It
	// is not ErrBadToken: the caller should answer 5xx, not 401.
	ErrTokenCheckFailed = errors.New("token check failed")
	// ErrInvalidTTL is returned by NewToken for a negative lifetime.
	ErrInvalidTTL = errors.New("token lifetime must not be negative")
)

func hashTokenSecret(secret []byte) []byte {
	sum := sha256.Sum256(secret)
	return sum[:]
}

// NewToken creates an API token for userID and returns its plaintext, which
// exists only in the return value: ymd_ followed by the base64url of 32 random
// bytes. Only the SHA-256 of those bytes is stored. A zero ttl means 90 days, a
// ttl above 365 days is reduced to 365 days, and a negative ttl is
// ErrInvalidTTL (rather than silently becoming the default).
func NewToken(ctx context.Context, db store.DBTX, userID int64, name string, ttl time.Duration, now time.Time) (string, store.Token, error) {
	switch {
	case ttl < 0:
		return "", store.Token{}, ErrInvalidTTL
	case ttl == 0:
		ttl = defaultTokenTTL
	case ttl > maxTokenTTL:
		ttl = maxTokenTTL
	}
	secret := make([]byte, tokenBytes)
	if _, err := rand.Read(secret); err != nil {
		return "", store.Token{}, fmt.Errorf("generate token: %w", err)
	}
	tok, err := store.CreateToken(ctx, db, userID, name, hashTokenSecret(secret), now.Add(ttl))
	if err != nil {
		return "", store.Token{}, fmt.Errorf("create token: %w", err)
	}
	return tokenPrefix + base64.RawURLEncoding.EncodeToString(secret), tok, nil
}

// VerifyToken returns the owner of a usable token. Malformed, unknown, expired
// and revoked tokens all return ErrBadToken. A database failure returns an
// error wrapping ErrTokenCheckFailed instead, so an outage is never reported
// to the client as a bad credential.
//
// The token is found by an exact lookup of its SHA-256 on a unique index. The
// caller controls only the plaintext, so it cannot steer the hash towards a
// stored value byte by byte; timing of the lookup therefore reveals nothing
// useful and no further comparison of hashes is needed.
func VerifyToken(ctx context.Context, db store.DBTX, plaintext string, now time.Time) (int64, error) {
	body, ok := strings.CutPrefix(plaintext, tokenPrefix)
	if !ok {
		return 0, ErrBadToken
	}
	// Strict decoding refuses non-canonical encodings so one secret has one text.
	secret, err := base64.RawURLEncoding.Strict().DecodeString(body)
	if err != nil || len(secret) != tokenBytes {
		return 0, ErrBadToken
	}
	tok, err := store.TokenByHash(ctx, db, hashTokenSecret(secret))
	if errors.Is(err, store.ErrNotFound) {
		return 0, ErrBadToken
	}
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrTokenCheckFailed, err)
	}
	if tok.RevokedAt != nil || (tok.ExpiresAt != nil && !now.Before(*tok.ExpiresAt)) {
		return 0, ErrBadToken
	}
	if tok.LastUsedAt == nil || now.Sub(*tok.LastUsedAt) >= touchInterval {
		// Best effort: failing to note the use must not fail a valid request.
		_ = store.TouchToken(ctx, db, tok.ID, now)
	}
	return tok.UserID, nil
}
