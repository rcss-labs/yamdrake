package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/rcss-labs/yamdrake/internal/cli"
)

const restoreHelp = `usage: yumdrake restore RUN [--service NAME]... [--yes] [--watch] [--logs] [--wait-for-approval] [-o json]

Create a restore run that puts the services RUN changed back to what they were
before it (their previous task definition and config copies), print its plan
with warnings for config that cannot be restored, ask, and start it. Without
--service every service RUN changed is restored. Without a terminal and
without --yes the restore run is left planned and the exit code is 5.

Example:
  yumdrake restore 12 --service billing --yes --watch
`

const rerunFailedHelp = `usage: yumdrake runs rerun-failed RUN [--yes] [--watch] [--logs] [--wait-for-approval] [-o json]

Create a run for the services of RUN that failed, were skipped or were
cancelled, with the versions they had, print its plan, ask, and start it.

Example:
  yumdrake runs rerun-failed 12 --yes --watch
`

const reconcileHelp = `usage: yumdrake runs reconcile RUN --leg ID --outcome succeeded|failed [--yes] [-o json]

Tell the server what really happened to a service whose outcome was not known
(a leg in state unknown, in a run that needs reconciliation). Look at the
service first, or use "yumdrake runs check-again". It asks unless --yes.

Example:
  yumdrake runs reconcile 14 --leg 103 --outcome succeeded
`

const checkAgainHelp = `usage: yumdrake runs check-again RUN --leg ID [-o json]

Ask the server to look again at a service whose outcome was not known.

Example:
  yumdrake runs check-again 14 --leg 103
`

func restoreCmd(args []string, stdout, stderr io.Writer, e env) int {
	flags := newAPIFlags("restore", restoreHelp)
	var services stringList
	var yes bool
	var watch watchFlags
	flags.Var(&services, "service", "restore only this service (repeatable)")
	flags.BoolVar(&yes, "yes", false, "do not ask before starting")
	watch.register(flags.commandFlags)
	positional, code, done := flags.parseArgs(args, stdout, stderr)
	if done {
		return code
	}
	if code, ok := needArgs(flags.commandFlags, positional, stderr, 1, "restore needs one RUN"); !ok {
		return code
	}
	id, err := parseRunID(positional[0])
	if err != nil {
		return fail(stderr, err)
	}
	s, err := e.openSession(flags, stdout, stderr)
	if err != nil {
		return fail(stderr, err)
	}
	stop := s.catchInterrupt()
	defer stop()

	_, source, err := s.getRun(id)
	if err != nil {
		return fail(stderr, err)
	}
	names := source.serviceNames()
	for _, name := range services {
		if !containsString(names, name) {
			return fail(stderr, usageError("run %d has no service %q. Services: %s", id, name, listNames(names)))
		}
	}
	chosen := []string(services)
	if chosen == nil {
		chosen = []string{}
	}
	return s.createPlanAndStart(fmt.Sprintf("/api/v1/runs/%d/restore", id), map[string]any{"services": chosen}, yes, watch)
}

func runsRerunFailed(args []string, stdout, stderr io.Writer, e env) int {
	flags := newAPIFlags("runs rerun-failed", rerunFailedHelp)
	var yes bool
	var watch watchFlags
	flags.BoolVar(&yes, "yes", false, "do not ask before starting")
	watch.register(flags.commandFlags)
	positional, code, done := flags.parseArgs(args, stdout, stderr)
	if done {
		return code
	}
	if code, ok := needArgs(flags.commandFlags, positional, stderr, 1, "runs rerun-failed needs one RUN"); !ok {
		return code
	}
	id, err := parseRunID(positional[0])
	if err != nil {
		return fail(stderr, err)
	}
	s, err := e.openSession(flags, stdout, stderr)
	if err != nil {
		return fail(stderr, err)
	}
	stop := s.catchInterrupt()
	defer stop()
	return s.createPlanAndStart(fmt.Sprintf("/api/v1/runs/%d/rerun-failed", id), struct{}{}, yes, watch)
}

// createPlanAndStart posts to a route that creates a run (restore, re-run),
// waits for its plan, prints it, asks and starts.
func (s *session) createPlanAndStart(path string, body any, yes bool, watch watchFlags) int {
	var created runDetail
	if err := s.client.Do(s.ctx, http.MethodPost, path, body, &created); err != nil {
		return fail(s.stderr, err)
	}
	p, err := s.planCreated(jobSummary{}, created.ID)
	if err != nil {
		return fail(s.stderr, err)
	}
	return s.confirmAndStart(p, yes, watch)
}

