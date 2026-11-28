package resolve

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	"github.com/rcss-labs/yamdrake/internal/awsx"
)

// ECSAPI is the part of the ECS client the resolve and revalidate steps use.
// *ecs.Client satisfies it.
type ECSAPI interface {
	DescribeServices(ctx context.Context, in *ecs.DescribeServicesInput, opts ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	DescribeTaskDefinition(ctx context.Context, in *ecs.DescribeTaskDefinitionInput, opts ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error)
}

// ECRAPI is the part of the ECR client the steps use. *ecr.Client satisfies it.
type ECRAPI interface {
	DescribeImages(ctx context.Context, in *ecr.DescribeImagesInput, opts ...func(*ecr.Options)) (*ecr.DescribeImagesOutput, error)
}

// S3API is the part of the S3 client the steps use. *s3.Client satisfies it.
type S3API interface {
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// STSAPI is the part of the STS client the steps use: asking which account a
// credential signs in to. *sts.Client satisfies it.
type STSAPI interface {
	GetCallerIdentity(ctx context.Context, in *sts.GetCallerIdentityInput, opts ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

// Clients are the AWS clients for one account and region.
type Clients struct {
	ECS ECSAPI
	ECR ECRAPI
	S3  S3API
	STS STSAPI
}

// NewClients builds the SDK clients over cfg. S3 uses path-style addressing
// when o points the SDK at an emulator. Throttling is retried by the SDK's
// standard retryer; nothing here adds to it.
func NewClients(cfg aws.Config, o awsx.Options) Clients {
	return Clients{
		ECS: ecs.NewFromConfig(cfg),
		ECR: ecr.NewFromConfig(cfg),
		S3:  s3.NewFromConfig(cfg, func(so *s3.Options) { so.UsePathStyle = o.UsePathStyleS3() }),
		STS: sts.NewFromConfig(cfg),
	}
}

// CallTimeout bounds one AWS call, so a stalled connection cannot hold a leg
// until TCP gives up. It is a variable only so tests can shorten it.
var CallTimeout = 30 * time.Second

// AccountMismatchError says a credential signs in to another account than the
// environment's. Both ids are public, so the text is safe to show.
type AccountMismatchError struct{ Got, Want string }

func (e *AccountMismatchError) Error() string {
	return fmt.Sprintf("the deployment credential signs in to account %s, not the environment's account %s", e.Got, e.Want)
}

// CheckCallerAccount fails with an *AccountMismatchError unless the credential
// behind clients signs in to wantAccount.
func CheckCallerAccount(ctx context.Context, clients Clients, wantAccount string) error {
	if clients.STS == nil {
		return errors.New("no STS client to check the credential's account")
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	identity, err := clients.STS.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return err
	}
	if got := aws.ToString(identity.Account); got != wantAccount {
		return &AccountMismatchError{Got: got, Want: wantAccount}
	}
	return nil
}

// ARNScopeProblem is "" when arn belongs to account and, if region is not
// empty, to that region; otherwise it says which accounts or regions differ.
// An ARN with no account (an unusable answer) is a problem too: a service is
// never accepted by name alone.
func ARNScopeProblem(arn, account, region string) string {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 || parts[0] != "arn" {
		return "its ARN is missing or unusable, so its account cannot be checked"
	}
	if parts[4] != account {
		return fmt.Sprintf("it is in account %s, not the environment's account %s", parts[4], account)
	}
	if region != "" && parts[3] != region {
		return fmt.Sprintf("it is in region %s, not %s", parts[3], region)
	}
	return ""
}

// transientAWSError reports whether an AWS call may succeed if repeated:
// throttling, a server error or a timeout.
func transientAWSError(err error) bool {
	var api smithy.APIError
	if errors.As(err, &api) {
		switch code := api.ErrorCode(); {
		case strings.HasPrefix(code, "Throttl"), code == "RequestLimitExceeded", code == "TooManyRequestsException",
			code == "ServiceUnavailable", code == "InternalError", code == "InternalFailure", code == "RequestTimeout", code == "SlowDown":
			return true
		}
	}
	var response *awshttp.ResponseError
	if errors.As(err, &response) && response.HTTPStatusCode() >= 500 {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// Retry runs fn up to attempts times, each under CallTimeout, waiting base,
// 2*base, 4*base... between tries, but only while the error is transient and
// ctx (the leg's) is still alive. It returns the last error.
func Retry(ctx context.Context, attempts int, base time.Duration, fn func(ctx context.Context) error) error {
	var err error
	for attempt := 0; attempt < max(attempts, 1); attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return err
			case <-time.After(base << (attempt - 1)):
			}
		}
		callCtx, cancel := context.WithTimeout(ctx, CallTimeout)
		err = fn(callCtx)
		cancel()
		if err == nil || ctx.Err() != nil || !transientAWSError(err) {
			return err
		}
	}
	return err
}

// ErrorDetail describes a failed AWS call for a check and a log line: the
// operation, the AWS error code and the request id, never the error message
// (which can quote ARNs, principals or content). Errors that are not AWS
// API errors get the code "RequestFailed", or "Canceled" and "Timeout" for
// the context errors.
func ErrorDetail(operation string, err error) string {
	code := "RequestFailed"
	var api smithy.APIError
	var mismatch *AccountMismatchError
	switch {
	case errors.As(err, &mismatch):
		return fmt.Sprintf("%s failed: %s", operation, mismatch.Error())
	case errors.As(err, &api):
		code = api.ErrorCode()
	case errors.Is(err, context.Canceled):
		code = "Canceled"
	case errors.Is(err, context.DeadlineExceeded):
		code = "Timeout"
	}
	var response *awshttp.ResponseError
	if errors.As(err, &response) && response.ServiceRequestID() != "" {
		return fmt.Sprintf("%s failed: %s (request id %s)", operation, code, response.ServiceRequestID())
	}
	return fmt.Sprintf("%s failed: %s", operation, code)
}

// ErrorCode returns the AWS error code of err, as ErrorDetail names it.
func ErrorCode(err error) string {
	var api smithy.APIError
	if errors.As(err, &api) {
		return api.ErrorCode()
	}
	return ""
}
