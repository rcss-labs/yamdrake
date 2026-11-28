//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/rcss-labs/yumdrake/internal/auth"
	"github.com/rcss-labs/yumdrake/internal/db"
	"github.com/rcss-labs/yumdrake/internal/store"
	"github.com/rcss-labs/yumdrake/internal/testutil/testdb"
)

const adminPassword = "a perfectly good passphrase"

func uniqueEmail(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("random: %v", err)
	}
	return fmt.Sprintf("admin-%x@example.test", raw)
}

type adminRun struct {
	code           int
	stdout, stderr string
}

// runCreateAdmin runs the subcommand with stdin as the password source.
func runCreateAdmin(t *testing.T, env map[string]string, stdin io.Reader, extra ...string) adminRun {
	t.Helper()
	var stdout, stderr bytes.Buffer
	args := append([]string{"yumdrake-orchestrator", "create-admin"}, extra...)
	code := run(context.Background(), args, envFrom(env), stdin, &stdout, &stderr)
	return adminRun{code, stdout.String(), stderr.String()}
}

func adminEnv(t *testing.T) (map[string]string, string) {
	t.Helper()
	dbURL := testdb.New(t)
	if err := db.Migrate(context.Background(), dbURL); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return baseEnv(t, dbURL, freeAddr(t)), dbURL
}

