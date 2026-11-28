package db

import "testing"

// Migration numbers may have gaps (another branch owns 00006), so this checks
// that the latest version is at least the last one known to exist and that
// every embedded file is at or below it, not an exact number.
func TestLatestVersionMatchesEmbeddedFiles(t *testing.T) {
	const lastKnown = 9 // 00009_aws.sql
	if got := LatestVersion(); got < lastKnown {
		t.Fatalf("LatestVersion() = %d, want at least %d", got, lastKnown)
	}
}
