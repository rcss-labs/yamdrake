package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rcss-labs/yumdrake/internal/cli"
)

const loginHelp = `usage: yumdrake login [--server URL] [--no-browser] [--with-token]

Sign in with a code you confirm in the browser, and save the token in the
config file (mode 0600). With --with-token the token is read from standard
input instead; the token is never accepted as an argument.

  --server URL    the Yumdrake server (else YUMDRAKE_SERVER, else the config file)
  --no-browser    do not try to open the browser
  --with-token    read a token from standard input
  --insecure-http allow plain http:// to a host other than localhost (the token
                  would be sent unencrypted; also YUMDRAKE_INSECURE_HTTP=1)
  -o json         print the signed-in user as JSON

The config file must not be readable by group or others; this check does not
apply on Windows, where file modes are not enforced.

Example:
  yumdrake login --server https://yumdrake.example.com
  echo "$TOKEN" | yumdrake login --with-token --server https://yumdrake.example.com
`

const logoutHelp = `usage: yumdrake logout

Revoke this machine's token on the server and remove it from the config file.

Example:
  yumdrake logout
`

const whoamiHelp = `usage: yumdrake whoami [-o json]

Show who you are signed in as and what you may do.

Example:
  yumdrake whoami
`

// defaultPollInterval is used when the server names no usable interval.
const defaultPollInterval = 5 * time.Second

// maxBackoff caps the wait after "polling too fast" answers.
const maxBackoff = 30 * time.Second

type meResponse struct {
	ID     int64       `json:"id"`
	Email  string      `json:"email"`
	Name   string      `json:"name"`
	Admin  bool        `json:"admin"`
	Grants []grantView `json:"grants"`
}

type grantView struct {
	Action      string `json:"action"`
	Label       string `json:"label"`
	Environment string `json:"environment"`
	FolderID    int64  `json:"folder_id"`
}

type deviceStart struct {
	DeviceCode string `json:"device_code"`
	UserCode   string `json:"user_code"`
	VerifyURL  string `json:"verify_url"`
	Interval   int    `json:"interval"`
	ExpiresIn  int    `json:"expires_in"`
}

type devicePoll struct {
	Status   string `json:"status"`
	Token    string `json:"token"`
	TokenID  int64  `json:"token_id"`
	Interval int    `json:"interval"`
}

func login(args []string, stdout, stderr io.Writer, e env) int {
	flags := newCommandFlags("login", loginHelp, true)
	var server string
	var noBrowser, withToken bool
	flags.StringVar(&server, "server", "", "")
	flags.BoolVar(&noBrowser, "no-browser", false, "")
	flags.BoolVar(&withToken, "with-token", false, "")
	if code, done := flags.parse(args, stdout, stderr); done {
		return code
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "yumdrake: login takes no arguments\n\n%s", loginHelp)
		return exitUsage
	}
	format, err := cli.ParseFormat(flags.output)
	if err != nil {
		return failSignIn(stderr, err)
	}
	cfg, path, err := e.loadConfig(server, flags.insecure)
	if err != nil {
		return failSignIn(stderr, err)
	}
	// A token from the environment or the file is replaced by the new one.
	cfg.Token, cfg.TokenID, cfg.TokenFromEnv = "", 0, false
	ctx, stop := signal.NotifyContext(e.context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if withToken {
		token, err := readToken(e.Stdin)
		if err != nil {
			return failSignIn(stderr, err)
		}
		cfg.Token = token
		me, err := fetchMe(ctx, cfg)
		if err != nil {
			return failSignIn(stderr, err)
		}
		if err := cli.SaveConfig(path, cfg); err != nil {
			return failSignIn(stderr, err)
		}
		return printSignedIn(stdout, format, me)
	}

	client, err := cli.NewClient(cfg)
	if err != nil {
		return failSignIn(stderr, err)
	}
	token, tokenID, err := deviceSignIn(ctx, client, stderr, e, noBrowser)
	if err != nil {
		return failSignIn(stderr, err)
	}
	cfg.Token, cfg.TokenID = token, tokenID
	// Save before anything else can fail: the server issued this token once.
	if err := cli.SaveConfig(path, cfg); err != nil {
		return failSignIn(stderr, err)
	}
	me, err := fetchMe(ctx, cfg)
	if err != nil {
		return failSignIn(stderr, err)
	}
	return printSignedIn(stdout, format, me)
}

