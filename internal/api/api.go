// Package api serves the /api/v1 HTTP API: inventory, folders, job sources and
// GitHub connections. Until sign-in exists every route requires the bootstrap
// token (see guard.go).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rcss-labs/yamdrake/internal/config"
	"github.com/rcss-labs/yamdrake/internal/secrets"
	"github.com/rcss-labs/yamdrake/internal/store"
)

// maxBodyBytes limits every request body.
const maxBodyBytes = 1 << 20

// Error codes of the API.
const (
	codeUnauthorized = "unauthorized"
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
	SetInventorySource(ctx context.Context, source store.InventorySource) error
	SyncJob(ctx context.Context, jobSourceID int64) error
}

// Deps are the collaborators of the API.
type Deps struct {
	Pool   *pgxpool.Pool
	Engine Syncer
	Cipher secrets.Cipher
	// BootstrapToken guards every route; when empty the API answers 404.
	BootstrapToken config.Secret
	Log            *slog.Logger
	// OnConnectionCreated, when set, is called with the (lower case) owner
	// after a connection is stored so caches for that owner can be dropped.
	OnConnectionCreated func(owner string)
}

type server struct{ Deps }

// New returns the handler for the whole /api/v1 tree (full paths are
// registered, so mount it at "/api/v1/").
func New(deps Deps) http.Handler {
	if deps.Log == nil {
		deps.Log = slog.New(slog.DiscardHandler)
	}
	if deps.BootstrapToken.Reveal() == "" {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusNotFound, codeNotFound, "not found")
		})
	}
	s := &server{deps}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/inventory", s.getInventory)
	mux.HandleFunc("GET /api/v1/inventory/services/{name}", s.getService)
	mux.HandleFunc("POST /api/v1/inventory/sync", s.syncInventory)
	mux.HandleFunc("PUT /api/v1/admin/inventory-source", s.putInventorySource)
	mux.HandleFunc("GET /api/v1/admin/github-connections", s.listConnections)
	mux.HandleFunc("POST /api/v1/admin/github-connections", s.createConnection)
	mux.HandleFunc("GET /api/v1/folders", s.listFolders)
	mux.HandleFunc("POST /api/v1/folders", s.createFolder)
	mux.HandleFunc("GET /api/v1/jobs", s.listJobs)
	mux.HandleFunc("POST /api/v1/jobs", s.registerJob)
	mux.HandleFunc("GET /api/v1/jobs/{id}", s.getJob)
	mux.HandleFunc("PATCH /api/v1/jobs/{id}", s.moveJob)
	mux.HandleFunc("POST /api/v1/jobs/{id}/sync", s.syncJob)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
	})
	return guard(deps.BootstrapToken.Reveal(), mux)
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
