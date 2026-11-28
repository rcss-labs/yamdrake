package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rcss-labs/yamdrake/internal/cli"
)

// apiFlags are the flags every command that talks to the server shares.
type apiFlags struct {
	*commandFlags
	server string
}

func newAPIFlags(name, help string) *apiFlags {
	f := &apiFlags{commandFlags: newCommandFlags(name, help, true)}
	f.StringVar(&f.server, "server", "", "the Yumdrake server")
	return f
}

// session is one command's connection to the server.
type session struct {
	e      env
	ctx    context.Context
	client *cli.Client
	cfg    cli.Config
	format cli.Format
	stdout io.Writer
	stderr io.Writer
}

// openSession reads the configuration and makes a signed-in client. A usage
// or configuration problem comes back as an *cli.ExitError.
func (e env) openSession(f *apiFlags, stdout, stderr io.Writer) (*session, error) {
	format, err := cli.ParseFormat(f.output)
	if err != nil {
		return nil, err
	}
	cfg, _, err := e.loadConfig(f.server, f.insecure)
	if err != nil {
		return nil, err
	}
	client, err := signedInClient(cfg)
	if err != nil {
		return nil, err
	}
	return &session{e: e, ctx: e.context(), client: client, cfg: cfg, format: format, stdout: stdout, stderr: stderr}, nil
}

func (s *session) json() bool { return s.format == cli.FormatJSON }

func (s *session) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(s.stdout, format, args...)
}

func (s *session) progressf(format string, args ...any) {
	_, _ = fmt.Fprintf(s.stderr, format, args...)
}

// parseArgs parses flags and positional arguments in any order.
func (c *commandFlags) parseArgs(args []string, stdout, stderr io.Writer) (positional []string, code int, done bool) {
	for {
		if err := c.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				_, _ = fmt.Fprint(stdout, c.help)
				return nil, exitOK, true
			}
			_, _ = fmt.Fprintf(stderr, "yumdrake: %v\n\n%s", err, c.help)
			return nil, exitUsage, true
		}
		if c.NArg() == 0 {
			return positional, 0, false
		}
		positional = append(positional, c.Arg(0))
		args = c.Args()[1:]
	}
}

// usageError is an *cli.ExitError for a bad command line.
func usageError(format string, args ...any) *cli.ExitError {
	return &cli.ExitError{Code: cli.ExitUsage, Message: fmt.Sprintf(format, args...)}
}

// needArgs checks the number of positional arguments, printing the command's
// help on a mistake.
func needArgs(f *commandFlags, positional []string, stderr io.Writer, want int, what string) (int, bool) {
	if len(positional) != want {
		_, _ = fmt.Fprintf(stderr, "yumdrake: %s\n\n%s", what, f.help)
		return exitUsage, false
	}
	return 0, true
}

func parseRunID(s string) (int64, error) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, usageError("%q is not a run number", s)
	}
	return id, nil
}

// subcommands maps the second word of a command to its function.
type subcommands map[string]func(args []string, stdout, stderr io.Writer, e env) int

func (m subcommands) dispatch(group, usage string, args []string, stdout, stderr io.Writer, e env) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "-h", "--help", "help":
		_, _ = fmt.Fprint(stdout, usage)
		return exitOK
	}
	if fn, ok := m[args[0]]; ok {
		return fn(args[1:], stdout, stderr, e)
	}
	_, _ = fmt.Fprintf(stderr, "yumdrake: unknown command %q for %s\n\n%s", args[0], group, usage)
	return exitUsage
}

// listNames renders valid names for an error message.
func listNames(names []string) string {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	return strings.Join(sorted, ", ")
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}

func formatTimePtr(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return formatTime(*t)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
