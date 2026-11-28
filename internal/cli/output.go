package cli

import (
	"encoding/json"
	"io"
	"os"
	"text/tabwriter"
)

// Format is how a command prints its result.
type Format string

// The output formats of -o.
const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

// ParseFormat reads the value of -o; empty means text.
func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case "", FormatText:
		return FormatText, nil
	case FormatJSON:
		return FormatJSON, nil
	}
	return "", &ExitError{Code: ExitUsage, Message: "-o must be text or json"}
}

// WriteJSON prints v as exactly one JSON document.
func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// IsTerminal reports whether w is a terminal.
func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// ColorEnabled is true only when w is a terminal and NO_COLOR is unset.
func ColorEnabled(w io.Writer, getenv func(string) string) bool {
	return IsTerminal(w) && getenv("NO_COLOR") == ""
}

// Table prints aligned columns.
type Table struct{ w *tabwriter.Writer }

// NewTable makes a table that writes to w.
func NewTable(w io.Writer) *Table {
	return &Table{w: tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)}
}

// Row adds one row.
func (t *Table) Row(columns ...string) {
	for i, c := range columns {
		if i > 0 {
			_, _ = io.WriteString(t.w, "\t")
		}
		_, _ = io.WriteString(t.w, c)
	}
	_, _ = io.WriteString(t.w, "\n")
}

// Flush writes the table out.
func (t *Table) Flush() error { return t.w.Flush() }
