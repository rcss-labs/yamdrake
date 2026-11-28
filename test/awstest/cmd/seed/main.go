// Command seed seeds the AWS world of the browser test and the demo into a
// Floci and prints what it created as JSON on standard output (see
// awstest.Stack). It is idempotent: run again with the same flags it finds the
// world and changes nothing.
//
// With --repair it moves the crashing version of api to the good image instead,
// which is what the browser test does between a failed run and its re-run.
//
// It is a development tool, not part of any product image.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/rcss-labs/yamdrake/test/awstest"
)

const usage = `usage: seed [--endpoint URL] [--suffix S] [--dev-account ID] [--prod-account ID] [--platform-account ID] [--repair]

Seeds a Floci with two environments (dev-1, prod-1), three ECS services each
running 1.4.1, images for 1.4.2, a crashing 1.4.3 of api, and config buckets.
Prints the seeded names as JSON. Accounts and suffix default to random values.
--repair needs all of --suffix, --dev-account, --prod-account.

The endpoint defaults to $YUMDRAKE_TEST_AWS_ENDPOINT.
Exit codes: 0 done, 1 failure, 2 usage.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("seed", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() { _, _ = io.WriteString(stderr, usage) }
	endpoint := flags.String("endpoint", os.Getenv(awstest.EndpointVariable), "Floci URL")
	var opts awstest.StackOptions
	flags.StringVar(&opts.Suffix, "suffix", "", "part of every resource name")
	flags.StringVar(&opts.DevAccount, "dev-account", "", "12-digit account id of dev-1")
	flags.StringVar(&opts.ProdAccount, "prod-account", "", "12-digit account id of prod-1")
	flags.StringVar(&opts.PlatformAccount, "platform-account", "", "12-digit account id that holds the run artifacts")
	repair := flags.Bool("repair", false, "move the crashing version of api to the good image")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.Usage()
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "seed: %v\n\n%s", err, usage)
		return 2
	}
	if flags.NArg() > 0 || *endpoint == "" {
		_, _ = io.WriteString(stderr, "seed: an endpoint is required and no arguments are taken\n\n"+usage)
		return 2
	}
	if *repair && (opts.Suffix == "" || opts.DevAccount == "" || opts.ProdAccount == "") {
		_, _ = io.WriteString(stderr, "seed: --repair needs --suffix, --dev-account and --prod-account\n\n"+usage)
		return 2
	}

	program := &awstest.Program{}
	defer program.Close()
	var seeded *awstest.Stack
	err := program.Run(func() {
		world := awstest.NewWorld(*endpoint, program)
		if *repair {
			awstest.RepairCrashingVersion(world, opts)
			return
		}
		seeded = awstest.SeedStack(world, opts)
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "seed: %v\n", err)
		return 1
	}
	if seeded != nil {
		if err := json.NewEncoder(stdout).Encode(seeded); err != nil {
			_, _ = fmt.Fprintf(stderr, "seed: %v\n", err)
			return 1
		}
	}
	return 0
}
