package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/yamdrake/internal/auth"
	"github.com/rcss-labs/yamdrake/internal/notify"
	"github.com/rcss-labs/yamdrake/internal/spec"
	"github.com/rcss-labs/yamdrake/internal/store"
)

// ErrManifestChanged means the approver saw a plan that is no longer the
// run's: nothing was recorded.
var ErrManifestChanged = errors.New("the plan changed")

// onEnterState carries out what entering a state means beyond the state
// itself. Every path that changes a run's state calls it, in the same
// transaction, once the new state is stored and only when the state really
// changed: people's actions (moveTo) and the engine's own decisions (apply).
// The caller holds the run row lock.
func (e *Engine) onEnterState(ctx context.Context, tx pgx.Tx, run store.Run, newState string, now time.Time) error {
	switch {
	case newState == "waiting_approval":
		if err := e.requestApproval(ctx, tx, run.ID, now); err != nil {
			return err
		}
		return e.enqueueNotification(ctx, tx, run.ID, notify.KindApprovalRequested, "", now)
	case newState == "running":
		return e.enqueueNotification(ctx, tx, run.ID, notify.KindStarted, "", now)
	case isFinalRunState(newState):
		// Approve, reject and expiry close their own approval first; this is
		// for a run that ends otherwise (cancelled, invalidated).
		open, found, err := store.OpenApproval(ctx, tx, run.ID)
		if err != nil {
			return err
		}
		if found {
			if err := store.DecideApproval(ctx, tx, open.ID, "withdrawn", nil, "", now); err != nil {
				return err
			}
		}
		if announcesFinish(newState) {
			return e.enqueueNotification(ctx, tx, run.ID, notify.KindFinished, "", now)
		}
	case newState == "needs_reconciliation":
		return e.enqueueNotification(ctx, tx, run.ID, notify.KindFinished, "", now)
	}
	return nil
}

// announcesFinish reports whether a run ending in state is announced as
// finished. A run that was never started (rejected, expired, invalidated)
// has its own message or none.
func announcesFinish(state string) bool {
	switch state {
	case "succeeded", "partially_succeeded", "failed", "cancelled":
		return true
	}
	return false
}

// requestApproval opens the approval for the plan the run holds now. The run is
// re-read because the manifest may have been stored in this very transaction.
func (e *Engine) requestApproval(ctx context.Context, tx pgx.Tx, runID int64, now time.Time) error {
	run, err := store.GetRun(ctx, tx, runID)
	if err != nil {
		return err
	}
	_, err = store.RequestApproval(ctx, tx, store.Approval{
		RunID: run.ID, ManifestID: run.ManifestID, RequestedBy: run.RequestedBy,
		RequestedAt: now, ExpiresAt: now.Add(ApprovalExpiry(run)),
	})
	return err
}

// Approve lets an approver (not the person who started the run, unless an
// admin) approve exactly the plan they were shown: manifestID must be the
// run's. The run is then queued.
func (e *Engine) Approve(ctx context.Context, p auth.Principal, runID int64, manifestID, comment string) error {
	return e.decide(ctx, p, runID, "run.approve", "approved", "queued", &manifestID, comment)
}

// Reject ends a run waiting for approval as rejected.
func (e *Engine) Reject(ctx context.Context, p auth.Principal, runID int64, comment string) error {
	return e.decide(ctx, p, runID, "run.reject", "rejected", "rejected", nil, comment)
}

