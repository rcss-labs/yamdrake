package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/rcss-labs/yamdrake/internal/cli"
)

const approvalsUsage = `usage: yumdrake approvals <command>

Commands:
  list
  approve RUN [--comment TEXT] [--yes]
  reject RUN [--comment TEXT]

Example:
  yumdrake approvals list
`

const approvalsListHelp = `usage: yumdrake approvals list [-o json]

List the runs that wait for approval, who asked, when the request expires, and
whether you may approve each one.

Example:
  yumdrake approvals list
`

func approvalsCmd(args []string, stdout, stderr io.Writer, e env) int {
	return approvalsCommands().dispatch("approvals", approvalsUsage, args, stdout, stderr, e)
}

func approvalsCommands() subcommands {
	return subcommands{"list": approvalsList, "approve": approvalsApprove, "reject": approvalsReject}
}

func approvalsList(args []string, stdout, stderr io.Writer, e env) int {
	flags := newAPIFlags("approvals list", approvalsListHelp)
	positional, code, done := flags.parseArgs(args, stdout, stderr)
	if done {
		return code
	}
	if code, ok := needArgs(flags.commandFlags, positional, stderr, 0, "approvals list takes no arguments"); !ok {
		return code
	}
	s, err := e.openSession(flags, stdout, stderr)
	if err != nil {
		return fail(stderr, err)
	}
	var raw json.RawMessage
	if err := s.client.Do(s.ctx, http.MethodGet, "/api/v1/approvals", nil, &raw); err != nil {
		return fail(stderr, unsupportedOn404(err, "approvals"))
	}
	if s.json() {
		_ = cli.WriteJSON(stdout, raw)
		return exitOK
	}
	var out struct {
		Approvals []approvalItem `json:"approvals"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fail(stderr, &cli.ExitError{Code: cli.ExitServer, Message: "the server's answer was not understood"})
	}
	if len(out.Approvals) == 0 {
		s.printf("No runs are waiting for approval.\n")
		return exitOK
	}
	table := cli.NewTable(stdout)
	table.Row("RUN", "JOB", "ENV", "ASKED BY", "EXPIRES", "YOU CAN APPROVE")
	for _, a := range out.Approvals {
		can := "yes"
		if !a.CanApprove {
			can = "no: " + orDash(a.Reason)
		}
		table.Row(itoa(a.RunID), a.JobID, a.Environment, orDash(a.RequestedByName), formatTimePtr(a.ExpiresAt), can)
	}
	_ = table.Flush()
	return exitOK
}

const approvalsApproveHelp = `usage: yumdrake approvals approve RUN [--comment TEXT] [--yes] [-o json]

Show the plan of a run that waits for approval, ask, and approve exactly that
plan: the server refuses with "The plan changed" if the run was planned again
since you looked. Without a terminal --yes is required.

Example:
  yumdrake approvals approve 20 --comment "checked the config changes"
`

const approvalsRejectHelp = `usage: yumdrake approvals reject RUN [--comment TEXT] [-o json]

Reject a run that waits for approval.

Example:
  yumdrake approvals reject 20 --comment "not before the release freeze ends"
`

func approvalsApprove(args []string, stdout, stderr io.Writer, e env) int {
	flags := newAPIFlags("approvals approve", approvalsApproveHelp)
	var comment string
	var yes bool
	flags.StringVar(&comment, "comment", "", "a note recorded with the decision")
	flags.BoolVar(&yes, "yes", false, "do not ask")
	positional, code, done := flags.parseArgs(args, stdout, stderr)
	if done {
		return code
	}
	if code, ok := needArgs(flags.commandFlags, positional, stderr, 1, "approvals approve needs one RUN"); !ok {
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
	_, run, err := s.getRun(id)
	if err != nil {
		return fail(stderr, err)
	}
	if run.State != "waiting_approval" {
		return fail(stderr, usageError("run %d is %s, not waiting for approval", id, run.State))
	}
	p := &planned{run: run}
	if err := s.client.Do(s.ctx, http.MethodGet, fmt.Sprintf("/api/v1/runs/%d/plan", id), nil, &p.planRaw); err != nil {
		return fail(stderr, err)
	}
	p.plan = &manifest{}
	if err := json.Unmarshal(p.planRaw, p.plan); err != nil {
		return fail(stderr, &cli.ExitError{Code: cli.ExitServer, Message: "the server's plan was not understood"})
	}
	s.printPlan(p)
	s.textf("Plan id: %s\n", p.plan.ID)
	if !yes {
		if !e.StdinIsTerminal {
			return fail(stderr, usageError("there is no terminal to ask on: review the plan above, then pass --yes to approve it"))
		}
		if !s.confirm("Approve this plan?") {
			s.progressf("Not approved.\n")
			return cli.ExitFailed
		}
	}
	body := map[string]string{"manifest_id": p.plan.ID}
	if comment != "" {
		body["comment"] = comment
	}
	var raw json.RawMessage
	if err := s.client.Do(s.ctx, http.MethodPost, fmt.Sprintf("/api/v1/runs/%d/approve", id), body, &raw); err != nil {
		var apiErr *cli.ExitError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict {
			s.progressf("Nothing was approved. Review the run again with: yumdrake approvals approve %d\n", id)
		}
		return fail(stderr, err)
	}
	if s.json() {
		_ = cli.WriteJSON(stdout, raw)
		return exitOK
	}
	s.printf("Approved run %d. Follow it: yumdrake runs watch %d\n", id, id)
	return exitOK
}

func approvalsReject(args []string, stdout, stderr io.Writer, e env) int {
	flags := newAPIFlags("approvals reject", approvalsRejectHelp)
	var comment string
	flags.StringVar(&comment, "comment", "", "a note recorded with the decision")
	positional, code, done := flags.parseArgs(args, stdout, stderr)
	if done {
		return code
	}
	if code, ok := needArgs(flags.commandFlags, positional, stderr, 1, "approvals reject needs one RUN"); !ok {
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
	body := map[string]string{}
	if comment != "" {
		body["comment"] = comment
	}
	var raw json.RawMessage
	if err := s.client.Do(s.ctx, http.MethodPost, fmt.Sprintf("/api/v1/runs/%d/reject", id), body, &raw); err != nil {
		return fail(stderr, err)
	}
	if s.json() {
		_ = cli.WriteJSON(stdout, raw)
		return exitOK
	}
	s.printf("Rejected run %d.\n", id)
	return exitOK
}
