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

// KindBreakGlassSignIn is the kind of the message that tells people a local
// (break-glass) account signed in. It is not about a run.
const KindBreakGlassSignIn = "break_glass_sign_in"

// EnqueueBreakGlassSignIn adds one message per channel saying who signed in with
// a local account and when. The email is escaped for Slack like every other
// value.
func EnqueueBreakGlassSignIn(ctx context.Context, db store.DBTX, channels []string, email string, at time.Time) error {
	payload, err := json.Marshal(struct {
		Text string `json:"text"`
	}{fmt.Sprintf("Break-glass sign-in: %s signed in with a local account at %s.", slackEscape(email), at.UTC().Format("2006-01-02 15:04:05 UTC"))})
	if err != nil {
		return fmt.Errorf("render notification: %w", err)
	}
	for _, channel := range channels {
		if err := store.EnqueueNotification(ctx, db, store.Notification{
			Kind: KindBreakGlassSignIn, Channel: channel, Payload: payload, NextAttemptAt: at, CreatedAt: at,
		}); err != nil {
			return err
		}
	}
	return nil
}
