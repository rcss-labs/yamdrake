package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/rcss-labs/yamdrake/internal/spec"
	"github.com/rcss-labs/yamdrake/internal/store"
	"github.com/rcss-labs/yamdrake/internal/versions"
)

// VersionLister is implemented by *versions.Service.
type VersionLister interface {
	ForJob(ctx context.Context, job spec.Job, inv spec.Inventory, envName string) (versions.JobVersions, error)
}

// getJobVersions answers GET /api/v1/jobs/{id}/versions?environment=<name>:
// the image versions each service of the job's groups can be deployed at.
func (s *server) getJobVersions(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(w, "job")
		return
	}
	envName := r.URL.Query().Get("environment")
	if envName == "" {
		invalid(w, "environment is required")
		return
	}
	source, err := store.GetJobSource(r.Context(), s.Pool, id)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w, "job")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if s.Versions == nil {
		writeError(w, http.StatusServiceUnavailable, codeUnavailable, "version lists are not available")
		return
	}
	_, inventory, _, active, err := s.inventoryView(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !active || len(source.LastGood) == 0 {
		writeError(w, http.StatusConflict, codeConflict, "the job or the inventory has no valid definition yet")
		return
	}
	if _, known := inventory.Environment(envName); !known {
		invalid(w, "unknown environment")
		return
	}
	var job spec.Job
	if err := json.Unmarshal(source.LastGood, &job); err != nil {
		s.internalError(w, r, err)
		return
	}
	result, err := s.Versions.ForJob(r.Context(), job, inventory, envName)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
