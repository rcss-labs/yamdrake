package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rcss-labs/yamdrake/internal/cli"
)

const selectionHelp = `Choosing what to deploy:
  --env ENV              the environment (required)
  --set GROUP=VERSION    deploy the whole group at VERSION (repeatable)
  --set GROUP.SERVICE=VERSION
                         give one service of the group its own version
  --only NAME[,NAME]...  narrow the selection to these groups or services
  --latest               a group with no --set takes its newest common version
Every name is checked against the job before anything is created.
`

const planHelp = `usage: yumdrake plan JOB --env ENV [--set GROUP=VERSION]... [--set GROUP.SERVICE=VERSION]...
                     [--only NAME[,NAME]...] [--latest] [-o json]

Create a planned run and print its plan: the version changes, the config
changes (key names only, never values), the checks and any fallback
warnings, and the run number. The run is not started. Exit 0 when every check
passed, 1 when one failed.

` + selectionHelp + `
Example:
  yumdrake plan roll-backends --env dev-01 --set backends=1.4.2 --set workers=2.0.0
`

const runHelp = `usage: yumdrake run JOB --env ENV [--set GROUP=VERSION]... [--only NAME[,NAME]...]
                    [--latest] [--yes] [--watch] [--logs] [--wait-for-approval] [-o json]

Plan a run, print the plan, ask "Start this run? [y/N]" (unless --yes), start
it and, with --watch, follow it until it ends. Without a terminal and without
--yes the run is left planned and the exit code is 5.

` + selectionHelp + `
  --yes                  do not ask
  --watch                follow the run; the exit code is that of its final state
  --logs                 with --watch, print every log line
  --wait-for-approval    with --watch, wait for an approval instead of exiting 5

Exit codes: 0 succeeded, 1 failed, 3 partially succeeded, 4 cancelled, rejected,
expired or invalidated, 5 the run needs a person, 7 the server was not reachable.

Example:
  yumdrake run roll-backends --env dev-01 --set backends=1.4.2 --set workers=2.0.0 --yes --watch
`

const runsStartHelp = `usage: yumdrake runs start RUN [--watch] [--logs] [--wait-for-approval] [-o json]

Start a planned run. With --watch follow it as "yumdrake runs watch" does.

Example:
  yumdrake runs start 14 --watch
`

const runsWatchHelp = `usage: yumdrake runs watch RUN [--logs] [--wait-for-approval] [-o json]

Follow a run: one line for every change of the run and of each service, and
with --logs every log line. It ends when the run is final and exits with the
code for its state: 0 succeeded, 1 failed, 3 partially succeeded, 4 cancelled,
rejected, expired or invalidated. It exits 5 when the run needs a person
(approval, reconciliation, or it is only planned), unless --wait-for-approval
keeps it waiting for an approval. If the connection drops it reconnects and
carries on; after 2 minutes without a connection it exits 7. Ctrl-C stops
watching; the run continues and is never cancelled by this command.

Example:
  yumdrake runs watch 14 --logs
`

const runsCancelHelp = `usage: yumdrake runs cancel RUN [--yes] [-o json]

Ask the server to cancel a run. It asks first unless --yes; without a terminal
--yes is required.

Example:
  yumdrake runs cancel 14 --yes
`

const maxPlanWait = 5 * time.Minute

// selectionFlags are the flags that choose what a run deploys.
type selectionFlags struct {
	env    string
	sets   stringList
	only   stringList
	latest bool
}

func (sf *selectionFlags) register(c *commandFlags) {
	c.StringVar(&sf.env, "env", "", "the environment")
	c.Var(&sf.sets, "set", "GROUP=VERSION or GROUP.SERVICE=VERSION (repeatable)")
	c.Var(&sf.only, "only", "narrow to these groups or services (comma separated, repeatable)")
	c.BoolVar(&sf.latest, "latest", false, "a group with no --set takes its newest common version")
}

// catchInterrupt makes Ctrl-C and SIGTERM end the session's context.
func (s *session) catchInterrupt() (stop func()) {
	ctx, stop := signal.NotifyContext(s.ctx, os.Interrupt, syscall.SIGTERM)
	s.ctx = ctx
	return stop
}