// serviceNames are the distinct services of a run's legs, in order.
func (d runDetail) serviceNames() []string {
	var names []string
	for _, l := range d.Legs {
		if l.Service != "" && !containsString(names, l.Service) {
			names = append(names, l.Service)
		}
	}
	return names
}

// legArgs checks --leg and returns the leg id.
func legArg(value string) (int64, error) {
	if value == "" {
		return 0, usageError("--leg ID is required")
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, usageError("%q is not a leg id", value)
	}
	return id, nil
}

func runsReconcile(args []string, stdout, stderr io.Writer, e env) int {
	flags := newAPIFlags("runs reconcile", reconcileHelp)
	var leg, outcome string
	var yes bool
	flags.StringVar(&leg, "leg", "", "the leg to reconcile")
	flags.StringVar(&outcome, "outcome", "", "what happened: succeeded or failed")
	flags.BoolVar(&yes, "yes", false, "do not ask")
	positional, code, done := flags.parseArgs(args, stdout, stderr)
	if done {
		return code
	}
	if code, ok := needArgs(flags.commandFlags, positional, stderr, 1, "runs reconcile needs one RUN"); !ok {
		return code
	}
	id, err := parseRunID(positional[0])
	if err != nil {
		return fail(stderr, err)
	}
	legID, err := legArg(leg)
	if err != nil {
		return fail(stderr, err)
	}
	switch outcome {
	case "":
		return fail(stderr, usageError("--outcome succeeded|failed is required"))
	case "succeeded", "failed":
	default:
		return fail(stderr, usageError("unknown outcome %q. Outcomes: succeeded, failed", outcome))
	}
	s, err := e.openSession(flags, stdout, stderr)
	if err != nil {
		return fail(stderr, err)
	}
	_, run, err := s.getRun(id)
	if err != nil {
		return fail(stderr, err)
	}
	var target *runLeg
	var unknown []string
	for i, l := range run.Legs {
		if l.State == "unknown" {
			unknown = append(unknown, fmt.Sprintf("%d (%s)", l.ID, legLabel(l)))
		}
		if l.ID == legID {
			target = &run.Legs[i]
		}
	}
	needing := "no leg of this run needs it"
	if len(unknown) > 0 {
		needing = "legs that need it: " + listNames(unknown)
	}
	switch {
	case target == nil:
		return fail(stderr, usageError("run %d has no leg %d; %s", id, legID, needing))
	case target.State != "unknown":
		return fail(stderr, usageError("leg %d (%s) is %s; only a leg whose outcome is unknown can be reconciled; %s", legID, legLabel(*target), target.State, needing))
	}
	if !yes {
		if !e.StdinIsTerminal {
			return fail(stderr, usageError("there is no terminal to ask on: check the service yourself, then pass --yes"))
		}
		ok, err := s.confirm(fmt.Sprintf("Record that %s in run %d %s?", legLabel(*target), id, outcome))
		if err != nil {
			return s.interrupted()
		}
		if !ok {
			s.progressf("Nothing was recorded.\n")
			return cli.ExitFailed
		}
	}
	return s.legAction(id, legID, "reconcile", map[string]string{"outcome": outcome})
}

func runsCheckAgain(args []string, stdout, stderr io.Writer, e env) int {
	flags := newAPIFlags("runs check-again", checkAgainHelp)
	var leg string
	flags.StringVar(&leg, "leg", "", "the leg to look at again")
	positional, code, done := flags.parseArgs(args, stdout, stderr)
	if done {
		return code
	}
	if code, ok := needArgs(flags.commandFlags, positional, stderr, 1, "runs check-again needs one RUN"); !ok {
		return code
	}
	id, err := parseRunID(positional[0])
	if err != nil {
		return fail(stderr, err)
	}
	legID, err := legArg(leg)
	if err != nil {
		return fail(stderr, err)
	}
	s, err := e.openSession(flags, stdout, stderr)
	if err != nil {
		return fail(stderr, err)
	}
	return s.legAction(id, legID, "check-again", struct{}{})
}

// legAction posts an action on one leg and reports the run's new state.
func (s *session) legAction(runID, legID int64, action string, body any) int {
	var raw json.RawMessage
	path := fmt.Sprintf("/api/v1/runs/%d/legs/%d/%s", runID, legID, action)
	if err := s.client.Do(s.ctx, http.MethodPost, path, body, &raw); err != nil {
		return fail(s.stderr, err)
	}
	if s.json() {
		_ = cli.WriteJSON(s.stdout, raw)
		return exitOK
	}
	var run runDetail
	_ = json.Unmarshal(raw, &run)
	s.printf("Run %d: %s. Follow it: yumdrake runs watch %d\n", runID, run.State, runID)
	return exitOK
}