func readToken(r io.Reader) (string, error) {
	line, err := bufio.NewReader(io.LimitReader(r, 4096)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", &cli.ExitError{Code: cli.ExitUsage, Message: "could not read the token from standard input"}
	}
	token := strings.TrimSpace(line)
	if token == "" {
		return "", &cli.ExitError{Code: cli.ExitUsage, Message: "no token on standard input"}
	}
	return token, nil
}

func fetchMe(ctx context.Context, cfg cli.Config) (meResponse, error) {
	client, err := cli.NewClient(cfg)
	if err != nil {
		return meResponse{}, err
	}
	var me meResponse
	if err := client.Do(ctx, http.MethodGet, "/api/v1/me", nil, &me); err != nil {
		return meResponse{}, err
	}
	return me, nil
}

func printSignedIn(stdout io.Writer, format cli.Format, me meResponse) int {
	if format == cli.FormatJSON {
		_ = cli.WriteJSON(stdout, map[string]any{"email": me.Email, "name": me.Name})
		return exitOK
	}
	_, _ = fmt.Fprintf(stdout, "Signed in as %s\n", me.Email)
	return exitOK
}

// deviceSignIn runs the device flow and returns the token and its id. The
// prompts go to stderr so that stdout stays for the result.
func deviceSignIn(ctx context.Context, client *cli.Client, stderr io.Writer, e env, noBrowser bool) (string, int64, error) {
	var start deviceStart
	if err := client.Do(ctx, http.MethodPost, "/api/v1/cli/login", struct{}{}, &start); err != nil {
		return "", 0, err
	}
	_, _ = fmt.Fprintf(stderr, "To sign in, open:\n  %s\nand confirm the code: %s\n", start.VerifyURL, start.UserCode)
	if e.StdoutIsTerminal && !noBrowser && e.OpenBrowser != nil && isWebURL(start.VerifyURL) {
		// Failing to open a browser is fine: the URL is printed above.
		_ = e.OpenBrowser(start.VerifyURL)
	}
	_, _ = fmt.Fprintln(stderr, "Waiting for you to confirm...")

	interval := pollInterval(start.Interval)
	wait := interval
	deadline := e.Now().Add(time.Duration(start.ExpiresIn) * time.Second)
	expired := &cli.ExitError{Code: cli.ExitNotSignIn, Message: "The sign-in expired before it was confirmed. Run yumdrake login again."}
	for {
		if start.ExpiresIn > 0 && !e.Now().Before(deadline) {
			return "", 0, expired
		}
		if err := e.Wait(ctx, wait); err != nil {
			return "", 0, err
		}
		var poll devicePoll
		err := client.Do(ctx, http.MethodPost, "/api/v1/cli/login/token", map[string]string{"device_code": start.DeviceCode}, &poll)
		var apiErr *cli.ExitError
		switch {
		case errors.As(err, &apiErr) && apiErr.APICode == "expired_token":
			return "", 0, expired
		case errors.As(err, &apiErr) && apiErr.Status == http.StatusTooManyRequests:
			// Polled too early: obey Retry-After, else back off up to 30s.
			if apiErr.RetryAfter > 0 {
				wait = apiErr.RetryAfter
			} else {
				interval = min(interval*2, maxBackoff)
				wait = interval
			}
			continue
		case err != nil:
			return "", 0, err
		}
		switch poll.Status {
		case "complete":
			if poll.Token == "" {
				return "", 0, &cli.ExitError{Code: cli.ExitServer, Message: "the server confirmed the sign-in without a token"}
			}
			return poll.Token, poll.TokenID, nil
		case "pending":
			if poll.Interval > 0 {
				interval = pollInterval(poll.Interval)
			}
			wait = interval
		default:
			return "", 0, &cli.ExitError{Code: cli.ExitServer, Message: "the server's answer was not understood"}
		}
	}
}

func pollInterval(seconds int) time.Duration {
	if seconds <= 0 {
		return defaultPollInterval
	}
	return time.Duration(seconds) * time.Second
}

