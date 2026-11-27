// Package api serves the /api/v1 HTTP API: sign-in, inventory, folders, job
// sources and GitHub connections. Every request is made by a principal (a
// bearer token or a session cookie) and every route names the action it needs.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	valkey "github.com/valkey-io/valkey-go"

	"github.com/rcss-labs/yamdrake/internal/auth"
	"github.com/rcss-labs/yamdrake/internal/secrets"
	"github.com/rcss-labs/yamdrake/internal/session"
	"github.com/rcss-labs/yamdrake/internal/store"
)

// maxBodyBytes limits every request body.
const maxBodyBytes = 1 << 20

// Error codes of the API.
const (
	codeUnauthorized = "unauthorized"
	codeForbidden    = "forbidden"
	codeUnavailable  = "unavailable"
	codeTooMany      = "too_many_requests"
	codeNotFound     = "not_found"
	codeInvalid      = "invalid"
	codeConflict     = "conflict"
	codeInternal     = "internal"
)

// Syncer runs on-demand syncs; *sync.Engine satisfies it. The request context
// is passed through, so a slow sync ends with its request.
type Syncer interface {
	SyncInventory(ctx context.Context) error
	// SetInventorySource stores the source and syncs it as one serialised step.
	SetInventorySource(ctx context.Context, source store.InventorySource, inTx func(context.Context, store.DBTX) error) error
	SyncJob(ctx context.Context, jobSourceID int64) error
}

// Deps are the collaborators of the API.
type Deps struct {
	Pool   *pgxpool.Pool
	Engine Syncer
	Cipher secrets.Cipher
	Log    *slog.Logger
	// OnConnectionCreated, when set, is called with the (lower case) owner
	// after a connection is stored so caches for that owner can be dropped.
	OnConnectionCreated func(owner string)

	// Sessions stores browser sessions; Valkey holds the sign-in failure
	// counters. Both are required.
	Sessions *session.Manager
	Valkey   valkey.Client
	// PublicURL is the address users reach the service at; cookie requests that
	// change state must come from it. Required.
	PublicURL string
	// LockoutWindow is how long five failed sign-ins for one email lock it
	// (default 15 minutes). Tests shorten it.
	LockoutWindow time.Duration
	// Now is the clock (default time.Now).
	Now func() time.Time
	// DevicePollInterval is how often a CLI may poll a device sign-in (default
	// 5 seconds). Tests shorten it.
	DevicePollInterval time.Duration
}

// valkeyTimeout bounds every Valkey call made for a request: against a dead
// server valkey-go keeps redialling until its context ends.
var valkeyTimeout = 3 * time.Second

type server struct {
	Deps
	// dummyHash is verified when the account does not exist (or cannot sign in
	// with a password), so every sign-in costs one argon2 verification.
	dummyHash string
}

// dummyPasswordHash is computed once per process, with the production parameters.
var dummyPasswordHash = sync.OnceValues(func() (string, error) {
	return auth.HashPassword("a password nobody has chosen")
})

// New returns the handler for the whole /api/v1 tree (full paths are
// registered, so mount it at "/api/v1/"). Every route except sign-in needs an
// authenticated principal, and every route states the action it needs.
func New(deps Deps) http.Handler {
	if deps.Pool == nil || deps.Sessions == nil || deps.Valkey == nil || deps.PublicURL == "" {
		panic("api.New: Pool, Sessions, Valkey and PublicURL are required")
	}
	if deps.Log == nil {
		deps.Log = slog.New(slog.DiscardHandler)
	}
	if deps.LockoutWindow <= 0 {
		deps.LockoutWindow = defaultLockoutWindow
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.DevicePollInterval <= 0 {
		deps.DevicePollInterval = defaultDevicePollInterval
	}
	dummy, err := dummyPasswordHash()
	if err != nil {
		panic("api.New: dummy password hash: " + err.Error())
	}
	s := &server{Deps: deps, dummyHash: dummy}

	authenticated := http.NewServeMux()
	route := func(pattern string, action auth.Action, handler http.HandlerFunc) {
		authenticated.Handle(pattern, s.require(action, nil)(handler))
	}
	route("GET /api/v1/me", auth.View, s.me)
	route("POST /api/v1/auth/logout", auth.View, s.logout)
	route("GET /api/v1/inventory", auth.View, s.getInventory)
	route("GET /api/v1/inventory/services/{name}", auth.View, s.getService)
	route("POST /api/v1/inventory/sync", auth.ManageConnections, s.syncInventory)
	route("PUT /api/v1/admin/inventory-source", auth.ManageConnections, s.putInventorySource)
	route("GET /api/v1/admin/github-connections", auth.ManageConnections, s.listConnections)
	route("POST /api/v1/admin/github-connections", auth.ManageConnections, s.createConnection)
	route("GET /api/v1/folders", auth.View, s.listFolders)
	route("POST /api/v1/folders", auth.ManageJobs, s.createFolder)
	route("GET /api/v1/jobs", auth.View, s.listJobs)
	route("POST /api/v1/jobs", auth.ManageJobs, s.registerJob)
	route("GET /api/v1/jobs/{id}", auth.View, s.getJob)
	route("PATCH /api/v1/jobs/{id}", auth.ManageJobs, s.moveJob)
	route("POST /api/v1/jobs/{id}/sync", auth.ManageJobs, s.syncJob)
	s.accessRoutes(route)
	// Unknown paths: 401 without credentials, 404 with them.
	authenticated.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
	})

	root := http.NewServeMux()
	// Sign-in is the one route that is open to everyone; it carries no session
	// cookie to check, so it has its own origin check (login CSRF).
	root.HandleFunc("POST /api/v1/auth/login", s.login)
	// The CLI's device sign-in starts and finishes without credentials (the
	// CLI has none yet). Neither route reads a cookie, so neither can be used
	// cross-site to act as a signed-in browser user; confirm is authenticated.
	root.HandleFunc("POST /api/v1/cli/login", s.startDeviceLogin)
	root.HandleFunc("POST /api/v1/cli/login/token", s.pollDeviceLogin)
	root.Handle("/", s.authenticate(authenticated))
	return root
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	var body errorBody
	body.Error.Code, body.Error.Message = code, message
	writeJSON(w, status, body)
}

// internalError logs the detail and answers with a generic message.
func (s *server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.Log.Error("api request failed", "method", r.Method, "path", r.URL.Path, "error", err.Error())
	writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
}

func invalid(w http.ResponseWriter, message string) {
	writeError(w, http.StatusBadRequest, codeInvalid, message)
}

// decodeBody reads one JSON object into dst, rejecting unknown fields,
// trailing data and bodies over 1 MB. It writes the error response itself and
// reports whether decoding succeeded.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(dst)
	if err == nil {
		if more := decoder.Decode(&struct{}{}); !errors.Is(more, io.EOF) {
			err = errors.New("the body must contain exactly one JSON object")
		}
	}
	if err == nil {
		return true
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, codeInvalid, "request body is larger than 1 MB")
		return false
	}
	invalid(w, "invalid request body: "+err.Error())
	return false
}

// pathID parses the {id} path value.
func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func notFound(w http.ResponseWriter, what string) {
	writeError(w, http.StatusNotFound, codeNotFound, what+" not found")
}
