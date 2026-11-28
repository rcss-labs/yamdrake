package versions_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/aws/smithy-go"

	"github.com/rcss-labs/yamdrake/internal/awsx/discovery"
	"github.com/rcss-labs/yamdrake/internal/spec"
	"github.com/rcss-labs/yamdrake/internal/versions"
)

// fleetECR answers DescribeImages for several repositories, by name. A
// repository in failures answers with that error. block, when set, is waited
// on inside every call, so a test can hold calls open and count them.
type fleetECR struct {
	images   map[string][]ecrtypes.ImageDetail
	failures map[string]error
	block    chan struct{}

	calls    atomic.Int64
	running  atomic.Int64
	peak     atomic.Int64
	mu       sync.Mutex
	accounts []string // RegistryId of every call
}

func (f *fleetECR) DescribeImages(_ context.Context, in *ecr.DescribeImagesInput, _ ...func(*ecr.Options)) (*ecr.DescribeImagesOutput, error) {
	f.calls.Add(1)
	now := f.running.Add(1)
	defer f.running.Add(-1)
	for {
		peak := f.peak.Load()
		if now <= peak || f.peak.CompareAndSwap(peak, now) {
			break
		}
	}
	f.mu.Lock()
	f.accounts = append(f.accounts, aws.ToString(in.RegistryId))
	f.mu.Unlock()
	if f.block != nil {
		<-f.block
	}
	name := aws.ToString(in.RepositoryName)
	if err := f.failures[name]; err != nil {
		return nil, err
	}
	return &ecr.DescribeImagesOutput{ImageDetails: f.images[name]}, nil
}

// fleetDiscovery hands out an empty configuration for connected accounts.
type fleetDiscovery struct {
	notConnected map[string]bool
	sessions     atomic.Value // last session name
}

func (d *fleetDiscovery) For(_ context.Context, accountID, region, sessionName string) (aws.Config, error) {
	d.sessions.Store(sessionName)
	if d.notConnected[accountID] {
		return aws.Config{}, fmt.Errorf("account %s: %w", accountID, discovery.ErrNotConnected)
	}
	return aws.Config{Region: region}, nil
}

func newFleetService(ecrClient versions.ECR, disc versions.Discovery) *versions.Service {
	return versions.New(versions.Config{
		Discovery: disc,
		NewClient: func(aws.Config) versions.ECR { return ecrClient },
	})
}

const repoPrefix = "%s.dkr.ecr.eu-west-1.amazonaws.com/"

func serviceIn(name, env, account string, rule spec.VersionRule) spec.Service {
	return spec.Service{
		Name: name, Versions: rule,
		Environments: map[string]spec.ServiceEnv{
			env: {ECRRepository: fmt.Sprintf(repoPrefix, account) + name},
		},
	}
}

var semverRule = spec.VersionRule{Pattern: `^\d+\.\d+\.\d+$`, Sort: "semver", Limit: 100}

func fleetImages(tags ...string) []ecrtypes.ImageDetail {
	var out []ecrtypes.ImageDetail
	for i, tag := range tags {
		out = append(out, image("sha256:"+tag, t0.Add(time.Duration(i)*time.Hour), tag))
	}
	return out
}

