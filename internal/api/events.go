package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/rcss-labs/yamdrake/internal/auth"
	"github.com/rcss-labs/yamdrake/internal/logs"
	"github.com/rcss-labs/yamdrake/internal/store"
	"github.com/rcss-labs/yamdrake/internal/wake"
)

const (
	defaultStreamKeepAlive    = 15 * time.Second
	defaultStreamPollInterval = time.Second
	defaultStreamBatchLines   = 200
)

// Waker delivers the ids of runs that changed; *wake.Bus satisfies it.
type Waker interface {
	Subscribe(ctx context.Context, topic string) <-chan int64
}

// eventStream is the state of one client's stream.
type eventStream struct {
	s        *server
	run      store.Run
	steps    []store.RunStep
	withLogs bool
	// cursor is the log position after the last batch handed to the client;
	// lastState is the last state event body, so unchanged state is not resent.
	cursor    logs.Cursor
	lastState []byte
}

// streamEvents serves GET /runs/{id}/events as Server-Sent Events.
//
// The client first gets a state event, then log events (id: the cursor after
// the batch), then state events as the run changes, and an end event once the
// run is final and its last lines were sent. A client that reconnects with
// Last-Event-ID (or ?after=) gets the state again and then only the lines after
// that cursor: the cursor holds the last sequence seen per leg, so no line is
// repeated or skipped whatever was committed in between. The stream looks for
// changes when a wake-up arrives and also on a timer, so it works without
// Valkey. The cursor belongs to an unfiltered log, which is what the stream
// reads.
func (s *server) streamEvents(w http.ResponseWriter, r *http.Request) {
	run, ok := s.runFromPath(w, r)
	if !ok {
		return
	}
	principal, ok := s.principal(w, r)
	if !ok {
		return
	}
	steps, err := store.StepsOfRun(r.Context(), s.Pool, run.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	stream := &eventStream{s: s, run: run, steps: steps, withLogs: principal.Can(auth.ViewLogs, targetOfRun(run))}
	// The header is what a reconnecting browser sends; ?after= is for clients
	// that start a stream at a known position.
	if id := r.Header.Get("Last-Event-ID"); id != "" {
		stream.cursor = logs.Cursor(id)
	} else {
		stream.cursor = logs.Cursor(r.URL.Query().Get("after"))
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// The first round runs before any header is sent, so a bad cursor or a
	// database failure is still an ordinary error answer.
	var out bytes.Buffer
	final, err := stream.collect(ctx, &out)
	switch {
	case errors.Is(err, logs.ErrBadCursor):
		invalid(w, "the cursor is not one this log produced")
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}

	// A stream is open for as long as the run runs: lift any write deadline.
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Time{})
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("X-Accel-Buffering", "no") // nginx: do not buffer the stream
	w.WriteHeader(http.StatusOK)
	send := func(data []byte) bool {
		if _, err := w.Write(data); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	if !send(out.Bytes()) {
		return
	}

	var wakeRuns, wakeLogs <-chan int64
	if s.Wake != nil {
		wakeRuns = s.Wake.Subscribe(ctx, wake.TopicRuns)
		wakeLogs = s.Wake.Subscribe(ctx, wake.TopicLogs)
	}
	poll := time.NewTicker(s.StreamPollInterval)
	defer poll.Stop()
	keepAlive := time.NewTicker(s.StreamKeepAlive)
	defer keepAlive.Stop()

	for {
		if final {
			send(frame("end", "", stream.lastState))
			return
		}
		// Wait for a reason to look again.
		select {
		case <-ctx.Done():
			return
		case <-keepAlive.C:
			if !send([]byte(": keep-alive\n\n")) {
				return
			}
			continue
		case id, open := <-wakeRuns:
			if !open {
				wakeRuns = nil
			}
			if open && id != run.ID {
				continue
			}
		case id, open := <-wakeLogs:
			if !open {
				wakeLogs = nil
			}
			if open && id != run.ID {
				continue
			}
		case <-poll.C:
		}
		out.Reset()
		final, err = stream.collect(ctx, &out)
		// Whatever was collected before an error is valid and the cursor
		// moved past it: send it, then end the stream; the browser reconnects
		// with Last-Event-ID and carries on.
		if out.Len() > 0 && !send(out.Bytes()) {
			return
		}
		if err != nil {
			if ctx.Err() == nil {
				s.Log.Error("event stream ended", "run", run.ID, "error", err.Error())
			}
			return
		}
	}
}

// collect appends to out the events for what changed since the last call: a
// state event when the state differs, then log events until the log is read to
// its end. It reports whether the run was final when its state was read; the
// log is read after the state, so a final run's lines are all included.
func (e *eventStream) collect(ctx context.Context, out *bytes.Buffer) (final bool, err error) {
	run, err := store.GetRun(ctx, e.s.Pool, e.run.ID)
	if err != nil {
		return false, err
	}
	state, err := e.s.buildState(ctx, run, e.steps)
	if err != nil {
		return false, err
	}
	body, err := json.Marshal(state)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(body, e.lastState) {
		e.lastState = body
		out.Write(frame("state", "", body))
	}
	final = finalRuns[run.State]
	if !e.withLogs {
		return final, nil
	}
	for {
		page, err := e.s.Logs.Read(ctx, run.ID, logs.Filter{}, e.cursor, e.s.StreamBatchLines)
		if err != nil {
			return false, err
		}
		if len(page.Lines) == 0 {
			return final, nil
		}
		data, err := json.Marshal(shapeLogPage(page))
		if err != nil {
			return false, err
		}
		e.cursor = page.Next
		out.Write(frame("log", string(page.Next), data))
		if len(page.Lines) < e.s.StreamBatchLines {
			return final, nil
		}
	}
}

// frame is one Server-Sent Event. data is compact JSON, which has no line
// breaks, so one data line holds it.
func frame(event, id string, data []byte) []byte {
	var b bytes.Buffer
	b.WriteString("event: " + event + "\n")
	if id != "" {
		b.WriteString("id: " + id + "\n")
	}
	b.WriteString("data: ")
	b.Write(data)
	b.WriteString("\n\n")
	return b.Bytes()
}
