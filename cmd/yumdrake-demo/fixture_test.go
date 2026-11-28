package main

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/rcss-labs/yumdrake/internal/spec"
	"github.com/rcss-labs/yumdrake/test/awstest"
)

// demoDir is the sample inventory and jobs the demo serves; the image copies them to /demo/platform.
const demoDir = "../../deploy/compose/demo/platform"

// TestDemoFixtureIsValid runs the check `yumdrake validate` runs, so a sample
// that the product would reject cannot be committed.
func TestDemoFixtureIsValid(t *testing.T) {
	jobs, err := filepath.Glob(filepath.Join(demoDir, "jobs", "*.yaml"))
	if err != nil || len(jobs) == 0 {
		t.Fatalf("job files = %v, %v; want at least one", jobs, err)
	}
	inventory, loaded, problems := spec.LoadDir(demoDir, jobs)
	for _, problem := range problems {
		t.Errorf("problem: %v", spec.Problems{problem}.Error())
	}
	if len(problems) > 0 {
		return
	}
	if got := len(inventory.Environments); got != len(demoEnvironments) {
		t.Errorf("environments = %d, want %d (demoEnvironments)", got, len(demoEnvironments))
	}
	for _, env := range inventory.Environments {
		if !slices.ContainsFunc(demoEnvironments, func(want awstest.EnvironmentSpec) bool {
			return want.Name == env.Name && want.Label == env.Label && want.Account == env.AWSAccount
		}) {
			t.Errorf("environment %+v is not one the demo seeds into its local AWS (demoEnvironments)", env)
		}
	}
	if got := len(inventory.Services); got != len(awstest.Services) {
		t.Errorf("services = %d, want the %d the local AWS is seeded with", got, len(awstest.Services))
	}
	if len(loaded) != len(jobs) {
		t.Errorf("loaded jobs = %d, want %d", len(loaded), len(jobs))
	}
}