// runLink is the web address of a run.
func (s *session) runLink(id int64) string {
	return fmt.Sprintf("%s/runs/%d", strings.TrimRight(s.cfg.Server, "/"), id)
}

// exitForState is the exit code of a run in this state.
func exitForState(state string) int {
	switch state {
	case "succeeded":
		return cli.ExitOK
	case "failed":
		return cli.ExitFailed
	case "partially_succeeded":
		return cli.ExitPartial
	case "cancelled", "rejected", "expired", "invalidated":
		return cli.ExitStopped
	case "waiting_approval", "needs_reconciliation", "planned":
		return cli.ExitNeedsPerson
	}
	return cli.ExitOK // resolving, queued, running, cancelling: nothing is wrong yet
}

// planned is a run that was created, planned and fetched.
type planned struct {
	job     jobSummary
	run     runDetail
	plan    *manifest
	planRaw json.RawMessage
}

// planRun checks the selection against the job, creates a planned run, waits
// for its plan and fetches it.
func (s *session) planRun(jobID string, sf *selectionFlags) (*planned, error) {
	if sf.env == "" {
		return nil, usageError("--env ENV is required")
	}
	job, err := s.findJob(jobID)
	if err != nil {
		return nil, err
	}
	var detail jobDetail
	if err := s.client.Do(s.ctx, http.MethodGet, fmt.Sprintf("/api/v1/jobs/%d", job.ID), nil, &detail); err != nil {
		return nil, err
	}
	if detail.Definition == nil {
		return nil, usageError("job %q has no readable definition yet; run `yumdrake jobs show %s` to see its problems", jobID, jobID)
	}
	selection, err := buildSelection(*detail.Definition, sf.sets, sf.only, sf.latest, func(group string) (string, error) {
		return s.newestCommonVersion(job.ID, sf.env, group)
	})
	if err != nil {
		return nil, err
	}
	body := map[string]any{"environment": sf.env, "selection": selection, "start": false}
	var created runDetail
	if err := s.client.Do(s.ctx, http.MethodPost, fmt.Sprintf("/api/v1/jobs/%d/runs", job.ID), body, &created); err != nil {
		return nil, err
	}
	return s.planCreated(job, created.ID)
}

// planCreated waits for a created run to be planned and fetches its plan.
func (s *session) planCreated(job jobSummary, id int64) (*planned, error) {
	s.textf("Created run %d. Planning...\n", id)
	run, err := s.waitForPlan(id)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			s.progressf("Run %d was created and is still being planned: yumdrake runs show %d\n", id, id)
		}
		return nil, err
	}
	p := &planned{job: job, run: run}
	err = s.client.Do(s.ctx, http.MethodGet, fmt.Sprintf("/api/v1/runs/%d/plan", run.ID), nil, &p.planRaw)
	var apiErr *cli.ExitError
	switch {
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound:
		p.planRaw = nil // a run that failed before it had a plan
	case err != nil:
		return nil, err
	default:
		p.plan = &manifest{}
		if err := json.Unmarshal(p.planRaw, p.plan); err != nil {
			return nil, &cli.ExitError{Code: cli.ExitServer, Message: "the server's plan was not understood"}
		}
	}
	return p, nil
}

// newestCommonVersion is the first tag every service of the group has.
func (s *session) newestCommonVersion(jobID int64, envName, group string) (string, error) {
	var versions jobVersions
	path := fmt.Sprintf("/api/v1/jobs/%d/versions?environment=%s", jobID, url.QueryEscape(envName))
	if err := s.client.Do(s.ctx, http.MethodGet, path, nil, &versions); err != nil {
		return "", unsupportedOn404(err, "version lists")
	}
	g, ok := versions.Groups[group]
	if !ok || len(g.Common) == 0 {
		return "", errors.New("no tag exists that every service of the group has; choose one with --set")
	}
	return g.Common[0], nil
}

// waitForPlan polls until the run leaves "resolving".
func (s *session) waitForPlan(id int64) (runDetail, error) {
	deadline := s.e.Now().Add(maxPlanWait)
	for {
		_, run, err := s.getRun(id)
		if err != nil {
			return runDetail{}, err
		}
		if run.State != "resolving" {
			return run, nil
		}
		if !s.e.Now().Before(deadline) {
			return runDetail{}, &cli.ExitError{Code: cli.ExitServer,
				Message: fmt.Sprintf("run %d is still being planned after %s: yumdrake runs show %d", id, maxPlanWait, id)}
		}
		if err := s.e.Wait(s.ctx, time.Second); err != nil {
			return runDetail{}, err
		}
	}
}

