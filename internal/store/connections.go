package store

import (
	"context"
	"fmt"
	"time"
)

// Connection describes a GitHub connection. The secret is never part of it.
type Connection struct {
	ID             int64
	Owner, Method  string // Method: "app" | "token"
	AppID          int64
	InstallationID int64
	CreatedAt      time.Time
}

const connectionColumns = "id, owner, method, app_id, installation_id, created_at"

func scanConnection(row interface{ Scan(...any) error }, extra ...any) (Connection, error) {
	var c Connection
	dest := append([]any{&c.ID, &c.Owner, &c.Method, &c.AppID, &c.InstallationID, &c.CreatedAt}, extra...)
	err := row.Scan(dest...)
	return c, err
}

// CreateConnection stores a connection with its already-encrypted secret.
// It returns ErrDuplicate when the owner already has a connection.
func CreateConnection(ctx context.Context, db DBTX, c Connection, secretCiphertext []byte) (Connection, error) {
	row := db.QueryRow(ctx,
		`INSERT INTO github_connections (owner, method, app_id, installation_id, secret_ciphertext)
		 VALUES ($1, $2, $3, $4, $5) RETURNING `+connectionColumns,
		c.Owner, c.Method, c.AppID, c.InstallationID, secretCiphertext)
	created, err := scanConnection(row)
	if err != nil {
		return Connection{}, fmt.Errorf("create connection: %w", mapError(err))
	}
	return created, nil
}

// ListConnections returns all connections ordered by owner, without secrets.
func ListConnections(ctx context.Context, db DBTX) ([]Connection, error) {
	rows, err := db.Query(ctx, "SELECT "+connectionColumns+" FROM github_connections ORDER BY owner")
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, fmt.Errorf("scan connection: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	return out, nil
}

// ConnectionSecret returns the connection for owner and its stored ciphertext.
// It returns ErrNotFound when the owner has no connection.
func ConnectionSecret(ctx context.Context, db DBTX, owner string) (Connection, []byte, error) {
	var secret []byte
	row := db.QueryRow(ctx,
		"SELECT "+connectionColumns+", secret_ciphertext FROM github_connections WHERE owner = $1", owner)
	c, err := scanConnection(row, &secret)
	if err != nil {
		return Connection{}, nil, fmt.Errorf("get connection secret: %w", mapError(err))
	}
	return c, secret, nil
}
