package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// User is a person or service account. Source is "saml", "local" or "service".
type User struct {
	ID          int64
	Email       string
	Name        string
	Source      string
	Disabled    bool
	LastLoginAt *time.Time
	CreatedAt   time.Time
}

const userColumns = "id, email, name, source, disabled, last_login_at, created_at"

type rowScanner interface{ Scan(dest ...any) error }

func scanUser(r rowScanner) (User, error) {
	var u User
	err := r.Scan(&u.ID, &u.Email, &u.Name, &u.Source, &u.Disabled, &u.LastLoginAt, &u.CreatedAt)
	return u, err
}

func normalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// UpsertSAMLUser creates the user on first sign-in and otherwise updates the
// name and last login time. A local or service user with the same email is not
// taken over: the result is ErrDuplicate.
func UpsertSAMLUser(ctx context.Context, db DBTX, email, name string, at time.Time) (User, error) {
	u, _, err := UpsertSAMLUserCreated(ctx, db, email, name, at)
	return u, err
}

// UpsertSAMLUserCreated is UpsertSAMLUser that also reports whether this call
// inserted the row. The answer comes from the statement itself, so of two
// concurrent first sign-ins exactly one sees true.
func UpsertSAMLUserCreated(ctx context.Context, db DBTX, email, name string, at time.Time) (User, bool, error) {
	email = normalizeEmail(email)
	if email == "" {
		return User{}, false, fmt.Errorf("upsert saml user: %w: empty email", ErrInvalid)
	}
	var created bool
	row := db.QueryRow(ctx, `
INSERT INTO users (email, name, source, last_login_at) VALUES ($1, $2, 'saml', $3)
ON CONFLICT (lower(email)) DO UPDATE SET name = EXCLUDED.name, last_login_at = EXCLUDED.last_login_at
WHERE users.source = 'saml'
RETURNING `+userColumns+`, (xmax = 0)`, email, name, dbTime(at))
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.Source, &u.Disabled, &u.LastLoginAt, &u.CreatedAt, &created)
	if err != nil {
		if mapped := mapError(err); errors.Is(mapped, ErrNotFound) {
			// The conflicting row exists but is not a SAML user.
			return User{}, false, fmt.Errorf("upsert saml user: %w", ErrDuplicate)
		}
		return User{}, false, fmt.Errorf("upsert saml user: %w", mapError(err))
	}
	return u, created, nil
}

// RecordSignIn sets the user's last sign-in time. Every interactive sign-in
// (password or SAML) calls it; token use does not.
func RecordSignIn(ctx context.Context, db DBTX, userID int64, at time.Time) error {
	if _, err := db.Exec(ctx, "UPDATE users SET last_login_at = $2 WHERE id = $1", userID, dbTime(at)); err != nil {
		return fmt.Errorf("record sign-in: %w", mapError(err))
	}
	return nil
}

// CreateLocalUser creates a user that signs in with a password (source
// "local") or a service account (source "service"). An empty passwordHash is
// stored as no password. It returns ErrDuplicate for a taken email and
// ErrInvalid for any other source.
func CreateLocalUser(ctx context.Context, db DBTX, email, name, source, passwordHash string) (User, error) {
	email = normalizeEmail(email)
	if email == "" || (source != "local" && source != "service") {
		return User{}, fmt.Errorf("create user: %w: email and source local|service required", ErrInvalid)
	}
	var hash *string
	if passwordHash != "" {
		hash = &passwordHash
	}
	u, err := scanUser(db.QueryRow(ctx,
		"INSERT INTO users (email, name, source, password_hash) VALUES ($1, $2, $3, $4) RETURNING "+userColumns,
		email, name, source, hash))
	if err != nil {
		return User{}, fmt.Errorf("create user: %w", mapError(err))
	}
	return u, nil
}

func UserByEmail(ctx context.Context, db DBTX, email string) (User, error) {
	u, err := scanUser(db.QueryRow(ctx, "SELECT "+userColumns+" FROM users WHERE lower(email) = $1", normalizeEmail(email)))
	if err != nil {
		return User{}, fmt.Errorf("user by email: %w", mapError(err))
	}
	return u, nil
}

func UserByID(ctx context.Context, db DBTX, id int64) (User, error) {
	u, err := scanUser(db.QueryRow(ctx, "SELECT "+userColumns+" FROM users WHERE id = $1", id))
	if err != nil {
		return User{}, fmt.Errorf("user by id: %w", mapError(err))
	}
	return u, nil
}

// PasswordHash returns the stored hash of a local or service user. It returns
// ErrNotFound for an unknown user, a SAML user or a user without a password, so
// callers cannot tell those cases apart.
func PasswordHash(ctx context.Context, db DBTX, userID int64) (string, error) {
	var hash *string
	err := db.QueryRow(ctx, "SELECT password_hash FROM users WHERE id = $1 AND source IN ('local', 'service')", userID).Scan(&hash)
	if err != nil {
		return "", fmt.Errorf("password hash: %w", mapError(err))
	}
	if hash == nil {
		return "", fmt.Errorf("password hash: %w", ErrNotFound)
	}
	return *hash, nil
}

// SetDisabled returns ErrNotFound for an unknown user.
func SetDisabled(ctx context.Context, db DBTX, userID int64, disabled bool) error {
	tag, err := db.Exec(ctx, "UPDATE users SET disabled = $2 WHERE id = $1", userID, disabled)
	if err != nil {
		return fmt.Errorf("set disabled: %w", mapError(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("set disabled: %w", ErrNotFound)
	}
	return nil
}

// ListUsers returns users ordered by email.
func ListUsers(ctx context.Context, db DBTX) ([]User, error) {
	rows, err := db.Query(ctx, "SELECT "+userColumns+" FROM users ORDER BY email")
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