// printPlan describes a plan for a person and returns the number of failed
// checks.
func (s *session) printPlan(p *planned) int {
	run := p.run
	s.textf("\nRun %d: %s in %s (%s), %s\n", run.ID, run.JobID, run.Environment, run.Label, run.State)
	if p.plan == nil {
		s.textf("There is no plan: %s\n", orDash(run.EndReason))
		return 0
	}
	for _, svc := range p.plan.Services {
		s.textf("  %s (%s): %s\n", svc.Name, orDash(svc.Group), describeChange(svc))
		for _, c := range svc.Config {
			s.textf("    %s: %s%s\n", c.Name, orDash(c.Change), describeKeys(c.Keys))
			if c.Fallback {
				s.textf("    WARNING: %s uses the fallback branch %q because the version's branch does not exist\n", c.Name, c.Branch)
			}
			if c.NotRestored != "" {
				s.textf("    WARNING: %s is not restored: %s\n", c.Name, c.NotRestored)
			}
		}
	}
	var passed, failed, unverified int
	for _, c := range p.plan.Checks {
		switch c.Status {
		case "passed":
			passed++
		case "failed":
			failed++
		default:
			unverified++
		}
	}
	s.textf("Checks: %d passed, %d failed, %d not verified\n", passed, failed, unverified)
	for _, c := range p.plan.Checks {
		if c.Status == "passed" {
			continue
		}
		label := "FAILED"
		if c.Status != "failed" {
			label = "not verified"
		}
		where := c.Name
		if c.Service != "" {
			where += " (" + c.Service + ")"
		}
		s.textf("  %s: %s", label, where)
		if c.Detail != "" {
			s.textf(": %s", c.Detail)
		}
		s.textf("\n")
	}
	return failed
}

func describeChange(svc manifestService) string {
	if svc.Version == "" && svc.TargetTaskDefinition != "" {
		return fmt.Sprintf("task definition %s -> %s", orDash(svc.PreviousTaskDefinition), svc.TargetTaskDefinition)
	}
	previous := svc.PreviousVersion
	if previous == "" {
		previous = "unknown"
	}
	return previous + " -> " + svc.Version
}

func describeKeys(k *keyDiff) string {
	if k == nil {
		return ""
	}
	var parts []string
	for _, group := range []struct {
		word  string
		names []string
	}{{"added", k.Added}, {"removed", k.Removed}, {"changed", k.Changed}} {
		if len(group.names) > 0 {
			parts = append(parts, group.word+" "+strings.Join(group.names, ", "))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, "; ") + ")"
}

// planResult is the JSON of plan and run: the run and its plan.
func planResult(run any, planRaw json.RawMessage) map[string]any {
	out := map[string]any{"run": run, "plan": nil}
	if planRaw != nil {
		out["plan"] = planRaw
	}
	return out
}

// notStarted is the JSON of a run that was planned and then left alone (no
// terminal, or the question was declined): the plan document and
// "started": false, so a script can tell it from a started run.
func notStarted(p *planned) map[string]any {
	out := planResult(p.run, p.planRaw)
	out["started"] = false
	return out
}

// planExitCode is the exit code for a plan: 1 for a failed check.
func planExitCode(p *planned, failedChecks int) int {
	switch {
	case failedChecks > 0:
		return cli.ExitFailed
	case p.run.State != "planned":
		return exitForState(p.run.State)
	}
	return cli.ExitOK
}

func planCmd(args []string, stdout, stderr io.Writer, e env) int {
	flags := newAPIFlags("plan", planHelp)
	var sf selectionFlags
	sf.register(flags.commandFlags)
	positional, code, done := flags.parseArgs(args, stdout, stderr)
	if done {
		return code
	}
	if code, ok := needArgs(flags.commandFlags, positional, stderr, 1, "plan needs one JOB"); !ok {
		return code
	}
	s, err := e.openSession(flags, stdout, stderr)
	if err != nil {
		return fail(stderr, err)
	}
	stop := s.catchInterrupt()
	defer stop()
	p, err := s.planRun(positional[0], &sf)
	if err != nil {
		return fail(stderr, err)
	}
	failed := s.printPlan(p)
	if p.run.State == "planned" {
		s.textf("\nRun %d is planned, not started. Start it within an hour: yumdrake runs start %d\n%s\n", p.run.ID, p.run.ID, s.runLink(p.run.ID))
	}
	if s.json() {
		_ = cli.WriteJSON(stdout, planResult(p.run, p.planRaw))
	}
	return planExitCode(p, failed)
}

// confirm asks a yes or no question on standard error and reads the answer
// from standard input. The default is no.
func (s *session) confirm(question string) (bool, error) {
	_, _ = fmt.Fprintf(s.stderr, "%s [y/N] ", question)
	// The read cannot be interrupted, so it runs on its own goroutine and the
	// wait ends with the context. An abandoned read ends with the process.
	answer := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(io.LimitReader(s.e.Stdin, 1024)).ReadString('\n')
		answer <- line
	}()
	select {
	case <-s.ctx.Done():
		return false, s.ctx.Err()
	case line := <-answer:
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return true, nil
		}
		return false, nil
	}
}

