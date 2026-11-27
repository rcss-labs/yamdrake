package db

import "testing"

func TestLatestVersionMatchesEmbeddedFiles(t *testing.T) {
	if got := LatestVersion(); got != 4 {
		t.Fatalf("LatestVersion() = %d, want 4", got)
	}
}
