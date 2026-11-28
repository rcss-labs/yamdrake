package awstest

// The seeded world the browser test and the demo share: a platform account (the
// bucket that holds run artifacts), a dev and a prod account, and in each of
// them three ECS services (web, api, worker) running RunningVersion, with
// images for NewVersion, and for api one more tag, CrashVersion, whose image
// exits at once. Config buckets hold the files a deployment copies.
//
// SeedStack is idempotent: it finds what exists and creates only what is
// missing, so a second call changes nothing. Nothing it creates is removed by
// a cleanup; the caller owns the lifetime (the demo's Floci is thrown away
// with the demo, the browser test's accounts are random and unique).

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const (
	// Region is where everything is seeded.
	Region = "us-east-1"

	DevEnvironment  = "dev-1"
	ProdEnvironment = "prod-1"

	// RunningVersion is what every service runs after seeding, NewVersion the
	// version to deploy, CrashVersion a version of api whose image crashes.
	RunningVersion = "1.4.1"
	NewVersion     = "1.4.2"
	CrashVersion   = "1.4.3"
	// CrashExitCode is what the crashing image exits with.
	CrashExitCode = 3
)

// Services are the services of every environment, in order.
var Services = []string{"web", "api", "worker"}

// The files in the destination buckets before any deployment. The browser test
// and the demo put different content in the config repository, so a deployment
// writes all three.
var (
	DestinationWebEnv  = []byte("API_KEY=seed-old-key\nLOG_LEVEL=info\nOLD_FLAG=1\n")
	DestinationWebToml = []byte("token = \"seed-old-toml-token\"\n")
	DestinationAPIEnv  = []byte("PORT=7070\nDB_PASSWORD=seed-old-db-password\n")
	SourceWebToml      = []byte("token = \"seed-new-toml-token\"\n")
)

// StackOptions names the seeded world. Empty fields get random values.
type StackOptions struct {
	// Suffix is part of every resource name, so that two worlds in one Floci
	// never meet.
	Suffix                                   string
	DevAccount, ProdAccount, PlatformAccount string
	// Environments, when set, replace the default pair (DevEnvironment in
	// DevAccount, ProdEnvironment in ProdAccount). Environments may share an
	// account; the resource names carry the environment's name.
	Environments []EnvironmentSpec
}

// EnvironmentSpec places one environment in an AWS account.
type EnvironmentSpec struct {
	Name, Label, Account string
}

func (o StackOptions) withDefaults() StackOptions {
	if o.Suffix == "" {
		o.Suffix = RandomSuffix()
	}
	if len(o.Environments) == 0 {
		if o.DevAccount == "" {
			o.DevAccount = RandomAccountID()
		}
		if o.ProdAccount == "" {
			o.ProdAccount = RandomAccountID()
		}
		o.Environments = []EnvironmentSpec{
			{DevEnvironment, "dev", o.DevAccount},
			{ProdEnvironment, "prod", o.ProdAccount},
		}
	}
	if o.PlatformAccount == "" {
		o.PlatformAccount = RandomAccountID()
	}
	return o
}

// Stack is what was seeded, as JSON for the programs that print it.
type Stack struct {
	Suffix          string                  `json:"suffix"`
	Region          string                  `json:"region"`
	PlatformAccount string                  `json:"platform_account"`
	ArtifactBucket  string                  `json:"artifact_bucket"`
	Environments    map[string]*Environment `json:"environments"`
}

// Environment is one environment's AWS account and what it holds.
type Environment struct {
	Name              string                    `json:"name"`
	Label             string                    `json:"label"`
	Account           string                    `json:"account"`
	SourceBucket      string                    `json:"source_bucket"`
	DestinationBucket string                    `json:"destination_bucket"`
	Services          map[string]*ServiceSeeded `json:"services"`
}

// ServiceSeeded is one ECS service and its repository.
type ServiceSeeded struct {
	Repository string `json:"repository"`
	// RepositoryURI is the repository as the inventory names it
	// (<account>.dkr.ecr.<region>.amazonaws.com/<name>), which is how the task
	// definitions name their image; Floci's own URI has another host.
	RepositoryURI  string `json:"repository_uri"`
	Cluster        string `json:"cluster"`
	ServiceARN     string `json:"service_arn"`
	TaskDefinition string `json:"task_definition"`
	// Tags maps every tag the repository holds to its digest.
	Tags map[string]string `json:"tags"`
}

// SeedStack seeds the world opts names and returns what is there.
func SeedStack(w *World, opts StackOptions) *Stack {
	w.t.Helper()
	opts = opts.withDefaults()
	platform := w.Account(opts.PlatformAccount)
	platform.EnsureBucket(artifactBucketName(opts.Suffix), false)
	stack := &Stack{
		Suffix:          opts.Suffix,
		Region:          Region,
		PlatformAccount: opts.PlatformAccount,
		ArtifactBucket:  artifactBucketName(opts.Suffix),
		Environments:    map[string]*Environment{},
	}
	for _, env := range opts.Environments {
		stack.Environments[env.Name] = seedEnvironment(w, opts.Suffix, env.Name, env.Label, env.Account)
	}
	return stack
}

func artifactBucketName(suffix string) string { return "yd-art-" + suffix }

