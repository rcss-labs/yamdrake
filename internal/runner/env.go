package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/rcss-labs/yamdrake/internal/queue"
	"github.com/rcss-labs/yamdrake/internal/store"
)

// ErrAmbiguous is returned by Env.Do, with the stored operation, when an
// earlier attempt recorded the intent to make a change but never recorded its
// result. The change may or may not have happened. The executor must look at
// the real system (AWS, for instance), decide whether the change is in place,
// and finish the leg accordingly. It must not call Do's fn again blindly.
var ErrAmbiguous = errors.New("operation intended but not recorded as done: reconcile against the real system")

// ErrPreviouslyFailed is returned by Env.Do, with the stored operation, when
// an operation with this key already ran and failed. Do never repeats it; the
// executor decides what a failed change means for the leg.
var ErrPreviouslyFailed = errors.New("operation already failed")

// Env is what the runner gives an executor for one attempt at one leg.
//
// Every write behind it is conditional on the leg's generation: once the leg
// has been taken from this runner, the calls return queue.ErrLeaseLost (use
// errors.Is) and the context passed to Execute is cancelled. Methods taking a
// context should be given the one Execute received.
type Env interface {
	// Log adds a line to the leg's log. level is info, ok, warn or error.
	// Lines are batched and flushed before the leg is completed.
	Log(level, text string)

	// Resume decodes the last checkpoint, from this attempt or an earlier
	// one, into v. found is false when there is none.
	Resume(v any) (found bool, err error)

	// Checkpoint stores v (JSON) for a later attempt to resume from. Call it
	// after each stage that need not be repeated.
	Checkpoint(ctx context.Context, v any) error

	// Ops lists the leg's operations from every attempt, in the order they
	// were intended.
	Ops(ctx context.Context) ([]store.Op, error)

	// Do is the only way an executor makes an external change. (name, key)
	// names the change; key must be the same on every attempt for the same
	// change, for example derived from the service and the version.
	//
	//   - An operation with this key already done: returned as it is, fn is
	//     not called, error nil. op.Result holds what fn returned back then.
	//   - Intended but never finished: returned with ErrAmbiguous, fn not
	//     called. Reconcile against the real system.
	//   - Failed before: returned with ErrPreviouslyFailed, fn not called.
	//   - Otherwise: the intent (JSON of intent) is recorded, the lease is
	//     checked, fn is called, and its result (JSON) is recorded as done.
	//     If fn returns an error the operation is recorded as failed and the
	//     error is returned.
	//
	// The recorded intent must be enough for a later attempt to reconcile.
	Do(ctx context.Context, name, key string, intent any, fn func() (result any, err error)) (store.Op, error)

	// CancelRequested reports whether a person asked for this leg to stop.
	// Executors check it between stages and return a cancelled outcome.
	CancelRequested(ctx context.Context) bool
}

// legEnv implements Env for one claimed leg.
type legEnv struct {
	q     *queue.Queue
	sink  LineSink
	leg   store.Leg // as claimed
	log   *slog.Logger
	mu    sync.Mutex
	resum []byte // last checkpoint, kept current by Checkpoint
}

func newLegEnv(q *queue.Queue, sink LineSink, leg store.Leg, log *slog.Logger) *legEnv {
	return &legEnv{q: q, sink: sink, leg: leg, log: log, resum: leg.Resume}
}

func (e *legEnv) Log(level, text string) {
	e.sink.Write(e.leg.RunID, e.leg.ID, e.leg.Generation, level, text)
}

func (e *legEnv) Resume(v any) (bool, error) {
	e.mu.Lock()
	data := e.resum
	e.mu.Unlock()
	if len(data) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(data, v); err != nil {
		return true, fmt.Errorf("decode checkpoint: %w", err)
	}
	return true, nil
}

func (e *legEnv) Checkpoint(ctx context.Context, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode checkpoint: %w", err)
	}
	if err := e.q.Checkpoint(ctx, e.leg.ID, e.leg.Generation, data); err != nil {
		return err
	}
	e.mu.Lock()
	e.resum = data
	e.mu.Unlock()
	return nil
}

func (e *legEnv) Ops(ctx context.Context) ([]store.Op, error) {
	return e.q.Ops(ctx, e.leg.ID)
}

func (e *legEnv) CancelRequested(ctx context.Context) bool {
	requested, err := e.q.CancelRequested(ctx, e.leg.ID)
	if err != nil {
		e.log.Warn("could not read the cancel flag", "leg_id", e.leg.ID, "error", err.Error())
		return false
	}
	return requested
}

func (e *legEnv) Do(ctx context.Context, name, key string, intent any, fn func() (any, error)) (store.Op, error) {
	// Look before recording: Intend returns an existing operation and a new one
	// alike, and only this lookup tells an earlier attempt's intent from ours.
	// Nobody else writes this leg's operations while we hold its generation.
	ops, err := e.q.Ops(ctx, e.leg.ID)
	if err != nil {
		return store.Op{}, fmt.Errorf("do %q: %w", key, err)
	}
	for _, op := range ops {
		if op.Key != key {
			continue
		}
		switch op.State {
		case "done":
			return op, nil
		case "failed":
			return op, fmt.Errorf("do %q: %w", key, ErrPreviouslyFailed)
		default:
			return op, fmt.Errorf("do %q: %w", key, ErrAmbiguous)
		}
	}

	intentJSON, err := json.Marshal(intent)
	if err != nil {
		return store.Op{}, fmt.Errorf("do %q: encode intent: %w", key, err)
	}
	op, err := e.q.Intend(ctx, e.leg.ID, e.leg.Generation, name, key, intentJSON)
	if err != nil {
		return store.Op{}, fmt.Errorf("do %q: %w", key, err)
	}
	// The last check before the change: the lease is still ours and the
	// context (cancelled when the lease is lost or the runner shuts down) is live.
	if err := ctx.Err(); err != nil {
		return op, fmt.Errorf("do %q: %w", key, err)
	}
	if err := e.q.Heartbeat(ctx, e.leg.ID, e.leg.Generation); err != nil {
		return op, fmt.Errorf("do %q: %w", key, err)
	}

	result, fnErr := fn()

	// Record the outcome even when the leg's context ended during fn: the
	// change happened, and the record is what stops the next attempt guessing.
	recordCtx := context.WithoutCancel(ctx)
	if fnErr != nil {
		failure, _ := json.Marshal(map[string]string{"error": fnErr.Error()})
		if err := e.q.Finish(recordCtx, e.leg.ID, e.leg.Generation, op.ID, false, failure); err != nil {
			return op, fmt.Errorf("do %q: %w (change failed with: %s)", key, err, fnErr.Error())
		}
		op.State = "failed"
		return op, fnErr
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return op, fmt.Errorf("do %q: encode result: %w", key, err)
	}
	if err := e.q.Finish(recordCtx, e.leg.ID, e.leg.Generation, op.ID, true, resultJSON); err != nil {
		return op, fmt.Errorf("do %q: %w", key, err)
	}
	op.State, op.Result = "done", resultJSON
	return op, nil
}
