package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copyValidInventory copies the valid fixtures into a temp directory and
// returns it, so a test can break one file.
func copyValidInventory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "services"), 0o750); err != nil {
		t.Fatal(err)
	}
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), readFixture(t, "valid/"+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("environments.yaml")
	write("services/backend-api.yaml")
	write("services/orders.yaml")
	return dir
}

func TestLoadDirSyntaxErrorGivesOnlyProblemsWithAFile(t *testing.T) {
	dir := copyValidInventory(t)
	broken := filepath.Join(dir, "services", "broken.yaml")
	if err := os.WriteFile(broken, []byte("name: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, problems := LoadDir(dir, nil)
	if len(problems) != 1 {
		t.Fatalf("problems = %v, want exactly the syntax error", problems)
	}
	if problems[0].File != broken {
		t.Errorf("problem file = %q, want %q (%v)", problems[0].File, broken, problems[0])
	}
}

func TestLoadDirReportsYmlServiceFiles(t *testing.T) {
	dir := copyValidInventory(t)
	renamed := filepath.Join(dir, "services", "payments.yml")
	if err := os.WriteFile(renamed, readFixture(t, "valid/services/orders.yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, problems := LoadDir(dir, nil)
	if len(problems) != 1 || problems[0].File != renamed || !strings.Contains(problems[0].Message, ".yaml extension") {
		t.Fatalf("problems = %v, want one problem naming %s about the .yaml extension", problems, renamed)
	}
}
