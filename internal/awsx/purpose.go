package awsx

// Encryption purposes of the two secret access keys of an AWS account. They
// are separate so the orchestrator, which only ever decrypts the discovery key,
// cannot be made to hand out a deployment key (and KMS enforces it through the
// encryption context). The administration API seals both; only
// internal/awsx/discovery opens PurposeDiscovery and only
// internal/awsx/deploy opens PurposeDeploy. awsx/boundary_test.go enforces it.
const (
	PurposeDiscovery = "aws-discovery"
	PurposeDeploy    = "aws-deploy"
)