// interrupted ends a command whose prompt was cut short by Ctrl-C: the cursor
// moves to a fresh line and the exit code is the usual 130.
func (s *session) interrupted() int {
	_, _ = fmt.Fprintln(s.stderr)
	return exitInterrupted
}

func runCmd(args []string, stdout, stderr io.Writer, e env) int {
	flags := newAPIFlags("run", runHelp)
	var sf selectionFlags
	sf.register(flags.commandFlags)
	var yes bool
	var watch watchFlags
	flags.BoolVar(&yes, "yes", false, "do not ask before starting")
	watch.register(flags.commandFlags)
	positional, code, done := flags.parseArgs(args, stdout, stderr)
	if done {
		return code
	}
	if code, ok := needArgs(flags.commandFlags, positional, stderr, 1, "run needs one JOB"); !ok {
		return code
	}
	s, err := e.openSession(flags, stdout, stderr)
	if err != nil {
		return fail(stderr, err)
	}
	stop := s.catchInterrupt()
	defer stop()
	p, err := s.planRun(positional[0], &sf)
	if err != nil {
		return fail(stderr, err)
	}
	return s.confirmAndStart(p, yes, watch)
}

// confirmAndStart prints a plan, asks whether to start it (unless yes) and
// starts and follows the run as asked. A run that failed its checks is not
// started. Without a terminal and without yes the run is left planned.
func (s *session) confirmAndStart(p *planned, yes bool, watch watchFlags) int {
	failed := s.printPlan(p)
	if code := planExitCode(p, failed); code != cli.ExitOK {
		if s.json() {
			_ = cli.WriteJSON(s.stdout, planResult(p.run, p.planRaw))
		}
		return code
	}
	id := p.run.ID
	if !yes {
		if !s.e.StdinIsTerminal {
			s.progressf("Run %d is planned and was not started, because there is no terminal to ask and --yes was not given.\nStart it with: yumdrake runs start %d\n%s\n", id, id, s.runLink(id))
			if s.json() {
				_ = cli.WriteJSON(s.stdout, notStarted(p))
			}
			return cli.ExitNeedsPerson
		}
		ok, err := s.confirm("Start this run?")
		if err != nil {
			return s.interrupted()
		}
		if !ok {
			s.progressf("Not started. Run %d stays planned for an hour: yumdrake runs start %d\n", id, id)
			if s.json() {
				_ = cli.WriteJSON(s.stdout, notStarted(p))
			}
			return cli.ExitNeedsPerson
		}
	}
	return s.startAndFollow(id, watch)
}

