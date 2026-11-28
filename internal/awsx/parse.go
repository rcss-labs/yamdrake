// Package awsx holds the small AWS helpers shared by the services: parsing of
// the identifiers that manifests carry, and construction of SDK configuration.
package awsx

import (
	"fmt"
	"regexp"
	"strings"
)

// ECSServiceRef identifies an ECS service.
type ECSServiceRef struct{ Region, Account, Cluster, Service string }

// ECRRepoRef identifies an ECR repository.
type ECRRepoRef struct{ Account, Region, Name string }

// S3Ref identifies an S3 object.
type S3Ref struct{ Bucket, Key string }

var (
	// Only the new ARN format (with the cluster name) is accepted: the old
	// format names the service alone, which is ambiguous across clusters.
	ecsServicePattern = regexp.MustCompile(`^arn:aws[a-z-]*:ecs:([a-z0-9-]+):(\d{12}):service/([A-Za-z0-9_-]{1,255})/([A-Za-z0-9_.-]{1,255})$`)

	// <account>.dkr.ecr.<region>.<domain>/<name>. The domain is AWS's, or
	// localhost (with an optional port) for the Floci test emulator.
	ecrPattern = regexp.MustCompile(`^(\d{12})\.dkr\.ecr\.([a-z0-9-]+)\.(?:amazonaws\.com(?:\.cn)?|localhost(?::\d{1,5})?)/([a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*)$`)

	bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
)

// ParseECSService parses arn:aws:ecs:<region>:<account>:service/<cluster>/<service>.
func ParseECSService(arn string) (ECSServiceRef, error) {
	m := ecsServicePattern.FindStringSubmatch(arn)
	if m == nil {
		return ECSServiceRef{}, fmt.Errorf("invalid ECS service ARN %q: want arn:aws:ecs:<region>:<account>:service/<cluster>/<service>", arn)
	}
	return ECSServiceRef{Region: m[1], Account: m[2], Cluster: m[3], Service: m[4]}, nil
}

// ParseECRRepository parses a repository URI without a tag or digest:
// <account>.dkr.ecr.<region>.amazonaws.com/<name>.
func ParseECRRepository(uri string) (ECRRepoRef, error) {
	m := ecrPattern.FindStringSubmatch(uri)
	if m == nil {
		return ECRRepoRef{}, fmt.Errorf("invalid ECR repository URI %q: want <account>.dkr.ecr.<region>.amazonaws.com/<name> without a tag or digest", uri)
	}
	return ECRRepoRef{Account: m[1], Region: m[2], Name: m[3]}, nil
}

// ParseS3 parses s3://<bucket>/<key>; both parts are required.
func ParseS3(uri string) (S3Ref, error) {
	rest, ok := strings.CutPrefix(uri, "s3://")
	if !ok {
		return S3Ref{}, fmt.Errorf("invalid S3 URI %q: want s3://<bucket>/<key>", uri)
	}
	bucket, key, found := strings.Cut(rest, "/")
	if !found || key == "" || !bucketPattern.MatchString(bucket) {
		return S3Ref{}, fmt.Errorf("invalid S3 URI %q: want s3://<bucket>/<key> with a valid bucket name", uri)
	}
	return S3Ref{Bucket: bucket, Key: key}, nil
}