func logout(args []string, stdout, stderr io.Writer, e env) int {
	flags := newCommandFlags("logout", logoutHelp, false)
	if code, done := flags.parse(args, stdout, stderr); done {
		return code
	}
	cfg, path, err := e.loadConfig("", flags.insecure)
	if err != nil {
		return fail(stderr, err)
	}
	if cfg.Token == "" {
		_, _ = fmt.Fprintln(stderr, "Not signed in.")
		return exitOK
	}
	if cfg.TokenFromEnv {
		_, _ = fmt.Fprintln(stderr, "The token comes from YUMDRAKE_TOKEN; unset it to sign out. Nothing was changed.")
		return exitOK
	}
	client, err := cli.NewClient(cfg)
	if err != nil {
		return fail(stderr, err)
	}
	// A token revokes itself through the logout route, so this also works for
	// a token pasted with --with-token, whose id the CLI does not know.
	err = client.Do(e.context(), http.MethodPost, "/api/v1/auth/logout", nil, nil)
	var apiErr *cli.ExitError
	// 401 means the server no longer honours the token: nothing to revoke.
	if err != nil && (!errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized) {
		if errors.Is(err, context.Canceled) {
			return fail(stderr, err)
		}
		_, _ = fmt.Fprintln(stderr, "yumdrake: could not revoke the token; it was kept in the config file so you can try again")
		return fail(stderr, err)
	}
	cfg.Token, cfg.TokenID = "", 0
	if err := cli.SaveConfig(path, cfg); err != nil {
		return fail(stderr, err)
	}
	_, _ = fmt.Fprintln(stdout, "Signed out")
	return exitOK
}

func whoami(args []string, stdout, stderr io.Writer, e env) int {
	flags := newCommandFlags("whoami", whoamiHelp, true)
	if code, done := flags.parse(args, stdout, stderr); done {
		return code
	}
	format, err := cli.ParseFormat(flags.output)
	if err != nil {
		return fail(stderr, err)
	}
	cfg, _, err := e.loadConfig("", flags.insecure)
	if err != nil {
		return fail(stderr, err)
	}
	client, err := signedInClient(cfg)
	if err != nil {
		return fail(stderr, err)
	}
	var me meResponse
	if err := client.Do(e.context(), http.MethodGet, "/api/v1/me", nil, &me); err != nil {
		return fail(stderr, err)
	}
	if format == cli.FormatJSON {
		_ = cli.WriteJSON(stdout, me)
		return exitOK
	}
	_, _ = fmt.Fprintf(stdout, "Name:  %s\nEmail: %s\n", me.Name, me.Email)
	_, _ = fmt.Fprintln(stdout, "Permissions:")
	for _, line := range permissionLines(me) {
		_, _ = fmt.Fprintf(stdout, "  %s\n", line)
	}
	return exitOK
}

var actionWords = map[string]string{
	"view":               "view jobs and runs",
	"view_logs":          "read run logs",
	"run":                "start runs",
	"cancel":             "cancel runs",
	"approve":            "approve or reject runs",
	"restore":            "restore a service to an earlier version",
	"manage_jobs":        "manage jobs",
	"manage_connections": "manage connections",
	"manage_access":      "manage users, groups and grants",
	"view_audit":         "read the audit log",
}

// permissionLines describes a person's access in words.
func permissionLines(me meResponse) []string {
	if me.Admin {
		return []string{"Administrator: can do everything everywhere"}
	}
	lines := []string{"Can " + actionWords["view"] + " (everyone can)"}
	for _, g := range me.Grants {
		words, ok := actionWords[g.Action]
		if !ok {
			words = g.Action
		}
		scope := "everywhere"
		switch {
		case g.Environment != "":
			scope = "in environment " + g.Environment
		case g.Label != "":
			scope = "in environments labelled " + g.Label
		}
		if g.FolderID != 0 {
			scope += fmt.Sprintf(", folder %d only", g.FolderID)
		}
		lines = append(lines, fmt.Sprintf("Can %s %s", words, scope))
	}
	return lines
}

// isWebURL guards the browser opener: only http and https URLs are opened,
// whatever the server sent.
func isWebURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
