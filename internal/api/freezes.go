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
	"github.com/rcss-labs/yamdrake/internal/store"
)

const (
	// maxFreezeLength is the longest a freeze may last from the moment it is made.
	maxFreezeLength = 30 * 24 * time.Hour
	// maxFreezeReasonRunes bounds the reason, which is shown in run wait reasons and messages.
	maxFreezeReasonRunes = 300
	// recentFreezeWindow and recentFreezeLimit bound the ended freezes the list shows.
	recentFreezeWindow = 30 * 24 * time.Hour
	recentFreezeLimit  = 50
)

func (s *server) freezeRoutes() []routeDef {
	return []routeDef{
		{"GET /api/v1/freezes", auth.View, s.listFreezes},
		{"POST /api/v1/admin/freezes", auth.ManageAccess, s.createFreeze},
		{"DELETE /api/v1/admin/freezes/{id}", auth.ManageAccess, s.liftFreeze},
	}
}

type freezeResponse struct {
	ID          int64      `json:"id"`
	Label       string     `json:"label"`
	Environment string     `json:"environment"`
	Reason      string     `json:"reason"`
	CreatedBy   int64      `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	LiftedBy    *int64     `json:"lifted_by"`
	LiftedAt    *time.Time `json:"lifted_at"`
	Active      bool       `json:"active"`
}

type freezeListResponse struct {
	Active []freezeResponse `json:"active"`
	Recent []freezeResponse `json:"recent"`
}

func freezeResponseOf(f store.Freeze, now time.Time) freezeResponse {
	r := freezeResponse{
		ID: f.ID, Label: f.Label, Environment: f.Environment, Reason: f.Reason, CreatedBy: f.CreatedBy,
		CreatedAt: f.CreatedAt.UTC(), ExpiresAt: f.ExpiresAt.UTC(), LiftedBy: f.LiftedBy,
		Active: f.LiftedAt == nil && f.ExpiresAt.After(now),
	}
	if f.LiftedAt != nil {
		at := f.LiftedAt.UTC()
		r.LiftedAt = &at
	}
	return r
}

// endedAt is when the freeze stopped applying: lifted, or expired, whichever came first.
func endedAt(f store.Freeze) time.Time {
	if f.LiftedAt != nil && f.LiftedAt.Before(f.ExpiresAt) {
		return *f.LiftedAt
	}
	return f.ExpiresAt
}

func (s *server) listFreezes(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	all, err := store.ListFreezes(r.Context(), s.Pool, 500)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := freezeListResponse{Active: []freezeResponse{}, Recent: []freezeResponse{}}
	// ListFreezes is newest first; active ones are shown oldest first.
	for i := len(all) - 1; i >= 0; i-- {
		if f := all[i]; f.LiftedAt == nil && f.ExpiresAt.After(now) {
			out.Active = append(out.Active, freezeResponseOf(f, now))
		}
	}
	for _, f := range all {
		active := f.LiftedAt == nil && f.ExpiresAt.After(now)
		if !active && endedAt(f).After(now.Add(-recentFreezeWindow)) && len(out.Recent) < recentFreezeLimit {
			out.Recent = append(out.Recent, freezeResponseOf(f, now))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type createFreezeRequest struct {
	Label       string     `json:"label"`
	Environment string     `json:"environment"`
	Reason      string     `json:"reason"`
	ExpiresAt   *time.Time `json:"expires_at"`
}

func (s *server) createFreeze(w http.ResponseWriter, r *http.Request) {
	var req createFreezeRequest
	if !decodeBody(w, r, &req) {
		return
	}
	now := s.Now()
	if problem := validateFreeze(req, now); problem != "" {
		invalid(w, problem)
		return
	}
	principal, _ := PrincipalFrom(r.Context())
	var created store.Freeze
	err := s.auditedWrite(r, func(ctx context.Context, tx pgx.Tx) (audit.Event, error) {
		var err error
		created, err = store.CreateFreeze(ctx, tx, store.Freeze{
			Label: req.Label, Environment: req.Environment, Reason: strings.TrimSpace(req.Reason),
			CreatedBy: principal.UserID, CreatedAt: now, ExpiresAt: *req.ExpiresAt,
		})
		return audit.Event{Action: "freeze.create", TargetType: "freeze", TargetID: idString(created.ID), Detail: map[string]any{
			"label": created.Label, "environment": created.Environment, "reason": created.Reason, "expires_at": created.ExpiresAt.UTC(),
		}}, err
	})
	if err != nil {
		s.writeAccessError(w, r, err, "freeze")
		return
	}
	writeJSON(w, http.StatusCreated, freezeResponseOf(created, now))
}

// validateFreeze returns what is wrong with a create request, or "".
func validateFreeze(req createFreezeRequest, now time.Time) string {
	reason := strings.TrimSpace(req.Reason)
	switch {
	case reason == "":
		return "reason is required"
	case utf8.RuneCountInString(reason) > maxFreezeReasonRunes:
		return "reason is too long"
	case (req.Label == "") == (req.Environment == ""):
		return "give exactly one of label and environment"
	case req.Environment != "" && !environmentPattern.MatchString(req.Environment):
		return "environment must be an environment name: lower case letters, digits and hyphens, at most 63 characters"
	case req.Label != "" && (req.Label != strings.TrimSpace(req.Label) || utf8.RuneCountInString(req.Label) > 63):
		return "label must have no surrounding spaces and be at most 63 characters"
	case req.ExpiresAt == nil:
		return "expires_at is required"
	case !req.ExpiresAt.After(now):
		return "expires_at must be in the future"
	case req.ExpiresAt.After(now.Add(maxFreezeLength)):
		return "expires_at must be at most 30 days from now"
	}
	return ""
}

func (s *server) liftFreeze(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(w, "freeze")
		return
	}
	principal, _ := PrincipalFrom(r.Context())
	err := s.auditedWrite(r, func(ctx context.Context, tx pgx.Tx) (audit.Event, error) {
		if err := store.LiftFreeze(ctx, tx, id, principal.UserID, s.Now()); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: "freeze.lift", TargetType: "freeze", TargetID: idString(id)}, nil
	})
	if err != nil {
		s.writeAccessError(w, r, err, "freeze")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
