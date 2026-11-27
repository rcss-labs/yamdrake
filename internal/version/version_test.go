package version

import "testing"

func TestStringIncludesVersionAndCommit(t *testing.T) {
	originalVersion, originalCommit := Version, Commit
	t.Cleanup(func() {
		Version, Commit = originalVersion, originalCommit
	})

	Version, Commit = "1.2.3", "abc1234"
	if got, want := String(), "1.2.3 (abc1234)"; got != want {
		t.Errorf("String() with set values = %q, want %q", got, want)
	}

	Version, Commit = "dev", "unknown"
	if got, want := String(), "dev (unknown)"; got != want {
		t.Errorf("String() with defaults = %q, want %q", got, want)
	}
}
