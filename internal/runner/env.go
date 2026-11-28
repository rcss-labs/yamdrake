package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

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
	//
	// What fn's error means. A plain error means "not applied": the system
	// refused or the call never went out; the operation is recorded as failed
	// and the error returned. If the call may have taken effect, wrap
	// ErrMaybeApplied. The same is assumed when the leg's context ended during
	// fn or fn returned context.Canceled or context.DeadlineExceeded. In those
	// cases nothing is recorded, the operation stays intended, and the next
	// attempt gets ErrAmbiguous. ErrPreviouslyFailed therefore only ever means
	// a definite refusal; it also appears when a leg that failed is re-queued
	// by a "check again" and calls Do with the same key, in which case choose a
	// new key (for example with a counter) for a deliberate second try.
	//
	// If fn succeeded but recording its result fails, Do retries the record a
	// few times while the lease is held; when it still cannot record, it
	// returns an error wrapping ErrMaybeApplied (the change is in place, the
	// operation stays intended).
	//
	// Executors must map ErrMaybeApplied, and an ErrAmbiguous they cannot
	// resolve by looking at the real system, to an "unknown" outcome, never
	// to "failed": a failed leg releases the run's locks, an unknown one
	// keeps them until a person reconciles.
	//
	// Do must not be called concurrently with the same key. The recorded intent
	// must be enough for a later attempt to reconcile.
	Do(ctx context.Context, name, key string, intent any, fn func() (result any, err error)) (store.Op, error)

	// CancelRequested reports whether a person asked for this leg to stop.
	// Executors check it between stages and return a cancelled outcome. It
	// never cancels ctx, and it returns false when the flag cannot be read
	// (database error, logged by the runner).
	CancelRequested(ctx context.Context) bool
}

// fnMayHaveApplied reports whether an error from Do's fn leaves the change in
// doubt: fn said so (ErrMaybeApplied), or the leg's context ended (lease lost
// or shutdown), or fn itself was cut by a cancellation or deadline.
func fnMayHaveApplied(ctx context.Context, err error) bool {
	return errors.Is(err, ErrMaybeApplied) || ctx.Err() != nil ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// legEnv implements Env for one claimed leg.
type legEnv struct {
	q     *queue.Queue
	sink  LineSink
	leg   store.Leg // as claimed
	log   *slog.Logger
	mu    sync.Mutex
	resum []byte // last checkpoint, kept current by Checkpoint

	// onLogFailure is called once, with the sink's error, the first time a log
	// line is refused for good. The runner uses it to stop the leg.
	logFailure   sync.Once
	onLogFailure func(err error)
}

func newLegEnv(q *queue.Queue, sink LineSink, leg store.Leg, log *slog.Logger) *legEnv {
	return &legEnv{q: q, sink: sink, leg: leg, log: log, resum: leg.Resume}
}

func (e *legEnv) Log(level, text string) {
	if err := e.sink.Write(e.leg.RunID, e.leg.ID, e.leg.Generation, level, text); err != nil {
		// The line is lost and more will be: stop the leg (once) rather than let
		// memory grow while PostgreSQL is unreachable.
		e.logFailure.Do(func() {
			e.log.Error("log lines are being refused; stopping the leg", "leg_id", e.leg.ID, "error", err.Error())
			if e.onLogFailure != nil {
				e.onLogFailure(err)
			}
		})
	}
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
	if fnErr != nil && fnMayHaveApplied(ctx, fnErr) {
		// Cut short, not refused: the change may have gone through. Leave the
		// operation intended so the next attempt gets ErrAmbiguous and checks.
		return op, fmt.Errorf("do %q: %w", key, fnErr)
	}
	if fnErr != nil {
		failure, _ := json.Marshal(map[string]string{"error": fnErr.Error()})
		if err := e.finishWithRetry(recordCtx, op.ID, false, failure); err != nil {
			return op, fmt.Errorf("do %q: %w (change failed with: %s)", key, err, fnErr.Error())
		}
		op.State = "failed"
		return op, fnErr
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		// fn succeeded: the change is in place and its result cannot be recorded.
		return op, fmt.Errorf("do %q: encode result: %w: %w", key, ErrMaybeApplied, err)
	}
	if err := e.finishWithRetry(recordCtx, op.ID, true, resultJSON); err != nil {
		// The change was made but nothing says so. Reporting a plain error would
		// let the executor call the leg failed and release the run's locks over
		// an applied change; ErrMaybeApplied makes it an unknown outcome, and the
		// operation stays intended so the next attempt reconciles it.
		return op, fmt.Errorf("do %q: the change was made but its result could not be recorded: %w: %w", key, ErrMaybeApplied, err)
	}
	op.State, op.Result = "done", resultJSON
	return op, nil
}

const finishTries = 4

// finishRetryWait is the pause before the first retry of a failed record; it
// doubles each time. A variable so tests need not wait.
var finishRetryWait = 100 * time.Millisecond

// finishWithRetry records an operation's outcome, retrying a failure a few
// times with a short backoff while the lease is still held. A lost lease is
// final: nothing can be recorded any more.
func (e *legEnv) finishWithRetry(ctx context.Context, opID int64, ok bool, result []byte) error {
	var err error
	wait := finishRetryWait
	for try := 0; try < finishTries; try++ {
		if try > 0 {
			time.Sleep(wait)
			wait *= 2
		}
		if err = e.q.Finish(ctx, e.leg.ID, e.leg.Generation, opID, ok, result); err == nil || errors.Is(err, queue.ErrLeaseLost) {
			return err
		}
		e.log.Warn("could not record an operation's outcome", "leg_id", e.leg.ID, "operation_id", opID, "try", try+1, "error", err.Error())
	}
	return err
}

// ErrMaybeApplied is for Do's fn to wrap when its call may have taken effect
// although it returned an error (a timeout or a dropped connection after the
// request was sent).
var ErrMaybeApplied = errors.New("change may have been applied")
