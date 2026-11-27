package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/yamdrake/internal/app"
	"github.com/rcss-labs/yamdrake/internal/audit"
	"github.com/rcss-labs/yamdrake/internal/auth"
	"github.com/rcss-labs/yamdrake/internal/config"
	"github.com/rcss-labs/yamdrake/internal/db"
	"github.com/rcss-labs/yamdrake/internal/store"
)

const (
	adminGroupName = "Administrators"
	createAdminUse = `usage: yumdrake-orchestrator create-admin --email <email> --name <name>

Creates a local user in the admin group "Administrators" (made if it does not
exist). The password is read from standard input, one line, never from an
argument or the environment:

  printf '%s\n' "$PASSWORD" | yumdrake-orchestrator create-admin --email a@example.com --name "Ada"

The database is configured through the same YUMDRAKE_* variables as the
service. Prints "created" on success. Exit codes: 0 created, 1 failure, 2 usage.
`
)

// createAdmin implements the create-admin subcommand and returns its exit code.
func createAdmin(ctx context.Context, args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("create-admin", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = io.WriteString(stderr, createAdminUse) }
	email := flags.String("email", "", "email address of the new admin")
	name := flags.String("name", "", "display name of the new admin")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return app.ExitOK
		}
		return app.ExitUsage
	}
	if flags.NArg() > 0 || strings.TrimSpace(*email) == "" || strings.TrimSpace(*name) == "" {
		_, _ = io.WriteString(stderr, "create-admin: --email and --name are required and nothing else is accepted\n\n"+createAdminUse)
		return app.ExitUsage
	}
	cfg, err := config.LoadOrchestrator(getenv)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return app.ExitUsage
	}
	password, ok := readPassword(stdin)
	if !ok {
		_, _ = io.WriteString(stderr, "create-admin: the password must be given on standard input\n\n"+createAdminUse)
		return app.ExitUsage
	}

	// Hash before the database is touched: a weak password fails fast.
	hash, err := auth.HashPassword(password)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "create-admin:", err)
		return app.ExitFailure
	}
	openCtx, cancel := context.WithTimeout(ctx, cfg.StartupTimeout)
	defer cancel()
	pool, err := db.Open(openCtx, cfg.DatabaseURL)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "create-admin: open database:", config.Scrub(err.Error(), cfg.DatabaseURL))
		return app.ExitFailure
	}
	defer pool.Close()

	tx, err := pool.Begin(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "create-admin: begin:", config.Scrub(err.Error(), cfg.DatabaseURL))
		return app.ExitFailure
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := insertAdmin(ctx, tx, *email, *name, hash, time.Now()); err != nil {
		_, _ = fmt.Fprintln(stderr, "create-admin:", config.Scrub(err.Error(), cfg.DatabaseURL))
		return app.ExitFailure
	}
	if err := tx.Commit(ctx); err != nil {
		_, _ = fmt.Fprintln(stderr, "create-admin: commit:", config.Scrub(err.Error(), cfg.DatabaseURL))
		return app.ExitFailure
	}
	_, _ = fmt.Fprintln(stdout, "created")
	return app.ExitOK
}

// insertAdmin makes the user, the admin group when needed, the membership and
// their audit entries (made by the system: nobody is signed in yet).
func insertAdmin(ctx context.Context, tx pgx.Tx, email, name, passwordHash string, at time.Time) error {
	user, err := store.CreateLocalUser(ctx, tx, email, name, "local", passwordHash)
	if errors.Is(err, store.ErrDuplicate) {
		return errors.New("a user with that email already exists")
	}
	if err != nil {
		return err
	}
	userID := strconv.FormatInt(user.ID, 10)
	if err := audit.Record(ctx, tx, nil, at, audit.Event{Action: "user.create", TargetType: "user", TargetID: userID,
		Detail: map[string]any{"email": user.Email, "source": "local", "via": "create-admin"}}); err != nil {
		return err
	}

	groups, err := store.ListGroups(ctx, tx)
	if err != nil {
		return err
	}
	var group *store.Group
	for i := range groups {
		if strings.EqualFold(groups[i].Name, adminGroupName) {
			group = &groups[i]
		}
	}
	switch {
	case group == nil:
		created, err := store.CreateGroup(ctx, tx, adminGroupName, true)
		if err != nil {
			return err
		}
		group = &created
		if err := audit.Record(ctx, tx, nil, at, audit.Event{Action: "group.create", TargetType: "group",
			TargetID: strconv.FormatInt(group.ID, 10), Detail: map[string]any{"name": group.Name, "admin": true}}); err != nil {
			return err
		}
	case !group.Admin:
		// Never promote an existing group silently: it may hold people who are not admins.
		return fmt.Errorf("a group named %q exists but is not an admin group; fix or rename it first", adminGroupName)
	}
	if err := store.SetManualMembership(ctx, tx, user.ID, group.ID, true); err != nil {
		return err
	}
	return audit.Record(ctx, tx, nil, at, audit.Event{Action: "group.member_add", TargetType: "group",
		TargetID: strconv.FormatInt(group.ID, 10), Detail: map[string]any{"user_id": user.ID, "group": group.Name}})
}

// readPassword reads one line from stdin. It reports false when there is no
// line at all (a missing or empty input), so "nothing given" is a usage error
// while an empty or short line is a rejected password. On a terminal it turns
// echo off for the read.
func readPassword(stdin io.Reader) (string, bool) {
	if stdin == nil {
		return "", false
	}
	if file, ok := stdin.(*os.File); ok {
		if info, err := file.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			restore := disableEcho(file)
			defer restore()
		}
	}
	line, err := bufio.NewReader(io.LimitReader(stdin, 4096)).ReadString('\n')
	if line == "" && err != nil {
		return "", false
	}
	return strings.TrimRight(line, "\r\n"), true
}

// disableEcho turns terminal echo off with stty and returns the function that
// turns it back on. Without stty it does nothing.
func disableEcho(tty *os.File) func() {
	off := exec.Command("stty", "-echo") //nolint:gosec // fixed command
	off.Stdin = tty
	if err := off.Run(); err != nil {
		return func() {}
	}
	return func() {
		on := exec.Command("stty", "echo") //nolint:gosec // fixed command
		on.Stdin = tty
		_ = on.Run()
		_, _ = fmt.Fprintln(os.Stderr)
	}
}
