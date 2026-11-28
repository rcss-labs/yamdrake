// Command yumdrake is the offline command line tool: it validates inventory
// and job YAML without a database or network.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/rcss-labs/yamdrake/internal/spec"
	"github.com/rcss-labs/yamdrake/internal/version"
)

// Exit codes: 0 valid, 1 problems found, 2 usage error.
const (
	exitOK      = 0
	exitProblem = 1
	exitUsage   = 2
	// exitInterrupted is the usual 128+SIGINT.
	exitInterrupted = 130
)

const usageText = `usage: yumdrake <command>

Commands:
  validate <inventory-dir> [--job <file>]...
      Check environments.yaml, services/*.yaml and any job files.
      Prints problems as file:line: message on stderr and a summary on stdout.
      Exit 0 when valid, 1 when problems are found, 2 on a usage error.
  login [--server URL] [--no-browser] [--with-token]
      Sign in through the browser and save the token (mode 0600).
  logout
      Revoke the token on the server and remove it from the config file.
  whoami [-o json]
      Show who you are signed in as and what you may do.
  jobs list [--folder NAME] | jobs show JOB
      List jobs, or show a job's groups, steps and problems.
  versions JOB --env ENV [--group G | --service S] | versions running [--service S] [--env E]
      List image versions for a job, or what is deployed now.
  plan JOB --env ENV --set GROUP=VERSION... [--only NAME,...] [--latest]
      Create a planned run and print its plan (exit 0, or 1 when a check failed).
  run JOB --env ENV --set GROUP=VERSION... [--yes] [--watch] [--logs] [--wait-for-approval]
      Plan, ask, start and optionally follow a run.
  runs list | show RUN | log RUN | start RUN | watch RUN | cancel RUN
      List, inspect, start, follow and cancel runs.
  runs rerun-failed RUN | reconcile RUN --leg ID --outcome succeeded|failed | check-again RUN --leg ID
      Re-run what failed, or settle a service whose outcome is not known.
  restore RUN [--service NAME]... [--yes] [--watch]
      Put the services a run changed back as they were.
  approvals list | approve RUN | reject RUN
      List runs waiting for approval, approve the plan you saw, or reject it.
  version
      Print the version.

Environment: YUMDRAKE_SERVER, YUMDRAKE_TOKEN, NO_COLOR.
Exit codes: 0 ok (a run succeeded), 1 failed, 2 usage or configuration error,
3 partially succeeded, 4 cancelled, rejected, expired or invalidated, 5 the run
needs a person (approval, reconciliation, or planned and not started), 6 not
signed in or not permitted, 7 server unreachable or failing.
Every command takes -h for help with an example, and -o json for scripts.

Use -h to show this text.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the command in args (without the program name) and returns the exit code.
func run(args []string, stdout, stderr io.Writer) int {
	return runEnv(args, stdout, stderr, defaultEnv(stdout))
}

// runEnv is run with the machine-dependent parts supplied, so tests can replace them.
func runEnv(args []string, stdout, stderr io.Writer, e env) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usageText)
		return exitUsage
	}
	switch args[0] {
	case "-h", "--help", "help":
		_, _ = fmt.Fprint(stdout, usageText)
		return exitOK
	case "version":
		_, _ = fmt.Fprintln(stdout, version.String())
		return exitOK
	case "validate":
		return validate(args[1:], stdout, stderr)
	case "login":
		return login(args[1:], stdout, stderr, e)
	case "logout":
		return logout(args[1:], stdout, stderr, e)
	case "whoami":
		return whoami(args[1:], stdout, stderr, e)
	case "plan":
		return planCmd(args[1:], stdout, stderr, e)
	case "run":
		return runCmd(args[1:], stdout, stderr, e)
	case "restore":
		return restoreCmd(args[1:], stdout, stderr, e)
	case "jobs":
		return jobsCmd(args[1:], stdout, stderr, e)
	case "versions":
		return versionsCmd(args[1:], stdout, stderr, e)
	case "runs":
		return runsCmd(args[1:], stdout, stderr, e)
	case "approvals":
		return approvalsCmd(args[1:], stdout, stderr, e)
	default:
		_, _ = fmt.Fprintf(stderr, "yumdrake: unknown command %q\n\n%s", args[0], usageText)
		return exitUsage
	}
}

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

func validate(args []string, stdout, stderr io.Writer) int {
	var jobs stringList
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Var(&jobs, "job", "job file to validate (repeatable)")

	// The directory may come before or after --job, so parse again after each positional.
	var positional []string
	for {
		if err := flags.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				_, _ = fmt.Fprint(stdout, usageText)
				return exitOK
			}
			_, _ = fmt.Fprintf(stderr, "yumdrake: %v\n\n%s", err, usageText)
			return exitUsage
		}
		if flags.NArg() == 0 {
			break
		}
		positional = append(positional, flags.Arg(0))
		args = flags.Args()[1:]
	}
	if len(positional) != 1 {
		_, _ = fmt.Fprintf(stderr, "yumdrake: validate needs exactly one inventory directory\n\n%s", usageText)
		return exitUsage
	}

	inv, loadedJobs, problems := spec.LoadDir(positional[0], jobs)
	for _, problem := range problems {
		_, _ = fmt.Fprintln(stderr, spec.Problems{problem}.Error())
	}
	if len(problems) > 0 {
		_, _ = fmt.Fprintf(stdout, "FAILED: %d problems\n", len(problems))
		return exitProblem
	}
	_, _ = fmt.Fprintf(stdout, "OK: %d environments, %d services, %d jobs\n", len(inv.Environments), len(inv.Services), len(loadedJobs))
	return exitOK
}
