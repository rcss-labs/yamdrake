package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/rcss-labs/yamdrake/internal/auth"
	"github.com/rcss-labs/yamdrake/internal/engine"
	"github.com/rcss-labs/yamdrake/internal/spec"
	"github.com/rcss-labs/yamdrake/internal/store"
)

// approvalView is the run's open approval, or its latest one.
type approvalView struct {
	RequestedBy int64      `json:"requested_by"`
	RequestedAt time.Time  `json:"requested_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	ManifestID  string     `json:"manifest_id"`
	Decision    string     `json:"decision"` // "" while open, then approved | rejected | expired
	DecidedBy   *int64     `json:"decided_by"`
	DecidedAt   *time.Time `json:"decided_at"`
	Comment     string     `json:"comment"`
}

func shapeApproval(a store.Approval) *approvalView {
	return &approvalView{
		RequestedBy: a.RequestedBy, RequestedAt: a.RequestedAt, ExpiresAt: a.ExpiresAt, ManifestID: a.ManifestID,
		Decision: a.Decision, DecidedBy: a.DecidedBy, DecidedAt: a.DecidedAt, Comment: a.Comment,
	}
}

// approvalItemView is one run waiting for approval, as the approvals page shows it.
type approvalItemView struct {
	RunID       int64     `json:"run_id"`
	JobID       string    `json:"job_id"`
	JobTitle    string    `json:"job_title"`
	Environment string    `json:"environment"`
	Label       string    `json:"label"`
	Versions    string    `json:"versions"`
	ManifestID  string    `json:"manifest_id"`
	RequestedBy int64     `json:"requested_by"`
	RequestedAt time.Time `json:"requested_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	CanApprove  bool      `json:"can_approve"`
	Reason      string    `json:"reason"`
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
		_ = json.Unmarshal(run.JobSnapshot, &job) // a snapshot that cannot be read just has no title
		reason := approvalApproverReason(principal, run)
		items = append(items, approvalItemView{
			RunID: run.ID, JobID: run.JobID, JobTitle: job.Title, Environment: run.Environment, Label: run.Label,
			Versions: engine.VersionsSummary(run), ManifestID: a.ManifestID, RequestedBy: a.RequestedBy,
			RequestedAt: a.RequestedAt, ExpiresAt: a.ExpiresAt, CanApprove: reason == "", Reason: reason,
		})
	}
	writeJSON(w, http.StatusOK, items)
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
