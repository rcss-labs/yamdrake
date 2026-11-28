package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/rcss-labs/yumdrake/internal/cli"
)

const runsUsage = `usage: yumdrake runs <command>

Commands:
  list [--job JOB] [--env ENV] [--state STATE]... [--limit N]
  show RUN
  log RUN [--step S] [--service S] [--level L] [--grep TEXT]
  start RUN [--watch]
  watch RUN [--logs] [--wait-for-approval]
  cancel RUN [--yes]
  rerun-failed RUN [--yes] [--watch]
  reconcile RUN --leg ID --outcome succeeded|failed [--yes]
  check-again RUN --leg ID

Example:
  yumdrake runs list --state failed
`

const runsListHelp = `usage: yumdrake runs list [--job JOB] [--env ENV] [--state STATE]... [--limit N] [-o json]

List runs, newest first. --state may be repeated. --limit defaults to 20.

Example:
  yumdrake runs list --job roll-backends --state failed --state cancelled
`

const runsShowHelp = `usage: yumdrake runs show RUN [-o json]

Show a run: its state and why, its steps and services, and every failure with
its full reason.

Example:
  yumdrake runs show 12
`

const runsLogHelp = `usage: yumdrake runs log RUN [--step S] [--service S] [--level L] [--grep TEXT] [-o json]

Print the stored log of a run, through all pages. Lines the server lost are
reported on standard error. --level is one of info, ok, warn, error.

Example:
  yumdrake runs log 12 --service billing --level error
`

// runStates are the states the server knows, for checking --state early.
var runStates = []string{"resolving", "planned", "waiting_approval", "queued", "running", "cancelling", "needs_reconciliation",
	"succeeded", "partially_succeeded", "failed", "cancelled", "rejected", "expired", "invalidated"}

func runsCmd(args []string, stdout, stderr io.Writer, e env) int {
	return runsCommands().dispatch("runs", runsUsage, args, stdout, stderr, e)
}

func runsCommands() subcommands {
	return subcommands{"list": runsList, "show": runsShow, "log": runsLog, "start": runsStart, "watch": runsWatch, "cancel": runsCancel,
		"rerun-failed": runsRerunFailed, "reconcile": runsReconcile, "check-again": runsCheckAgain}
}

const maxRunsPerPage = 100

func runsList(args []string, stdout, stderr io.Writer, e env) int {
	flags := newAPIFlags("runs list", runsListHelp)
	var job, envName string
	var states stringList
	limit := 20
	flags.StringVar(&job, "job", "", "only runs of this job")
	flags.StringVar(&envName, "env", "", "only runs in this environment")
	flags.Var(&states, "state", "only runs in this state (repeatable)")
	flags.IntVar(&limit, "limit", 20, "how many runs to list")
	positional, code, done := flags.parseArgs(args, stdout, stderr)
	if done {
		return code
	}
	if code, ok := needArgs(flags.commandFlags, positional, stderr, 0, "runs list takes no arguments"); !ok {
		return code
	}
	if limit < 1 {
		_, _ = fmt.Fprintf(stderr, "yumdrake: --limit must be at least 1\n\n%s", runsListHelp)
		return exitUsage
	}
	for _, st := range states {
		if !containsString(runStates, st) {
			return fail(stderr, usageError("unknown state %q. States: %s", st, listNames(runStates)))
		}
	}
	s, err := e.openSession(flags, stdout, stderr)
	if err != nil {
		return fail(stderr, err)
	}
	query := url.Values{}
	if job != "" {
		found, err := s.findJob(job)
		if err != nil {
			return fail(stderr, err)
		}
		query.Set("job", strconv.FormatInt(found.ID, 10))
	}
	if envName != "" {
		query.Set("environment", envName)
	}
	for _, st := range states {
		query.Add("state", st)
	}
	var all []json.RawMessage
	var typed []runSummary
	before := ""
	for len(all) < limit {
		page := query
		page.Set("limit", strconv.Itoa(min(limit-len(all), maxRunsPerPage)))
		if before != "" {
			page.Set("before", before)
		}
		var out struct {
			Runs       []json.RawMessage `json:"runs"`
			NextBefore *int64            `json:"next_before"`
		}
		if err := s.client.Do(s.ctx, http.MethodGet, "/api/v1/runs?"+page.Encode(), nil, &out); err != nil {
			return fail(stderr, err)
		}
		for _, raw := range out.Runs {
			var r runSummary
			if err := json.Unmarshal(raw, &r); err != nil {
				return fail(stderr, &cli.ExitError{Code: cli.ExitServer, Message: "the server's answer was not understood"})
			}
			all, typed = append(all, raw), append(typed, r)
		}
		if out.NextBefore == nil || len(out.Runs) == 0 {
			break
		}
		before = strconv.FormatInt(*out.NextBefore, 10)
	}
	if all == nil {
		all = []json.RawMessage{}
	}
	if s.json() {
		_ = cli.WriteJSON(stdout, map[string]any{"runs": all})
		return exitOK
	}
	if len(typed) == 0 {
		s.printf("No runs.\n")
		return exitOK
	}
	table := cli.NewTable(stdout)
	table.Row("RUN", "JOB", "ENV", "STATE", "KIND", "VIA", "CREATED")
	for _, r := range typed {
		table.Row(strconv.FormatInt(r.ID, 10), r.JobID, r.Environment, r.State, r.Kind, r.Via, formatTime(r.CreatedAt))
	}
	_ = table.Flush()
	return exitOK
}

