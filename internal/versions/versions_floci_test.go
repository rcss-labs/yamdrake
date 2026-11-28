//go:build integration && floci

package versions_test

import (
	"context"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/ecr"

	"github.com/rcss-labs/yumdrake/internal/awsx"
	"github.com/rcss-labs/yumdrake/internal/spec"
	"github.com/rcss-labs/yumdrake/internal/versions"
	"github.com/rcss-labs/yumdrake/test/awstest"
)

func TestListAgainstFloci(t *testing.T) {
	w := awstest.New(t)
	accountID := awstest.RandomAccountID()
	acct := w.Account(accountID)
	const region = "us-east-1"
	name := "app-" + awstest.RandomSuffix()
	uri, digest := acct.Repo(region, name, "1.4.1", "1.4.2", "latest")
	ref, err := awsx.ParseECRRepository(uri, awsx.Options{EndpointURL: w.Endpoint})
	if err != nil {
		t.Fatalf("ParseECRRepository(%q): %v", uri, err)
	}

	client := ecr.NewFromConfig(acct.AWSConfig(region))
	rule := spec.VersionRule{Pattern: `^\d+\.\d+\.\d+$`, Sort: "semver", Limit: 100}
	got, err := versions.List(context.Background(), client, ref, rule)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var tags []string
	for _, v := range got {
		tags = append(tags, v.Tag)
		// Every tag was pushed from the one test image, so each shares the
		// digest that Repo returned.
		if v.Digest == "" || v.Digest != digest {
			t.Errorf("version %s digest = %q, want %q", v.Tag, v.Digest, digest)
		}
		if v.PushedAt.IsZero() {
			t.Errorf("version %s has no push time", v.Tag)
		}
	}
	if want := []string{"1.4.2", "1.4.1"}; !slices.Equal(tags, want) {
		t.Errorf("tags = %v, want %v (latest filtered out, newest first)", tags, want)
	}
}
