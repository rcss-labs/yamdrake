//go:build integration && floci

package versions_test

import (
	"context"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"

	"github.com/rcss-labs/yamdrake/internal/awsx"
	"github.com/rcss-labs/yamdrake/internal/spec"
	"github.com/rcss-labs/yamdrake/internal/versions"
	"github.com/rcss-labs/yamdrake/test/awstest"
)

func TestListAgainstFloci(t *testing.T) {
	w := awstest.New(t)
	accountID := awstest.RandomAccountID()
	acct := w.Account(accountID)
	const region = "us-east-1"
	name := "app-" + awstest.RandomSuffix()
	uri, _ := acct.Repo(region, name, "1.4.1", "1.4.2", "latest")
	ref, err := awsx.ParseECRRepository(uri)
	if err != nil {
		t.Fatalf("ParseECRRepository(%q): %v", uri, err)
	}

	client := ecr.NewFromConfig(acct.AWSConfig(region))
	rule := spec.VersionRule{Pattern: `^\d+\.\d+\.\d+$`, Sort: "semver", Limit: 100}
	got, err := versions.List(context.Background(), client, ref, rule)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// The digest of each tag, as the registry itself reports it. Floci answers
	// with one entry per tag, which is why List has to group by digest.
	described, err := client.DescribeImages(context.Background(), &ecr.DescribeImagesInput{RepositoryName: aws.String(name)})
	if err != nil {
		t.Fatalf("DescribeImages: %v", err)
	}
	digestOfTag := map[string]string{}
	for _, detail := range described.ImageDetails {
		for _, tag := range detail.ImageTags {
			digestOfTag[tag] = aws.ToString(detail.ImageDigest)
		}
	}
	var tags []string
	for _, v := range got {
		tags = append(tags, v.Tag)
		if v.Digest == "" || v.Digest != digestOfTag[v.Tag] {
			t.Errorf("version %s digest = %q, want %q", v.Tag, v.Digest, digestOfTag[v.Tag])
		}
		if v.PushedAt.IsZero() {
			t.Errorf("version %s has no push time", v.Tag)
		}
	}
	if want := []string{"1.4.2", "1.4.1"}; !slices.Equal(tags, want) {
		t.Errorf("tags = %v, want %v (latest filtered out, newest first)", tags, want)
	}
}
