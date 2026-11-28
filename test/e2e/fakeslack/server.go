// Package fakeslack is a fake Slack Web API for end-to-end tests. It answers
// POST /chat.postMessage, checks the bot token and records each message in the
// order it arrived.
package fakeslack

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
)

// Message is one posted message.
type Message struct {
	Channel string
	Text    string
}

// Server is the fake. Its URL is the API base: point the orchestrator's
// YUMDRAKE_SLACK_API_URL at it.
type Server struct {
	srv   *httptest.Server
	token string

	mu       sync.Mutex
	messages []Message
	failing  int
}

// New starts a fake that accepts only the bot token given.
func New(token string) *Server {
	s := &Server{token: token}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /chat.postMessage", s.postMessage)
	s.srv = httptest.NewServer(mux)
	return s
}

// URL is the base URL of the fake API (no trailing slash).
func (s *Server) URL() string { return s.srv.URL }

// Close stops the server.
func (s *Server) Close() { s.srv.Close() }

// SetFailing makes every post answer with the HTTP status (an outage). Zero
// restores normal answers. A failed post is not recorded.
func (s *Server) SetFailing(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failing = status
}

// Messages returns the messages recorded so far, in arrival order.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.messages...)
}

func (s *Server) postMessage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	s.mu.Lock()
	failing := s.failing
	s.mu.Unlock()
	if failing != 0 {
		w.WriteHeader(failing)
		_, _ = w.Write([]byte(`{"ok":false,"error":"fake_outage"}`))
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.token {
		_, _ = w.Write([]byte(`{"ok":false,"error":"invalid_auth"}`))
		return
	}
	var body struct {
		Channel string `json:"channel"`
		Text    string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		_, _ = w.Write([]byte(`{"ok":false,"error":"invalid_json"}`))
		return
	}
	s.mu.Lock()
	s.messages = append(s.messages, Message{Channel: body.Channel, Text: body.Text})
	s.mu.Unlock()
	_, _ = w.Write([]byte(`{"ok":true}`))
}