// decide records an approver's decision and moves the run, in this order:
// permission, then "already decided" (how the loser of a simultaneous Approve
// and Reject learns it lost), then wrong state, then the manifest check. The
// run row lock makes two deciders take turns; store.DecideApproval's own
// conditional update is the second line of defence.
func (e *Engine) decide(ctx context.Context, p auth.Principal, runID int64, action, decision, next string, manifestID *string, comment string) error {
	return e.inTx(ctx, runID, func(tx pgx.Tx, o *outcome) error {
		run, err := store.LockRun(ctx, tx, runID)
		if err != nil {
			return fmt.Errorf("%s %d: %w", action, runID, err)
		}
		if !p.MayApproveRunStartedBy(run.RequestedBy, targetOf(run)) {
			return fmt.Errorf("%s %d: %w", action, runID, ErrForbidden)
		}
		if run.State != "waiting_approval" {
			latest, found, err := store.LatestApproval(ctx, tx, runID)
			if err != nil {
				return err
			}
			if found && latest.Decision != "" {
				return fmt.Errorf("%s %d: %w", action, runID, store.ErrAlreadyDecided)
			}
			return fmt.Errorf("%s %d: %w: it is %s", action, runID, ErrWrongState, run.State)
		}
		if manifestID != nil && *manifestID != run.ManifestID {
			return fmt.Errorf("%s %d: %w", action, runID, ErrManifestChanged)
		}
		open, found, err := store.OpenApproval(ctx, tx, runID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%s %d: %w: it has no open approval", action, runID, ErrWrongState)
		}
		now := e.now()
		if err := store.DecideApproval(ctx, tx, open.ID, decision, &p.UserID, comment, now); err != nil {
			return fmt.Errorf("%s %d: %w", action, runID, err)
		}
		kind := notify.KindApproved
		reason := ""
		if next == "rejected" {
			kind = notify.KindRejected
			reason = "rejected by " + displayName(p)
		}
		if err := e.enqueueNotification(ctx, tx, runID, kind, displayName(p), now); err != nil {
			return err
		}
		if err := e.moveTo(ctx, tx, run, next, reason, displayName(p), o); err != nil {
			return err
		}
		return e.record(ctx, tx, &p, action, runID, map[string]any{"manifest_id": run.ManifestID, "comment": comment})
	})
}

// expireApproval closes the run's open approval as expired, by nobody. It is
// part of the transaction that expires the run.
func (e *Engine) expireApproval(ctx context.Context, tx pgx.Tx, runID int64) error {
	open, found, err := store.OpenApproval(ctx, tx, runID)
	if err != nil || !found {
		return err
	}
	now := e.now()
	if err := store.DecideApproval(ctx, tx, open.ID, "expired", nil, "", now); err != nil {
		return err
	}
	return e.enqueueNotification(ctx, tx, runID, notify.KindApprovalExpired, "", now)
}

// approvalDue reports whether a run waiting for approval has waited too long.
// The open approval's expiry is the authority; a run that waits without an
// approval row (made before approvals existed) falls back to the job's expiry
// measured from the state change.
// window is how long the request allowed, for the end reason.
func (e *Engine) approvalDue(ctx context.Context, tx pgx.Tx, run store.Run) (due bool, window time.Duration, err error) {
	open, found, err := store.OpenApproval(ctx, tx, run.ID)
	if err != nil {
		return false, 0, err
	}
	if found {
		return !e.now().Before(open.ExpiresAt), open.ExpiresAt.Sub(open.RequestedAt), nil
	}
	window = ApprovalExpiry(run)
	return e.now().Sub(run.StateChangedAt) > window, window, nil
}

// VersionsSummary words what a run deploys the way approvers and Slack read it:
// "<group label> <version> (<service count>)", one per selected group in the
// job's order, joined by ", " (a group without a label shows its id). A run
// whose snapshot or selection cannot be read gives "".
func VersionsSummary(run store.Run) string {
	var job spec.Job
	if err := json.Unmarshal(run.JobSnapshot, &job); err != nil {
		return ""
	}
	var selection Selection
	if err := json.Unmarshal(run.Selection, &selection); err != nil {
		return ""
	}
	var parts []string
	for _, group := range job.Groups {
		chosen, ok := selection[group.ID]
		if !ok || len(chosen.Services) == 0 {
			continue
		}
		label := group.Label
		if label == "" {
			label = group.ID
		}
		parts = append(parts, fmt.Sprintf("%s %s (%d)", label, chosen.Version, len(chosen.Services)))
	}
	return strings.Join(parts, ", ")
}

// VersionEntry is one service a run deploys and the version it gets.
type VersionEntry struct {
	Service string `json:"service"`
	Version string `json:"version"`
}

// VersionEntries lists what a run deploys, service by service: groups in the
// job's order, then each group's selected services in the job's order. A
// service's own override is its version; otherwise it gets its group's. A run
// whose snapshot or selection cannot be read, or that selects nothing, gives
// an empty list, never nil.
func VersionEntries(run store.Run) []VersionEntry {
	entries := []VersionEntry{}
	var job spec.Job
	if err := json.Unmarshal(run.JobSnapshot, &job); err != nil {
		return entries
	}
	var selection Selection
	if err := json.Unmarshal(run.Selection, &selection); err != nil {
		return entries
	}
	for _, group := range job.Groups {
		chosen, ok := selection[group.ID]
		if !ok {
			continue
		}
		for _, service := range group.Services {
			override, selected := chosen.Services[service]
			if !selected {
				continue
			}
			version := chosen.Version
			if override != "" {
				version = override
			}
			entries = append(entries, VersionEntry{Service: service, Version: version})
		}
	}
	return entries
}
