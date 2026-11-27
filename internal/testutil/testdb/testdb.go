//go:build integration

// Package testdb creates throwaway PostgreSQL databases for integration tests.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

const databaseURLVar = "YUMDRAKE_TEST_DATABASE_URL"

// New creates a uniquely named empty database on the server named by
// YUMDRAKE_TEST_DATABASE_URL, drops it (with FORCE) when the test ends and
// returns its URL.
func New(t *testing.T) string {
	t.Helper()
	adminURL := os.Getenv(databaseURLVar)
	if adminURL == "" {
		t.Fatalf("%s must be set for integration tests", databaseURLVar)
	}

	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("generate database name: %v", err)
	}
	name := "yumdrake_test_" + hex.EncodeToString(suffix)

	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect to admin database: %v", err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("create test database: %v", err)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		conn, err := pgx.Connect(ctx, adminURL)
		if err != nil {
			t.Errorf("connect to drop test database: %v", err)
			return
		}
		defer conn.Close(ctx)
		if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("drop test database: %v", err)
		}
	})

	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatalf("parse admin URL: %v", err)
	}
	u.Path = "/" + name
	return u.String()
}
