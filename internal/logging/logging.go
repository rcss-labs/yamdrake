// Package logging builds the structured JSON logger used by the services.
package logging

import (
	"io"
	"log/slog"

	"github.com/rcss-labs/yamdrake/internal/version"
)

// New returns a JSON logger that tags every line with the service name and build version.
func New(w io.Writer, level slog.Level, service string) *slog.Logger {
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(handler).With("service", service, "version", version.String())
}
