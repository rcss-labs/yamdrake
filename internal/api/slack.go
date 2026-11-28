package api

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/yamdrake/internal/audit"
	"github.com/rcss-labs/yamdrake/internal/auth"
	"github.com/rcss-labs/yamdrake/internal/notify"
)

const (
	defaultSlackAPIURL = "https://slack.com/api"
	// maxSlackTokenLength and maxSlackChannelRunes bound what the settings page may send.
	maxSlackTokenLength  = 512
	maxSlackChannelRunes = 200
	slackTestMessage     = "Yumdrake can post here."
	// slackTestTimeout bounds the test post, which a person is waiting on.
	slackTestTimeout = 15 * time.Second
)

func (s *server) slackRoutes() []routeDef {
	return []routeDef{
		{"GET /api/v1/admin/slack", auth.ManageConnections, s.getSlack},
		{"PUT /api/v1/admin/slack", auth.ManageConnections, s.putSlack},
		{"POST /api/v1/admin/slack/test", auth.ManageConnections, s.testSlack},
	}
}

// slackResponse says whether a Slack token is stored. The token itself is
// never part of any response.
type slackResponse struct {
	TokenSet bool       `json:"token_set"`
	SetAt    *time.Time `json:"set_at"`
	// SetByName is who stored the token: their name, else their email; "" when unset.
	SetByName string `json:"set_by_name"`
}

// slackTestResponse is Slack's answer to a test post. Code is Slack's error
// code when the post failed, and "" when it did not.
type slackTestResponse struct {
	OK   bool   `json:"ok"`
	Code string `json:"code"`
}

func (s *server) slackView(ctx context.Context) (slackResponse, error) {
	status, err := notify.LoadSlackStatus(ctx, s.Pool)
	if err != nil {
		return slackResponse{}, err
	}
	out := slackResponse{TokenSet: status.Configured}
	if status.SetAt != nil {
		at := status.SetAt.UTC()
		out.SetAt = &at
	}
	if status.SetBy != nil {
		setBy, err := s.personName(ctx, map[int64]string{}, *status.SetBy)
		if err != nil {
			return slackResponse{}, err
		}
		out.SetByName = setBy
	}
	return out, nil
}

func (s *server) getSlack(w http.ResponseWriter, r *http.Request) {
	view, err := s.slackView(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

type putSlackRequest struct {
	Token string `json:"token"`
}

func (s *server) putSlack(w http.ResponseWriter, r *http.Request) {
	var req putSlackRequest
	if !decodeBody(w, r, &req) {
		return
	}
	token := strings.TrimSpace(req.Token)
	switch {
	case token == "":
		invalid(w, "token is required")
		return
	case len(token) > maxSlackTokenLength:
		invalid(w, "token is too long")
		return
	}
	principal, _ := PrincipalFrom(r.Context())
	err := s.auditedWrite(r, func(ctx context.Context, tx pgx.Tx) (audit.Event, error) {
		if err := notify.SaveSlackToken(ctx, tx, s.Cipher, token, principal.UserID, s.Now()); err != nil {
			return audit.Event{}, err
		}
		// No detail: the token must not reach the audit log in any form.
		return audit.Event{Action: "slack.set", TargetType: "slack", TargetID: "slack"}, nil
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.getSlack(w, r)
}

type testSlackRequest struct {
	Channel string `json:"channel"`
}

// testSlack posts a message to a channel with the stored token and reports
// Slack's answer. A refusal by Slack is a 200 with ok false and Slack's code:
// the request itself was fine and the person needs the code to fix the setup.
func (s *server) testSlack(w http.ResponseWriter, r *http.Request) {
	var req testSlackRequest
	if !decodeBody(w, r, &req) {
		return
	}
	channel := strings.TrimSpace(req.Channel)
	if channel == "" || utf8.RuneCountInString(channel) > maxSlackChannelRunes {
		invalid(w, "channel is required")
		return
	}
	token, err := notify.SlackTokenSource(s.Pool, s.Cipher)(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if token == "" {
		writeError(w, http.StatusConflict, codeConflict, "Slack is not connected.")
		return
	}

	slack := s.Slack
	if slack == nil {
		slack = notify.NewSlack(defaultSlackAPIURL, nil)
	}
	ctx, cancel := context.WithTimeout(r.Context(), slackTestTimeout)
	defer cancel()
	result := slackTestResponse{OK: true}
	detail := map[string]any{"channel": channel, "ok": true}
	if postErr := slack.Post(ctx, token, channel, slackTestMessage); postErr != nil {
		code := notify.ErrorCode(postErr)
		result = slackTestResponse{OK: false, Code: code}
		detail = map[string]any{"channel": channel, "ok": false, "error": code}
	}
	if err := s.recordAudit(r.Context(), s.Pool, r, audit.Event{Action: "slack.test", TargetType: "slack", TargetID: "slack", Detail: detail}); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
