//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/yumdrake/test/e2e/fakegithub"
)

const (
	accessAdminEmail    = "root@access.example.test"
	accessAdminPassword = "access admin passphrase"
	memberEmail         = "member@access.example.test"
	memberPassword      = "member passphrase 123"
	sessionCookie       = "yumdrake_session"
)

// answer is what a call returned. The body is kept for decoding only; it is
// never printed, because it may hold a password or a token.
type answer struct {
	status int
	code   string
	body   []byte
	cookie string
}

// caller sends API requests with one set of credentials: a session cookie
// (sent by hand, the cookie is Secure) or a bearer token, or neither.
type caller struct {
	t       *testing.T
	base    string
	session string
	token   string
}

func (c caller) send(method, path string, body any) answer {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("encode %s %s: %v", method, path, err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		c.t.Fatalf("build %s %s: %v", method, path, err)
	}
	if c.session != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: c.session})
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	// Browsers send the page's Origin on writes; the API checks it against the public URL.
	req.Header.Set("Origin", c.base)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	got := answer{status: resp.StatusCode, body: raw}
	var failure struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if resp.StatusCode >= 300 && json.Unmarshal(raw, &failure) == nil {
		got.code = failure.Error.Code
	}
	for _, cookie := range resp.Cookies() {
		if cookie.Name == sessionCookie {
			got.cookie = cookie.Value
		}
	}
	return got
}

// expect fails unless the call answered with the status (and, for a refusal,
// the error code). Only the status and code are ever printed.
func (c caller) expect(status int, code, method, path string, body any) answer {
	c.t.Helper()
	got := c.send(method, path, body)
	if got.status != status || got.code != code {
		c.t.Fatalf("%s %s: status %d code %q, want status %d code %q", method, path, got.status, got.code, status, code)
	}
	return got
}

// decode reads a JSON answer into out without printing the body on failure.
func (a answer) decode(t *testing.T, what string, out any) {
	t.Helper()
	if err := json.Unmarshal(a.body, out); err != nil {
		t.Fatalf("decode %s: %v", what, err)
	}
}