// getRun fetches one run, as raw JSON and decoded.
func (s *session) getRun(id int64) (json.RawMessage, runDetail, error) {
	var raw json.RawMessage
	if err := s.client.Do(s.ctx, http.MethodGet, fmt.Sprintf("/api/v1/runs/%d", id), nil, &raw); err != nil {
		return nil, runDetail{}, err
	}
	var detail runDetail
	if err := json.Unmarshal(raw, &detail); err != nil {
		return nil, runDetail{}, &cli.ExitError{Code: cli.ExitServer, Message: "the server's answer was not understood"}
	}
	return raw, detail, nil
}

func runsShow(args []string, stdout, stderr io.Writer, e env) int {
	flags := newAPIFlags("runs show", runsShowHelp)
	positional, code, done := flags.parseArgs(args, stdout, stderr)
	if done {
		return code
	}
	if code, ok := needArgs(flags.commandFlags, positional, stderr, 1, "runs show needs one RUN"); !ok {
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
	raw, detail, err := s.getRun(id)
	if err != nil {
		return fail(stderr, err)
	}
	if s.json() {
		_ = cli.WriteJSON(stdout, raw)
		return exitOK
	}
	s.printRun(detail)
	return exitOK
}

// printRun describes a run for a person.
func (s *session) printRun(d runDetail) {
	s.printf("Run %d  %s\n", d.ID, d.State)
	if d.WaitReason != "" {
		s.printf("Waiting: %s\n", d.WaitReason)
	}
	if d.EndReason != "" {
		s.printf("Reason: %s\n", d.EndReason)
	}
	s.printf("Job: %s (%s run)\n", d.JobID, d.Kind)
	s.printf("Environment: %s (%s)\n", d.Environment, d.Label)
	s.printf("Started by: user %d via %s\n", d.RequestedBy, orDash(d.Via))
	s.printf("Created: %s   Started: %s   Finished: %s\n", formatTime(d.CreatedAt), formatTimePtr(d.StartedAt), formatTimePtr(d.FinishedAt))
	if d.CancelRequested {
		s.printf("Cancel requested.\n")
	}
	if a := d.Approval; a != nil {
		line := "Approval: " + orDash(a.Decision)
		if a.Decision == "" {
			line = "Approval: open"
		}
		if a.ExpiresAt != nil {
			line += ", expires " + formatTimePtr(a.ExpiresAt)
		}
		if a.Comment != "" {
			line += fmt.Sprintf(", comment: %q", a.Comment)
		}
		s.printf("%s\n", line)
	}
	s.printf("\nSteps and services:\n")
	table := cli.NewTable(s.stdout)
	table.Row("  STEP", "SERVICE", "VERSION", "STATE", "TRY")
	for _, l := range d.Legs {
		step, service := l.Step, l.Service
		if l.Kind == "resolve" {
			step, service = "(plan)", "-"
		}
		table.Row("  "+orDash(step), orDash(service), orDash(l.Version), l.State, strconv.Itoa(l.Attempt))
	}
	_ = table.Flush()
	if len(d.Summary.Failures) > 0 {
		s.printf("\nFailures:\n")
		for _, f := range d.Summary.Failures {
			s.printf("  %s / %s: %s: %s\n", orDash(f.Step), orDash(f.Service), f.State, orDash(f.Short))
			if f.Detail != "" {
				for _, line := range strings.Split(strings.TrimRight(f.Detail, "\n"), "\n") {
					s.printf("    %s\n", line)
				}
			}
		}
	}
}

func runsLog(args []string, stdout, stderr io.Writer, e env) int {
	flags := newAPIFlags("runs log", runsLogHelp)
	var step, service, level, grep string
	flags.StringVar(&step, "step", "", "only this step")
	flags.StringVar(&service, "service", "", "only this service")
	flags.StringVar(&level, "level", "", "only this level: info, ok, warn or error")
	flags.StringVar(&grep, "grep", "", "only lines containing this text")
	positional, code, done := flags.parseArgs(args, stdout, stderr)
	if done {
		return code
	}
	if code, ok := needArgs(flags.commandFlags, positional, stderr, 1, "runs log needs one RUN"); !ok {
		return code
	}
	id, err := parseRunID(positional[0])
	if err != nil {
		return fail(stderr, err)
	}
	if level != "" && !containsString([]string{"info", "ok", "warn", "error"}, level) {
		return fail(stderr, usageError("unknown level %q. Levels: error, info, ok, warn", level))
	}
	s, err := e.openSession(flags, stdout, stderr)
	if err != nil {
		return fail(stderr, err)
	}
	_, detail, err := s.getRun(id)
	if err != nil {
		return fail(stderr, err)
	}
	names := map[int64]string{}
	for _, l := range detail.Legs {
		if l.Kind == "resolve" {
			names[l.ID] = "plan"
		} else {
			names[l.ID] = l.Step + "/" + l.Service
		}
	}
	legName := func(leg int64) string {
		if n, ok := names[leg]; ok {
			return n
		}
		return fmt.Sprintf("leg %d", leg)
	}

	query := url.Values{"limit": {"1000"}}
	for key, val := range map[string]string{"step": step, "service": service, "level": level, "q": grep} {
		if val != "" {
			query.Set(key, val)
		}
	}
	collected := logPage{Lines: []logLine{}, Gaps: []logGap{}}
	after := ""
	for {
		page := query
		if after != "" {
			page.Set("after", after)
		}
		var out logPage
		if err := s.client.Do(s.ctx, http.MethodGet, fmt.Sprintf("/api/v1/runs/%d/log?%s", id, page.Encode()), nil, &out); err != nil {
			return fail(stderr, err)
		}
		for _, gap := range out.Gaps {
			s.progressf("yumdrake: the log of %s is missing lines %d to %d\n", legName(gap.LegID), gap.FromSeq, gap.ToSeq)
		}
		collected.Gaps = append(collected.Gaps, out.Gaps...)
		collected.Lines = append(collected.Lines, out.Lines...)
		if !s.json() {
			for _, l := range out.Lines {
				s.printf("%s  %-24s %-5s %s\n", l.At.UTC().Format("15:04:05"), legName(l.LegID), l.Level, l.Text)
			}
		}
		if len(out.Lines) == 0 || out.Next == "" || out.Next == after {
			break
		}
		after = out.Next
	}
	if s.json() {
		_ = cli.WriteJSON(stdout, collected)
	}
	return exitOK
}
