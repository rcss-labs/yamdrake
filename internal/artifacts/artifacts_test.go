package artifacts_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/rcss-labs/yamdrake/internal/artifacts"
)

type recorder struct{ header http.Header }

func (r *recorder) Do(req *http.Request) (*http.Response, error) {
	r.header = req.Header.Clone()
	_, _ = io.Copy(io.Discard, req.Body)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
}

func TestPutRequestsServerSideEncryption(t *testing.T) {
	for name, tc := range map[string]struct{ kmsKey, wantAlgo, wantKey string }{
		"s3 managed": {"", "AES256", ""},
		"kms":        {"key-123", "aws:kms", "key-123"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{}
			client := s3.New(s3.Options{
				Region: "us-east-1", HTTPClient: rec, UsePathStyle: true,
				Credentials:  credentials.NewStaticCredentialsProvider("a", "b", ""),
				BaseEndpoint: aws.String("http://example.invalid"),
			})
			if _, _, err := artifacts.New(client, "bkt", tc.kmsKey).Put(context.Background(), 1, "svc", "values", "a.yaml", false, []byte("x")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			if got := rec.header.Get("X-Amz-Server-Side-Encryption"); got != tc.wantAlgo {
				t.Errorf("SSE header = %q, want %q", got, tc.wantAlgo)
			}
			if got := rec.header.Get("X-Amz-Server-Side-Encryption-Aws-Kms-Key-Id"); got != tc.wantKey {
				t.Errorf("KMS key header = %q, want %q", got, tc.wantKey)
			}
		})
	}
}
