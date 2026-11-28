package resolve

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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

// Clients are the AWS clients for one account and region.
type Clients struct {
	ECS ECSAPI
	ECR ECRAPI
	S3  S3API
}

// NewClients builds the SDK clients over cfg. S3 uses path-style addressing
// when o points the SDK at an emulator. Throttling is retried by the SDK's
// standard retryer; nothing here adds to it.
func NewClients(cfg aws.Config, o awsx.Options) Clients {
	return Clients{
		ECS: ecs.NewFromConfig(cfg),
		ECR: ecr.NewFromConfig(cfg),
		S3:  s3.NewFromConfig(cfg, func(so *s3.Options) { so.UsePathStyle = o.UsePathStyleS3() }),
	}
}

// ErrorDetail describes a failed AWS call for a check and a log line: the
// operation, the AWS error code and the request id, never the error message
// (which can quote ARNs, principals or content). Errors that are not AWS
// API errors get the code "RequestFailed", or "Canceled" and "Timeout" for
// the context errors.
func ErrorDetail(operation string, err error) string {
	code := "RequestFailed"
	var api smithy.APIError
	switch {
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
