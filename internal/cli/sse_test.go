package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReadEventsParsesTheFrames(t *testing.T) {
	stream := ": keep-alive\n\n" +
		"event: state\ndata: {\"a\":1}\n\n" +
		"event: log\nid: c1\ndata: line one\ndata: line two\n\n" +
		"data: no name\r\n\r\n" +
		"event: end\ndata: {}\n"
	var got []Event
	err := ReadEvents(strings.NewReader(stream), func(e Event) error { got = append(got, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	want := []Event{
		{Name: "state", Data: `{"a":1}`},
		{Name: "log", ID: "c1", Data: "line one\nline two"},
		{Name: "message", Data: "no name"},
	}
	if len(got) != len(want) {
		t.Fatalf("events = %+v, want %+v (an unfinished frame must not be delivered)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// followHarness runs Follow against a scripted server with a fake clock.
type followHarness struct {
	srv   *httptest.Server
	mu    sync.Mutex
	conns int
	ids   []string // Last-Event-ID of each connection
	now   time.Time
	waits []time.Duration
}

func newFollowHarness(t *testing.T, handler func(h *followHarness, conn int, w http.ResponseWriter, r *http.Request)) *followHarness {
	t.Helper()
	h := &followHarness{now: time.Unix(1_800_000_000, 0)}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.conns++
		conn := h.conns
		h.ids = append(h.ids, r.Header.Get("Last-Event-ID"))
		h.mu.Unlock()
		handler(h, conn, w, r)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *followHarness) options() FollowOptions {
	return FollowOptions{
		Path: "/api/v1/runs/1/events",
		Now:  func() time.Time { return h.now },
		Wait: func(_ context.Context, d time.Duration) error {
			h.waits = append(h.waits, d)
			h.now = h.now.Add(d)
			return nil
		},
	}
}

func frameText(event, id, data string) string {
	s := "event: " + event + "\n"
	if id != "" {
		s += "id: " + id + "\n"
	}
	return s + "data: " + data + "\n\n"
}

func TestFollowReconnectsWithTheLastEventIDAndSeesEachEventOnce(t *testing.T) {
	h := newFollowHarness(t, func(_ *followHarness, conn int, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if conn == 1 {
			_, _ = fmt.Fprint(w, frameText("state", "", "{}")+frameText("log", "c1", "one"))
			return // the connection drops
		}
		if r.Header.Get("Last-Event-ID") != "c1" {
			t.Errorf("reconnected with Last-Event-ID %q, want c1", r.Header.Get("Last-Event-ID"))
		}
		_, _ = fmt.Fprint(w, frameText("log", "c2", "two")+frameText("end", "", "{}"))
	})
	client, _ := NewClient(Config{Server: h.srv.URL, Token: secretToken})
	var seen []string
	err := client.Follow(context.Background(), h.options(), func(e Event) error {
		seen = append(seen, e.Name+":"+e.Data)
		if e.Name == "end" {
			return ErrStopFollowing
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(seen, " "); got != "state:{} log:one log:two end:{}" {
		t.Errorf("events = %q", got)
	}
	if h.conns != 2 {
		t.Errorf("connections = %d, want 2", h.conns)
	}
}

func TestFollowGivesUpAfterTwoMinutesWithoutAConnection(t *testing.T) {
	h := newFollowHarness(t, func(_ *followHarness, _ int, w http.ResponseWriter, _ *http.Request) {
		// Accept the connection, then cut it before any response.
		hj, _ := w.(http.Hijacker)
		conn, _, _ := hj.Hijack()
		_ = conn.Close()
	})
	client, _ := NewClient(Config{Server: h.srv.URL, Token: secretToken})
	start := h.now
	err := client.Follow(context.Background(), h.options(), func(Event) error { return nil })
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != ExitServer || exit.APICode != "gave_up" {
		t.Fatalf("err = %v, want an ExitError with code 7 and API code gave_up", err)
	}
	if elapsed := h.now.Sub(start); elapsed < 2*time.Minute || elapsed > 3*time.Minute {
		t.Errorf("gave up after %s on the fake clock, want about 2 minutes", elapsed)
	}
}

func TestFollowKeepsTheClockWhileConnectionsKeepFailingAfterAnEvent(t *testing.T) {
	// One good event, then the server is gone: the two minutes count from the loss.
	var first sync.Once
	h := newFollowHarness(t, func(h *followHarness, _ int, w http.ResponseWriter, _ *http.Request) {
		served := false
		first.Do(func() {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, frameText("state", "", "{}"))
			served = true
		})
		if served {
			return
		}
		hj, _ := w.(http.Hijacker)
		conn, _, _ := hj.Hijack()
		_ = conn.Close()
	})
	client, _ := NewClient(Config{Server: h.srv.URL, Token: secretToken})
	events := 0
	err := client.Follow(context.Background(), h.options(), func(Event) error { events++; return nil })
	var exit *ExitError
	if !errors.As(err, &exit) || exit.APICode != "gave_up" || events != 1 {
		t.Errorf("err = %v events = %d", err, events)
	}
}

func TestFollowDoesNotRetryWhenTheServerRefuses(t *testing.T) {
	for status, wantCode := range map[int]int{401: ExitNotSignIn, 403: ExitNotSignIn, 404: ExitUsage} {
		h := newFollowHarness(t, func(_ *followHarness, _ int, w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		})
		client, _ := NewClient(Config{Server: h.srv.URL, Token: secretToken})
		err := client.Follow(context.Background(), h.options(), func(Event) error { return nil })
		if ExitCodeFor(err) != wantCode || h.conns != 1 {
			t.Errorf("status %d: err %v after %d connections, want code %d after 1", status, err, h.conns, wantCode)
		}
		if err != nil && strings.Contains(err.Error(), secretToken) {
			t.Errorf("token in error: %v", err)
		}
	}
}

func TestFollowRetriesServerErrors(t *testing.T) {
	h := newFollowHarness(t, func(_ *followHarness, conn int, w http.ResponseWriter, _ *http.Request) {
		if conn == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = fmt.Fprint(w, frameText("end", "", "{}"))
	})
	client, _ := NewClient(Config{Server: h.srv.URL, Token: secretToken})
	err := client.Follow(context.Background(), h.options(), func(e Event) error { return ErrStopFollowing })
	if err != nil || h.conns != 2 {
		t.Errorf("err %v after %d connections", err, h.conns)
	}
}

func TestFollowStopsWhenTheContextIsCancelled(t *testing.T) {
	h := newFollowHarness(t, func(_ *followHarness, _ int, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, frameText("state", "", "{}"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	client, _ := NewClient(Config{Server: h.srv.URL, Token: secretToken})
	ctx, cancel := context.WithCancel(context.Background())
	err := client.Follow(ctx, h.options(), func(Event) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestFollowReconnectsWhenTheStreamGoesSilent(t *testing.T) {
	h := newFollowHarness(t, func(_ *followHarness, conn int, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if conn == 1 {
			_, _ = fmt.Fprint(w, frameText("log", "c1", "one"))
			w.(http.Flusher).Flush()
			<-r.Context().Done() // silent from now on
			return
		}
		_, _ = fmt.Fprint(w, frameText("end", "", "{}"))
	})
	client, _ := NewClient(Config{Server: h.srv.URL, Token: secretToken})
	opts := h.options()
	opts.Idle = 150 * time.Millisecond
	var names []string
	err := client.Follow(context.Background(), opts, func(e Event) error {
		names = append(names, e.Name)
		if e.Name == "end" {
			return ErrStopFollowing
		}
		return nil
	})
	if err != nil || strings.Join(names, ",") != "log,end" || h.ids[1] != "c1" {
		t.Errorf("err %v names %v ids %v", err, names, h.ids)
	}
}

func TestClientShowsTheProblemsOf422(t *testing.T) {
	srv := serverReplying(422, `{"error":{"code":"invalid","message":"the selection cannot be planned","problems":[{"file":"jobs/a.yaml","line":0,"message":"service \"x\" is not in group \"g\""}]}}`)
	defer srv.Close()
	client, _ := NewClient(Config{Server: srv.URL, Token: secretToken})
	err := client.Do(context.Background(), "POST", "/x", struct{}{}, nil)
	if ExitCodeFor(err) != ExitUsage || err == nil ||
		!strings.Contains(err.Error(), "the selection cannot be planned") || !strings.Contains(err.Error(), `service "x" is not in group "g"`) {
		t.Errorf("err = %v", err)
	}
}
