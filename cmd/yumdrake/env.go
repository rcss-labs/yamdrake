package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/rcss-labs/yamdrake/internal/cli"
)

// env is everything a command takes from the machine it runs on. Tests replace
// the parts that would touch the network, the terminal or the clock.
type env struct {
	Getenv           func(string) string
	Stdin            io.Reader
	StdoutIsTerminal bool
	// StdinIsTerminal is true when a person can answer a question.
	StdinIsTerminal bool
	// ConfigPath is the config file; empty means the default location.
	ConfigPath  string
	OpenBrowser func(url string) error
	// Wait sleeps for d or until ctx ends.
	Wait func(ctx context.Context, d time.Duration) error
	Now  func() time.Time
	// Ctx ends the command early; nil means the background context.
	Ctx context.Context
}

func defaultEnv(stdout io.Writer) env {
	return env{
		Getenv:           os.Getenv,
		Stdin:            os.Stdin,
		StdoutIsTerminal: cli.IsTerminal(stdout),
		StdinIsTerminal:  cli.IsTerminal(os.Stdin),
		OpenBrowser:      openBrowser,
		Wait:             sleepContext,
		Now:              time.Now,
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// openBrowser starts the system's URL opener and does not wait for it.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url) //nolint:gosec // url is checked to be http(s) by the caller
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url) //nolint:gosec // url is checked to be http(s) by the caller
	default:
		cmd = exec.Command("xdg-open", url) //nolint:gosec // url is checked to be http(s) by the caller
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func (e env) configPath() (string, error) {
	if e.ConfigPath != "" {
		return e.ConfigPath, nil
	}
	return cli.DefaultConfigPath(e.Getenv)
}

// loadConfig reads the config file and environment; serverFlag (--server)
// wins over both.
func (e env) loadConfig(serverFlag string, insecure bool) (cli.Config, string, error) {
	path, err := e.configPath()
	if err != nil {
		return cli.Config{}, "", err
	}
	cfg, err := cli.LoadConfig(e.Getenv, path)
	if err != nil {
		return cli.Config{}, "", err
	}
	cfg.InsecureHTTP = cfg.InsecureHTTP || insecure
	return cfg.WithServer(serverFlag), path, nil
}

// signedInClient is a client for the commands that need a token.
func signedInClient(cfg cli.Config) (*cli.Client, error) {
	if cfg.Token == "" {
		return nil, cli.NotSignedIn()
	}
	return cli.NewClient(cfg)
}

// commandFlags makes a flag set for one command: -h prints help (with its
// example) and -o/--output chooses the format.
type commandFlags struct {
	*flag.FlagSet
	output string
	help   string
	// insecure is --insecure-http: allow plain http:// to a remote host.
	insecure bool
}

func newCommandFlags(name, help string, withOutput bool) *commandFlags {
	c := &commandFlags{FlagSet: flag.NewFlagSet(name, flag.ContinueOnError), help: help}
	c.SetOutput(io.Discard)
	c.BoolVar(&c.insecure, "insecure-http", false, "allow plain http:// to a remote server")
	if withOutput {
		c.StringVar(&c.output, "o", "text", "output format: text or json")
		c.StringVar(&c.output, "output", "text", "output format: text or json")
	}
	return c
}

// parse returns done=true when the command should return code now (help shown
// or a usage error printed).
func (c *commandFlags) parse(args []string, stdout, stderr io.Writer) (code int, done bool) {
	if err := c.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = fmt.Fprint(stdout, c.help)
			return exitOK, true
		}
		_, _ = fmt.Fprintf(stderr, "yumdrake: %v\n\n%s", err, c.help)
		return exitUsage, true
	}
	return 0, false
}

// context is the command's context: the caller's, or the background one.
func (e env) context() context.Context {
	if e.Ctx != nil {
		return e.Ctx
	}
	return context.Background()
}

// fail prints err for a person and returns its exit code. A cancelled command
// says "Stopped".
func fail(stderr io.Writer, err error) int {
	return failWith(stderr, err, "Stopped")
}

// failSignIn is fail for the login command, whose cancel message is its own.
func failSignIn(stderr io.Writer, err error) int {
	return failWith(stderr, err, "Sign-in cancelled")
}

func failWith(stderr io.Writer, err error, cancelled string) int {
	if errors.Is(err, context.Canceled) {
		_, _ = fmt.Fprintln(stderr, cancelled)
		return exitInterrupted
	}
	_, _ = fmt.Fprintf(stderr, "yumdrake: %s\n", err)
	return cli.ExitCodeFor(err)
}
