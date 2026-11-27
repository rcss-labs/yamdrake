package db

import "testing"

func TestLatestVersionMatchesEmbeddedFiles(t *testing.T) {
	if got := LatestVersion(); got != 3 {
		t.Fatalf("LatestVersion() = %d, want 3", got)
	}
}
