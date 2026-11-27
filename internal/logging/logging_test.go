package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestNewWritesJSONWithServiceAndVersion(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, slog.LevelInfo, "orchestrator").Info("hello")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("output is not JSON: %v: %q", err, buf.String())
	}
	if line["service"] != "orchestrator" {
		t.Errorf("service = %v", line["service"])
	}
	if v, _ := line["version"].(string); v == "" {
		t.Errorf("version missing: %v", line)
	}
	if line["msg"] != "hello" {
		t.Errorf("msg = %v", line["msg"])
	}
	for _, key := range []string{"time", "level"} {
		if _, ok := line[key]; !ok {
			t.Errorf("key %q missing", key)
		}
	}
}

func TestNewHonoursLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelInfo, "runner")
	logger.Debug("hidden")
	if strings.Contains(buf.String(), "hidden") {
		t.Errorf("debug line written at info level: %q", buf.String())
	}
	logger.Info("shown")
	if !strings.Contains(buf.String(), "shown") {
		t.Errorf("info line missing: %q", buf.String())
	}
}
