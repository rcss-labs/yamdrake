// Package runner is the worker pool that executes legs claimed from the queue.
//
// # Contract for executors (plans 5 and 6 write the real ones)
//
// An Executor is registered under a kind: "resolve" and "revalidate" for those
// legs, otherwise the step's `use` ("copy-config", "ecs-deploy", ...). Execute
// runs one attempt at one leg and returns how it ended.
//
//   - Make every external change through Env.Do with a stable key. Do records
//     the intent before and the result after the change, so a crash in between
//     is visible to the next attempt as ErrAmbiguous, which the executor must
//     settle by looking at the real system. See Env and ErrAmbiguous.
//   - Use Env.Checkpoint after stages that need not be repeated and Env.Resume
//     at the start to skip them.
//   - Honour ctx: it ends when the lease is lost (another runner now owns the
//     leg; stop at once, whatever you return is dropped) or when the runner is
//     shutting down (the leg is released to the queue for another attempt).
//   - Return queue.Outcome: succeeded, failed (with a short reason a person can
//     read and a detail), unknown (the change may or may not have happened) or
//     cancelled (after Env.CancelRequested). A panic counts as unknown.
//   - Env.Log lines are batched; the runner flushes them before completing. If
//     the flush fails it is logged and the leg is completed anyway: settling
//     the leg matters more than its log.
//   - A panic in the executor's own goroutine is recovered. A panic in a
//     goroutine the executor started is not: recover it there.
package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/rcss-labs/yamdrake/internal/queue"
)

// LineSink takes the log lines of legs. The generation stamps each line so that
// a runner that lost its leg cannot add lines to the new attempt's log.
type LineSink interface {
	Write(runID, legID, generation int64, level, text string)
	Flush(ctx context.Context, legID int64) error
}

// Executor runs one attempt at a leg. See the package documentation.
type Executor interface {
	Execute(ctx context.Context, env Env, task queue.Claimed) queue.Outcome
}

const (
	defaultShutdownTimeout = 20 * time.Second
	// settleTimeout bounds the writes that settle a leg once its context is gone.
	settleTimeout = 10 * time.Second
	// stopGrace is how long Run waits for executors to return after it has
	// told them to stop at the end of the shutdown timeout.
	stopGrace = 5 * time.Second
	// completeTries and completeRetryWait bound the retry of a failed Complete.
	completeTries     = 3
	completeRetryWait = 200 * time.Millisecond
)

var (
	errLeaseLost = errors.New("lease lost")
	errShutdown  = errors.New("runner shutting down")
)

// Runner claims legs and runs them on up to `concurrency` goroutines.
type Runner struct {
	q           *queue.Queue
	sink        LineSink
	id          string
	concurrency int
	heartbeat   time.Duration
	poll        time.Duration
	shutdown    time.Duration
	log         *slog.Logger
	onSettled   func(runID int64)

	mu        sync.Mutex
	executors map[string]Executor
	running   int
}

// New returns a runner named id. The heartbeat must be shorter than the
// queue's lease; poll is how often it looks for queued legs without a wake-up.
func New(q *queue.Queue, sink LineSink, id string, concurrency int, heartbeat, poll time.Duration, log *slog.Logger) *Runner {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Runner{
		q: q, sink: sink, id: id, concurrency: concurrency, heartbeat: heartbeat, poll: poll,
		shutdown: defaultShutdownTimeout, log: log, executors: map[string]Executor{},
	}
}

// WithShutdownTimeout sets how long running legs get to finish once Run's
// context ends before they are told to stop and released. The default is 20s.
func (r *Runner) WithShutdownTimeout(d time.Duration) *Runner {
	r.shutdown = d
	return r
}

// WithSettledNotifier sets a function called with the run id after a leg of
// that run has been settled (completed in the database), so the orchestrator
// can advance the run without waiting for its timer. Call it before Run.
func (r *Runner) WithSettledNotifier(f func(runID int64)) *Runner {
	r.onSettled = f
	return r
}

// Register sets the executor for a kind: "resolve", "revalidate", or a step's
// `use` such as "copy-config" or "ecs-deploy". Call it before Run.
func (r *Runner) Register(kind string, e Executor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.executors[kind] = e
}

