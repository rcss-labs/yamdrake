package wake

import (
	"context"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	valkey "github.com/valkey-io/valkey-go"
)

const (
	publishTimeout   = time.Second
	subscriberBuffer = 256
	retryWaitMin     = 100 * time.Millisecond
	retryWaitMax     = 2 * time.Second
	// warnEvery limits how often a Valkey outage is logged at warning level.
	warnEvery = 30 * time.Second
)

// Option configures a Bus.
type Option func(*Bus)

// WithKeyPrefix puts prefix in front of every channel name, so services (or
// tests) that share one Valkey do not hear each other.
func WithKeyPrefix(prefix string) Option { return func(b *Bus) { b.prefix = prefix } }

// Bus carries wake-ups over Valkey pub/sub. A wake-up is only a hint that
// something changed: every consumer also polls on a timer, so a lost message
// (Valkey down, a subscriber reconnecting) costs latency, never progress.
type Bus struct {
	client valkey.Client
	log    *slog.Logger
	prefix string

	lastWarn atomic.Int64 // unix nanoseconds of the last warning
}

// New returns a Bus on c. A nil log discards logging.
func New(c valkey.Client, log *slog.Logger, opts ...Option) *Bus {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	b := &Bus{client: c, log: log}
	for _, o := range opts {
		o(b)
	}
	return b
}

func (b *Bus) channel(topic string) string { return b.prefix + "wake:" + topic }

// warn logs an outage at most once per warnEvery, and at debug level otherwise.
func (b *Bus) warn(msg string, args ...any) {
	now := time.Now().UnixNano()
	last := b.lastWarn.Load()
	if now-last >= int64(warnEvery) && b.lastWarn.CompareAndSwap(last, now) {
		b.log.Warn(msg, args...)
		return
	}
	b.log.Debug(msg, args...)
}

// Publish announces id on topic. It is best effort and never reports a
// failure: with Valkey down it does nothing.
func (b *Bus) Publish(ctx context.Context, topic string, id int64) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), publishTimeout)
	defer cancel()
	cmd := b.client.B().Publish().Channel(b.channel(topic)).Message(strconv.FormatInt(id, 10)).Build()
	if err := b.client.Do(ctx, cmd).Error(); err != nil {
		b.warn("wake-up not published; the timers will cover it", "topic", topic, "id", id, "error", err.Error())
	}
}

// Subscribe returns the ids published on topic. It resubscribes on its own
// after a lost connection (wake-ups sent in the meantime are lost) and closes
// the channel when ctx ends. A subscriber that falls behind drops wake-ups
// instead of blocking the connection.
func (b *Bus) Subscribe(ctx context.Context, topic string) <-chan int64 {
	out := make(chan int64, subscriberBuffer)
	go func() {
		defer close(out)
		wait := retryWaitMin
		for ctx.Err() == nil {
			started := time.Now()
			err := b.client.Receive(ctx, b.client.B().Subscribe().Channel(b.channel(topic)).Build(), func(m valkey.PubSubMessage) {
				id, err := strconv.ParseInt(m.Message, 10, 64)
				if err != nil {
					return
				}
				select {
				case out <- id:
				default: // full: the next timer tick advances the run anyway
				}
			})
			if ctx.Err() != nil {
				return
			}
			b.warn("wake-up subscription lost; retrying", "topic", topic, "error", errText(err))
			if time.Since(started) > retryWaitMax {
				wait = retryWaitMin // it ran for a while: start the back-off over
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = min(wait*2, retryWaitMax)
		}
	}()
	return out
}

func errText(err error) string {
	if err == nil {
		return "subscription ended"
	}
	return err.Error()
}
