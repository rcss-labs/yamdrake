package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultSlackTimeout = 10 * time.Second
	// maxSlackResponse bounds how much of a response is read; Slack replies are tiny.
	maxSlackResponse = 64 << 10
)

// permanentSlackCodes never succeed on retry without a person changing something.
var permanentSlackCodes = map[string]bool{
	"channel_not_found": true,
	"not_in_channel":    true,
	"invalid_auth":      true,
	"account_inactive":  true,
	"token_revoked":     true,
}

// slackCodePattern is the shape of a Slack error code. Anything else in the
// body is not trusted enough to put into an error.
var slackCodePattern = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// Slack posts messages through the Slack Web API.
type Slack struct {
	baseURL string
	client  *http.Client
}

// NewSlack builds a client for the API at baseURL. A nil client gets a 10 second timeout.
func NewSlack(baseURL string, client *http.Client) *Slack {
	if client == nil {
		client = &http.Client{Timeout: defaultSlackTimeout}
	}
	return &Slack{baseURL: strings.TrimRight(baseURL, "/"), client: client}
}

// SlackError is a failed post that Slack (or its gateway) answered.
type SlackError struct {
	Code       string        // Slack's error code, or "http_<status>" when the body had none
	Status     int           // HTTP status
	RetryAfter time.Duration // set on 429 from the Retry-After header
	Permanent  bool          // retrying cannot help
}

func (e *SlackError) Error() string {
	if e.Status != http.StatusOK {
		return fmt.Sprintf("slack: %s (HTTP %d)", e.Code, e.Status)
	}
	return "slack: " + e.Code
}

// Post sends text to channel with the given bot token.
// Errors never contain the token or the raw response body.
func (s *Slack) Post(ctx context.Context, token, channel, text string) error {
	payload, err := json.Marshal(map[string]string{"channel": channel, "text": text})
	if err != nil {
		return fmt.Errorf("slack: encode message: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/chat.postMessage", bytes.NewReader(payload))
	if err != nil {
		return errors.New("slack: build request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := s.client.Do(req)
	if err != nil {
		return transportError(err, token)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSlackResponse))
	if err != nil {
		return transportError(err, token)
	}

	var reply struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	parsed := json.Unmarshal(body, &reply) == nil

	if resp.StatusCode == http.StatusOK && parsed && reply.OK {
		return nil
	}

	code := reply.Error
	if !parsed || !slackCodePattern.MatchString(code) {
		code = "http_" + strconv.Itoa(resp.StatusCode)
		if resp.StatusCode == http.StatusOK {
			code = "invalid_response"
		}
	}
	se := &SlackError{Code: code, Status: resp.StatusCode, Permanent: permanentSlackCodes[code]}
	if resp.StatusCode == http.StatusTooManyRequests {
		if secs, convErr := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); convErr == nil && secs > 0 {
			se.RetryAfter = time.Duration(secs) * time.Second
		}
	}
	return se
}

// transportError reports a network failure without the request URL and with the token scrubbed.
func transportError(err error, token string) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	msg := err.Error()
	if token != "" {
		msg = strings.ReplaceAll(msg, token, "[redacted]")
	}
	return errors.New("slack: request failed: " + msg)
}
