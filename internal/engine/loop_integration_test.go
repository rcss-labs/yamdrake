//go:build integration

package engine

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/yamdrake/internal/cache"
	"github.com/rcss-labs/yamdrake/internal/queue"
	"github.com/rcss-labs/yamdrake/internal/store"
	"github.com/rcss-labs/yamdrake/internal/wake"
)

// startLoop runs h.eng.Loop and returns a function that stops it and checks
// that it returns promptly.
func (h *harness) startLoop(wakeRuns <-chan int64, every time.Duration, q *queue.Queue, expire func(store.Run) time.Duration) (stop func()) {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.eng.Loop(ctx, wakeRuns, every, q, expire)
		close(done)
	}()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				h.t.Error("Loop did not return after its context ended")
			}
		})
	}
	h.t.Cleanup(stop)
	return stop
}

func (h *harness) waitRunState(runID int64, want string) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if h.run(runID).State == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("run %d is %q, want %q after 10s", runID, h.run(runID).State, want)
}

// settleQueuedLegs stands in for a runner: every queued leg succeeds.
func (h *harness) settleQueuedLegs() {
	_, err := h.pool.Exec(context.Background(),
		`UPDATE legs SET state = 'succeeded', result = '{"ok":true}'::jsonb, finished_at = now() WHERE state = 'queued'`)
	if err != nil {
		h.t.Errorf("settle queued legs: %v", err)
	}
}

func TestLoopAdvancesOnWake(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	run := h.create(h.starter, false, "s1")
	h.resolved(run.ID) // the runner's work is done; only the orchestrator's step is missing

	wakes := make(chan int64, 1)
	h.startLoop(wakes, time.Hour, nil, nil) // no timer tick can help within the test
	time.Sleep(100 * time.Millisecond)
	h.expectState(run.ID, "resolving")

	wakes <- run.ID
	h.waitRunState(run.ID, "planned")
}

func TestLoopMakesProgressWithoutValkey(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	client, err := cache.Open("127.0.0.1:1") // nothing listens on this port
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	t.Cleanup(client.Close)
	bus := wake.New(client, nil)
	h.eng = New(Deps{DB: h.pool, Now: h.clock.Now, Publisher: bus})

	run := h.create(h.starter, true, "s1", "s2") // auto-start: no person is needed
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.startLoop(bus.Subscribe(ctx, wake.TopicRuns), 20*time.Millisecond, nil, nil)

	// A runner stand-in settles whatever the loop queues.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && h.run(run.ID).State != "succeeded" {
		h.settleQueuedLegs()
		time.Sleep(10 * time.Millisecond)
	}
	h.expectState(run.ID, "succeeded")
}

