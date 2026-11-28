package awsx

import (
	"strings"
	"testing"
)

func TestParseECSService(t *testing.T) {
	valid := []struct {
		in   string
		want ECSServiceRef
	}{
		{"arn:aws:ecs:eu-west-1:123456789012:service/prod/web", ECSServiceRef{"eu-west-1", "123456789012", "prod", "web"}},
		{"arn:aws:ecs:us-east-1:000000000000:service/my_cluster-1/api.v2", ECSServiceRef{"us-east-1", "000000000000", "my_cluster-1", "api.v2"}},
		{"arn:aws-cn:ecs:cn-north-1:123456789012:service/c/s", ECSServiceRef{"cn-north-1", "123456789012", "c", "s"}},
	}
	for _, tc := range valid {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseECSService(tc.in)
			if err != nil {
				t.Fatalf("ParseECSService(%q) error = %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseECSService(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
	invalid := []string{
		"",
		"web",
		"arn:aws:ecs:eu-west-1:123456789012:service/web", // old format without a cluster
		"arn:aws:ecs:eu-west-1:123456789012:cluster/prod",
		"arn:aws:ecs:eu-west-1:123456789012:task-definition/web:3",
		"arn:aws:s3:eu-west-1:123456789012:service/prod/web",
		"arn:aws:ecs::123456789012:service/prod/web",
		"arn:aws:ecs:eu-west-1:12345:service/prod/web",
		"arn:aws:ecs:eu-west-1:123456789012:service/prod/web/extra",
		"arn:aws:ecs:eu-west-1:123456789012:service//web",
		"arn:aws:ecs:eu-west-1:123456789012:service/prod/",
		"arn:aws:ecs:eu-west-1:123456789012:service/pr od/web",
	}
	for _, in := range invalid {
		t.Run("invalid "+in, func(t *testing.T) {
			_, err := ParseECSService(in)
			if err == nil {
				t.Fatalf("ParseECSService(%q) succeeded, want an error", in)
			}
			if !strings.Contains(err.Error(), in) {
				t.Errorf("error %q does not name the value %q", err, in)
			}
		})
	}
}

func TestParseECRRepository(t *testing.T) {
	valid := []struct {
		in   string
		want ECRRepoRef
	}{
		{"123456789012.dkr.ecr.eu-west-1.amazonaws.com/app", ECRRepoRef{"123456789012", "eu-west-1", "app"}},
		{"123456789012.dkr.ecr.us-east-1.amazonaws.com/team/app-api", ECRRepoRef{"123456789012", "us-east-1", "team/app-api"}},
		{"123456789012.dkr.ecr.cn-north-1.amazonaws.com.cn/app", ECRRepoRef{"123456789012", "cn-north-1", "app"}},
		{"111111111111.dkr.ecr.us-east-1.localhost:4577/app", ECRRepoRef{"111111111111", "us-east-1", "app"}},
	}
	for _, tc := range valid {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseECRRepository(tc.in)
			if err != nil {
				t.Fatalf("ParseECRRepository(%q) error = %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseECRRepository(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
	invalid := []string{
		"",
		"app",
		"nginx:latest",
		"docker.io/library/nginx",
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com",
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com/",
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com/app:v1",
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com/app@sha256:abc",
		"12345.dkr.ecr.eu-west-1.amazonaws.com/app",
		"123456789012.dkr.ecr..amazonaws.com/app",
		"123456789012.dkr.ecr.eu-west-1.example.com/app",
		"https://123456789012.dkr.ecr.eu-west-1.amazonaws.com/app",
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com/App",
	}
	for _, in := range invalid {
		t.Run("invalid "+in, func(t *testing.T) {
			_, err := ParseECRRepository(in)
			if err == nil {
				t.Fatalf("ParseECRRepository(%q) succeeded, want an error", in)
			}
			if !strings.Contains(err.Error(), in) {
				t.Errorf("error %q does not name the value %q", err, in)
			}
		})
	}
}

func TestParseS3(t *testing.T) {
	valid := []struct {
		in   string
		want S3Ref
	}{
		{"s3://my-bucket/key", S3Ref{"my-bucket", "key"}},
		{"s3://my.bucket.name/path/to/object.json", S3Ref{"my.bucket.name", "path/to/object.json"}},
		{"s3://abc/a b/c", S3Ref{"abc", "a b/c"}},
	}
	for _, tc := range valid {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseS3(tc.in)
			if err != nil {
				t.Fatalf("ParseS3(%q) error = %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseS3(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
	invalid := []string{
		"",
		"my-bucket/key",
		"https://my-bucket.s3.amazonaws.com/key",
		"s3://",
		"s3:///key",
		"s3://my-bucket",
		"s3://my-bucket/",
		"s3://My_Bucket/key",
		"s3://ab/key",
		"s3://-bucket/key",
	}
	for _, in := range invalid {
		t.Run("invalid "+in, func(t *testing.T) {
			_, err := ParseS3(in)
			if err == nil {
				t.Fatalf("ParseS3(%q) succeeded, want an error", in)
			}
			if !strings.Contains(err.Error(), in) {
				t.Errorf("error %q does not name the value %q", err, in)
			}
		})
	}
}
