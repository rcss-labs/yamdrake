package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const secretToken = "yd_SECRET-TOKEN-VALUE" //nolint:gosec // a fake token

func serverReplying(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestClientMapsStatusToExitCode(t *testing.T) {
	apiErr := `{"error":{"code":"x","message":"the server said so"}}`
	tests := []struct {
		name     string
		status   int
		wantCode int
		wantMsg  string
	}{
		{"unauthorized", 401, 6, "Not signed in. Run yumdrake login."},
		{"forbidden", 403, 6, "the server said so"},
		{"not found", 404, 2, "the server said so"},
		{"conflict", 409, 2, "the server said so"},
		{"unprocessable", 422, 2, "the server said so"},
		{"bad request", 400, 2, "the server said so"},
		{"server error", 500, 7, "server"},
		{"bad gateway", 502, 7, "server"},
		{"too many", 429, 7, "the server said so"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := serverReplying(tt.status, apiErr)
			defer srv.Close()
			client, err := NewClient(Config{Server: srv.URL, Token: secretToken})
			if err != nil {
				t.Fatal(err)
			}
			err = client.Do(context.Background(), "GET", "/api/v1/me", nil, nil)
			if got := ExitCodeFor(err); got != tt.wantCode {
				t.Errorf("exit code = %d, want %d (err %v)", got, tt.wantCode, err)
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("message = %v, want it to contain %q", err, tt.wantMsg)
			}
		})
	}
}

func TestClientConnectionRefusedIsExit7(t *testing.T) {
	srv := serverReplying(200, "{}")
	url := srv.URL
	srv.Close()
	client, err := NewClient(Config{Server: url, Token: secretToken})
	if err != nil {
		t.Fatal(err)
	}
	err = client.Do(context.Background(), "GET", "/api/v1/me", nil, nil)
	if ExitCodeFor(err) != 7 {
		t.Fatalf("exit code = %d, want 7 (err %v)", ExitCodeFor(err), err)
	}
	if strings.Contains(err.Error(), secretToken) {
		t.Error("token appears in the error")
	}
}

func TestClientNeverPutsTokenInErrors(t *testing.T) {
	// A server that echoes the Authorization header into its message must not leak it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "invalid", "message": "bad " + r.Header.Get("Authorization")}})
	}))
	defer srv.Close()
	client, _ := NewClient(Config{Server: srv.URL, Token: secretToken})
	err := client.Do(context.Background(), "GET", "/x", nil, nil)
	if err == nil || strings.Contains(err.Error(), secretToken) {
		t.Errorf("error = %v, must not contain the token", err)
	}
}

func TestClientSendsBearerAndJSONAndDecodes(t *testing.T) {
	var gotAuth, gotType string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotType = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":7}`))
	}))
	defer srv.Close()
	client, _ := NewClient(Config{Server: srv.URL + "/", Token: secretToken})
	var out struct{ ID int }
	if err := client.Do(context.Background(), "POST", "/api/v1/tokens", map[string]string{"name": "n"}, &out); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer "+secretToken || gotType != "application/json" || gotBody["name"] != "n" || out.ID != 7 {
		t.Errorf("auth=%q type=%q body=%v out=%+v", gotAuth, gotType, gotBody, out)
	}
}

func TestClientNoContentAndNoTokenHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	client, _ := NewClient(Config{Server: srv.URL})
	if err := client.Do(context.Background(), "DELETE", "/api/v1/tokens/1", nil, nil); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want none without a token", gotAuth)
	}
}

func TestNewClientNeedsServer(t *testing.T) {
	_, err := NewClient(Config{})
	if ExitCodeFor(err) != 2 || !strings.Contains(err.Error(), "--server") {
		t.Errorf("err = %v (code %d)", err, ExitCodeFor(err))
	}
	if _, err := NewClient(Config{Server: "ftp://x"}); ExitCodeFor(err) != 2 {
		t.Errorf("non-http server accepted: %v", err)
	}
}

func TestAPIErrorCarriesStatusAndCode(t *testing.T) {
	srv := serverReplying(400, `{"error":{"code":"expired_token","message":"gone"}}`)
	defer srv.Close()
	client, _ := NewClient(Config{Server: srv.URL})
	err := client.Do(context.Background(), "POST", "/x", nil, nil)
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Status != 400 || exit.APICode != "expired_token" {
		t.Errorf("err = %#v", err)
	}
}

func TestExitCodeFor(t *testing.T) {
	if ExitCodeFor(nil) != 0 || ExitCodeFor(context.Canceled) != 1 || ExitCodeFor(&ExitError{Code: 3}) != 3 {
		t.Error("unexpected exit codes")
	}
}

func TestNewClientServerURLRules(t *testing.T) {
	for _, ok := range []string{"http://localhost:8080", "http://127.0.0.1:9", "http://[::1]:9", "https://yumdrake.example.com", "https://example.com/yumdrake/"} {
		if _, err := NewClient(Config{Server: ok}); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	_, err := NewClient(Config{Server: "http://yumdrake.example.com"})
	if ExitCodeFor(err) != 2 || err == nil || !strings.Contains(err.Error(), "unencrypted") {
		t.Errorf("remote http: %v", err)
	}
	if _, err := NewClient(Config{Server: "http://yumdrake.example.com", InsecureHTTP: true}); err != nil {
		t.Errorf("opt-in refused: %v", err)
	}
	for _, bad := range []string{"https://x.example/?a=b", "https://x.example/#frag", "https://x.example/p?"} {
		if _, err := NewClient(Config{Server: bad}); ExitCodeFor(err) != 2 {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestClientKeepsPathPrefix(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.URL.Path }))
	defer srv.Close()
	client, _ := NewClient(Config{Server: srv.URL + "/yumdrake/"})
	_ = client.Do(context.Background(), "GET", "/api/v1/me", nil, nil)
	if got != "/yumdrake/api/v1/me" {
		t.Errorf("path = %q", got)
	}
}

func TestClientReportsRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "9")
		w.WriteHeader(429)
	}))
	defer srv.Close()
	client, _ := NewClient(Config{Server: srv.URL})
	var exit *ExitError
	if err := client.Do(context.Background(), "GET", "/x", nil, nil); !errors.As(err, &exit) || exit.RetryAfter != 9*time.Second {
		t.Errorf("err = %#v", err)
	}
}
