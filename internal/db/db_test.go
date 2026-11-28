package db

import "testing"

func TestLatestVersionMatchesEmbeddedFiles(t *testing.T) {
	if got := LatestVersion(); got != 7 {
		t.Fatalf("LatestVersion() = %d, want 7", got)
	}
}