func (r *Runner) executorFor(kind string) Executor {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.executors[kind]
}

// Run claims and executes legs until ctx ends. It claims whenever there is a
// free slot, on every receive from wake (may be nil) and every poll interval,
// and again as soon as a leg finishes. When ctx ends it stops claiming, gives
// running legs the shutdown timeout to finish, then tells the rest to stop and
// releases them to the queue. It returns nil after a clean shutdown.
func (r *Runner) Run(ctx context.Context, wake <-chan struct{}) error {
	// Legs outlive ctx: shutdown is graceful, so they are stopped through
	// stopLegs, not by ctx ending.
	legCtx, stopLegs := context.WithCancelCause(context.WithoutCancel(ctx))
	defer stopLegs(errShutdown)

	var legs sync.WaitGroup
	slotFreed := make(chan struct{}, 1)
	ticker := time.NewTicker(r.poll)
	defer ticker.Stop()

	r.log.Info("runner started", "runner_id", r.id, "concurrency", r.concurrency)
	for {
		r.claimWhileFree(ctx, legCtx, &legs, slotFreed)
		select {
		case <-ctx.Done():
			return r.drain(&legs, stopLegs)
		case <-wake:
		case <-ticker.C:
		case <-slotFreed:
		}
	}
}

// claimWhileFree starts legs until the slots are full or nothing is queued.
func (r *Runner) claimWhileFree(ctx, legCtx context.Context, legs *sync.WaitGroup, slotFreed chan<- struct{}) {
	for ctx.Err() == nil {
		r.mu.Lock()
		if r.running >= r.concurrency {
			r.mu.Unlock()
			return
		}
		r.running++
		r.mu.Unlock()

		claimed, err := r.q.Claim(ctx, r.id)
		if err != nil || claimed == nil {
			r.mu.Lock()
			r.running--
			r.mu.Unlock()
			if err != nil && ctx.Err() == nil {
				r.log.Error("claim failed", "error", err.Error())
			}
			return
		}
		legs.Add(1)
		go func() {
			defer legs.Done()
			r.runLeg(legCtx, *claimed)
			r.mu.Lock()
			r.running--
			r.mu.Unlock()
			select {
			case slotFreed <- struct{}{}:
			default:
			}
		}()
	}
}

// drain waits for the running legs for the shutdown timeout, then stops them.
func (r *Runner) drain(legs *sync.WaitGroup, stopLegs context.CancelCauseFunc) error {
	finished := make(chan struct{})
	go func() { legs.Wait(); close(finished) }()

	timer := time.NewTimer(r.shutdown)
	defer timer.Stop()
	select {
	case <-finished:
		return nil
	case <-timer.C:
	}
	r.log.Warn("shutdown timeout reached; releasing unfinished legs", "timeout", r.shutdown.String())
	stopLegs(errShutdown)
	grace := time.NewTimer(stopGrace)
	defer grace.Stop()
	select {
	case <-finished:
		return nil
	case <-grace.C:
		return fmt.Errorf("runner %s: executors did not stop within %s of being told to", r.id, stopGrace)
	}
}

