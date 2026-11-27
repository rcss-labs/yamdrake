package store

import (
	"context"
	"fmt"
	"time"
)

// Token is an API token. Only its hash is stored, so the secret is never here.
type Token struct {
	ID         int64
	UserID     int64
	Name       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
}

const tokenColumns = "id, user_id, name, created_at, last_used_at, expires_at, revoked_at" //nolint:gosec // column names, not a credential

func scanToken(r rowScanner) (Token, error) {
	var t Token
	err := r.Scan(&t.ID, &t.UserID, &t.Name, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt, &t.RevokedAt)
	return t, err
}

// CreateToken stores a token by its hash. A zero expiresAt means it does not
// expire. ErrDuplicate for a hash already stored, ErrInvalidReference for an
// unknown user.
func CreateToken(ctx context.Context, db DBTX, userID int64, name string, hash []byte, expiresAt time.Time) (Token, error) {
	var expires *time.Time
	if !expiresAt.IsZero() {
		e := dbTime(expiresAt)
		expires = &e
	}
	t, err := scanToken(db.QueryRow(ctx,
		"INSERT INTO api_tokens (user_id, name, hash, expires_at) VALUES ($1, $2, $3, $4) RETURNING "+tokenColumns,
		userID, name, hash, expires))
	if err != nil {
		return Token{}, fmt.Errorf("create token: %w", mapError(err))
	}
	return t, nil
}

// TokenByHash finds a token including revoked and expired ones; the caller
// decides what those mean. ErrNotFound when no token has the hash.
func TokenByHash(ctx context.Context, db DBTX, hash []byte) (Token, error) {
	t, err := scanToken(db.QueryRow(ctx, "SELECT "+tokenColumns+" FROM api_tokens WHERE hash = $1", hash))
	if err != nil {
		return Token{}, fmt.Errorf("token by hash: %w", mapError(err))
	}
	return t, nil
}

func TouchToken(ctx context.Context, db DBTX, id int64, at time.Time) error {
	tag, err := db.Exec(ctx, "UPDATE api_tokens SET last_used_at = $2 WHERE id = $1", id, dbTime(at))
	if err != nil {
		return fmt.Errorf("touch token: %w", mapError(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("touch token: %w", ErrNotFound)
	}
	return nil
}

// RevokeToken revokes a token owned by userID. A token of another user is
// ErrNotFound, indistinguishable from an unknown one. Revoking twice keeps the
// first revocation time.
func RevokeToken(ctx context.Context, db DBTX, id, userID int64, at time.Time) error {
	tag, err := db.Exec(ctx,
		"UPDATE api_tokens SET revoked_at = coalesce(revoked_at, $3) WHERE id = $1 AND user_id = $2",
		id, userID, dbTime(at))
	if err != nil {
		return fmt.Errorf("revoke token: %w", mapError(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("revoke token: %w", ErrNotFound)
	}
	return nil
}

// ListTokens returns the user's tokens, newest first.
func ListTokens(ctx context.Context, db DBTX, userID int64) ([]Token, error) {
	rows, err := db.Query(ctx, "SELECT "+tokenColumns+" FROM api_tokens WHERE user_id = $1 ORDER BY id DESC", userID)
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, fmt.Errorf("scan token: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeAllTokens revokes every token of the user that is not revoked yet and
// returns how many it revoked. Earlier revocation times are kept.
func RevokeAllTokens(ctx context.Context, db DBTX, userID int64, at time.Time) (int64, error) {
	tag, err := db.Exec(ctx, "UPDATE api_tokens SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL", userID, dbTime(at))
	if err != nil {
		return 0, fmt.Errorf("revoke all tokens: %w", mapError(err))
	}
	return tag.RowsAffected(), nil
}
