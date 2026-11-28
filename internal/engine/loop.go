package engine

import (
	"context"
	"time"

	"github.com/rcss-labs/yamdrake/internal/queue"
	"github.com/rcss-labs/yamdrake/internal/store"
)

const defaultSweepEvery = 2 * time.Second

// Loop keeps runs moving until ctx ends. A run id arriving on wakeRuns (may be
// nil) advances that run at once. Every sweepEvery it also sweeps expired
// leases (q may be nil), expires planned runs and approvals nobody acted on,
// and advances every active run, so progress never depends on a wake-up having
// arrived: the timer alone is enough when Valkey is down. expireApprovalsAfter
// says how long a run may wait for approval; nil means ApprovalExpiry. A failure
// for one run is logged and the others carry on.
func (e *Engine) Loop(ctx context.Context, wakeRuns <-chan int64, sweepEvery time.Duration, q *queue.Queue, expireApprovalsAfter func(store.Run) time.Duration) {
	if sweepEvery <= 0 {
		sweepEvery = defaultSweepEvery
	}
	if expireApprovalsAfter == nil {
		expireApprovalsAfter = ApprovalExpiry
	}
	ticker := time.NewTicker(sweepEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case id, ok := <-wakeRuns:
			if !ok {
				wakeRuns = nil // closed: only the timer is left
				continue
			}
			e.advanceLogged(ctx, id)
		case <-ticker.C:
			e.sweepTick(ctx, q, expireApprovalsAfter)
		}
	}
}

func (e *Engine) advanceLogged(ctx context.Context, runID int64) {
	if err := e.Advance(ctx, runID); err != nil && ctx.Err() == nil {
		e.log.Error("could not advance run", "run_id", runID, "error", err.Error())
	}
}

// sweepTick is one pass of the fallback timer.
func (e *Engine) sweepTick(ctx context.Context, q *queue.Queue, approvalAfter func(store.Run) time.Duration) {
	if q != nil {
		// Sweep touches legs only; advancing every active run below is what
		// makes the run notice a requeued or unknown leg.
		requeued, unknown, err := q.Sweep(ctx)
		switch {
		case err != nil:
			if ctx.Err() == nil {
				e.log.Error("lease sweep failed", "error", err.Error())
			}
		case requeued > 0 || unknown > 0:
			e.log.Warn("swept expired leases", "requeued", requeued, "unknown", unknown)
		}
	}
	ids, err := store.ActiveRunIDs(ctx, e.db)
	if err != nil {
		if ctx.Err() == nil {
			e.log.Error("could not list active runs", "error", err.Error())
		}
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		e.expireIfDue(ctx, id, approvalAfter)
		e.advanceLogged(ctx, id)
	}
}

// expireIfDue expires a planned or approval-waiting run that has waited too long.
func (e *Engine) expireIfDue(ctx context.Context, runID int64, approvalAfter func(store.Run) time.Duration) {
	run, err := store.GetRun(ctx, e.db, runID)
	if err != nil {
		if ctx.Err() == nil {
			e.log.Error("could not read run", "run_id", runID, "error", err.Error())
		}
		return
	}
	if run.State != "planned" && run.State != "waiting_approval" {
		return
	}
	if err := e.expire(ctx, runID, approvalAfter); err != nil && ctx.Err() == nil {
		e.log.Error("could not expire run", "run_id", runID, "error", err.Error())
	}
}
