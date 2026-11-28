package db

import "testing"

func TestLatestVersionMatchesEmbeddedFiles(t *testing.T) {
	if got := LatestVersion(); got != 5 {
		t.Fatalf("LatestVersion() = %d, want 5", got)
	}
}
