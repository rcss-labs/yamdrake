package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testToken = "xoxb-fake-test-token-1234" //nolint:gosec // fake literal for tests

// fakeSlack serves every request with the given status, headers and body.
func fakeSlack(t *testing.T, status int, headers map[string]string, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func asSlackError(t *testing.T, err error) *SlackError {
	t.Helper()
	var se *SlackError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SlackError, got %T: %v", err, err)
	}
	return se
}

func TestPostSendsBearerAndBody(t *testing.T) {
	var gotPath, gotAuth, gotType, gotMethod string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		gotAuth = r.Header.Get("Authorization")
		gotType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	if err := NewSlack(srv.URL, nil).Post(context.Background(), testToken, "C123", "hello"); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/chat.postMessage" {
		t.Errorf("request = %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer "+testToken {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotType != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", gotType)
	}
	if gotBody["channel"] != "C123" || gotBody["text"] != "hello" {
		t.Errorf("body = %v", gotBody)
	}
}

func TestOkFalseIsErrorWithCode(t *testing.T) {
	srv := fakeSlack(t, 200, nil, `{"ok":false,"error":"channel_not_found"}`)
	err := NewSlack(srv.URL, nil).Post(context.Background(), testToken, "C1", "x")
	if err == nil {
		t.Fatal("expected an error")
	}
	se := asSlackError(t, err)
	if se.Code != "channel_not_found" {
		t.Errorf("Code = %q", se.Code)
	}
	if !strings.Contains(err.Error(), "channel_not_found") {
		t.Errorf("Error() = %q, want the Slack code", err.Error())
	}
}

func TestRateLimitCarriesRetryAfter(t *testing.T) {
	srv := fakeSlack(t, 429, map[string]string{"Retry-After": "7"}, `{"ok":false,"error":"ratelimited"}`)
	err := NewSlack(srv.URL, nil).Post(context.Background(), testToken, "C1", "x")
	se := asSlackError(t, err)
	if se.RetryAfter != 7*time.Second {
		t.Errorf("RetryAfter = %v", se.RetryAfter)
	}
	if se.Permanent {
		t.Error("a rate limit must not be permanent")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("Error() = %q, want the HTTP status", err.Error())
	}
}

func TestPermanentCodesAreMarkedPermanent(t *testing.T) {
	cases := []struct {
		code      string
		permanent bool
	}{
		{"channel_not_found", true},
		{"not_in_channel", true},
		{"invalid_auth", false},
		{"account_inactive", false},
		{"token_revoked", false},
		{"ratelimited", false},
		{"internal_error", false},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			srv := fakeSlack(t, 200, nil, `{"ok":false,"error":"`+tc.code+`"}`)
			se := asSlackError(t, NewSlack(srv.URL, nil).Post(context.Background(), testToken, "C1", "x"))
			if se.Permanent != tc.permanent {
				t.Errorf("Permanent = %v, want %v", se.Permanent, tc.permanent)
			}
		})
	}
}

func TestServerErrorIsNotPermanent(t *testing.T) {
	srv := fakeSlack(t, 503, nil, "upstream down")
	se := asSlackError(t, NewSlack(srv.URL, nil).Post(context.Background(), testToken, "C1", "x"))
	if se.Permanent {
		t.Error("a 5xx must not be permanent")
	}
}

func TestErrorNeverContainsToken(t *testing.T) {
	// Servers that echo the token back must not leak it through the error.
	leaky := testToken
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()

	cases := []struct {
		name   string
		url    string
		client *http.Client
	}{
		{"ok false", fakeSlack(t, 200, nil, `{"ok":false,"error":"invalid_auth","detail":"`+leaky+`"}`).URL, nil},
		{"rate limited", fakeSlack(t, 429, map[string]string{"Retry-After": "3"}, `{"ok":false,"error":"`+leaky+`"}`).URL, nil},
		{"server error", fakeSlack(t, 500, nil, "boom "+leaky).URL, nil},
		{"transport error", closedURL, nil},
		// A transport whose own error carries the token (as a proxy or a
		// custom dialer might): the scrub must remove it.
		{"transport error echoing the token", "http://slack.invalid", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed for Bearer " + leaky)
		})}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := NewSlack(tc.url, tc.client).Post(context.Background(), testToken, "C1", "x")
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), testToken) {
				t.Errorf("error leaks the token: %q", err.Error())
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCancelledContextGivesContextCanceled(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release) // runs first, so a failed assertion cannot leave Close waiting

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	err := NewSlack(srv.URL, nil).Post(ctx, testToken, "C1", "x")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want errors.Is(err, context.Canceled)", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("error leaks the token: %q", err.Error())
	}
}
