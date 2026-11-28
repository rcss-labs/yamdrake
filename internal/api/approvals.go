package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/rcss-labs/yamdrake/internal/auth"
	"github.com/rcss-labs/yamdrake/internal/engine"
	"github.com/rcss-labs/yamdrake/internal/spec"
	"github.com/rcss-labs/yamdrake/internal/store"
)

// personName is a person's display name: their name, else their email. A user
// who no longer exists has "". names caches lookups for one response.
func (s *server) personName(ctx context.Context, names map[int64]string, id int64) (string, error) {
	if name, known := names[id]; known {
		return name, nil
	}
	user, err := store.UserByID(ctx, s.Pool, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	name := user.Name
	if name == "" {
		name = user.Email
	}
	names[id] = name
	return name, nil
}

// approvalView is the run's open approval, or its latest one.
type approvalView struct {
	RequestedBy     int64      `json:"requested_by"`
	RequestedByName string     `json:"requested_by_name"`
	RequestedAt     time.Time  `json:"requested_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
	ManifestID      string     `json:"manifest_id"`
	Decision        string     `json:"decision"` // "" while open, then approved | rejected | expired | withdrawn
	DecidedBy       *int64     `json:"decided_by"`
	DecidedByName   *string    `json:"decided_by_name"`
	DecidedAt       *time.Time `json:"decided_at"`
	Comment         string     `json:"comment"`
}

func (s *server) shapeApproval(ctx context.Context, names map[int64]string, a store.Approval) (*approvalView, error) {
	requester, err := s.personName(ctx, names, a.RequestedBy)
	if err != nil {
		return nil, err
	}
	view := &approvalView{
		RequestedBy: a.RequestedBy, RequestedByName: requester, RequestedAt: a.RequestedAt, ExpiresAt: a.ExpiresAt,
		ManifestID: a.ManifestID, Decision: a.Decision, DecidedBy: a.DecidedBy, DecidedAt: a.DecidedAt, Comment: a.Comment,
	}
	if a.DecidedBy != nil {
		decider, err := s.personName(ctx, names, *a.DecidedBy)
		if err != nil {
			return nil, err
		}
		view.DecidedByName = &decider
	}
	return view, nil
}

// approvalItemView is one run waiting for approval, as the approvals page shows it.
type approvalItemView struct {
	RunID       int64                 `json:"run_id"`
	JobID       string                `json:"job_id"`
	JobName     string                `json:"job_name"`
	JobSourceID int64                 `json:"job_source_id"`
	Environment string                `json:"environment"`
	Label       string                `json:"label"`
	Versions    []engine.VersionEntry `json:"versions"`
	ManifestID  string                `json:"manifest_id"`
	RequestedBy int64                 `json:"requested_by"`
	// RequestedByName is the requester's name, or their email when they have none.
	RequestedByName string    `json:"requested_by_name"`
	RequestedAt     time.Time `json:"requested_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	CanApprove      bool      `json:"can_approve"`
	Reason          string    `json:"reason"`
}

// approvalListResponse wraps the list: the command-line tool reads "approvals".
type approvalListResponse struct {
	Approvals []approvalItemView `json:"approvals"`
}

// approvalApproverReason says why the principal may not decide a run's approval
// ("" when they may). Having no permission is the more basic reason, so it
// comes first.
func approvalApproverReason(p auth.Principal, run store.Run) string {
	target := targetOfRun(run)
	switch {
	case p.MayApproveRunStartedBy(run.RequestedBy, target):
		return ""
	case !p.Can(auth.Approve, target):
		return "You do not have approve permission in " + run.Label + "."
	default:
		return "You started this run."
	}
}

// listApprovals lists the runs waiting for approval, oldest request first. An
// approval left open by a run that stopped waiting (cancelled, say) is not one.
func (s *server) listApprovals(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.principal(w, r)
	if !ok {
		return
	}
	open, err := store.OpenApprovals(r.Context(), s.Pool)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]approvalItemView, 0, len(open))
	requesterNames := map[int64]string{} // one lookup per requester
	for _, a := range open {
		run, err := store.GetRun(r.Context(), s.Pool, a.RunID)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if run.State != "waiting_approval" {
			continue
		}
		var job spec.Job
		_ = json.Unmarshal(run.JobSnapshot, &job) // a snapshot that cannot be read just has no name
		name, err := s.personName(r.Context(), requesterNames, a.RequestedBy)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		reason := approvalApproverReason(principal, run)
		items = append(items, approvalItemView{
			RunID: run.ID, JobID: run.JobID, JobName: job.Title, JobSourceID: run.JobSourceID,
			Environment: run.Environment, Label: run.Label,
			Versions: engine.VersionEntries(run), ManifestID: a.ManifestID, RequestedBy: a.RequestedBy, RequestedByName: name,
			RequestedAt: a.RequestedAt, ExpiresAt: a.ExpiresAt, CanApprove: reason == "", Reason: reason,
		})
	}
	writeJSON(w, http.StatusOK, approvalListResponse{Approvals: items})
}

type approveRequest struct {
	ManifestID string `json:"manifest_id"`
	Comment    string `json:"comment"`
}

type rejectRequest struct {
	Comment string `json:"comment"`
}

func (s *server) approveRun(w http.ResponseWriter, r *http.Request) {
	var req approveRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.ManifestID == "" {
		invalid(w, "manifest_id is required: it names the plan the approver was shown")
		return
	}
	s.doRunAction(w, r, func(ctx context.Context, p auth.Principal, runID int64) error {
		return s.Runs.Approve(ctx, p, runID, req.ManifestID, req.Comment)
	})
}

func (s *server) rejectRun(w http.ResponseWriter, r *http.Request) {
	var req rejectRequest
	if !decodeBody(w, r, &req) {
		return
	}
	s.doRunAction(w, r, func(ctx context.Context, p auth.Principal, runID int64) error {
		return s.Runs.Reject(ctx, p, runID, req.Comment)
	})
}