// startAndFollow starts a planned run and, with --watch, follows it.
func (s *session) startAndFollow(id int64, watch watchFlags) int {
	var started json.RawMessage
	if err := s.client.Do(s.ctx, http.MethodPost, fmt.Sprintf("/api/v1/runs/%d/start", id), struct{}{}, &started); err != nil {
		return fail(s.stderr, err)
	}
	var run runDetail
	if err := json.Unmarshal(started, &run); err != nil {
		return fail(s.stderr, &cli.ExitError{Code: cli.ExitServer, Message: "the server's answer was not understood"})
	}
	s.textf("Run %d started: %s\n", id, run.State)
	if watch.watch {
		return s.watchRun(id, watch.options())
	}
	if run.State == "waiting_approval" {
		s.printApprovalNeeded(run.ID, run.Label)
	} else {
		s.textf("Watch it: yumdrake runs watch %d\n%s\n", id, s.runLink(id))
	}
	if s.json() {
		_ = cli.WriteJSON(s.stdout, started)
	}
	return exitForState(run.State)
}

// printApprovalNeeded explains who must act. The server does not say who may
// approve, so the explanation names the rule.
func (s *session) printApprovalNeeded(id int64, label string) {
	s.progressf("Run %d is waiting for approval. Someone other than the person who started it, with the approve permission for %s environments, must approve it:\n  %s\nThey can run: yumdrake approvals approve %d\nThen follow the run with: yumdrake runs watch %d\n",
		id, orDash(label), s.runLink(id), id, id)
}

// watchFlags are the flags of every command that can follow a run.
type watchFlags struct {
	watch, logs, waitForApproval bool
}

func (w *watchFlags) register(c *commandFlags) {
	c.BoolVar(&w.watch, "watch", false, "follow the run until it ends")
	c.BoolVar(&w.logs, "logs", false, "with --watch, print every log line")
	c.BoolVar(&w.waitForApproval, "wait-for-approval", false, "with --watch, wait for an approval instead of exiting 5")
}

func (w watchFlags) options() watchOptions {
	return watchOptions{logs: w.logs, waitForApproval: w.waitForApproval}
}

func runsStart(args []string, stdout, stderr io.Writer, e env) int {
	flags := newAPIFlags("runs start", runsStartHelp)
	var watch watchFlags
	watch.register(flags.commandFlags)
	positional, code, done := flags.parseArgs(args, stdout, stderr)
	if done {
		return code
	}
	if code, ok := needArgs(flags.commandFlags, positional, stderr, 1, "runs start needs one RUN"); !ok {
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
	return s.startAndFollow(id, watch)
}

func runsWatch(args []string, stdout, stderr io.Writer, e env) int {
	flags := newAPIFlags("runs watch", runsWatchHelp)
	var watch watchFlags
	flags.BoolVar(&watch.logs, "logs", false, "print every log line")
	flags.BoolVar(&watch.waitForApproval, "wait-for-approval", false, "wait for an approval instead of exiting 5")
	positional, code, done := flags.parseArgs(args, stdout, stderr)
	if done {
		return code
	}
	if code, ok := needArgs(flags.commandFlags, positional, stderr, 1, "runs watch needs one RUN"); !ok {
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
	return s.watchRun(id, watch.options())
}

func runsCancel(args []string, stdout, stderr io.Writer, e env) int {
	flags := newAPIFlags("runs cancel", runsCancelHelp)
	var yes bool
	flags.BoolVar(&yes, "yes", false, "do not ask")
	positional, code, done := flags.parseArgs(args, stdout, stderr)
	if done {
		return code
	}
	if code, ok := needArgs(flags.commandFlags, positional, stderr, 1, "runs cancel needs one RUN"); !ok {
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
	if !yes {
		if !e.StdinIsTerminal {
			return fail(stderr, usageError("there is no terminal to ask on: pass --yes to cancel run %d", id))
		}
		ok, err := s.confirm(fmt.Sprintf("Cancel run %d?", id))
		if err != nil {
			return s.interrupted()
		}
		if !ok {
			s.progressf("Not cancelled.\n")
			return cli.ExitFailed
		}
	}
	var raw json.RawMessage
	if err := s.client.Do(s.ctx, http.MethodPost, fmt.Sprintf("/api/v1/runs/%d/cancel", id), struct{}{}, &raw); err != nil {
		return fail(stderr, err)
	}
	var run runDetail
	_ = json.Unmarshal(raw, &run)
	if s.json() {
		_ = cli.WriteJSON(stdout, raw)
		return exitOK
	}
	s.printf("Run %d: cancel requested (%s). Follow it: yumdrake runs watch %d\n", id, run.State, id)
	return exitOK
}
