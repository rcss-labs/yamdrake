package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/yamdrake/internal/cli"
)

const (
	testToken      = "yd_TOP-SECRET-TOKEN" //nolint:gosec // a fake token
	testDeviceCode = "device-code-value-0123456789012345678901234"
)

// fakeServer is a stand-in for the Plan 3 routes the commands use.
type fakeServer struct {
	*httptest.Server
	mu              sync.Mutex
	polls           int
	pendingFor      int    // polls answered "pending" before "complete"
	pollStatus      int    // when set, every poll answers this status
	pollCode        string // error code for pollStatus
	revokeCalls     int
	revokeAuth      string
	revokeCode      int
	meCode          int
	meAuth          string
	pollReplies     []int
	retryAfter      string // Retry-After header on the scripted replies
	pendingInterval int    // interval in pending answers; 0 means 3
	verifyURL       string // overrides verify_url when set
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{revokeCode: http.StatusNoContent, meCode: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/cli/login", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, 200, map[string]any{
			"device_code": testDeviceCode, "user_code": "ABCD2345",
			"verify_url": f.verifyOr(f.URL + "/device?user_code=ABCD2345"), "interval": 3, "expires_in": 600,
		})
	})
	mux.HandleFunc("POST /api/v1/cli/login/token", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.polls++
		var req struct {
			DeviceCode string `json:"device_code"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.DeviceCode != testDeviceCode {
			writeTestJSON(w, 400, errBody("invalid", "bad device code"))
			return
		}
		if len(f.pollReplies) > 0 {
			code := f.pollReplies[0]
			f.pollReplies = f.pollReplies[1:]
			if f.retryAfter != "" {
				w.Header().Set("Retry-After", f.retryAfter)
			}
			writeTestJSON(w, code, errBody("too_many_requests", "slow down"))
			return
		}
		if f.pollStatus != 0 {
			writeTestJSON(w, f.pollStatus, errBody(f.pollCode, "the device code is unknown or has expired"))
			return
		}
		if f.polls <= f.pendingFor {
			interval := f.pendingInterval
			if interval == 0 {
				interval = 3
			}
			writeTestJSON(w, 200, map[string]any{"status": "pending", "interval": interval})
			return
		}
		writeTestJSON(w, 200, map[string]any{"status": "complete", "token": testToken, "token_id": 41, "expires_at": nil})
	})
	mux.HandleFunc("GET /api/v1/me", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.meAuth = r.Header.Get("Authorization")
		f.mu.Unlock()
		if f.meCode != http.StatusOK {
			writeTestJSON(w, f.meCode, errBody("unauthorized", "authentication required"))
			return
		}
		writeTestJSON(w, 200, map[string]any{"id": 1, "email": "ada@example.com", "name": "Ada Lovelace", "admin": false,
			"grants": []map[string]any{
				{"action": "run", "label": "", "environment": "dev-01", "folder_id": 0},
				{"action": "approve", "label": "prod", "environment": "", "folder_id": 0},
			}})
	})
	mux.HandleFunc("POST /api/v1/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.revokeCalls++
		f.revokeAuth = r.Header.Get("Authorization")
		f.mu.Unlock()
		if f.revokeCode != http.StatusNoContent {
			writeTestJSON(w, f.revokeCode, errBody("unauthorized", "x"))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeServer) verifyOr(def string) string {
	if f.verifyURL != "" {
		return f.verifyURL
	}
	return def
}

func errBody(code, message string) map[string]any {
	return map[string]any{"error": map[string]string{"code": code, "message": message}}
}

func writeTestJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type harness struct {
	configPath string
	opened     []string
	openErr    error
	waits      []time.Duration
	terminal   bool
	// stdinTerminal makes the command believe a person is at the keyboard.
	stdinTerminal bool
	stdin         string
	environ       map[string]string
	ctx           context.Context
	clock         time.Time // fake time, advanced by every wait
	onWait        func(call int) error
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return &harness{configPath: filepath.Join(t.TempDir(), "yumdrake", "config.json"), environ: map[string]string{}, clock: time.Unix(1_800_000_000, 0)}
}

func (h *harness) run(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	e := env{
		Getenv:           func(k string) string { return h.environ[k] },
		Stdin:            strings.NewReader(h.stdin),
		StdoutIsTerminal: h.terminal,
		StdinIsTerminal:  h.stdinTerminal,
		ConfigPath:       h.configPath,
		OpenBrowser:      func(u string) error { h.opened = append(h.opened, u); return h.openErr },
		Wait: func(_ context.Context, d time.Duration) error {
			h.waits = append(h.waits, d)
			h.clock = h.clock.Add(d)
			if h.onWait != nil {
				return h.onWait(len(h.waits))
			}
			return nil
		},
		Now: func() time.Time { return h.clock },
		Ctx: h.ctx,
	}
	code = runEnv(args, &out, &errOut, e)
	return code, out.String(), errOut.String()
}

func (h *harness) saved(t *testing.T) cli.Config {
	t.Helper()
	cfg, err := cli.LoadConfig(func(string) string { return "" }, h.configPath)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func (h *harness) seed(t *testing.T, cfg cli.Config) {
	t.Helper()
	if err := cli.SaveConfig(h.configPath, cfg); err != nil {
		t.Fatal(err)
	}
}

func assertNoToken(t *testing.T, outputs ...string) {
	t.Helper()
	for _, o := range outputs {
		if strings.Contains(o, testToken) {
			t.Errorf("token appears in output: %q", o)
		}
	}
}

func TestLoginDeviceFlowHappyPath(t *testing.T) {
	srv := newFakeServer(t)
	srv.pendingFor = 2
	h := newHarness(t)
	h.terminal = true

	code, stdout, stderr := h.run("login", "--server", srv.URL)
	if code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "Signed in as ada@example.com") {
		t.Errorf("stdout = %q", stdout)
	}
	for _, want := range []string{"ABCD2345", "/device?user_code=ABCD2345"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q: %q", want, stderr)
		}
	}
	assertNoToken(t, stdout, stderr)
	if srv.polls != 3 {
		t.Errorf("polls = %d, want 3", srv.polls)
	}
	// One wait before each poll, at the server's interval.
	if len(h.waits) != 3 || h.waits[0] != 3*time.Second || h.waits[2] != 3*time.Second {
		t.Errorf("waits = %v, want three of 3s", h.waits)
	}
	if len(h.opened) != 1 || !strings.Contains(h.opened[0], "user_code=ABCD2345") {
		t.Errorf("opened = %v", h.opened)
	}
	cfg := h.saved(t)
	if cfg.Server != srv.URL || cfg.Token != testToken || cfg.TokenID != 41 {
		t.Errorf("saved config = %+v", cfg)
	}
	info, _ := os.Stat(h.configPath)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %v", info.Mode().Perm())
	}
	if srv.meAuth != "Bearer "+testToken {
		t.Errorf("/me auth = %q", srv.meAuth)
	}
}

func TestLoginDoesNotOpenBrowserWithoutTerminalOrWithNoBrowser(t *testing.T) {
	for name, tc := range map[string]struct {
		terminal bool
		args     []string
	}{
		"not a terminal": {false, nil},
		"no-browser":     {true, []string{"--no-browser"}},
	} {
		t.Run(name, func(t *testing.T) {
			srv := newFakeServer(t)
			h := newHarness(t)
			h.terminal = tc.terminal
			if code, _, stderr := h.run(append([]string{"login", "--server", srv.URL}, tc.args...)...); code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			if len(h.opened) != 0 {
				t.Errorf("opened %v", h.opened)
			}
		})
	}
}

func TestLoginBrowserFailureIsNotAnError(t *testing.T) {
	srv := newFakeServer(t)
	h := newHarness(t)
	h.terminal = true
	h.openErr = os.ErrNotExist
	if code, _, stderr := h.run("login", "--server", srv.URL); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

func TestLoginExpiredDeviceCode(t *testing.T) {
	srv := newFakeServer(t)
	srv.pollStatus, srv.pollCode = 400, "expired_token"
	h := newHarness(t)
	code, stdout, stderr := h.run("login", "--server", srv.URL)
	if code != 6 {
		t.Errorf("exit = %d, want 6; stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "expired") || !strings.Contains(stderr, "yumdrake login") {
		t.Errorf("stderr = %q", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q", stdout)
	}
	if _, err := os.Stat(h.configPath); err == nil {
		t.Error("config written after a failed sign-in")
	}
}

func TestLoginKeepsPollingAfterTooManyRequests(t *testing.T) {
	srv := newFakeServer(t)
	srv.pollReplies = []int{429}
	h := newHarness(t)
	if code, _, stderr := h.run("login", "--server", srv.URL); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if srv.polls != 2 {
		t.Errorf("polls = %d, want 2", srv.polls)
	}
}

func TestLoginWithTokenReadsStdin(t *testing.T) {
	srv := newFakeServer(t)
	h := newHarness(t)
	h.stdin = testToken + "\n"
	code, stdout, stderr := h.run("login", "--with-token", "--server", srv.URL)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "Signed in as ada@example.com") {
		t.Errorf("stdout = %q", stdout)
	}
	assertNoToken(t, stdout, stderr)
	if srv.meAuth != "Bearer "+testToken || srv.polls != 0 {
		t.Errorf("meAuth = %q polls = %d", srv.meAuth, srv.polls)
	}
	if cfg := h.saved(t); cfg.Token != testToken || cfg.TokenID != 0 {
		t.Errorf("saved config = %+v", cfg)
	}
}

func TestLoginWithTokenRejectsBadTokenAndEmptyInput(t *testing.T) {
	srv := newFakeServer(t)
	srv.meCode = 401
	h := newHarness(t)
	h.stdin = testToken
	code, _, stderr := h.run("login", "--with-token", "--server", srv.URL)
	if code != 6 || !strings.Contains(stderr, "Not signed in. Run yumdrake login.") {
		t.Errorf("exit %d stderr %q", code, stderr)
	}
	assertNoToken(t, stderr)
	if _, err := os.Stat(h.configPath); err == nil {
		t.Error("a rejected token was saved")
	}

	h2 := newHarness(t)
	if code, _, _ := h2.run("login", "--with-token", "--server", srv.URL); code != 2 {
		t.Errorf("empty stdin exit = %d, want 2", code)
	}
}

func TestLoginHasNoTokenFlagAndNeedsServer(t *testing.T) {
	h := newHarness(t)
	if code, _, _ := h.run("login", "--token", "abc", "--server", "http://x"); code != 2 {
		t.Errorf("--token exit = %d, want 2", code)
	}
	if code, _, stderr := h.run("login"); code != 2 || !strings.Contains(stderr, "--server") {
		t.Errorf("no server: exit %d stderr %q", code, stderr)
	}
}

func TestLoginServerPrecedenceFlagEnvFile(t *testing.T) {
	srv := newFakeServer(t)
	h := newHarness(t)
	h.seed(t, cli.Config{Server: "http://127.0.0.1:1"})
	h.environ["YUMDRAKE_SERVER"] = "http://127.0.0.1:2"
	if code, _, stderr := h.run("login", "--server", srv.URL); code != 0 {
		t.Fatalf("flag did not win: exit %d: %s", code, stderr)
	}
}

func TestHelpHasAnExample(t *testing.T) {
	for _, cmd := range []string{"login", "logout", "whoami"} {
		h := newHarness(t)
		code, stdout, _ := h.run(cmd, "-h")
		if code != 0 || !strings.Contains(stdout, "Example:") {
			t.Errorf("%s -h: exit %d, stdout %q", cmd, code, stdout)
		}
	}
}

func TestLogoutRevokesTheTokenAndClearsConfig(t *testing.T) {
	srv := newFakeServer(t)
	h := newHarness(t)
	h.seed(t, cli.Config{Server: srv.URL, Token: testToken, TokenID: 41})
	code, stdout, stderr := h.run("logout")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if srv.revokeCalls != 1 || srv.revokeAuth != "Bearer "+testToken {
		t.Errorf("revoke calls %d with %q", srv.revokeCalls, srv.revokeAuth)
	}
	cfg := h.saved(t)
	if cfg.Token != "" || cfg.TokenID != 0 || cfg.Server != srv.URL {
		t.Errorf("config after logout = %+v", cfg)
	}
	assertNoToken(t, stdout, stderr)
}

func TestLogoutClearsConfigWhenServerAlreadyRejectsToken(t *testing.T) {
	srv := newFakeServer(t)
	srv.revokeCode = 401
	h := newHarness(t)
	h.seed(t, cli.Config{Server: srv.URL, Token: testToken, TokenID: 41})
	if code, _, stderr := h.run("logout"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if h.saved(t).Token != "" {
		t.Error("token kept")
	}
}

func TestLogoutKeepsTokenWhenServerUnreachable(t *testing.T) {
	srv := newFakeServer(t)
	srv.revokeCode = 500
	h := newHarness(t)
	h.seed(t, cli.Config{Server: srv.URL, Token: testToken, TokenID: 41})
	code, _, stderr := h.run("logout")
	if code != 7 {
		t.Errorf("exit = %d, want 7", code)
	}
	if h.saved(t).Token != testToken {
		t.Error("token removed although it was not revoked")
	}
	assertNoToken(t, stderr)
}

// A token pasted with --with-token has no id the CLI knows; the server lets a
// token revoke itself, so logout works for it too.
func TestLogoutRevokesATokenWithoutAKnownID(t *testing.T) {
	srv := newFakeServer(t)
	h := newHarness(t)
	h.seed(t, cli.Config{Server: srv.URL, Token: testToken})
	code, _, stderr := h.run("logout")
	if code != 0 || srv.revokeCalls != 1 || srv.revokeAuth != "Bearer "+testToken {
		t.Errorf("exit %d revoke calls %d auth %q", code, srv.revokeCalls, srv.revokeAuth)
	}
	if strings.Contains(stderr, "not revoked") {
		t.Errorf("stderr = %q", stderr)
	}
	if h.saved(t).Token != "" {
		t.Error("token kept")
	}
}

func TestLogoutWhenNotSignedIn(t *testing.T) {
	h := newHarness(t)
	code, _, stderr := h.run("logout")
	if code != 0 || !strings.Contains(stderr, "Not signed in") {
		t.Errorf("exit %d stderr %q", code, stderr)
	}
}

func TestWhoamiText(t *testing.T) {
	srv := newFakeServer(t)
	h := newHarness(t)
	h.seed(t, cli.Config{Server: srv.URL, Token: testToken, TokenID: 41})
	code, stdout, stderr := h.run("whoami")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{"Ada Lovelace", "ada@example.com", "view", "run", "dev-01", "approve", "prod"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	assertNoToken(t, stdout, stderr)
}

func TestWhoamiJSONIsOneDocument(t *testing.T) {
	srv := newFakeServer(t)
	h := newHarness(t)
	h.seed(t, cli.Config{Server: srv.URL, Token: testToken})
	code, stdout, stderr := h.run("whoami", "-o", "json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	dec := json.NewDecoder(strings.NewReader(stdout))
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil || dec.More() {
		t.Fatalf("stdout is not exactly one JSON document: %q (%v)", stdout, err)
	}
	if doc["email"] != "ada@example.com" {
		t.Errorf("doc = %v", doc)
	}
	assertNoToken(t, stdout, stderr)
}

func TestWhoamiRevokedTokenSaysNotSignedIn(t *testing.T) {
	srv := newFakeServer(t)
	srv.meCode = 401
	h := newHarness(t)
	h.seed(t, cli.Config{Server: srv.URL, Token: testToken})
	code, stdout, stderr := h.run("whoami")
	if code != 6 || strings.TrimSpace(stderr) != "yumdrake: Not signed in. Run yumdrake login." || stdout != "" {
		t.Errorf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
}

func TestWhoamiWithoutTokenAndWithEnvToken(t *testing.T) {
	srv := newFakeServer(t)
	h := newHarness(t)
	h.seed(t, cli.Config{Server: srv.URL})
	if code, _, stderr := h.run("whoami"); code != 6 || !strings.Contains(stderr, "Not signed in") {
		t.Errorf("exit %d stderr %q", code, stderr)
	}
	h.environ["YUMDRAKE_TOKEN"] = testToken
	if code, _, stderr := h.run("whoami"); code != 0 {
		t.Errorf("env token: exit %d stderr %q", code, stderr)
	}
	if srv.meAuth != "Bearer "+testToken {
		t.Errorf("auth = %q", srv.meAuth)
	}
}

func TestWhoamiUnreachableServerExits7(t *testing.T) {
	srv := newFakeServer(t)
	url := srv.URL
	srv.Close()
	h := newHarness(t)
	h.seed(t, cli.Config{Server: url, Token: testToken})
	code, _, stderr := h.run("whoami")
	if code != 7 {
		t.Errorf("exit = %d, want 7", code)
	}
	assertNoToken(t, stderr)
}

func TestLoginHonoursIntervalFromPendingAnswer(t *testing.T) {
	srv := newFakeServer(t)
	srv.pendingFor, srv.pendingInterval = 2, 7
	h := newHarness(t)
	if code, _, stderr := h.run("login", "--server", srv.URL); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	want := []time.Duration{3 * time.Second, 7 * time.Second, 7 * time.Second}
	if fmt.Sprint(h.waits) != fmt.Sprint(want) {
		t.Errorf("waits = %v, want %v", h.waits, want)
	}
}

func TestLoginStopsAtTheDeviceCodeDeadlineEvenIfServerNeverExpiresIt(t *testing.T) {
	srv := newFakeServer(t)
	srv.pendingFor = 1 << 30
	h := newHarness(t)
	h.onWait = func(call int) error {
		if call > 1000 {
			return errors.New("the login never ended")
		}
		return nil
	}
	code, _, stderr := h.run("login", "--server", srv.URL)
	if code != 6 || !strings.Contains(stderr, "expired") {
		t.Errorf("exit %d stderr %q", code, stderr)
	}
	if len(h.waits) < 100 || len(h.waits) > 300 {
		t.Errorf("waits = %d, want about 600s / 3s", len(h.waits))
	}
}

func TestLoginNeverOpensANonWebURL(t *testing.T) {
	for _, bad := range []string{"file:///etc/passwd", "javascript:alert(1)", "ftp://x.example/", "/device?user_code=A"} {
		srv := newFakeServer(t)
		srv.verifyURL = bad
		h := newHarness(t)
		h.terminal = true
		if code, _, stderr := h.run("login", "--server", srv.URL); code != 0 {
			t.Fatalf("%s: exit %d: %s", bad, code, stderr)
		}
		if len(h.opened) != 0 {
			t.Errorf("%s was passed to the opener: %v", bad, h.opened)
		}
	}
}

func TestLogoutWithEnvTokenChangesNothing(t *testing.T) {
	srv := newFakeServer(t)
	h := newHarness(t)
	h.seed(t, cli.Config{Server: srv.URL, Token: "file-token", TokenID: 41})
	h.environ["YUMDRAKE_TOKEN"] = testToken
	code, stdout, stderr := h.run("logout")
	if code != 0 || !strings.Contains(stderr, "YUMDRAKE_TOKEN") {
		t.Errorf("exit %d stderr %q", code, stderr)
	}
	if srv.revokeCalls != 0 {
		t.Errorf("revoke calls %d", srv.revokeCalls)
	}
	if cfg := h.saved(t); cfg.Token != "file-token" || cfg.TokenID != 41 {
		t.Errorf("config changed: %+v", cfg)
	}
	assertNoToken(t, stdout, stderr)
}

func TestLoginBacksOffOnTooManyRequests(t *testing.T) {
	t.Run("Retry-After wins", func(t *testing.T) {
		srv := newFakeServer(t)
		srv.pollReplies, srv.retryAfter = []int{429}, "9"
		h := newHarness(t)
		if code, _, stderr := h.run("login", "--server", srv.URL); code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		if fmt.Sprint(h.waits) != fmt.Sprint([]time.Duration{3 * time.Second, 9 * time.Second}) {
			t.Errorf("waits = %v", h.waits)
		}
	})
	t.Run("doubles up to 30s", func(t *testing.T) {
		srv := newFakeServer(t)
		srv.pollReplies = []int{429, 429, 429, 429, 429}
		h := newHarness(t)
		if code, _, stderr := h.run("login", "--server", srv.URL); code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		want := []time.Duration{3, 6, 12, 24, 30, 30}
		for i := range want {
			want[i] *= time.Second
		}
		if fmt.Sprint(h.waits) != fmt.Sprint(want) {
			t.Errorf("waits = %v, want %v", h.waits, want)
		}
	})
}

func TestLoginRefusesPlainHTTPToARemoteHost(t *testing.T) {
	h := newHarness(t)
	code, _, stderr := h.run("login", "--server", "http://yumdrake.example.com")
	if code != 2 || !strings.Contains(stderr, "unencrypted") {
		t.Errorf("exit %d stderr %q", code, stderr)
	}
	// With the opt-in the URL is accepted (the failure is then about reading the token).
	h.stdin = ""
	code, _, stderr = h.run("login", "--with-token", "--insecure-http", "--server", "http://yumdrake.example.com")
	if code != 2 || strings.Contains(stderr, "unencrypted") {
		t.Errorf("flag: exit %d stderr %q", code, stderr)
	}
}

func TestLoginCancelledByInterrupt(t *testing.T) {
	srv := newFakeServer(t)
	srv.pendingFor = 1 << 30
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.ctx = ctx
	h.onWait = func(call int) error {
		if call == 2 {
			cancel()
			return ctx.Err()
		}
		return nil
	}
	code, stdout, stderr := h.run("login", "--server", srv.URL)
	if code != 130 || !strings.Contains(stderr, "Sign-in cancelled") || stdout != "" {
		t.Errorf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if _, err := os.Stat(h.configPath); err == nil {
		t.Error("config written")
	}
}

func TestLoginHelpSaysFileModeChecksSkipWindows(t *testing.T) {
	h := newHarness(t)
	_, stdout, _ := h.run("login", "-h")
	if !strings.Contains(stdout, "Windows") {
		t.Errorf("help = %q", stdout)
	}
}

func TestCancelledCommandsSayStoppedNotSignInCancelled(t *testing.T) {
	srv := newFakeServer(t)
	h := newHarness(t)
	h.seed(t, cli.Config{Server: srv.URL, Token: testToken})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.ctx = ctx
	code, _, stderr := h.run("whoami")
	if code != 130 || !strings.Contains(stderr, "Stopped") || strings.Contains(stderr, "Sign-in") {
		t.Errorf("exit %d stderr %q", code, stderr)
	}
}