func TestLoopSweepsExpiredLeasesAndExpiresApprovals(t *testing.T) {
	t.Run("a leg whose lease ran out goes back to the queue", func(t *testing.T) {
		h := newHarness(t, harnessOptions{})
		run := h.create(h.starter, false, "s1")
		q := queue.New(h.pool, 30*time.Second, h.clock.Now)
		claimed, err := q.Claim(context.Background(), "runner-1")
		if err != nil || claimed == nil {
			t.Fatalf("Claim = %v, %v", claimed, err)
		}
		h.clock.Advance(time.Minute) // the runner went silent

		h.startLoop(nil, 20*time.Millisecond, q, nil)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) && h.legByKind(run.ID, "resolve").State != "queued" {
			time.Sleep(10 * time.Millisecond)
		}
		leg := h.legByKind(run.ID, "resolve")
		if leg.State != "queued" || leg.LeaseExpiries != 1 {
			t.Fatalf("leg = state %q after %d expiries, want queued after 1", leg.State, leg.LeaseExpiries)
		}
	})

	t.Run("a run nobody approved expires after the job's approval time", func(t *testing.T) {
		h := newHarness(t, harnessOptions{approvalFor: []string{testLabel}, approvalExpiry: time.Hour})
		run := h.planned(h.starter, "s1")
		if err := h.eng.Start(context.Background(), h.starter, run.ID); err != nil {
			t.Fatalf("Start: %v", err)
		}
		h.expectState(run.ID, "waiting_approval")

		h.startLoop(nil, 20*time.Millisecond, nil, nil)
		h.clock.Advance(30 * time.Minute)
		time.Sleep(200 * time.Millisecond) // several ticks
		h.expectState(run.ID, "waiting_approval")

		h.clock.Advance(2 * time.Hour)
		h.waitRunState(run.ID, "expired")
	})

	t.Run("the given function sets the approval time", func(t *testing.T) {
		h := newHarness(t, harnessOptions{approvalFor: []string{testLabel}, approvalExpiry: 24 * time.Hour})
		run := h.planned(h.starter, "s1")
		if err := h.eng.Start(context.Background(), h.starter, run.ID); err != nil {
			t.Fatalf("Start: %v", err)
		}
		h.clock.Advance(2 * time.Minute)
		h.startLoop(nil, 20*time.Millisecond, nil, func(store.Run) time.Duration { return time.Minute })
		h.waitRunState(run.ID, "expired")
	})

	t.Run("a planned run nobody started expires", func(t *testing.T) {
		h := newHarness(t, harnessOptions{})
		run := h.planned(h.starter, "s1")
		h.clock.Advance(2 * time.Hour)
		h.startLoop(nil, 20*time.Millisecond, nil, nil)
		h.waitRunState(run.ID, "expired")
	})
}

// failingDB makes locking one run fail, as a corrupt row or a lost
// connection would, and lets everything else through.
type failingDB struct {
	store.DBTX
	failID int64
}

func (f failingDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := f.DBTX.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return failingTx{Tx: tx, failID: f.failID}, nil
}

type failingTx struct {
	pgx.Tx
	failID int64
}

type failedRow struct{}

func (failedRow) Scan(...any) error { return errors.New("injected failure") }

func (f failingTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if strings.Contains(sql, "FOR NO KEY UPDATE") && len(args) == 1 && args[0] == f.failID {
		return failedRow{}
	}
	return f.Tx.QueryRow(ctx, sql, args...)
}

// lockedBuffer is a log destination several goroutines may write to.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestOneFailingRunDoesNotBlockOthers(t *testing.T) {
	setup := func(t *testing.T) (h *harness, bad, good store.Run, logs *lockedBuffer) {
		h = newHarness(t, harnessOptions{})
		bad = h.create(h.starter, false, "s1") // the lower id: a loop that gave up on the first error would never reach good
		good = h.create(h.starter, false, "s2")
		h.resolved(bad.ID)
		h.resolved(good.ID)
		logs = &lockedBuffer{}
		h.eng = New(Deps{DB: failingDB{DBTX: h.pool, failID: bad.ID}, Now: h.clock.Now,
			Log: slog.New(slog.NewTextHandler(logs, nil))})
		return h, bad, good, logs
	}
	assertFailureLogged := func(t *testing.T, logs *lockedBuffer) {
		t.Helper()
		if out := logs.String(); !strings.Contains(out, "injected failure") {
			t.Errorf("the failure was not logged; log:\n%s", out)
		}
	}

	t.Run("on the timer", func(t *testing.T) {
		h, bad, good, logs := setup(t)
		h.startLoop(nil, 20*time.Millisecond, nil, nil)
		h.waitRunState(good.ID, "planned")
		h.expectState(bad.ID, "resolving")
		assertFailureLogged(t, logs)
	})

	t.Run("on wake-ups", func(t *testing.T) {
		h, bad, good, logs := setup(t)
		wakes := make(chan int64, 2)
		h.startLoop(wakes, time.Hour, nil, nil)
		wakes <- bad.ID
		wakes <- good.ID
		h.waitRunState(good.ID, "planned")
		h.expectState(bad.ID, "resolving")
		assertFailureLogged(t, logs)
	})
}
