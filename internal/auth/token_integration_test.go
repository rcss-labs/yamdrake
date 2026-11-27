//go:build integration

package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rcss-labs/yamdrake/internal/db"
	"github.com/rcss-labs/yamdrake/internal/store"
	"github.com/rcss-labs/yamdrake/internal/testutil/testdb"
)

func newTokenPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	url := testdb.New(t)
	if err := db.Migrate(ctx, url); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func tokenUser(t *testing.T, pool store.DBTX) store.User {
	t.Helper()
	u, err := store.CreateLocalUser(context.Background(), pool,
		fmt.Sprintf("tok-%d@example.com", time.Now().UnixNano()), "Tok", "local", "hash")
	if err != nil {
		t.Fatalf("CreateLocalUser: %v", err)
	}
	return u
}

func TestNewTokenFormatAndOnlyHashStored(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newTokenPool(t)
	u := tokenUser(t, pool)
	now := time.Now()

	plaintext, tok, err := NewToken(ctx, pool, u.ID, "ci", time.Hour, now)
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if !strings.HasPrefix(plaintext, "ymd_") {
		t.Fatalf("token does not start with ymd_")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(plaintext, "ymd_"))
	if err != nil || len(raw) != 32 {
		t.Fatalf("token body decodes to %d bytes, err %v; want 32 bytes of base64url", len(raw), err)
	}
	if tok.UserID != u.ID || tok.Name != "ci" || tok.ExpiresAt == nil {
		t.Fatalf("token row = %+v", tok)
	}
	sum := sha256.Sum256(raw)
	stored, err := store.TokenByHash(ctx, pool, sum[:])
	if err != nil || stored.ID != tok.ID {
		t.Fatalf("lookup by SHA-256 of the secret = %+v, %v", stored, err)
	}

	// Neither the plaintext nor its body appears anywhere in the table.
	var leaks int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM api_tokens WHERE hash = $1 OR name = $2 OR hash = $3`,
		[]byte(plaintext), plaintext, raw).Scan(&leaks)
	if err != nil || leaks != 0 {
		t.Fatalf("plaintext or raw secret stored: count %d, err %v", leaks, err)
	}
	// Two tokens differ.
	other, _, err := NewToken(ctx, pool, u.ID, "ci2", time.Hour, now)
	if err != nil || other == plaintext {
		t.Fatalf("second token equal to first or failed: %v", err)
	}
}

func TestTTLDefaultAndCap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newTokenPool(t)
	u := tokenUser(t, pool)
	now := time.Now().Truncate(time.Microsecond)
	day := 24 * time.Hour

	for name, c := range map[string]struct {
		ttl  time.Duration
		want time.Duration
	}{
		"zero defaults to 90 days":    {0, 90 * day},
		"explicit one day":            {day, day},
		"exactly 365 days":            {365 * day, 365 * day},
		"above the cap is clamped":    {1000 * day, 365 * day},
		"huge duration is clamped":    {time.Duration(1<<63 - 1), 365 * day},
		"short ttl is kept":           {time.Minute, time.Minute},
		"just above the cap is clamp": {365*day + time.Nanosecond, 365 * day},
	} {
		_, tok, err := NewToken(ctx, pool, u.ID, name, c.ttl, now)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if tok.ExpiresAt == nil || !tok.ExpiresAt.Equal(now.Add(c.want)) {
			t.Errorf("%s: expires %v, want %v", name, tok.ExpiresAt, now.Add(c.want))
		}
	}

	if _, _, err := NewToken(ctx, pool, u.ID, "negative", -time.Second, now); !errors.Is(err, ErrInvalidTTL) {
		t.Fatalf("negative ttl error = %v, want ErrInvalidTTL", err)
	}
	if list, err := store.ListTokens(ctx, pool, u.ID); err != nil || len(list) != 7 {
		t.Fatalf("tokens after a refused ttl = %d, %v; want 7", len(list), err)
	}
}

func TestVerifyToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newTokenPool(t)
	u := tokenUser(t, pool)
	now := time.Now().Truncate(time.Microsecond)

	valid, _, err := NewToken(ctx, pool, u.ID, "valid", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	expired, _, err := NewToken(ctx, pool, u.ID, "expired", time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	revoked, revokedRow, err := NewToken(ctx, pool, u.ID, "revoked", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeToken(ctx, pool, revokedRow.ID, u.ID, now); err != nil {
		t.Fatal(err)
	}
	// A well-formed token nobody stored.
	unknown := "ymd_" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	body := strings.TrimPrefix(valid, "ymd_")

	cases := []struct {
		name      string
		plaintext string
		at        time.Time
		ok        bool
	}{
		{"valid", valid, now.Add(time.Second), true},
		{"unknown", unknown, now, false},
		{"malformed without prefix", body, now, false},
		{"malformed other prefix", "xyz_" + body, now, false},
		{"malformed not base64", "ymd_!!!not-base64!!!", now, false},
		{"malformed too short", "ymd_" + base64.RawURLEncoding.EncodeToString([]byte("short")), now, false},
		{"malformed trailing padding", valid + "=", now, false},
		{"malformed too long", valid + "AAAA", now, false},
		{"empty", "", now, false},
		{"prefix only", "ymd_", now, false},
		{"expired", expired, now.Add(time.Minute), false},
		{"revoked", revoked, now, false},
	}
	var failureText string
	for _, c := range cases {
		id, err := VerifyToken(ctx, pool, c.plaintext, c.at)
		if c.ok {
			if err != nil || id != u.ID {
				t.Errorf("%s: id %d, err %v; want user %d", c.name, id, err, u.ID)
			}
			continue
		}
		if !errors.Is(err, ErrBadToken) || id != 0 {
			t.Errorf("%s: id %d, err %v; want ErrBadToken", c.name, id, err)
			continue
		}
		if failureText == "" {
			failureText = err.Error()
		} else if err.Error() != failureText {
			t.Errorf("%s: error text differs from the other failures", c.name)
		}
	}
	// Still valid just before expiry.
	if _, err := VerifyToken(ctx, pool, expired, now.Add(time.Minute-time.Second)); err != nil {
		t.Errorf("token one second before expiry: %v", err)
	}
}

func TestVerifyTokenTouchesLastUsedAtAtMostOncePerMinute(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newTokenPool(t)
	u := tokenUser(t, pool)
	now := time.Now().Truncate(time.Microsecond)
	plaintext, row, err := NewToken(ctx, pool, u.ID, "touch", 24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	lastUsed := func() *time.Time {
		t.Helper()
		list, err := store.ListTokens(ctx, pool, u.ID)
		if err != nil || len(list) != 1 || list[0].ID != row.ID {
			t.Fatalf("ListTokens = %+v, %v", list, err)
		}
		return list[0].LastUsedAt
	}
	if lastUsed() != nil {
		t.Fatal("new token already has a last use")
	}
	steps := []struct {
		at   time.Time
		want time.Time // expected LastUsedAt afterwards
	}{
		{now.Add(time.Second), now.Add(time.Second)},                             // first use is recorded
		{now.Add(30 * time.Second), now.Add(time.Second)},                        // 29 s later: not rewritten
		{now.Add(time.Second + time.Minute), now.Add(time.Second + time.Minute)}, // exactly a minute: rewritten
		{now.Add(time.Second + time.Minute + 59*time.Second), now.Add(time.Second + time.Minute)},
	}
	for i, s := range steps {
		if _, err := VerifyToken(ctx, pool, plaintext, s.at); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		got := lastUsed()
		if got == nil || !got.Equal(s.want) {
			t.Fatalf("step %d: LastUsedAt = %v, want %v", i, got, s.want)
		}
	}
}

func TestVerifyTokenDatabaseFailureIsNotABadToken(t *testing.T) {
	t.Parallel()
	pool := newTokenPool(t)
	u := tokenUser(t, pool)
	now := time.Now()
	plaintext, _, err := NewToken(context.Background(), pool, u.ID, "x", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	pool.Close() // every later query fails with a connection-level error
	_, err = VerifyToken(context.Background(), pool, plaintext, now)
	if err == nil || errors.Is(err, ErrBadToken) {
		t.Fatalf("error = %v, want a database failure that is not ErrBadToken", err)
	}
	if !errors.Is(err, ErrTokenCheckFailed) {
		t.Fatalf("error = %v, want it to wrap ErrTokenCheckFailed", err)
	}
	if strings.Contains(err.Error(), plaintext) {
		t.Fatal("error text contains the token")
	}
}
