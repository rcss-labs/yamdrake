//go:build floci

package awstest_test

import (
	"reflect"
	"testing"

	"github.com/rcss-labs/yamdrake/test/awstest"
)

func seedOptions() awstest.StackOptions {
	return awstest.StackOptions{
		Suffix:          awstest.RandomSuffix(),
		DevAccount:      awstest.RandomAccountID(),
		ProdAccount:     awstest.RandomAccountID(),
		PlatformAccount: awstest.RandomAccountID(),
	}
}

// Seeding is run again by every start of the demo and the browser test stack
// against a Floci that may already hold the first run's world: the second run
// must find everything and change nothing (same tags and digests, same task
// definition revisions, no new services).
func TestSeedStackIsIdempotent(t *testing.T) {
	w := awstest.New(t)
	opts := seedOptions()

	first := awstest.SeedStack(w, opts)
	second := awstest.SeedStack(w, opts)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("seeding twice changed the world:\nfirst:  %+v\nsecond: %+v", first, second)
	}

	dev := first.Environments[awstest.DevEnvironment]
	api := dev.Services["api"]
	for _, tag := range []string{awstest.RunningVersion, awstest.NewVersion, awstest.CrashVersion} {
		if api.Tags[tag] == "" {
			t.Errorf("api has no image tagged %s, tags = %v", tag, api.Tags)
		}
	}
	if api.Tags[awstest.CrashVersion] == api.Tags[awstest.NewVersion] {
		t.Errorf("the crashing version shares the digest of %s", awstest.NewVersion)
	}
	if _, has := dev.Services["web"].Tags[awstest.CrashVersion]; has {
		t.Error("web has a crashing version, only api should")
	}
}

// RepairCrashingVersion moves the crashing tag to a good image, as the browser
// test does between a failed run and its re-run.
func TestRepairCrashingVersionMovesTheTag(t *testing.T) {
	w := awstest.New(t)
	opts := seedOptions()
	seeded := awstest.SeedStack(w, opts)
	before := seeded.Environments[awstest.DevEnvironment].Services["api"].Tags[awstest.CrashVersion]

	awstest.RepairCrashingVersion(w, opts)
	after := awstest.SeedStack(w, opts).Environments[awstest.DevEnvironment].Services["api"]
	if after.Tags[awstest.CrashVersion] == before {
		t.Errorf("the crashing tag still points at %s after the repair", before)
	}
	if after.Tags[awstest.CrashVersion] != after.Tags[awstest.NewVersion] {
		t.Errorf("the repaired tag is at %s, want the good image %s", after.Tags[awstest.CrashVersion], after.Tags[awstest.NewVersion])
	}
}
