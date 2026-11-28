package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/rcss-labs/yumdrake/internal/version"
)

const validTree = "../../internal/spec/testdata/valid"

func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestValidateValidTreeExits0(t *testing.T) {
	code, stdout, stderr := runCLI(t, "validate", validTree, "--job", validTree+"/jobs/deploy-backend.yaml")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr)
	}
	if got, want := strings.TrimSpace(stdout), "OK: 5 environments, 2 services, 1 jobs"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestValidateInvalidTreeExits1AndPrintsFileLine(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "inventory")
	if err := os.CopyFS(dir, os.DirFS(validTree)); err != nil {
		t.Fatal(err)
	}
	broken := "apiVersion: yumdrake/v1\nkind: Service\nname: orders\nbogus: 1\nenvironments:\n  dev-02:\n    ecs_service: x\n"
	if err := os.WriteFile(filepath.Join(dir, "services", "orders.yaml"), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runCLI(t, "validate", dir)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !regexp.MustCompile(`(?m)^.*services/orders\.yaml:\d+: `).MatchString(stderr) {
		t.Errorf("stderr has no services/*.yaml:<line>: problem line:\n%s", stderr)
	}
	if !strings.HasPrefix(stdout, "FAILED: ") {
		t.Errorf("stdout = %q, want prefix %q", stdout, "FAILED: ")
	}
}

func TestValidateMissingDirectoryExits1(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	code, stdout, stderr := runCLI(t, "validate", missing)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "environments.yaml") {
		t.Errorf("stderr = %q, want it to name environments.yaml", stderr)
	}
	if !strings.HasPrefix(stdout, "FAILED: ") {
		t.Errorf("stdout = %q, want prefix %q", stdout, "FAILED: ")
	}
}

func TestUsageErrorsExit2(t *testing.T) {
	cases := map[string][]string{
		"no arguments":            {},
		"unknown command":         {"frobnicate"},
		"validate without a dir":  {"validate"},
		"validate with bad flag":  {"validate", "--bogus", "dir"},
		"job flag without a file": {"validate", "dir", "--job"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := runCLI(t, args...)
			if code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if !strings.Contains(stderr, "usage") {
				t.Errorf("stderr = %q, want usage text", stderr)
			}
		})
	}
}

func TestHelpExits0WithUsageOnStdout(t *testing.T) {
	code, stdout, _ := runCLI(t, "-h")
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "usage") {
		t.Errorf("stdout = %q, want usage text", stdout)
	}
}

func TestVersion(t *testing.T) {
	code, stdout, _ := runCLI(t, "version")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if got := strings.TrimSpace(stdout); got != version.String() {
		t.Errorf("stdout = %q, want %q", got, version.String())
	}
}
