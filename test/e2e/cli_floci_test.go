//go:build e2e && floci

package e2e

// End-to-end proof of the yumdrake command line tool. The tool is built and
// run as a subprocess against the real orchestrator and runner on Floci, signed
// in as service accounts with tokens from the environment, exactly as a script
// or CI job would use it.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/yamdrake/test/awstest"
)

const (
	cliPassphrase = "cli end to end passphrase 123" // service accounts never sign in with it
	cliTimeout    = 6 * time.Minute
	// crashVersion is a tag only this test pushes: api's image exits with 3.
	crashVersion = "1.5.0"
)

type cliResult struct {
	code           int
	stdout, stderr string
}

// cliTool runs the built CLI with one service account's token.
type cliTool struct {
	t      *testing.T
	bin    string
	server string
	token  string
	home   string
}

// buildCLI compiles cmd/yumdrake into a temporary directory.
func buildCLI(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "yumdrake")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/rcss-labs/yamdrake/cmd/yumdrake")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the CLI: %v\n%s", err, out)
	}
	return bin
}

func (c cliTool) run(args ...string) cliResult {
	c.t.Helper()
	cmd := exec.Command(c.bin, args...)
	// A clean environment: only what the CLI reads, so no ambient AWS or CLI
	// settings reach it.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + c.home, "XDG_CONFIG_HOME=" + filepath.Join(c.home, "config"),
		"YUMDRAKE_SERVER=" + c.server, "YUMDRAKE_TOKEN=" + c.token,
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		c.t.Fatalf("start the CLI: %v", err)
	}
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-time.After(cliTimeout):
		_ = cmd.Process.Kill()
		c.t.Fatalf("yumdrake %s did not finish in %s\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), cliTimeout, stdout.String(), stderr.String())
	}
	res := cliResult{stdout: stdout.String(), stderr: stderr.String()}
	if exit, ok := err.(*exec.ExitError); ok {
		res.code = exit.ExitCode()
	} else if err != nil {
		c.t.Fatalf("run the CLI: %v", err)
	}
	if strings.Contains(res.stdout+res.stderr, c.token) {
		c.t.Fatalf("yumdrake %s printed the token", strings.Join(args, " "))
	}
	return res
}

// expect runs the CLI and requires the exit code.
func (c cliTool) expect(want int, args ...string) cliResult {
	c.t.Helper()
	got := c.run(args...)
	if got.code != want {
		c.t.Fatalf("yumdrake %s exited %d, want %d\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), got.code, want, got.stdout, got.stderr)
	}
	return got
}

// json requires standard output to be exactly one JSON document and decodes it.
func (r cliResult) json(t *testing.T, what string, out any) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(r.stdout))
	if err := dec.Decode(out); err != nil {
		t.Fatalf("%s: standard output is not JSON: %v\n%s", what, err, r.stdout)
	}
	if dec.More() {
		t.Fatalf("%s: standard output holds more than one JSON document:\n%s", what, r.stdout)
	}
}

// serviceAccount creates a service account in a group holding the grants and
// returns a CLI that signs in with a token minted for it.
func (s *flociStack) serviceAccount(bin, name string, grants ...[2]string) cliTool {
	s.t.Helper()
	var group, user struct {
		ID int64 `json:"id"`
	}
	s.api.call(http.MethodPost, "/api/v1/admin/groups", map[string]any{"name": name + "-group"}, &group)
	for _, grant := range grants {
		s.api.call(http.MethodPost, "/api/v1/admin/grants", map[string]any{"group_id": group.ID, "action": grant[0], "label": grant[1]}, nil)
	}
	s.api.call(http.MethodPost, "/api/v1/admin/users",
		map[string]any{"email": name + "@cli.example.test", "name": name, "source": "service", "password": cliPassphrase}, &user)
	s.api.call(http.MethodPatch, fmt.Sprintf("/api/v1/admin/users/%d", user.ID), map[string]any{"group_ids": []int64{group.ID}}, nil)
	var minted struct {
		Plaintext string `json:"plaintext"`
	}
	s.api.call(http.MethodPost, fmt.Sprintf("/api/v1/admin/users/%d/tokens", user.ID), map[string]any{"name": "cli-e2e"}, &minted)
	if minted.Plaintext == "" {
		s.t.Fatalf("no token was minted for %s", name)
	}
	return cliTool{t: s.t, bin: bin, server: s.api.base, token: minted.Plaintext, home: s.t.TempDir()}
}