func seedEnvironment(w *World, suffix, name, label, accountID string) *Environment {
	acct := w.Account(accountID)
	e := &Environment{
		Name: name, Label: label, Account: accountID,
		SourceBucket:      fmt.Sprintf("yd-src-%s-%s", suffix, name),
		DestinationBucket: fmt.Sprintf("yd-dst-%s-%s", suffix, name),
		Services:          map[string]*ServiceSeeded{},
	}
	for _, service := range Services {
		s := &ServiceSeeded{
			Repository:    fmt.Sprintf("yd-%s-%s-%s", service, suffix, name),
			Cluster:       fmt.Sprintf("yd-c-%s-%s-%s", service, suffix, name),
			RepositoryURI: fmt.Sprintf("%s.dkr.ecr.%s.amazonaws.com/yd-%s-%s-%s", accountID, Region, service, suffix, name),
		}
		acct.EnsureRepoTags(Region, s.Repository, RunningVersion, NewVersion)
		if service == "api" {
			acct.EnsureCrashingTag(Region, s.Repository, CrashVersion, CrashExitCode)
		}
		s.ServiceARN, s.TaskDefinition = acct.EnsureService(Region, s.Cluster, service, s.RepositoryURI+":"+RunningVersion)
		s.Tags = acct.RepoTags(Region, s.Repository)
		e.Services[service] = s
	}
	acct.EnsureBucket(e.SourceBucket, true)
	acct.EnsureBucket(e.DestinationBucket, false)
	acct.EnsureObject(e.SourceBucket, "web.toml", SourceWebToml)
	acct.EnsureObject(e.DestinationBucket, "web.env", DestinationWebEnv)
	acct.EnsureObject(e.DestinationBucket, "web.toml", DestinationWebToml)
	acct.EnsureObject(e.DestinationBucket, "api.env", DestinationAPIEnv)
	return e
}

// RepairCrashingVersion moves the crashing tag of api in every environment to
// the good image, as a person fixing the image and pushing it again would.
func RepairCrashingVersion(w *World, opts StackOptions) {
	w.t.Helper()
	opts = opts.withDefaults()
	for _, env := range opts.Environments {
		repo := fmt.Sprintf("yd-api-%s-%s", opts.Suffix, env.Name)
		w.Account(env.Account).RepoImage(Region, repo, CrashVersion, "base")
	}
}

// EnsureBucket creates the bucket unless it exists.
func (a *Account) EnsureBucket(name string, versioned bool) {
	a.w.t.Helper()
	if _, err := a.s3Client().HeadBucket(context.Background(), &s3.HeadBucketInput{Bucket: aws.String(name)}); err == nil {
		return
	}
	a.Bucket(name, versioned)
}

// EnsureObject writes the object unless it already holds exactly body, so that
// a versioned bucket does not grow a version on every seeding.
func (a *Account) EnsureObject(bucket, key string, body []byte) {
	a.w.t.Helper()
	if got, ok := a.Object(bucket, key); ok && string(got) == string(body) {
		return
	}
	a.PutObject(bucket, key, body)
}

// RepoTags maps every tag of the repository to its digest. It is empty when the repository does not exist.
func (a *Account) RepoTags(region, name string) map[string]string {
	a.w.t.Helper()
	tags := map[string]string{}
	out, err := ecr.NewFromConfig(a.AWSConfig(region)).DescribeImages(context.Background(), &ecr.DescribeImagesInput{RepositoryName: aws.String(name)})
	if err != nil {
		var missing *ecrtypes.RepositoryNotFoundException
		if errors.As(err, &missing) {
			return tags
		}
		a.w.t.Fatalf("awstest: DescribeImages %s: %v", name, err)
	}
	for _, detail := range out.ImageDetails {
		for _, tag := range detail.ImageTags {
			tags[tag] = aws.ToString(detail.ImageDigest)
		}
	}
	return tags
}

func (a *Account) ensureRepository(region, name string) {
	a.w.t.Helper()
	client := ecr.NewFromConfig(a.AWSConfig(region))
	if _, err := client.DescribeRepositories(context.Background(), &ecr.DescribeRepositoriesInput{RepositoryNames: []string{name}}); err == nil {
		return
	}
	if _, err := client.CreateRepository(context.Background(), &ecr.CreateRepositoryInput{
		RepositoryName: aws.String(name), ImageTagMutability: ecrtypes.ImageTagMutabilityMutable,
	}); err != nil {
		// A Floci that lost its records but kept its registry (a restart) reports the repository as existing.
		var exists *ecrtypes.RepositoryAlreadyExistsException
		if errors.As(err, &exists) {
			return
		}
		a.w.t.Fatalf("awstest: CreateRepository %s: %v", name, err)
	}
}

// EnsureRepoTags creates the repository if missing and pushes the test image
// under each tag the repository does not have yet.
func (a *Account) EnsureRepoTags(region, name string, tags ...string) {
	a.w.t.Helper()
	a.ensureRepository(region, name)
	have := a.RepoTags(region, name)
	for _, tag := range tags {
		if _, ok := have[tag]; !ok {
			a.push(region, name, tag, "base")
		}
	}
}

// EnsureCrashingTag pushes an image that exits with exitCode under tag unless the tag exists.
func (a *Account) EnsureCrashingTag(region, name, tag string, exitCode int) {
	a.w.t.Helper()
	a.ensureRepository(region, name)
	if _, ok := a.RepoTags(region, name)[tag]; !ok {
		a.RepoCrashingImage(region, name, tag, "crash", exitCode)
	}
}

// EnsureService creates the cluster, task definition and service unless the
// service is already active, and returns the service and task definition ARNs
// it runs now.
func (a *Account) EnsureService(region, cluster, name, image string) (serviceARN, taskDefARN string) {
	a.w.t.Helper()
	out, err := ecs.NewFromConfig(a.AWSConfig(region)).DescribeServices(context.Background(), &ecs.DescribeServicesInput{
		Cluster: aws.String(cluster), Services: []string{name},
	})
	if err == nil {
		for _, s := range out.Services {
			if strings.EqualFold(aws.ToString(s.Status), "ACTIVE") {
				return aws.ToString(s.ServiceArn), aws.ToString(s.TaskDefinition)
			}
		}
	}
	return a.createService(region, cluster, name, image)
}
