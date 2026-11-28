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
	return cfg, nil
}
