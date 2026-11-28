package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rcss-labs/yamdrake/internal/store"
)

// Enqueue renders e and adds it to the outbox in tx, so the message exists
// exactly when the transition that caused it does. An empty channel (a job
// without notify.slack) does nothing, and a run gets at most one message per
// kind.
func Enqueue(ctx context.Context, tx store.DBTX, channel string, e Event, publicURL string, now time.Time) error {
	if channel == "" {
		return nil
	}
	payload, err := json.Marshal(struct {
		Text string `json:"text"`
	}{Text(e, publicURL)})
	if err != nil {
		return fmt.Errorf("render notification: %w", err)
	}
	return store.EnqueueNotification(ctx, tx, store.Notification{
		RunID: e.Run.ID, Kind: e.Kind, Channel: channel, Payload: payload, NextAttemptAt: now, CreatedAt: now,
	})
}
