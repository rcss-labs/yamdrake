package awsx

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
)

// answerWith returns an HTTP client that answers every request with status and
// the given JSON error type.
func answerWith(status int, errorType string) aws.HTTPClient {
	return roundTripper(func(*http.Request) (*http.Response, error) {
		h := http.Header{"Content-Type": {"application/x-amz-json-1.1"}, "X-Amzn-Errortype": {errorType}}
		return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(bytes.NewReader([]byte(`{"__type":"` + errorType + `","message":"x"}`)))}, nil
	})
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestThrottlingResponsesAreCounted(t *testing.T) {
	TakeThrottles() // start from zero
	call := func(status int, errorType string, attempts int) {
		t.Helper()
		cfg := ObserveThrottles(aws.Config{
			Region: "eu-west-1", HTTPClient: answerWith(status, errorType),
			Credentials: aws.AnonymousCredentials{},
			Retryer:     func() aws.Retryer { return retryTimes{attempts} },
		})
		_, _ = ecs.NewFromConfig(cfg).ListClusters(context.Background(), &ecs.ListClustersInput{})
	}

	call(400, "AccessDeniedException", 1)
	if got := TakeThrottles(); got != 0 {
		t.Fatalf("a refusal that is not throttling counted %d, want 0", got)
	}
	call(400, "ThrottlingException", 1)
	if got := TakeThrottles(); got != 1 {
		t.Fatalf("one throttled call counted %d, want 1", got)
	}
	// Each throttled answer counts, retries included.
	call(400, "ThrottlingException", 3)
	if got := TakeThrottles(); got != 3 {
		t.Fatalf("three throttled attempts counted %d, want 3", got)
	}
	if got := TakeThrottles(); got != 0 {
		t.Fatalf("taking resets the count; the next take = %d, want 0", got)
	}
}

// retryTimes retries every error without waiting, up to max attempts.
type retryTimes struct{ max int }

func (r retryTimes) IsErrorRetryable(error) bool { return true }
func (r retryTimes) MaxAttempts() int            { return r.max }
func (r retryTimes) RetryDelay(int, error) (time.Duration, error) {
	return 0, nil
}
func (r retryTimes) GetRetryToken(context.Context, error) (func(error) error, error) {
	return func(error) error { return nil }, nil
}
func (r retryTimes) GetInitialToken() func(error) error { return func(error) error { return nil } }
