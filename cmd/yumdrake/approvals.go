package main

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/rcss-labs/yamdrake/internal/cli"
)

const approvalsUsage = `usage: yumdrake approvals <command>

Commands:
  list

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
	return subcommands{"list": approvalsList}
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
