package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// An API that encodes entries must emit the detail as JSON, not as base64.
func TestAuditEntryDetailEncodesAsJSON(t *testing.T) {
	t.Parallel()
	out, err := json.Marshal(AuditEntry{Action: "a", Detail: json.RawMessage(`{"n":1}`)})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(out), `"Detail":{"n":1}`) {
		t.Fatalf("encoded entry = %s, want the detail embedded as JSON", out)
	}
}