// cliRunID reads the run's id from the JSON a run command printed.
func cliRunID(t *testing.T, r cliResult) int64 {
	t.Helper()
	var run struct {
		ID    int64  `json:"id"`
		State string `json:"state"`
	}
	r.json(t, "run document", &run)
	if run.ID == 0 {
		t.Fatalf("the run document has no id:\n%s", r.stdout)
	}
	return run.ID
}

// TestCLIEndToEnd drives the command line tool through a whole working day:
// look, plan, deploy a good version, put it back, deploy a crashing version,
// read its log, and let a second person approve a prod run.
func TestCLIEndToEnd(t *testing.T) {
	bin := buildCLI(t)
	s := startFloci(t, flociOpts{seed: []string{devEnv, prodEnv}, real: true, approvalForProd: true})
	dev := s.envs[devEnv]
	s.staleDestinations(dev)
	s.staleDestinations(s.envs[prodEnv])

	// The deployer may run dev and prod, put dev back and read its logs; the
	// approver may approve prod only.
	deployer := s.serviceAccount(bin, "deployer", [2]string{"run", "dev"}, [2]string{"run", "prod"},
		[2]string{"restore", "dev"}, [2]string{"view_logs", "dev"})
	approver := s.serviceAccount(bin, "approver", [2]string{"approve", "prod"})

	// A version that makes only api crash: web and worker get a good image.
	for _, name := range []string{"web", "worker"} {
		dev.acct.RepoImage(flociRegion, dev.services[name].repo, crashVersion, "good-"+awstest.RandomSuffix())
	}
	dev.acct.RepoCrashingImage(flociRegion, dev.services["api"].repo, crashVersion, "crash-"+awstest.RandomSuffix(), 3)

	selection := func(version string) []string { return []string{"--env", devEnv, "--set", "all=" + version} }

	t.Run("jobs list", func(t *testing.T) {
		var out struct {
			Jobs []struct {
				JobID string `json:"job_id"`
			} `json:"jobs"`
		}
		got := deployer.expect(0, "jobs", "list")
		if !strings.Contains(got.stdout, "deploy") {
			t.Errorf("jobs list does not show the job:\n%s", got.stdout)
		}
		deployer.expect(0, "jobs", "list", "-o", "json").json(t, "jobs list", &out)
		if len(out.Jobs) != 1 || out.Jobs[0].JobID != "deploy" {
			t.Errorf("jobs list -o json = %+v, want the one job deploy", out.Jobs)
		}
	})

	t.Run("versions", func(t *testing.T) {
		got := deployer.expect(0, "versions", "deploy", "--env", devEnv)
		if !strings.Contains(got.stdout, planVersion) {
			t.Errorf("versions does not list %s:\n%s", planVersion, got.stdout)
		}
		var out map[string]any
		deployer.expect(0, "versions", "deploy", "--env", devEnv, "-o", "json").json(t, "versions", &out)
	})

	t.Run("plan", func(t *testing.T) {
		got := deployer.expect(0, append([]string{"plan", "deploy"}, selection(planVersion)...)...)
		for _, want := range []string{"web", "api", "worker", planVersion, "Checks:"} {
			if !strings.Contains(got.stdout, want) {
				t.Errorf("plan output lacks %q:\n%s", want, got.stdout)
			}
		}
		var out struct {
			Run  map[string]any `json:"run"`
			Plan map[string]any `json:"plan"`
		}
		deployer.expect(0, append([]string{"plan", "deploy", "-o", "json"}, selection(planVersion)...)...).json(t, "plan", &out)
		if out.Run["id"] == nil || out.Plan["id"] == nil {
			t.Errorf("plan -o json = %+v, want a run and a plan", out)
		}
		// Planning changed nothing in AWS.
		if now := s.serviceTaskDefinition(dev, "web"); now != dev.services["web"].taskDef {
			t.Errorf("plan deployed web: %s", now)
		}
	})

	var goodRun int64
	t.Run("run a good version", func(t *testing.T) {
		got := deployer.expect(0, append([]string{"run", "deploy", "--yes", "--watch", "-o", "json"}, selection(planVersion)...)...)
		goodRun = cliRunID(t, got)
		s.expectDeploy(goodRun, "succeeded")
		if now := s.serviceTaskDefinition(dev, "web"); now == dev.services["web"].taskDef {
			t.Error("run --yes --watch did not deploy web")
		}
	})

	t.Run("restore", func(t *testing.T) {
		if goodRun == 0 {
			t.Skip("needs the good run")
		}
		got := deployer.expect(0, "restore", fmt.Sprint(goodRun), "--yes", "--watch", "-o", "json")
		restoreRun := cliRunID(t, got)
		done := s.expectDeploy(restoreRun, "succeeded")
		if done.RestoreOf == nil || *done.RestoreOf != goodRun {
			t.Errorf("restore run %d restores %v, want %d", restoreRun, done.RestoreOf, goodRun)
		}
	})

	t.Run("crashing version", func(t *testing.T) {
		got := deployer.expect(3, append([]string{"run", "deploy", "--yes", "--watch", "-o", "json"}, selection(crashVersion)...)...)
		// With -o json the progress and the summary go to standard error, which
		// must name the service that failed and why.
		for _, want := range []string{"partially_succeeded", "api", "tasks keep stopping"} {
			if !strings.Contains(got.stderr, want) {
				t.Errorf("stderr lacks %q:\n%s", want, got.stderr)
			}
		}
		id := cliRunID(t, got)
		s.expectDeploy(id, "partially_succeeded")

		log := deployer.expect(0, "runs", "log", fmt.Sprint(id), "--service", "api")
		if !strings.Contains(log.stdout, "exit 3") {
			t.Errorf("runs log --service api does not show the error line:\n%s", log.stdout)
		}
		for _, other := range []string{"web/", "worker/"} {
			if strings.Contains(log.stdout, other) {
				t.Errorf("runs log --service api shows a line of %s:\n%s", other, log.stdout)
			}
		}
		var page map[string]any
		deployer.expect(0, "runs", "log", fmt.Sprint(id), "--service", "api", "-o", "json").json(t, "runs log", &page)
	})

	t.Run("prod needs a second person", func(t *testing.T) {
		started := deployer.expect(5, "run", "deploy", "--env", prodEnv, "--set", "all="+planVersion, "--yes", "-o", "json")
		id := cliRunID(t, started)
		if !strings.Contains(started.stderr, "waiting for approval") {
			t.Errorf("stderr does not say the run waits for approval:\n%s", started.stderr)
		}
		s.waitDeploy(id, "waiting_approval")

		var pending struct {
			Approvals []struct {
				RunID      int64 `json:"run_id"`
				CanApprove bool  `json:"can_approve"`
			} `json:"approvals"`
		}
		approver.expect(0, "approvals", "list", "-o", "json").json(t, "approvals list", &pending)
		if len(pending.Approvals) != 1 || pending.Approvals[0].RunID != id || !pending.Approvals[0].CanApprove {
			t.Errorf("approvals list = %+v, want run %d approvable", pending.Approvals, id)
		}
		// The person who started it may not approve it.
		if got := deployer.run("approvals", "approve", fmt.Sprint(id), "--yes"); got.code == 0 {
			t.Errorf("the requester approved their own run:\n%s", got.stderr)
		}

		approver.expect(0, "approvals", "approve", fmt.Sprint(id), "--yes", "--comment", "cli e2e")
		deployer.expect(0, "runs", "watch", fmt.Sprint(id), "-o", "json").json(t, "runs watch", &map[string]any{})
		s.expectDeploy(id, "succeeded")
	})
}
