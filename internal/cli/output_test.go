package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestWriteJSONIsOneDocument(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(&buf)
	var v map[string]any
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	if dec.More() {
		t.Error("more than one document")
	}
}

func TestParseFormat(t *testing.T) {
	for in, want := range map[string]Format{"": FormatText, "text": FormatText, "json": FormatJSON} {
		got, err := ParseFormat(in)
		if err != nil || got != want {
			t.Errorf("ParseFormat(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseFormat("yaml"); ExitCodeFor(err) != 2 {
		t.Errorf("yaml accepted: %v", err)
	}
}

func TestColorOnlyOnTerminalWithoutNoColor(t *testing.T) {
	var buf bytes.Buffer
	if ColorEnabled(&buf, func(string) string { return "" }) {
		t.Error("colour on a buffer")
	}
}

func TestTableAlignsColumns(t *testing.T) {
	var buf bytes.Buffer
	table := NewTable(&buf)
	table.Row("ID", "TITLE")
	table.Row("a", "Deploy")
	if err := table.Flush(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 || strings.Index(lines[0], "TITLE") != strings.Index(lines[1], "Deploy") {
		t.Errorf("not aligned: %q", lines)
	}
}