func TestForJobIsolatesOneServicesFailure(t *testing.T) {
	const env = "prod-01"
	inv := spec.Inventory{
		Environments: []spec.Environment{{Name: env}},
		Services: map[string]spec.Service{
			"alpha":   serviceIn("alpha", env, "111111111111", semverRule),
			"beta":    serviceIn("beta", env, "111111111111", semverRule),
			"broken":  serviceIn("broken", env, "111111111111", semverRule),
			"offsite": serviceIn("offsite", env, "999999999999", semverRule),
			// Present in the job but with no block for prod-01.
			"elsewhere": {Name: "elsewhere", Versions: semverRule, Environments: map[string]spec.ServiceEnv{
				"dev-01": {ECRRepository: fmt.Sprintf(repoPrefix, "111111111111") + "elsewhere"},
			}},
		},
	}
	job := spec.Job{ID: "deploy", Groups: []spec.Group{
		{ID: "apps", Services: []string{"alpha", "beta", "broken", "offsite", "elsewhere"}},
		{ID: "empty"},
	}}
	// The AWS message holds request detail that must never reach the result.
	denied := &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "role arn:aws:iam::111111111111:role/secret-detail is not allowed"}
	client := &fleetECR{
		images:   map[string][]ecrtypes.ImageDetail{"alpha": fleetImages("1.0.0", "1.1.0"), "beta": fleetImages("1.1.0", "2.0.0")},
		failures: map[string]error{"broken": fmt.Errorf("describe images of broken: %w", denied)},
	}
	disc := &fleetDiscovery{notConnected: map[string]bool{"999999999999": true}}

	got, err := newFleetService(client, disc).ForJob(context.Background(), job, inv, env)
	if err != nil {
		t.Fatalf("ForJob returned %v; one service's failure must not fail the call", err)
	}

	apps := got.Groups["apps"]
	if !slices.Equal(tagsOf(apps.Services["alpha"]), []string{"1.1.0", "1.0.0"}) {
		t.Errorf("alpha = %v, want [1.1.0 1.0.0]", tagsOf(apps.Services["alpha"]))
	}
	if !slices.Equal(tagsOf(apps.Services["beta"]), []string{"2.0.0", "1.1.0"}) {
		t.Errorf("beta = %v, want [2.0.0 1.1.0]", tagsOf(apps.Services["beta"]))
	}
	for _, name := range []string{"broken", "offsite", "elsewhere"} {
		if _, listed := apps.Services[name]; listed {
			t.Errorf("%s is in Services, want it in Unavailable only", name)
		}
	}
	// Common is over the services that could be listed.
	if !slices.Equal(apps.Common, []string{"1.1.0"}) {
		t.Errorf("Common = %v, want [1.1.0]", apps.Common)
	}
	if reason := apps.Unavailable["broken"]; reason != "AccessDeniedException" {
		t.Errorf("broken reason = %q, want the AWS error code and nothing else", reason)
	}
	if reason := apps.Unavailable["offsite"]; !strings.Contains(reason, "not connected") {
		t.Errorf("offsite reason = %q, want it to say the account is not connected", reason)
	}
	if reason := apps.Unavailable["elsewhere"]; reason != "not in this environment" {
		t.Errorf("elsewhere reason = %q, want %q", reason, "not in this environment")
	}
	for name, reason := range apps.Unavailable {
		if strings.Contains(reason, "secret-detail") {
			t.Errorf("reason of %s leaks AWS message text: %q", name, reason)
		}
	}
	// The repository's account is the one asked, and nothing else was asked.
	if got := client.calls.Load(); got != 3 {
		t.Errorf("ECR calls = %d, want 3 (alpha, beta, broken; offsite has no credentials)", got)
	}
	if session, _ := disc.sessions.Load().(string); session != "yumdrake-orchestrator-versions" {
		t.Errorf("session name = %q, want yumdrake-orchestrator-versions", session)
	}

	empty := got.Groups["empty"]
	if empty.Common == nil || empty.Services == nil || empty.Unavailable == nil {
		t.Errorf("an empty group has a nil list or map: %+v", empty)
	}
}

func TestForJobListsAtMostEightRepositoriesAtATime(t *testing.T) {
	const env = "prod-01"
	inv := spec.Inventory{Environments: []spec.Environment{{Name: env}}, Services: map[string]spec.Service{}}
	var names []string
	client := &fleetECR{images: map[string][]ecrtypes.ImageDetail{}, block: make(chan struct{})}
	for i := range 20 {
		name := fmt.Sprintf("svc-%02d", i)
		names = append(names, name)
		inv.Services[name] = serviceIn(name, env, "111111111111", semverRule)
		client.images[name] = fleetImages("1.0.0")
	}
	job := spec.Job{ID: "wide", Groups: []spec.Group{{ID: "all", Services: names}}}

	done := make(chan error, 1)
	go func() {
		_, err := newFleetService(client, &fleetDiscovery{}).ForJob(context.Background(), job, inv, env)
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for client.running.Load() < 8 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // room for a ninth call, if the limit were missing
	if running := client.running.Load(); running != 8 {
		t.Errorf("listings in flight = %d, want exactly 8 while all are held open", running)
	}
	close(client.block)
	if err := <-done; err != nil {
		t.Fatalf("ForJob: %v", err)
	}
	if peak := client.peak.Load(); peak > 8 {
		t.Errorf("peak concurrent listings = %d, want at most 8", peak)
	}
	if calls := client.calls.Load(); calls != 20 {
		t.Errorf("ECR calls = %d, want 20", calls)
	}
}

func TestForJobRefusesAnUnknownEnvironment(t *testing.T) {
	inv := spec.Inventory{Environments: []spec.Environment{{Name: "prod-01"}}}
	_, err := newFleetService(&fleetECR{}, &fleetDiscovery{}).ForJob(context.Background(), spec.Job{}, inv, "nope")
	if !errors.Is(err, versions.ErrUnknownEnvironment) {
		t.Errorf("ForJob error = %v, want ErrUnknownEnvironment", err)
	}
}
