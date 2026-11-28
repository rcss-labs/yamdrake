package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/rcss-labs/yumdrake/internal/cli"
)

// watchOptions are the choices of a watch.
type watchOptions struct {
	logs            bool
	waitForApproval bool
}

// watcher follows one run's event stream and prints what changes.
type watcher struct {
	s     *session
	id    int64
	label string
	opts  watchOptions

	lastState   string
	lastWait    string
	legStates   map[int64]string
	legNames    map[int64]string
	highestSeq  map[int64]int64
	waitPrinted bool

	// final is the state of the run when its end event came.
	final *runDetail
	// stopCode is the exit code when the watch ended because a person is needed.
	stopCode int
}

// quietLegStates are not worth a line: a leg has not started yet.
var quietLegStates = map[string]bool{"pending": true, "queued": true, "claimed": true}

// watchRun follows run id until it is final, needs a person, the connection
// is lost for good, or the person interrupts. It returns the exit code.
func (s *session) watchRun(id int64, opts watchOptions) int {
	_, detail, err := s.getRun(id)
	if err != nil {
		return fail(s.stderr, err)
	}
	w := &watcher{s: s, id: id, label: detail.Label, opts: opts,
		legStates: map[int64]string{}, legNames: map[int64]string{}, highestSeq: map[int64]int64{}}
	w.rememberLegs(detail.Legs)
	err = s.client.Follow(s.ctx, cli.FollowOptions{
		Path: fmt.Sprintf("/api/v1/runs/%d/events", id),
		Now:  s.e.Now,
		Wait: s.e.Wait,
	}, w.handle)
	var exit *cli.ExitError
	switch {
	case errors.Is(err, context.Canceled):
		// Ctrl-C: stop watching, leave the run alone.
		s.progressf("Stopped watching. The run continues: yumdrake runs watch %d. To cancel it: yumdrake runs cancel %d\n", id, id)
		return exitInterrupted
	case errors.As(err, &exit) && exit.APICode == "gave_up":
		s.progressf("yumdrake: lost the connection to the server for more than 2 minutes. The run is still going: watch it again with yumdrake runs watch %d\n", id)
		return cli.ExitServer
	case err != nil:
		return fail(s.stderr, err)
	}
	code := w.stopCode
	if w.final != nil {
		w.printSummary(*w.final)
		code = exitForState(w.final.State)
	}
	if s.json() {
		var raw json.RawMessage
		if err := s.client.Do(s.ctx, http.MethodGet, fmt.Sprintf("/api/v1/runs/%d", id), nil, &raw); err != nil {
			return fail(s.stderr, err)
		}
		_ = cli.WriteJSON(s.stdout, raw)
	}
	return code
}

func (w *watcher) rememberLegs(legs []runLeg) {
	for _, l := range legs {
		w.legNames[l.ID] = legLabel(l)
	}
}

func legLabel(l runLeg) string {
	if l.Kind == "resolve" {
		return "plan"
	}
	return orDash(l.Step) + " / " + orDash(l.Service)
}

// handle is the Follow callback.
func (w *watcher) handle(ev cli.Event) error {
	switch ev.Name {
	case "state", "end":
		var st runDetail
		if err := json.Unmarshal([]byte(ev.Data), &st); err != nil {
			return &cli.ExitError{Code: cli.ExitServer, Message: "the server's event was not understood"}
		}
		w.apply(st)
		if ev.Name == "end" {
			w.final = &st
			return cli.ErrStopFollowing
		}
		return w.needsPerson(st)
	case "log":
		var page logPage
		if err := json.Unmarshal([]byte(ev.Data), &page); err != nil {
			return &cli.ExitError{Code: cli.ExitServer, Message: "the server's event was not understood"}
		}
		for _, gap := range page.Gaps {
			w.s.progressf("yumdrake: the log of %s is missing lines %d to %d\n", w.name(gap.LegID), gap.FromSeq, gap.ToSeq)
		}
		if w.opts.logs {
			for _, l := range page.Lines {
				// A reconnect must not print a line twice.
				if l.Seq <= w.highestSeq[l.LegID] {
					continue
				}
				w.highestSeq[l.LegID] = l.Seq
				w.s.textf("[%s] %s: %s\n", w.name(l.LegID), l.Level, l.Text)
			}
		}
	}
	return nil
}

func (w *watcher) name(leg int64) string {
	if n, ok := w.legNames[leg]; ok {
		return n
	}
	return fmt.Sprintf("leg %d", leg)
}

// apply prints the changes in a state event.
func (w *watcher) apply(st runDetail) {
	if st.State != w.lastState || st.WaitReason != w.lastWait {
		line := fmt.Sprintf("Run %d: %s", w.id, st.State)
		if st.WaitReason != "" {
			line += " (" + st.WaitReason + ")"
		}
		w.s.textf("%s\n", line)
		w.lastState, w.lastWait = st.State, st.WaitReason
	}
	w.rememberLegs(st.Legs)
	for _, l := range st.Legs {
		previous := w.legStates[l.ID]
		if previous == "" {
			previous = "pending"
		}
		if l.State == previous {
			continue
		}
		w.legStates[l.ID] = l.State
		if quietLegStates[l.State] {
			continue
		}
		line := fmt.Sprintf("%s: %s", legLabel(l), l.State)
		if (l.State == "failed" || l.State == "unknown") && l.FailureShort != "" {
			line += ": " + l.FailureShort
		}
		w.s.textf("%s\n", line)
	}
}

// needsPerson ends the watch when the run waits for someone, unless the
// person asked to keep waiting for an approval.
func (w *watcher) needsPerson(st runDetail) error {
	switch st.State {
	case "waiting_approval":
		if w.opts.waitForApproval {
			if !w.waitPrinted {
				w.waitPrinted = true
				w.s.progressf("Waiting for approval of run %d (it expires if nobody decides): %s\n", w.id, w.s.runLink(w.id))
			}
			return nil
		}
		w.s.printApprovalNeeded(w.id, w.label)
		w.s.progressf("To keep waiting here: yumdrake runs watch %d --wait-for-approval\n", w.id)
	case "needs_reconciliation":
		w.s.progressf("Run %d needs reconciliation: the outcome of these services is not known:\n", w.id)
		for _, l := range st.Legs {
			if l.State == "unknown" {
				w.s.progressf("  %s: %s\n", legLabel(l), orDash(l.FailureShort))
			}
		}
		w.s.progressf("Decide in the web app: %s\nOr from here: yumdrake runs reconcile %d --leg ID --outcome succeeded|failed\n", w.s.runLink(w.id), w.id)
	case "planned":
		w.s.progressf("Run %d is planned and not started. Start it with: yumdrake runs start %d\n%s\n", w.id, w.id, w.s.runLink(w.id))
	default:
		return nil
	}
	w.stopCode = cli.ExitNeedsPerson
	return cli.ErrStopFollowing
}

// printSummary prints how a run ended.
func (w *watcher) printSummary(st runDetail) {
	w.s.textf("Run %d finished: %s\n", w.id, st.State)
	if st.EndReason != "" {
		w.s.textf("  %s\n", st.EndReason)
	}
	for _, f := range st.Summary.Failures {
		w.s.textf("  %s / %s: %s: %s\n", orDash(f.Step), orDash(f.Service), f.State, orDash(f.Short))
		if f.Detail != "" {
			for _, line := range strings.Split(strings.TrimRight(f.Detail, "\n"), "\n") {
				w.s.textf("    %s\n", line)
			}
		}
	}
	w.s.textf("%s\n", w.s.runLink(w.id))
}
