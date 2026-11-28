package db

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestLatestVersionMatchesEmbeddedFiles(t *testing.T) {
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	var highest int64
	for _, e := range entries {
		prefix, _, _ := strings.Cut(e.Name(), "_")
		n, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			t.Fatalf("migration %q does not start with a number", e.Name())
		}
		highest = max(highest, n)
	}
	if got := LatestVersion(); got != highest {
		t.Fatalf("LatestVersion() = %d, want %d (the highest numbered file in migrations/)", got, highest)
	}
}
