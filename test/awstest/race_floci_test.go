//go:build floci

package awstest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
)

// localTag is the shared local name earlier versions built and pushed from.
func localTag(marker string) string {
	sum := sha256.Sum256([]byte(marker))
	return "yumdrake-awstest:" + hex.EncodeToString(sum[:6])
}

// Other processes build the same marker image under the same local name; a
// rebuild that misses the cache yields a new image id. Pushing must not follow
// that name, or tags of one Repo call end up with different digests.
func TestRepoDigestsSurviveConcurrentRebuilds(t *testing.T) {
	w := New(t)
	acct := w.Account(RandomAccountID())
	const region = "us-east-1"

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = exec.Command("docker", "build", "-q", "--no-cache", "--build-arg", "MARKER=base", //nolint:gosec // test helper
				"-t", localTag("base"), imageDir()).Run()
		}
	}()
	defer func() { close(stop); wg.Wait() }()

	for i := 0; i < 3; i++ {
		name := "race-" + RandomSuffix()
		uri, digest := acct.Repo(region, name, "1.4.1", "1.4.2", "latest")
		if uri == "" {
			t.Fatal("empty repository URI")
		}
		out, err := ecr.NewFromConfig(acct.AWSConfig(region)).DescribeImages(context.Background(),
			&ecr.DescribeImagesInput{RepositoryName: aws.String(name)})
		if err != nil {
			t.Fatalf("DescribeImages: %v", err)
		}
		seen := 0
		for _, d := range out.ImageDetails {
			for _, tag := range d.ImageTags {
				seen++
				if aws.ToString(d.ImageDigest) != digest {
					t.Errorf("round %d: tag %s has digest %s, Repo returned %s", i, tag, aws.ToString(d.ImageDigest), digest)
				}
			}
		}
		if seen != 3 {
			t.Errorf("round %d: %d tags found, want 3", i, seen)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
