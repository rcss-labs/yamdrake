package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/yumdrake/internal/audit"
	"github.com/rcss-labs/yumdrake/internal/auth"
	"github.com/rcss-labs/yumdrake/internal/store"
)

const (
	reasonRequesterLostPermission = "the person who started the run no longer has permission to run it"
	reasonRequesterDisabled       = "the person who started the run is disabled"

	// freezeTimeLayout is how a freeze's end is worded in a wait reason.
	freezeTimeLayout = "2006-01-02 15:04 UTC"
)

// frozenReason words the freeze that blocks a run, or "" when none does. When
// several apply it names the one that ends last (the lowest id on a tie), as
// that is the one the run has to wait for.
func frozenReason(run store.Run, active []store.Freeze) string {
	var blocking *store.Freeze
	for i := range active {
		f := active[i]
		applies := (f.Label != "" && f.Label == run.Label) || (f.Environment != "" && f.Environment == run.Environment)
		if applies && (blocking == nil || f.ExpiresAt.After(blocking.ExpiresAt)) {
			blocking = &active[i]
		}
	}
	if blocking == nil {
		return ""
	}
	return fmt.Sprintf("%s, until %s", blocking.Reason, blocking.ExpiresAt.UTC().Format(freezeTimeLayout))
}

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
	view.Frozen = frozenReason(run, active)
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
