package metrics

import (
	"context"
	"log/slog"
	"time"

	"github.com/rcss-labs/yamdrake/internal/store"
)

// ThrottleRecordInterval is how often each service stores its throttling count.
const ThrottleRecordInterval = 10 * time.Second

// RecordThrottles stores the throttling answers counted since the last round (take
// returns and resets the count) as system events at now, so the orchestrator's
// AWSThrottles metric covers the runners' calls as well as its own. A quiet
// round writes nothing.
func RecordThrottles(ctx context.Context, db store.DBTX, take func() int64, now time.Time) error {
	n := take()
	if n <= 0 {
		return nil
	}
	return store.RecordEvents(ctx, db, "aws_throttled", int(n), now)
}

// RunThrottleRecorder calls RecordThrottles every interval until ctx ends, and
// once more on the way out so a stop does not lose the last count. A failed
// round is logged; the count it took is lost, which is acceptable for a metric.
func RunThrottleRecorder(ctx context.Context, db store.DBTX, take func() int64, interval time.Duration, log *slog.Logger) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			flush, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			if err := RecordThrottles(flush, db, take, time.Now()); err != nil {
				log.Warn("could not record the last AWS throttling count", "error", err.Error())
			}
			cancel()
			return
		case <-ticker.C:
			if err := RecordThrottles(ctx, db, take, time.Now()); err != nil && ctx.Err() == nil {
				log.Warn("could not record AWS throttling", "error", err.Error())
			}
		}
	}
}
