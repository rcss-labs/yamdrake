package versions_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/rcss-labs/yamdrake/internal/awsx"
	"github.com/rcss-labs/yamdrake/internal/spec"
	"github.com/rcss-labs/yamdrake/internal/versions"
)

// stubECR serves DescribeImages from fixed pages and records the requests.
type stubECR struct {
	pages    [][]ecrtypes.ImageDetail
	err      error
	requests []ecr.DescribeImagesInput
}

func (s *stubECR) DescribeImages(_ context.Context, in *ecr.DescribeImagesInput, _ ...func(*ecr.Options)) (*ecr.DescribeImagesOutput, error) {
	s.requests = append(s.requests, *in)
	if s.err != nil {
		return nil, s.err
	}
	index := 0
	if in.NextToken != nil {
		for i := range s.pages {
			if *in.NextToken == pageToken(i) {
				index = i
			}
		}
	}
	out := &ecr.DescribeImagesOutput{ImageDetails: s.pages[index]}
	if index+1 < len(s.pages) {
		out.NextToken = aws.String(pageToken(index + 1))
	}
	return out, nil
}

func pageToken(i int) string { return fmt.Sprintf("page-%d", i) }

func image(digest string, pushed time.Time, tags ...string) ecrtypes.ImageDetail {
	return ecrtypes.ImageDetail{ImageDigest: aws.String(digest), ImageTags: tags, ImagePushedAt: aws.Time(pushed)}
}

var (
	repo = awsx.ECRRepoRef{Account: "123456789012", Region: "us-east-1", Name: "api"}
	t0   = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
)

func tagsOf(list []versions.Version) []string {
	var tags []string
	for _, v := range list {
		tags = append(tags, v.Tag)
	}
	return tags
}

func TestListGroupsByDigestFiltersSortsAndLimits(t *testing.T) {
	// Floci reports one entry per tag, so 1.4.2 appears twice with a shared digest
	// and 1.4.2 must come out once, with that digest.
	stub := &stubECR{pages: [][]ecrtypes.ImageDetail{
		{image("sha256:a", t0, "1.4.1"), image("sha256:b", t0.Add(time.Hour), "1.4.2")},
		{image("sha256:b", t0.Add(time.Hour), "1.4.2", "latest"), image("sha256:c", t0.Add(2*time.Hour), "9f8e7d6c")},
		{image("sha256:d", t0.Add(3*time.Hour)), image("sha256:e", t0.Add(4*time.Hour), "1.10.0")},
	}}
	rule := spec.VersionRule{Pattern: `^\d+\.\d+\.\d+$`, Sort: "semver", Limit: 100}

	got, err := versions.List(context.Background(), stub, repo, rule)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []versions.Version{
		{Tag: "1.10.0", Digest: "sha256:e", PushedAt: t0.Add(4 * time.Hour)},
		{Tag: "1.4.2", Digest: "sha256:b", PushedAt: t0.Add(time.Hour)},
		{Tag: "1.4.1", Digest: "sha256:a", PushedAt: t0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List = %+v, want %+v", got, want)
	}
	if len(stub.requests) != 3 {
		t.Errorf("DescribeImages calls = %d, want 3 (one per page)", len(stub.requests))
	}
	for _, in := range stub.requests {
		if aws.ToString(in.RepositoryName) != "api" || aws.ToString(in.RegistryId) != "123456789012" {
			t.Errorf("request repository/registry = %q/%q, want api/123456789012", aws.ToString(in.RepositoryName), aws.ToString(in.RegistryId))
		}
	}

	t.Run("limit cuts after sorting", func(t *testing.T) {
		limited := rule
		limited.Limit = 2
		got, err := versions.List(context.Background(), stub, repo, limited)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if tags := tagsOf(got); !slices.Equal(tags, []string{"1.10.0", "1.4.2"}) {
			t.Errorf("tags = %v, want [1.10.0 1.4.2]", tags)
		}
	})

	t.Run("one version per matching tag of a digest", func(t *testing.T) {
		both := spec.VersionRule{Pattern: `^(1\.4\.2|latest)$`, Sort: "pushed", Limit: 100}
		got, err := versions.List(context.Background(), stub, repo, both)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 2 || got[0].Digest != "sha256:b" || got[1].Digest != "sha256:b" {
			t.Errorf("List = %+v, want two versions sharing sha256:b", got)
		}
	})

	t.Run("commit-hash tags match a permissive pattern", func(t *testing.T) {
		got, err := versions.List(context.Background(), stub, repo, spec.VersionRule{Pattern: `^[0-9a-f]{8}$`, Sort: "semver", Limit: 100})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if tags := tagsOf(got); !slices.Equal(tags, []string{"9f8e7d6c"}) {
			t.Errorf("tags = %v, want [9f8e7d6c]", tags)
		}
	})

	t.Run("AWS error is returned", func(t *testing.T) {
		boom := errors.New("AccessDenied")
		if _, err := versions.List(context.Background(), &stubECR{err: boom}, repo, rule); !errors.Is(err, boom) {
			t.Errorf("err = %v, want it to wrap %v", err, boom)
		}
	})

	t.Run("invalid pattern is an error", func(t *testing.T) {
		if _, err := versions.List(context.Background(), stub, repo, spec.VersionRule{Pattern: `(`, Sort: "semver", Limit: 5}); err == nil {
			t.Error("List with an invalid pattern returned no error")
		}
	})
}

func TestListSortByPushed(t *testing.T) {
	stub := &stubECR{pages: [][]ecrtypes.ImageDetail{{
		image("sha256:a", t0.Add(2*time.Hour), "2.0.0"),
		image("sha256:b", t0.Add(3*time.Hour), "1.0.0"),
		image("sha256:c", t0.Add(time.Hour), "3.0.0"),
	}}}
	got, err := versions.List(context.Background(), stub, repo, spec.VersionRule{Pattern: `.*`, Sort: "pushed", Limit: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if tags := tagsOf(got); !slices.Equal(tags, []string{"1.0.0", "2.0.0"}) {
		t.Errorf("tags = %v, want newest push first [1.0.0 2.0.0]", tags)
	}
}

func TestCommon(t *testing.T) {
	v := func(tags ...string) []versions.Version {
		var out []versions.Version
		for _, tag := range tags {
			out = append(out, versions.Version{Tag: tag})
		}
		return out
	}
	tests := []struct {
		name string
		in   map[string][]versions.Version
		want []string
	}{
		{"order of the lexically first service", map[string][]versions.Version{
			"web": v("1.2.0", "1.1.0", "1.0.0"),
			"api": v("1.0.0", "1.2.0", "0.9.0"),
		}, []string{"1.0.0", "1.2.0"}},
		{"no overlap", map[string][]versions.Version{"a": v("1"), "b": v("2")}, nil},
		{"single service", map[string][]versions.Version{"a": v("2", "1")}, []string{"2", "1"}},
		{"empty", map[string][]versions.Version{}, nil},
		{"a service with no versions empties the result", map[string][]versions.Version{"a": v("1"), "b": nil}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := versions.Common(tt.in); !slices.Equal(got, tt.want) {
				t.Errorf("Common = %v, want %v", got, tt.want)
			}
		})
	}
}
