package db

import "testing"

func TestLatestVersionMatchesEmbeddedFiles(t *testing.T) {
	if got := LatestVersion(); got != 2 {
		t.Fatalf("LatestVersion() = %d, want 2", got)
	}
}
