// Command orchestrator is the Yumdrake control-plane service.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/rcss-labs/yamdrake/internal/app"
	"github.com/rcss-labs/yamdrake/internal/config"
	"github.com/rcss-labs/yamdrake/internal/version"
)

const (
	serviceName     = app.OrchestratorServiceName
	httpAddrVar     = "YUMDRAKE_HTTP_ADDR"
	defaultHTTPAddr = ":8080"
)

func main() {
	ctx, stop := app.SignalContext(context.Background())
	code := run(ctx, os.Args, os.Getenv, os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run executes the subcommand named by args[1] (default: serve) and returns the exit code.
func run(ctx context.Context, args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	subcommand := ""
	if len(args) > 1 {
		subcommand = args[1]
	}
	switch subcommand {
	case "":
		return serve(ctx, getenv, stdout, stderr)
	case "version":
		_, _ = fmt.Fprintln(stdout, version.String())
		return app.ExitOK
	case "create-admin":
		return createAdmin(ctx, args[2:], getenv, stdin, stdout, stderr)
	case "unlock":
		return unlock(ctx, args[2:], getenv, stdout, stderr)
	case "healthcheck":
		return app.Healthcheck(ctx, getenv, httpAddrVar, defaultHTTPAddr, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "usage: %s [version|healthcheck|create-admin|unlock]\n\nWith no subcommand the service starts and is configured through YUMDRAKE_* environment variables.\n", serviceName)
		return app.ExitUsage
	}
}

func serve(ctx context.Context, getenv func(string) string, stdout, stderr io.Writer) int {
	cfg, err := config.LoadOrchestrator(getenv)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return app.ExitUsage
	}
	return app.Run(ctx, app.NewOrchestratorService(cfg), stdout)
}
