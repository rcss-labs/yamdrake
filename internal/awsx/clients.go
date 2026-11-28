package awsx

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
)

// Options carries the deployment-wide AWS settings.
type Options struct {
	// EndpointURL redirects every AWS call to one endpoint, for a local
	// emulator. Empty for real AWS. Set from YUMDRAKE_AWS_ENDPOINT_URL.
	EndpointURL string
}

// UsePathStyleS3 reports whether S3 clients must use path-style addressing: a
// single emulator endpoint cannot serve bucket.host names.
func (o Options) UsePathStyleS3() bool { return o.EndpointURL != "" }

// Config builds an SDK configuration for region. When creds is nil the default
// credential chain applies.
func Config(ctx context.Context, region string, creds aws.CredentialsProvider, o Options) (aws.Config, error) {
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if creds != nil {
		opts = append(opts, awsconfig.WithCredentialsProvider(creds))
	}
	if o.EndpointURL != "" {
		opts = append(opts, awsconfig.WithBaseEndpoint(o.EndpointURL))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("awsx: load AWS configuration for %s: %w", region, err)
	}
	return ObserveThrottles(cfg), nil
}

// BaseConfig builds the configuration of the service's own AWS identity: the
// SDK default chain (environment, shared files, container or instance role)
// with the optional endpoint override. Nothing is resolved until a call is
// made, so a service without AWS credentials still starts.
func BaseConfig(ctx context.Context, o Options) (aws.Config, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if o.EndpointURL != "" {
		opts = append(opts, awsconfig.WithBaseEndpoint(o.EndpointURL))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("awsx: load the default AWS configuration: %w", err)
	}
	// Every configuration the services use derives from this one (the
	// credential sources copy it), so throttling is counted for all of them.
	return ObserveThrottles(cfg), nil
}
