package db

import "testing"

func TestLatestVersionMatchesEmbeddedFiles(t *testing.T) {
	if got := LatestVersion(); got != 6 {
		t.Fatalf("LatestVersion() = %d, want 6", got)
	}
}