func openPool(t *testing.T, dbURL string) store.DBTX {
	t.Helper()
	pool, err := db.Open(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestCreateAdminReadsPasswordFromStdin(t *testing.T) {
	env, dbURL := adminEnv(t)
	email := uniqueEmail(t)

	// A real pipe, as in `echo secret | yumdrake-orchestrator create-admin ...`.
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer func() { _ = reader.Close() }()
	go func() {
		_, _ = writer.WriteString(adminPassword + "\n")
		_ = writer.Close()
	}()
	got := runCreateAdmin(t, env, reader, "--email", strings.ToUpper(email), "--name", "Ada Admin")
	if got.code != 0 || got.stdout != "created\n" || got.stderr != "" {
		t.Fatalf("create-admin = %d, stdout %q, stderr %q; want 0, \"created\\n\" and nothing else", got.code, got.stdout, got.stderr)
	}

	ctx := context.Background()
	pool := openPool(t, dbURL)
	user, err := store.UserByEmail(ctx, pool, email)
	if err != nil || user.Source != "local" || user.Name != "Ada Admin" || user.Disabled || user.Email != email {
		t.Fatalf("user = %+v, %v; want an enabled local user with a lower-cased email", user, err)
	}
	hash, err := store.PasswordHash(ctx, pool, user.ID)
	if err != nil {
		t.Fatalf("PasswordHash: %v", err)
	}
	if ok, err := auth.VerifyPassword(hash, adminPassword); err != nil || !ok {
		t.Fatalf("the stored hash does not verify the password (ok=%v, err=%v)", ok, err)
	}
	principal, err := auth.Resolve(ctx, pool, user.ID, "session")
	if err != nil || !principal.Admin {
		t.Fatalf("principal = %+v, %v; want a global admin", principal, err)
	}
	groups, _ := store.GroupsOfUser(ctx, pool, user.ID)
	if len(groups) != 1 || groups[0].Name != "Administrators" || !groups[0].Admin {
		t.Fatalf("groups = %+v, want the admin group Administrators", groups)
	}

	entries, err := store.ListAudit(ctx, pool, 0, 20)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	var created bool
	for _, e := range entries {
		created = created || (e.Action == "user.create" && strings.Contains(string(e.Detail), email))
		if strings.Contains(string(e.Detail), adminPassword) || strings.Contains(string(e.Detail), "argon2") {
			t.Errorf("audit entry %s holds a secret", e.Action)
		}
	}
	if !created {
		t.Errorf("no user.create audit entry (have %d entries)", len(entries))
	}

	// A second admin joins the same group instead of making another one.
	second := runCreateAdmin(t, env, strings.NewReader(adminPassword+"\n"), "--email", uniqueEmail(t), "--name", "Bob")
	if second.code != 0 {
		t.Fatalf("second create-admin = %d (stderr %q)", second.code, second.stderr)
	}
	groups, _ = store.ListGroups(ctx, pool)
	if len(groups) != 1 {
		t.Errorf("groups after two admins = %+v, want one", groups)
	}
}

func TestCreateAdminRefusesExistingEmail(t *testing.T) {
	env, dbURL := adminEnv(t)
	email := uniqueEmail(t)
	first := runCreateAdmin(t, env, strings.NewReader(adminPassword+"\n"), "--email", email, "--name", "First")
	if first.code != 0 {
		t.Fatalf("first create-admin = %d (stderr %q)", first.code, first.stderr)
	}
	again := runCreateAdmin(t, env, strings.NewReader("another passphrase here\n"), "--email", strings.ToUpper(email), "--name", "Second")
	if again.code != 1 || again.stdout != "" || !strings.Contains(again.stderr, "already exists") {
		t.Fatalf("second create-admin = %d, stdout %q, stderr %q; want 1 and \"already exists\"", again.code, again.stdout, again.stderr)
	}
	ctx := context.Background()
	pool := openPool(t, dbURL)
	user, _ := store.UserByEmail(ctx, pool, email)
	if user.Name != "First" {
		t.Errorf("the existing user was changed: %+v", user)
	}
	hash, _ := store.PasswordHash(ctx, pool, user.ID)
	if ok, _ := auth.VerifyPassword(hash, adminPassword); !ok {
		t.Error("the existing user's password was changed")
	}
}

func TestCreateAdminRejectsShortPassword(t *testing.T) {
	env, dbURL := adminEnv(t)
	email := uniqueEmail(t)
	got := runCreateAdmin(t, env, strings.NewReader("short\n"), "--email", email, "--name", "Ada")
	if got.code != 1 || got.stdout != "" || !strings.Contains(got.stderr, "12 characters") {
		t.Fatalf("create-admin = %d, stdout %q, stderr %q; want 1 and the length rule", got.code, got.stdout, got.stderr)
	}
	if strings.Contains(got.stderr, "short") && !strings.Contains(got.stderr, "shorter") {
		t.Errorf("stderr repeats the password: %q", got.stderr)
	}
	if _, err := store.UserByEmail(context.Background(), openPool(t, dbURL), email); err == nil {
		t.Error("a user was created with a weak password")
	}
}

func TestCreateAdminUsageErrorsExitTwo(t *testing.T) {
	env, _ := adminEnv(t)
	password := func() io.Reader { return strings.NewReader(adminPassword + "\n") }
	for name, tc := range map[string]struct {
		stdin io.Reader
		args  []string
	}{
		"no email":          {password(), []string{"--name", "Ada"}},
		"no name":           {password(), []string{"--email", "a@example.test"}},
		"unknown flag":      {password(), []string{"--email", "a@example.test", "--name", "Ada", "--password", "x"}},
		"stray argument":    {password(), []string{"--email", "a@example.test", "--name", "Ada", "extra"}},
		"no password given": {strings.NewReader(""), []string{"--email", "a@example.test", "--name", "Ada"}},
	} {
		got := runCreateAdmin(t, env, tc.stdin, tc.args...)
		if got.code != 2 || got.stdout != "" || got.stderr == "" {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want 2, nothing on stdout, and usage text", name, got.code, got.stdout, got.stderr)
		}
	}
	if strings.Contains(runCreateAdmin(t, env, password(), "--password", "leaked-secret-1234").stderr, "leaked-secret-1234") {
		t.Error("usage text repeats a password given as a flag")
	}
	// Without a database URL the configuration is wrong: usage error as well.
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"yumdrake-orchestrator", "create-admin", "--email", "a@example.test", "--name", "Ada"},
		envFrom(nil), password(), &stdout, &stderr); code != 2 {
		t.Errorf("create-admin without configuration = %d, want 2", code)
	}
}
