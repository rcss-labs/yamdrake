//go:build floci

package awstest_test

import (
	"testing"

	"github.com/rcss-labs/yamdrake/test/awstest"
)

// The demo seeds images with no Docker daemon: they are written over the
// registry API. The repository must report them as tagged images, with the
// same digest for the same content and another for a crashing image.
func TestBinaryImagesArePushedOverTheRegistryAPI(t *testing.T) {
	w := awstest.New(t)
	w.UseBinaryImages()
	acct := w.Account(awstest.RandomAccountID())
	repo := "bin-" + awstest.RandomSuffix()

	acct.EnsureRepoTags(awstest.Region, repo, "1.0.0", "1.0.1")
	acct.EnsureCrashingTag(awstest.Region, repo, "1.0.2", 3)
	tags := acct.RepoTags(awstest.Region, repo)

	if len(tags) != 3 {
		t.Fatalf("tags = %v, want 1.0.0, 1.0.1 and 1.0.2", tags)
	}
	if tags["1.0.0"] == "" || tags["1.0.0"] != tags["1.0.1"] {
		t.Errorf("tags of the same image have digests %q and %q, want one digest", tags["1.0.0"], tags["1.0.1"])
	}
	if tags["1.0.2"] == "" || tags["1.0.2"] == tags["1.0.0"] {
		t.Errorf("the crashing image has digest %q, want one different from %q", tags["1.0.2"], tags["1.0.0"])
	}
}