// createAdminWithCommand builds cmd/orchestrator and runs its create-admin
// subcommand with the password on standard input, as an operator would.
func createAdminWithCommand(t *testing.T, env map[string]string) {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "orchestrator")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/orchestrator") //nolint:gosec // fixed arguments
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build cmd/orchestrator: %v\n%s", err, output)
	}
	cmd := exec.Command(binary, "create-admin", "--email", accessAdminEmail, "--name", "Access Admin") //nolint:gosec // binary built above
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.Stdin = strings.NewReader(accessAdminPassword + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// stderr holds usage or error text, never the password.
		t.Fatalf("create-admin failed: %v\n%s", err, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "created" {
		t.Fatalf("create-admin printed %q, want %q", stdout.String(), "created")
	}
}

// signInAs signs in with email and password and returns a caller holding the session.
func signInAs(t *testing.T, base, email, password string) caller {
	t.Helper()
	anonymous := caller{t: t, base: base}
	got := anonymous.expect(http.StatusOK, "", http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": email, "password": password})
	if got.cookie == "" {
		t.Fatalf("sign-in of %s set no session cookie", email)
	}
	return caller{t: t, base: base, session: got.cookie}
}

type auditEntry struct {
	ID     int64  `json:"id"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
}

// readAudit returns the whole audit log, oldest first, and its raw JSON pages.
func readAudit(t *testing.T, admin caller) (entries []auditEntry, raw [][]byte) {
	t.Helper()
	path := "/api/v1/admin/audit?limit=200"
	for {
		page := admin.expect(http.StatusOK, "", http.MethodGet, path, nil)
		raw = append(raw, page.body)
		var parsed struct {
			Entries    []auditEntry `json:"entries"`
			NextBefore *int64       `json:"next_before"`
		}
		page.decode(t, "audit page", &parsed)
		entries = append(entries, parsed.Entries...)
		if parsed.NextBefore == nil {
			break
		}
		path = "/api/v1/admin/audit?limit=200&before=" + strconv.FormatInt(*parsed.NextBefore, 10)
	}
	// The API answers newest first.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	return entries, raw
}

// containsInOrder reports whether want occurs in actions as a subsequence.
func containsInOrder(actions, want []string) bool {
	next := 0
	for _, action := range actions {
		if next < len(want) && action == want[next] {
			next++
		}
	}
	return next == len(want)
}

func TestAccessEndToEnd(t *testing.T) {
	gh := fakegithub.New(githubToken)
	t.Cleanup(gh.Close)
	base, _, env := startService(t, gh.URL())

	// The first admin is made by the real create-admin command.
	createAdminWithCommand(t, env)
	admin := signInAs(t, base, accessAdminEmail, accessAdminPassword)
	var me struct {
		Email string `json:"email"`
		Admin bool   `json:"admin"`
	}
	admin.expect(http.StatusOK, "", http.MethodGet, "/api/v1/me", nil).decode(t, "me", &me)
	if me.Email != accessAdminEmail || !me.Admin {
		t.Fatalf("the create-admin user is not an admin on /me (admin: %v)", me.Admin)
	}

	// Group QA may run jobs labelled dev; a local user belongs to it.
	var group, user struct {
		ID int64 `json:"id"`
	}
	admin.expect(http.StatusCreated, "", http.MethodPost, "/api/v1/admin/groups", map[string]any{"name": "QA"}).decode(t, "group", &group)
	admin.expect(http.StatusCreated, "", http.MethodPost, "/api/v1/admin/grants",
		map[string]any{"group_id": group.ID, "action": "run", "label": "dev"})
	admin.expect(http.StatusCreated, "", http.MethodPost, "/api/v1/admin/users",
		map[string]any{"email": memberEmail, "name": "QA Member", "source": "local", "password": memberPassword}).decode(t, "user", &user)
	admin.expect(http.StatusOK, "", http.MethodPatch, "/api/v1/admin/users/"+strconv.FormatInt(user.ID, 10),
		map[string]any{"group_ids": []int64{group.ID}})

	// The member's session: view yes, manage jobs and the audit log no.
	member := signInAs(t, base, memberEmail, memberPassword)
	member.expect(http.StatusOK, "", http.MethodGet, "/api/v1/jobs", nil)
	member.expect(http.StatusForbidden, "forbidden", http.MethodPost, "/api/v1/folders", map[string]any{"name": "Nope"})
	member.expect(http.StatusForbidden, "forbidden", http.MethodGet, "/api/v1/admin/audit", nil)

	// A token for the member through the device flow: start, confirm in the
	// browser session, then collect the token.
	cli := caller{t: t, base: base}
	var started struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		Interval   int    `json:"interval"`
	}
	cli.expect(http.StatusOK, "", http.MethodPost, "/api/v1/cli/login", map[string]any{}).decode(t, "device login", &started)
	member.expect(http.StatusNoContent, "", http.MethodPost, "/api/v1/cli/login/confirm", map[string]string{"user_code": started.UserCode})

	var collected struct {
		Status string `json:"status"`
		Token  string `json:"token"`
	}
	for attempt := 0; attempt < 4 && collected.Token == ""; attempt++ {
		got := cli.send(http.MethodPost, "/api/v1/cli/login/token", map[string]string{"device_code": started.DeviceCode})
		switch got.status {
		case http.StatusOK:
			got.decode(t, "device token", &collected)
			if collected.Token == "" {
				// Still pending: wait out the interval before asking again.
				time.Sleep(time.Duration(max(started.Interval, 1)) * time.Second)
			}
		case http.StatusTooManyRequests:
			// Polling faster than the interval: wait it out and ask again.
			time.Sleep(time.Duration(max(started.Interval, 1)) * time.Second)
		default:
			t.Fatalf("collecting the token: status %d code %q", got.status, got.code)
		}
	}
	if collected.Status != "complete" || collected.Token == "" {
		t.Fatalf("the device sign-in did not give a token (status %q)", collected.Status)
	}
	withToken := caller{t: t, base: base, token: collected.Token}
	withToken.expect(http.StatusOK, "", http.MethodGet, "/api/v1/jobs", nil)
	withToken.expect(http.StatusForbidden, "forbidden", http.MethodPost, "/api/v1/folders", map[string]any{"name": "Nope"})

	// Disabling the user refuses the cookie and the token at once.
	admin.expect(http.StatusOK, "", http.MethodPatch, "/api/v1/admin/users/"+strconv.FormatInt(user.ID, 10),
		map[string]any{"disabled": true})
	member.expect(http.StatusUnauthorized, "unauthorized", http.MethodGet, "/api/v1/jobs", nil)
	withToken.expect(http.StatusUnauthorized, "unauthorized", http.MethodGet, "/api/v1/jobs", nil)

	// The audit log records each change in order and holds no secret values.
	entries, pages := readAudit(t, admin)
	actions := make([]string, len(entries))
	for i, entry := range entries {
		actions[i] = entry.Action
	}
	wantInOrder := []string{
		"user.create",       // create-admin: the admin
		"group.create",      // create-admin: Administrators
		"group.member_add",  // create-admin: the admin joins
		"auth.login",        // the admin signs in
		"group.create",      // QA
		"grant.create",      // run on label dev
		"user.create",       // the member
		"user.update",       // the member joins QA
		"auth.login",        // the member signs in
		"cli_login.confirm", // the member confirms the CLI
		"token.create",      // the token is issued
		"user.update",       // the member is disabled
	}
	if !containsInOrder(actions, wantInOrder) {
		t.Errorf("audit actions = %v, want them to contain %v in this order", actions, wantInOrder)
	}
	// Positive control: the searched JSON is not empty and holds a known
	// non-secret value, so a clean search means something.
	if joined := bytes.Join(pages, nil); !bytes.Contains(joined, []byte(memberEmail)) {
		t.Fatal("the audit JSON does not contain the member's email; the secret search would prove nothing")
	}
	// Who did what: the admin made the changes, the member confirmed the CLI sign-in and got the token.
	wantActor := func(action, actor string, which int) {
		t.Helper()
		seen := 0
		for _, entry := range entries {
			if entry.Action != action {
				continue
			}
			seen++
			if seen == which {
				if entry.Actor != actor {
					t.Errorf("audit %s #%d actor = %q, want %q", action, which, entry.Actor, actor)
				}
				return
			}
		}
		t.Errorf("no audit %s #%d", action, which)
	}
	wantActor("cli_login.confirm", memberEmail, 1)
	wantActor("token.create", memberEmail, 1)
	wantActor("user.update", accessAdminEmail, 2) // the disabling update (the first added the member to QA)
	for _, page := range pages {
		for name, secret := range map[string]string{
			"the admin password":  accessAdminPassword,
			"the member password": memberPassword,
			"the token":           collected.Token,
			"the device code":     started.DeviceCode,
			"the member session":  member.session,
			"the admin session":   admin.session,
		} {
			if secret != "" && bytes.Contains(page, []byte(secret)) {
				t.Errorf("the audit log contains %s", name)
			}
		}
	}
	// The token's own part after the "ymd_" prefix must not appear either.
	if body := strings.TrimPrefix(collected.Token, "ymd_"); body != "" {
		for _, page := range pages {
			if bytes.Contains(page, []byte(body)) {
				t.Error("the audit log contains the token body")
			}
		}
	}
}
