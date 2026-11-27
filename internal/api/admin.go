package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/yamdrake/internal/audit"
	"github.com/rcss-labs/yamdrake/internal/github"
	"github.com/rcss-labs/yamdrake/internal/store"
)

// connectionPurpose binds the ciphertext of a connection secret to its use.
const connectionPurpose = "github-connection"

// ownerPattern is GitHub's rule for user and organisation names.
var ownerPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)

type connectionResponse struct {
	ID             int64     `json:"id"`
	Owner          string    `json:"owner"`
	Method         string    `json:"method"`
	AppID          int64     `json:"app_id"`
	InstallationID int64     `json:"installation_id"`
	CreatedAt      time.Time `json:"created_at"`
}

func connectionView(c store.Connection) connectionResponse {
	return connectionResponse{ID: c.ID, Owner: c.Owner, Method: c.Method, AppID: c.AppID,
		InstallationID: c.InstallationID, CreatedAt: c.CreatedAt}
}

func (s *server) listConnections(w http.ResponseWriter, r *http.Request) {
	connections, err := store.ListConnections(r.Context(), s.Pool)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]connectionResponse, 0, len(connections))
	for _, c := range connections {
		out = append(out, connectionView(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": out})
}

type connectionRequest struct {
	Owner          string `json:"owner"`
	Method         string `json:"method"`
	Token          string `json:"token"`
	AppID          int64  `json:"app_id"`
	InstallationID int64  `json:"installation_id"`
	PrivateKey     string `json:"private_key"`
}

// secret validates the request and returns the bytes to encrypt. Messages
// never repeat a submitted secret.
func (q connectionRequest) secret() ([]byte, string) {
	switch q.Method {
	case "token":
		if q.Token == "" {
			return nil, `method "token" requires token`
		}
		if q.AppID != 0 || q.InstallationID != 0 || q.PrivateKey != "" {
			return nil, `method "token" takes only token`
		}
		return []byte(q.Token), ""
	case "app":
		if q.AppID <= 0 || q.InstallationID <= 0 || q.PrivateKey == "" {
			return nil, `method "app" requires app_id, installation_id and private_key`
		}
		if q.Token != "" {
			return nil, `method "app" takes no token`
		}
		// Parse the key now so a typo is reported at once; the base URL is
		// irrelevant here because nothing is sent.
		creds := github.Credentials{Method: "app", AppID: q.AppID, InstallationID: q.InstallationID, PrivateKeyPEM: []byte(q.PrivateKey)}
		if _, err := github.New("https://api.github.com", nil, creds); err != nil {
			return nil, "private_key is not a valid RSA private key in PEM form"
		}
		return []byte(q.PrivateKey), ""
	default:
		return nil, `method must be "token" or "app"`
	}
}

func (s *server) createConnection(w http.ResponseWriter, r *http.Request) {
	var req connectionRequest
	if !decodeBody(w, r, &req) {
		return
	}
	// GitHub owners are case-insensitive but the database is not, so owners are
	// stored (and looked up) in lower case.
	owner := strings.ToLower(strings.TrimSpace(req.Owner))
	if !ownerPattern.MatchString(owner) {
		invalid(w, fmt.Sprintf("owner %q is not a valid GitHub user or organisation name", req.Owner))
		return
	}
	plaintext, problem := req.secret()
	if problem != "" {
		invalid(w, problem)
		return
	}
	ciphertext, err := s.Cipher.Encrypt(r.Context(), connectionPurpose, plaintext)
	if err != nil {
		s.internalError(w, r, fmt.Errorf("encrypt connection secret: %w", err))
		return
	}
	var created store.Connection
	err = s.auditedWrite(r, func(ctx context.Context, tx pgx.Tx) (audit.Event, error) {
		var err error
		created, err = store.CreateConnection(ctx, tx, store.Connection{
			Owner: owner, Method: req.Method, AppID: req.AppID, InstallationID: req.InstallationID}, ciphertext)
		// Never the token or the key, only what identifies the connection.
		return audit.Event{Action: "connection.create", TargetType: "github_connection", TargetID: strconv.FormatInt(created.ID, 10),
			Detail: map[string]any{"owner": owner, "method": req.Method, "app_id": req.AppID, "installation_id": req.InstallationID}}, err
	})
	switch {
	case errors.Is(err, store.ErrDuplicate):
		writeError(w, http.StatusConflict, codeConflict, fmt.Sprintf("owner %q already has a connection", owner))
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	if s.OnConnectionCreated != nil {
		s.OnConnectionCreated(owner)
	}
	writeJSON(w, http.StatusCreated, connectionView(created))
}
