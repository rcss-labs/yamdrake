package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrate applies all embedded migrations under a PostgreSQL session lock so
// concurrent callers apply each migration once. It is a no-op when current.
func Migrate(ctx context.Context, url string) error {
	connConfig, err := pgx.ParseConfig(url)
	if err != nil {
		return redactedError("parse database URL", url, err)
	}
	sqlDB := stdlib.OpenDB(*connConfig)
	defer func() { _ = sqlDB.Close() }()

	migrations, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("open embedded migrations: %w", err)
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("create migration locker: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations, goose.WithSessionLocker(locker))
	if err != nil {
		return redactedError("create migration provider", url, err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return redactedError("apply migrations", url, err)
	}
	return nil
}

// LatestVersion returns the highest version among the embedded migration files.
func LatestVersion() int64 {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return 0
	}
	var latest int64
	for _, entry := range entries {
		prefix, _, found := strings.Cut(entry.Name(), "_")
		if !found {
			continue
		}
		if version, err := strconv.ParseInt(prefix, 10, 64); err == nil && version > latest {
			latest = version
		}
	}
	return latest
}
