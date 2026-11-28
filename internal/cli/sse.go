package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Event is one Server-Sent Event.
type Event struct {
	// Name is the event type; "message" when the frame names none.
	Name string
	// ID is the frame's id, "" when it had none.
	ID   string
	Data string
}

// ErrStopFollowing is returned by a Follow handler to end the stream normally.
var ErrStopFollowing = errors.New("stop following")

const maxEventLineBytes = 16 << 20

// pingEvent is the named keep-alive event the server sends on a quiet stream.
const pingEvent = "ping"

// ReadEvents parses a Server-Sent Events stream and calls handle for every
// complete frame. A frame the stream ended in the middle of is not delivered.
// Comment lines (": keep-alive") are ignored. It returns nil at the end of the
// stream, or the handler's error.
func ReadEvents(r io.Reader, handle func(Event) error) error {
	return readEvents(r, nil, handle)
}

// readEvents is ReadEvents that also calls onLine for every line received,
// comments and keep-alives included, so a caller can tell a quiet stream from a
// dead one.
func readEvents(r io.Reader, onLine func(), handle func(Event) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), maxEventLineBytes)
	var name, id string
	var data []string
	seen := false
	for scanner.Scan() {
		if onLine != nil {
			onLine()
		}
		line := scanner.Text()
		if line == "" {
			if seen {
				event := Event{Name: name, ID: id, Data: strings.Join(data, "\n")}
				if event.Name == "" {
					event.Name = "message"
				}
				if err := handle(event); err != nil {
					return err
				}
			}
			name, id, data, seen = "", "", nil, false
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			name, seen = value, true
		case "id":
			id, seen = value, true
		case "data":
			data, seen = append(data, value), true
		}
	}
	return scanner.Err()
}

// openStream opens a Server-Sent Events response. Failures are *ExitError the
// way Do reports them; the caller decides whether to try again.
func (c *Client) openStream(ctx context.Context, path, lastEventID string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.server+path, nil)
	if err != nil {
		return nil, &ExitError{Code: ExitUsage, Message: "invalid request path"}
	}
	req.Header.Set("Accept", "text/event-stream")
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.stream.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &ExitError{Code: ExitServer, Message: fmt.Sprintf("cannot reach the server %s: %s", c.server, c.scrub(rootCause(err)))}
	}
	if resp.StatusCode >= 400 {
		defer func() { _ = resp.Body.Close() }()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, c.statusError(resp.StatusCode, resp.Header.Get("Retry-After"), data)
	}
	return resp.Body, nil
}

// FollowOptions tune Follow.
type FollowOptions struct {
	// Path is the stream's path with its query.
	Path string
	// GiveUp is how long without a connection ends the attempt; default 2 minutes.
	GiveUp time.Duration
	// Idle is how long a silent connection is trusted; default 60 seconds. The
	// server sends a keep-alive every 15 seconds.
	Idle time.Duration
	// Now and Wait default to the real clock.
	Now  func() time.Time
	Wait func(ctx context.Context, d time.Duration) error
}

const (
	defaultGiveUp = 2 * time.Minute
	defaultIdle   = 60 * time.Second
	firstBackoff  = time.Second
	maxStreamWait = 10 * time.Second
)

// Follow reads the event stream at o.Path and calls handle for every event.
// When the connection drops it reconnects with the id of the last event it
// saw (the Last-Event-ID header), so the server resumes after that event.
// It ends with nil when handle returns ErrStopFollowing, with handle's other
// errors, with the context's error when ctx ends, with an *ExitError when the
// server refuses (not signed in, no such run), and with an *ExitError of code
// 7 and API code "gave_up" after o.GiveUp without a connection.
func (c *Client) Follow(ctx context.Context, o FollowOptions, handle func(Event) error) error {
	if o.GiveUp <= 0 {
		o.GiveUp = defaultGiveUp
	}
	if o.Idle <= 0 {
		o.Idle = defaultIdle
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Wait == nil {
		o.Wait = func(ctx context.Context, d time.Duration) error {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-timer.C:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	var lastID string
	var lostSince time.Time // zero while events are flowing
	backoff := firstBackoff
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		stop, err := c.followOnce(ctx, o, &lastID, &lostSince, &backoff, handle)
		if stop {
			return err
		}
		if lostSince.IsZero() {
			lostSince = o.Now()
		}
		if o.Now().Sub(lostSince) >= o.GiveUp {
			return &ExitError{Code: ExitServer, APICode: "gave_up",
				Message: fmt.Sprintf("no connection to the server for %s", o.GiveUp)}
		}
		if err := o.Wait(ctx, backoff); err != nil {
			return err
		}
		backoff = min(backoff*2, maxStreamWait)
	}
}

// followOnce makes one connection. stop is true when Follow must return err.
func (c *Client) followOnce(ctx context.Context, o FollowOptions, lastID *string, lostSince *time.Time, backoff *time.Duration, handle func(Event) error) (stop bool, err error) {
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	body, err := c.openStream(connCtx, o.Path, *lastID)
	if err != nil {
		var exit *ExitError
		if errors.As(err, &exit) && exit.Code == ExitServer {
			return false, nil // unreachable or failing: try again
		}
		return true, err
	}
	defer func() { _ = body.Close() }()
	idle := time.AfterFunc(o.Idle, cancel)
	defer idle.Stop()
	var handlerErr error
	err = readEvents(body, func() { idle.Reset(o.Idle) }, func(e Event) error {
		*lostSince, *backoff = time.Time{}, firstBackoff
		if e.Name == pingEvent {
			return nil // the server's keep-alive, not a run event
		}
		if e.ID != "" {
			*lastID = e.ID
		}
		handlerErr = handle(e)
		return handlerErr
	})
	switch {
	case errors.Is(handlerErr, ErrStopFollowing):
		return true, nil
	case handlerErr != nil:
		return true, handlerErr
	case ctx.Err() != nil:
		return true, ctx.Err()
	case errors.Is(err, bufio.ErrTooLong):
		return true, &ExitError{Code: ExitServer, Message: "the server sent an event that is too large"}
	}
	// The connection broke, went silent or was closed by the server: reconnect.
	return false, nil
}
