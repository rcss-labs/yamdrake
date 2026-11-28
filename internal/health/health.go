// Package health provides the /healthz and /readyz HTTP handlers and the
// client-side probe used by the Docker HEALTHCHECK.
package health

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// checkTimeout bounds each readiness check.
const checkTimeout = 2 * time.Second

// Check is a named readiness check.
type Check struct {
	Name string
	Fn   func(context.Context) error
}

type detail struct {
	name string
	fn   func(context.Context) (any, error)
}

type options struct {
	logger  *slog.Logger
	details []detail
}

// WithDetail adds a top-level field to the /readyz response, filled by fn on
// each request (for numbers an operator wants to see, such as how many
// notifications are waiting). It never affects readiness: when fn fails the
// field is left out and the failure is logged.
func WithDetail(name string, fn func(context.Context) (any, error)) Option {
	return func(o *options) { o.details = append(o.details, detail{name: name, fn: fn}) }
}

// Option configures Handler.
type Option func(*options)

// WithLogger makes Handler log failing checks (name and error) at warn level.
func WithLogger(logger *slog.Logger) Option {
	return func(o *options) {
		if logger != nil {
			o.logger = logger
		}
	}
}

type checkResult struct {
	name string
	err  error
}

// Handler serves GET /healthz (liveness, runs no checks) and GET /readyz
// (runs every check concurrently, each with its own 2s timeout).
func Handler(checks []Check, opts ...Option) http.Handler {
	cfg := options{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, opt := range opts {
		opt(&cfg)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		results := runChecks(r.Context(), checks, cfg.logger)
		status, code := "ok", http.StatusOK
		for _, v := range results {
			if v != "ok" {
				status, code = "unavailable", http.StatusServiceUnavailable
				break
			}
		}
		body := map[string]any{"status": status, "checks": results}
		for _, d := range cfg.details {
			detailCtx, cancel := context.WithTimeout(r.Context(), checkTimeout)
			value, err := d.fn(detailCtx)
			cancel()
			if err != nil {
				cfg.logger.Warn("readiness detail unavailable", "detail", d.name, "error", err)
				continue
			}
			body[d.name] = value
		}
		writeJSON(w, code, body)
	})
	return mux
}

// runChecks runs all checks concurrently and returns name -> "ok"|"failed".
// A check that ignores its context cannot hold the result past the timeout:
// any check without a result by the deadline is reported as failed.
func runChecks(ctx context.Context, checks []Check, logger *slog.Logger) map[string]string {
	results := make(map[string]string, len(checks))
	if len(checks) == 0 {
		return results
	}

	deadlineCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	// Buffered so goroutines never block after the collector has given up.
	done := make(chan checkResult, len(checks))
	finished := make(map[string]bool, len(checks))
	for _, c := range checks {
		results[c.Name] = "failed"
		go func() {
			checkCtx, cancelCheck := context.WithTimeout(ctx, checkTimeout)
			defer cancelCheck()
			done <- checkResult{name: c.Name, err: c.Fn(checkCtx)}
		}()
	}

	for range checks {
		select {
		case res := <-done:
			finished[res.name] = true
			if res.err != nil {
				logger.Warn("readiness check failed", "check", res.name, "error", res.err)
			} else {
				results[res.name] = "ok"
			}
		case <-deadlineCtx.Done():
			for name, v := range results {
				if v == "failed" && !finished[name] {
					logger.Warn("readiness check did not finish in time", "check", name)
				}
			}
			return results
		}
	}
	return results
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

// Probe performs a GET against url and returns nil only for status 200.
// The request lifetime is bounded by ctx alone.
func Probe(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("health probe: build request: %w", err)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("health probe: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health probe: unexpected status %s", resp.Status)
	}
	return nil
}
