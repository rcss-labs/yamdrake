package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/yamdrake/internal/audit"
	"github.com/rcss-labs/yamdrake/internal/auth"
	"github.com/rcss-labs/yamdrake/internal/store"
)

const (
	reasonRequesterLostPermission = "the person who started the run no longer has permission to run it"
	reasonRequesterDisabled       = "the person who started the run is disabled"
)

// checkQueuedRun fills the View fields Decide needs before a queued run may
// take locks: whether its requester may still run it, and whether a freeze
// applies. It runs inside the advance transaction.
func (e *Engine) checkQueuedRun(ctx context.Context, tx pgx.Tx, view *View) error {
	run := view.Run
	requester, err := auth.Resolve(ctx, tx, run.RequestedBy, "")
	switch {
	case errors.Is(err, auth.ErrDisabled):
		view.RequesterProblem = reasonRequesterDisabled
		return nil
	case errors.Is(err, store.ErrNotFound):
		view.RequesterProblem = reasonRequesterLostPermission
		return nil
	case err != nil:
		return fmt.Errorf("re-check the requester of run %d: %w", run.ID, err)
	}
	action := auth.Run
	if run.Kind == "restore" {
		action = auth.Restore
	}
	if !requester.Can(action, targetOf(run)) {
		view.RequesterProblem = reasonRequesterLostPermission
		return nil
	}
	active, err := store.ActiveFreezes(ctx, tx, e.now())
	if err != nil {
		return err
	}
	view.Frozen = store.FrozenReason(run, active)
	return nil
}

// recordUnauthorizedEnd audits a run ended because its requester may no longer
// run it. No principal: the system ended it.
func (e *Engine) recordUnauthorizedEnd(ctx context.Context, tx pgx.Tx, run store.Run, reason string) error {
	return audit.Record(ctx, tx, nil, e.now(), audit.Event{
		Action: "run.fail_unauthorized", TargetType: "run", TargetID: fmt.Sprint(run.ID),
		Detail: map[string]any{"requested_by": run.RequestedBy, "reason": reason},
	})
}
