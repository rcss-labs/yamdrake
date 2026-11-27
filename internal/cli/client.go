package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Exit codes shared by every command.
const (
	ExitOK        = 0
	ExitFailed    = 1
	ExitUsage     = 2
	ExitNotSignIn = 6
	ExitServer    = 7
)

// NotSignedInMessage is what every command says when the token is missing,
// expired or revoked.
const NotSignedInMessage = "Not signed in. Run yumdrake login."

// ExitError is an error that decides the process exit code. Its message is
// safe to print: it never contains the token.
type ExitError struct {
	Code    int
	Message string
	// Status and APICode are set for errors that came from an API response.
	Status  int
	APICode string
	// RetryAfter is the server's Retry-After, when it sent one.
	RetryAfter time.Duration
}

func (e *ExitError) Error() string { return e.Message }

// NotSignedIn is the error for a missing, expired or revoked token.
func NotSignedIn() *ExitError {
	return &ExitError{Code: ExitNotSignIn, Message: NotSignedInMessage}
}

// ExitCodeFor maps an error to the process exit code: 0 for nil, the code of
// an ExitError, else 1.
func ExitCodeFor(err error) int {
	if err == nil {
		return ExitOK
	}
	var exit *ExitError
	if errors.As(err, &exit) {
		return exit.Code
	}
	return ExitFailed
}

// Client calls the /api/v1 of one server with one token.
type Client struct {
	server string
	token  string
	http   *http.Client
}

// NewClient makes a client for c. The token may be empty for the calls that
// come before sign-in.
func NewClient(c Config) (*Client, error) {
	server := strings.TrimRight(c.Server, "/")
	if server == "" {
		return nil, &ExitError{Code: ExitUsage, Message: "no server: pass --server URL or set YUMDRAKE_SERVER"}
	}
	u, err := url.Parse(server)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, &ExitError{Code: ExitUsage, Message: "the server must be an http:// or https:// URL"}
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, &ExitError{Code: ExitUsage, Message: "the server URL must not have a query string or fragment"}
	}
	if u.Scheme == "http" && !c.InsecureHTTP && !isLoopbackHost(u.Hostname()) {
		return nil, &ExitError{Code: ExitUsage, Message: "the server URL is plain http:// to a remote host, so your token would be sent unencrypted; use https:// or pass --insecure-http (or set YUMDRAKE_INSECURE_HTTP=1)"}
	}
	return &Client{server: server, token: c.Token, http: &http.Client{Timeout: 30 * time.Second}}, nil
}

const maxResponseBytes = 16 << 20

// Do sends method and path (with its query) with in as the JSON body when not
// nil, and decodes a JSON answer into out when not nil. Failures are
// *ExitError: 401 is "Not signed in" (6), 403 is 6, 5xx and network failures
// are 7, other 4xx are 2 with the server's message.
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.server+path, body)
	if err != nil {
		return &ExitError{Code: ExitUsage, Message: "invalid request path"}
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// The transport error repeats the URL and the reason, never the headers.
		return &ExitError{Code: ExitServer, Message: fmt.Sprintf("cannot reach the server %s: %s", c.server, c.scrub(rootCause(err)))}
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return &ExitError{Code: ExitServer, Message: "the server's answer was cut off"}
	}
	if resp.StatusCode >= 400 {
		return c.statusError(resp.StatusCode, resp.Header.Get("Retry-After"), data)
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &ExitError{Code: ExitServer, Message: "the server's answer was not understood"}
	}
	return nil
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func rootCause(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	return err.Error()
}

// scrub removes the token from text that may have come from the network.
func (c *Client) scrub(s string) string {
	if c.token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.token, "[redacted]")
}

func (c *Client) statusError(status int, retryAfter string, data []byte) error {
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &body)
	message := c.scrub(body.Error.Message)
	if message == "" {
		message = fmt.Sprintf("the server answered %d", status)
	}
	e := &ExitError{Code: ExitUsage, Message: message, Status: status, APICode: body.Error.Code}
	if seconds, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && seconds > 0 {
		e.RetryAfter = time.Duration(seconds) * time.Second
	}
	switch {
	case status == http.StatusUnauthorized:
		e.Code, e.Message = ExitNotSignIn, NotSignedInMessage
	case status == http.StatusForbidden:
		e.Code = ExitNotSignIn
		e.Message = "Not permitted: " + message
	case status >= 500:
		e.Code = ExitServer
		e.Message = fmt.Sprintf("the server failed (%d)", status)
	case status == http.StatusTooManyRequests:
		e.Code = ExitServer
	}
	return e
}
