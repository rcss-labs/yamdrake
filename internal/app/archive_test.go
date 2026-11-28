package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type fakeArchiver struct {
	calls atomic.Int32
	fail  bool
}

func (f *fakeArchiver) ArchiveSettledLegs(context.Context) (int, error) {
	f.calls.Add(1)
	if f.fail {
		return 0, errors.New("disk full")
	}
	return 1, nil
}

func TestArchiveLoopRunsOnATimerSurvivesErrorsAndStopsWithItsContext(t *testing.T) {
	for _, fail := range []bool{false, true} {
		archiver := &fakeArchiver{fail: fail}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { archiveLoop(ctx, archiver, 5*time.Millisecond, nil); close(done) }()
		deadline := time.Now().Add(2 * time.Second)
		for archiver.calls.Load() < 3 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if archiver.calls.Load() < 3 {
			t.Fatalf("fail=%v: the loop called the archiver %d times, want it to keep going", fail, archiver.calls.Load())
		}
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("fail=%v: the loop did not stop when its context ended", fail)
		}
	}
}
