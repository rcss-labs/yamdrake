// Package versions lists the image versions a service can be deployed at.
package versions

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"

	"github.com/rcss-labs/yamdrake/internal/awsx"
	"github.com/rcss-labs/yamdrake/internal/spec"
)

// Version is one tag of an image. Tags of the same image share a Digest.
type Version struct {
	Tag      string    `json:"tag"`
	Digest   string    `json:"digest"`
	PushedAt time.Time `json:"pushed_at"`
}

// ECR is the part of the ECR client that List uses.
type ECR interface {
	DescribeImages(ctx context.Context, in *ecr.DescribeImagesInput, o ...func(*ecr.Options)) (*ecr.DescribeImagesOutput, error)
}

// List returns the versions of a repository whose tag matches rule.Pattern,
// newest first by rule.Sort ("semver", or "pushed" for push time), at most
// rule.Limit of them (no cut when the limit is zero or less). An image with
// several matching tags gives one Version per tag; untagged images are ignored.
func List(ctx context.Context, client ECR, repo awsx.ECRRepoRef, rule spec.VersionRule) ([]Version, error) {
	pattern, err := regexp.Compile(rule.Pattern)
	if err != nil {
		return nil, fmt.Errorf("version pattern %q: %w", rule.Pattern, err)
	}

	// Some registries (Floci) report one entry per tag, so the same digest can
	// come back several times; a (digest, tag) pair is kept once.
	type key struct{ digest, tag string }
	seen := map[key]bool{}
	var found []Version

	input := &ecr.DescribeImagesInput{
		RegistryId:     aws.String(repo.Account),
		RepositoryName: aws.String(repo.Name),
	}
	for {
		out, err := client.DescribeImages(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("describe images of %s: %w", repo.Name, err)
		}
		for _, detail := range out.ImageDetails {
			digest := aws.ToString(detail.ImageDigest)
			for _, tag := range detail.ImageTags {
				if !pattern.MatchString(tag) || seen[key{digest, tag}] {
					continue
				}
				seen[key{digest, tag}] = true
				found = append(found, Version{Tag: tag, Digest: digest, PushedAt: aws.ToTime(detail.ImagePushedAt)})
			}
		}
		if out.NextToken == nil || *out.NextToken == "" {
			break
		}
		input.NextToken = out.NextToken
	}

	slices.SortFunc(found, func(a, b Version) int {
		if rule.Sort != "pushed" {
			if c := CompareSemver(b.Tag, a.Tag); c != 0 {
				return c
			}
		}
		// Newest push first; the tag keeps the order deterministic.
		return cmp.Or(b.PushedAt.Compare(a.PushedAt), cmp.Compare(a.Tag, b.Tag))
	})
	if rule.Limit > 0 && len(found) > rule.Limit {
		found = found[:rule.Limit]
	}
	return found, nil
}

// Common returns the tags present for every service, in the order of the
// list of the service whose name sorts first (a map has no order of its own).
func Common(perService map[string][]Version) []string {
	if len(perService) == 0 {
		return nil
	}
	names := make([]string, 0, len(perService))
	for name := range perService {
		names = append(names, name)
	}
	slices.Sort(names)

	var common []string
	for _, candidate := range perService[names[0]] {
		inAll := true
		for _, name := range names[1:] {
			if !slices.ContainsFunc(perService[name], func(v Version) bool { return v.Tag == candidate.Tag }) {
				inAll = false
				break
			}
		}
		if inAll && !slices.Contains(common, candidate.Tag) {
			common = append(common, candidate.Tag)
		}
	}
	return common
}