// runLeg executes one claimed leg and settles it.
func (r *Runner) runLeg(parent context.Context, claimed queue.Claimed) {
	leg := claimed.Leg
	log := r.log.With("leg_id", leg.ID, "run_id", leg.RunID, "generation", leg.Generation, "attempt", leg.Attempt)
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)

	if err := r.q.MarkRunning(ctx, leg.ID, leg.Generation); err != nil {
		if errors.Is(err, queue.ErrLeaseLost) {
			log.Warn("lease lost before the leg started; dropping it")
			return
		}
		// Not ours to lose: give the leg back rather than hold it until the lease runs out.
		log.Error("could not start the leg; releasing it", "error", err.Error())
		releaseCtx, cancelRelease := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
		defer cancelRelease()
		if err := r.q.Release(releaseCtx, leg.ID, leg.Generation); err != nil {
			log.Warn("could not release the leg", "error", err.Error())
		}
		return
	}
	env := newLegEnv(r.q, r.sink, leg, log)
	if leg.Attempt > 1 {
		env.Log("warn", fmt.Sprintf("attempt %d: a previous attempt by runner %s did not finish; continuing from its last checkpoint",
			leg.Attempt, claimed.PreviousRunnerID))
	}

	stopHeartbeat := r.startHeartbeat(ctx, cancel, leg.ID, leg.Generation, log)
	outcome := r.execute(ctx, env, claimed, log)
	stopHeartbeat()

	cause := context.Cause(ctx)
	settle, cancelSettle := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancelSettle()

	// Lines first, so a leg is never settled ahead of its log.
	if err := r.sink.Flush(settle, leg.ID); err != nil {
		log.Warn("could not flush log lines", "error", err.Error())
	}

	if errors.Is(cause, errShutdown) && outcome.State != "succeeded" {
		if err := r.q.Release(settle, leg.ID, leg.Generation); err != nil {
			log.Warn("could not release the leg", "error", err.Error())
			return
		}
		log.Info("leg released for another attempt")
		return
	}
	err := r.completeWithRetry(settle, leg.ID, leg.Generation, outcome)
	if errors.Is(err, queue.ErrLeaseLost) {
		log.Warn("lease lost; result dropped", "state", outcome.State, "failure", outcome.FailureShort)
		return
	}
	if err != nil {
		// Not settled: the lease runs out and the leg is swept to another attempt.
		log.Error("could not complete the leg", "error", err.Error())
		return
	}
	log.Info("leg settled", "state", outcome.State)
	if r.onSettled != nil {
		r.onSettled(leg.RunID)
	}
}

// completeWithRetry retries a transient failure a few times; ErrLeaseLost is final.
func (r *Runner) completeWithRetry(ctx context.Context, legID, generation int64, outcome queue.Outcome) error {
	var err error
	for attempt := 0; attempt < completeTries; attempt++ {
		if err = r.q.Complete(ctx, legID, generation, outcome); err == nil || errors.Is(err, queue.ErrLeaseLost) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(completeRetryWait):
		}
	}
	return err
}

// execute finds the executor and runs it, turning a panic into an unknown outcome.
func (r *Runner) execute(ctx context.Context, env Env, claimed queue.Claimed, log *slog.Logger) (outcome queue.Outcome) {
	kind := claimed.Leg.Kind
	if kind == "step" {
		if claimed.Step == nil {
			return queue.Outcome{State: "failed", FailureShort: "step leg has no step", FailureDetail: "the leg refers to no step of the run"}
		}
		kind = claimed.Step.Use
	}
	executor := r.executorFor(kind)
	if executor == nil {
		env.Log("error", fmt.Sprintf("no executor is registered for %q", kind))
		return queue.Outcome{
			State: "failed", FailureShort: "no executor for this step",
			FailureDetail: fmt.Sprintf("no executor is registered for %q on runner %s", kind, r.id),
		}
	}
	defer func() {
		if p := recover(); p != nil {
			log.Error("executor panicked", "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			env.Log("error", fmt.Sprintf("executor panicked: %v", p))
			outcome = queue.Outcome{State: "unknown", FailureShort: "executor panicked", FailureDetail: fmt.Sprint(p)}
		}
	}()
	return executor.Execute(ctx, env, claimed)
}

// startHeartbeat renews the lease every r.heartbeat. When the queue says the
// lease is lost it cancels the leg's context so the executor stops; any other
// error is logged and retried on the next beat. The returned function stops it
// and waits for it to end.
func (r *Runner) startHeartbeat(ctx context.Context, cancel context.CancelCauseFunc, legID, generation int64, log *slog.Logger) (stop func()) {
	hbCtx, stopBeats := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(r.heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				err := r.q.Heartbeat(hbCtx, legID, generation)
				switch {
				case err == nil, hbCtx.Err() != nil:
				case errors.Is(err, queue.ErrLeaseLost):
					log.Warn("lease lost; stopping the executor")
					cancel(errLeaseLost)
					return
				default:
					log.Warn("heartbeat failed", "error", err.Error())
				}
			}
		}
	}()
	return func() { stopBeats(); <-done }
}
