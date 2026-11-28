//go:build floci

package awstest_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/rcss-labs/yamdrake/internal/awsx"
	"github.com/rcss-labs/yamdrake/test/awstest"
)

func TestSeedCreatesReadableWorld(t *testing.T) {
	ctx := context.Background()
	w := awstest.New(t)
	accountID := awstest.RandomAccountID()
	acct := w.Account(accountID)
	suffix := awstest.RandomSuffix()
	const region = "us-east-1"
	cfg := acct.AWSConfig(region)

	t.Run("role can be assumed", func(t *testing.T) {
		roleARN := acct.Role("deploy-" + suffix)
		out, err := sts.NewFromConfig(cfg).AssumeRole(ctx, &sts.AssumeRoleInput{
			RoleArn: aws.String(roleARN), RoleSessionName: aws.String("t1"),
		})
		if err != nil {
			t.Fatalf("AssumeRole(%s): %v", roleARN, err)
		}
		if out.Credentials == nil || aws.ToString(out.Credentials.AccessKeyId) == "" {
			t.Errorf("AssumeRole returned no credentials")
		}
	})

	t.Run("versioned bucket keeps both versions", func(t *testing.T) {
		bucket := "yd-" + suffix
		acct.Bucket(bucket, true)
		acct.PutObject(bucket, "dir/a.txt", []byte("one"))
		acct.PutObject(bucket, "dir/a.txt", []byte("two"))
		got, ok := acct.Object(bucket, "dir/a.txt")
		if !ok || !bytes.Equal(got, []byte("two")) {
			t.Fatalf("Object = %q, %v, want two, true", got, ok)
		}
		if _, ok := acct.Object(bucket, "missing"); ok {
			t.Error("Object(missing) reported found")
		}
		versions, err := s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true }).
			ListObjectVersions(ctx, &s3.ListObjectVersionsInput{Bucket: aws.String(bucket)})
		if err != nil {
			t.Fatalf("ListObjectVersions: %v", err)
		}
		if len(versions.Versions) != 2 {
			t.Errorf("versions = %d, want 2", len(versions.Versions))
		}
	})

	repoName := "app-" + suffix
	var repoURI, digest string
	t.Run("repo tags share one digest", func(t *testing.T) {
		repoURI, digest = acct.Repo(region, repoName, "v1", "v2")
		ref, err := awsx.ParseECRRepository(repoURI)
		if err != nil {
			t.Fatalf("ParseECRRepository(%q): %v", repoURI, err)
		}
		if ref.Account != accountID || ref.Region != region || ref.Name != repoName {
			t.Errorf("repository = %+v, want %s %s %s", ref, accountID, region, repoName)
		}
		out, err := ecr.NewFromConfig(cfg).DescribeImages(ctx, &ecr.DescribeImagesInput{RepositoryName: aws.String(repoName)})
		if err != nil {
			t.Fatalf("DescribeImages: %v", err)
		}
		tagged := map[string]string{}
		for _, d := range out.ImageDetails {
			for _, tag := range d.ImageTags {
				tagged[tag] = aws.ToString(d.ImageDigest)
			}
		}
		if tagged["v1"] != digest || tagged["v2"] != digest || digest == "" {
			t.Errorf("tags = %v, want v1 and v2 both at %q", tagged, digest)
		}
	})

	t.Run("another marker gives another digest", func(t *testing.T) {
		other := acct.RepoImage(region, repoName, "v3", "other")
		if other == "" || other == digest {
			t.Errorf("digest %q should differ from %q", other, digest)
		}
	})

	t.Run("service reaches running", func(t *testing.T) {
		if repoURI == "" {
			t.Skip("repository was not created")
		}
		serviceARN, taskDefARN := acct.Service(region, "c-"+suffix, "svc-"+suffix, repoURI+":v1")
		ref, err := awsx.ParseECSService(serviceARN)
		if err != nil {
			t.Fatalf("ParseECSService(%q): %v", serviceARN, err)
		}
		if ref.Account != accountID || ref.Cluster != "c-"+suffix || ref.Service != "svc-"+suffix {
			t.Errorf("service = %+v", ref)
		}
		out, err := ecs.NewFromConfig(cfg).DescribeServices(ctx, &ecs.DescribeServicesInput{
			Cluster: aws.String(ref.Cluster), Services: []string{ref.Service},
		})
		if err != nil || len(out.Services) != 1 {
			t.Fatalf("DescribeServices = %v, %v", out, err)
		}
		svc := out.Services[0]
		if svc.RunningCount < 1 || aws.ToString(svc.TaskDefinition) != taskDefARN {
			t.Errorf("running = %d, task definition = %s, want >=1 and %s", svc.RunningCount, aws.ToString(svc.TaskDefinition), taskDefARN)
		}
	})

	t.Run("kms key encrypts and decrypts", func(t *testing.T) {
		keyID := acct.KMSKey(region)
		client := kms.NewFromConfig(cfg)
		enc, err := client.Encrypt(ctx, &kms.EncryptInput{KeyId: aws.String(keyID), Plaintext: []byte("hello"), EncryptionAlgorithm: kmstypes.EncryptionAlgorithmSpecSymmetricDefault})
		if err != nil {
			t.Fatalf("Encrypt: %v", err)
		}
		dec, err := client.Decrypt(ctx, &kms.DecryptInput{CiphertextBlob: enc.CiphertextBlob, KeyId: aws.String(keyID)})
		if err != nil || string(dec.Plaintext) != "hello" {
			t.Errorf("Decrypt = %q, %v", dec.Plaintext, err)
		}
	})
}
