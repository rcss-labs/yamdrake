package versions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	valkey "github.com/valkey-io/valkey-go"

	"github.com/rcss-labs/yumdrake/internal/awsx"
	"github.com/rcss-labs/yumdrake/internal/awsx/discovery"
	"github.com/rcss-labs/yumdrake/internal/spec"
)

const (
	// maxConcurrentLists bounds the repositories listed at one time.
	maxConcurrentLists = 8
	// cacheTTL is how long one repository's list is kept in Valkey.
	cacheTTL = 60 * time.Second
	// valkeyTimeout bounds each Valkey call: against a dead server valkey-go
	// keeps redialling until its context ends.
	valkeyTimeout = 2 * time.Second
	// sessionName names the assumed-role session in AWS's logs.
	sessionName = "yumdrake-orchestrator-versions"

	reasonNotInEnvironment = "not in this environment"
)

// ErrUnknownEnvironment means the inventory has no such environment.
var ErrUnknownEnvironment = errors.New("versions: unknown environment")

// Discovery builds the discovery configuration of an account;
// *discovery.Provider satisfies it.
type Discovery interface {
	For(ctx context.Context, accountID, region, sessionName string) (aws.Config, error)
}

// Config holds the collaborators of a Service.
type Config struct {
	// Discovery supplies the read-only credentials of each repository's account.
	Discovery Discovery
	// NewClient builds an ECR client from a configuration; the default is
	// ecr.NewFromConfig. Tests replace it.
	NewClient func(aws.Config) ECR
	// Valkey caches each repository's list for 60 seconds. Optional: without
	// it, or while it is down, every call lists from ECR.
	Valkey valkey.Client
	// KeyPrefix goes in front of every Valkey key.
	KeyPrefix string
	// AWS is the deployment-wide AWS options (the emulator endpoint in tests).
	AWS awsx.Options
}

// Service lists the versions a job's services can be deployed at.
type Service struct{ cfg Config }

// New returns a Service.
func New(cfg Config) *Service {
	if cfg.NewClient == nil {
		cfg.NewClient = func(c aws.Config) ECR { return ecr.NewFromConfig(c) }
	}
	return &Service{cfg: cfg}
}

// JobVersions is the version lists of one job in one environment, by group id.
type JobVersions struct {
	Groups map[string]GroupVersions `json:"groups"`
}

// GroupVersions holds one group's lists. Common is the tags every listed
// service has. Unavailable maps a service that could not be listed to the
// reason: "not in this environment", or the AWS error code. None of the three
// is ever nil.
type GroupVersions struct {
	Common      []string             `json:"common"`
	Services    map[string][]Version `json:"services"`
	Unavailable map[string]string    `json:"unavailable"`
}

// listing is the outcome for one service.
type listing struct {
	versions []Version
	reason   string // set when the service is unavailable
}

// ForJob lists the versions of every service of every group of job in
// environment envName. A service that cannot be listed (no block for the
// environment, an account that is not connected, an AWS failure) appears in
// Unavailable and does not fail the others. It fails only for an unknown
// environment.
func (s *Service) ForJob(ctx context.Context, job spec.Job, inv spec.Inventory, envName string) (JobVersions, error) {
	if _, ok := inv.Environment(envName); !ok {
		return JobVersions{}, fmt.Errorf("%w: %q", ErrUnknownEnvironment, envName)
	}

	// One slot per (group, service); the listings run concurrently, a bounded
	// number at a time.
	type slot struct {
		group, service string
		result         listing
	}
	var slots []*slot
	for _, group := range job.Groups {
		for _, name := range group.Services {
			slots = append(slots, &slot{group: group.ID, service: name})
		}
	}
	limit := make(chan struct{}, maxConcurrentLists)
	var wg sync.WaitGroup
	for _, sl := range slots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			limit <- struct{}{}
			defer func() { <-limit }()
			sl.result = s.listService(ctx, inv, sl.service, envName)
		}()
	}
	wg.Wait()

	out := JobVersions{Groups: make(map[string]GroupVersions, len(job.Groups))}
	for _, group := range job.Groups {
		out.Groups[group.ID] = GroupVersions{Common: []string{}, Services: map[string][]Version{}, Unavailable: map[string]string{}}
	}
	for _, sl := range slots {
		g := out.Groups[sl.group]
		if sl.result.reason != "" {
			g.Unavailable[sl.service] = sl.result.reason
		} else {
			g.Services[sl.service] = sl.result.versions
		}
	}
	for id, g := range out.Groups {
		if common := Common(g.Services); common != nil {
			g.Common = common
		}
		out.Groups[id] = g
	}
	return out, nil
}

func (s *Service) listService(ctx context.Context, inv spec.Inventory, name, envName string) listing {
	service, ok := inv.Services[name]
	if !ok {
		return listing{reason: reasonNotInEnvironment}
	}
	env, ok := service.Environments[envName]
	if !ok {
		return listing{reason: reasonNotInEnvironment}
	}
	repo, err := awsx.ParseECRRepository(env.ECRRepository, s.cfg.AWS)
	if err != nil {
		return listing{reason: "invalid repository"}
	}

	key := cacheKey(s.cfg.KeyPrefix, repo, service.Versions)
	if cached, ok := s.cacheGet(ctx, key); ok {
		return listing{versions: cached}
	}
	config, err := s.cfg.Discovery.For(ctx, repo.Account, repo.Region, sessionName)
	if errors.Is(err, discovery.ErrNotConnected) {
		return listing{reason: "AWS account " + repo.Account + " is not connected"}
	}
	if err != nil {
		return listing{reason: errorCode(err)}
	}
	found, err := List(ctx, s.cfg.NewClient(config), repo, service.Versions)
	if err != nil {
		return listing{reason: errorCode(err)}
	}
	if found == nil {
		found = []Version{} // a list, never null, in the cache and the response
	}
	s.cacheSet(ctx, key, found)
	return listing{versions: found}
}

// errorCode is the AWS error code of err, and never its message, which can
// carry request details. Both credsource errors and smithy API errors have
// ErrorCode.
func errorCode(err error) string {
	var coded interface{ ErrorCode() string }
	switch {
	case errors.As(err, &coded) && coded.ErrorCode() != "":
		return coded.ErrorCode()
	case errors.Is(err, context.DeadlineExceeded):
		return "Timeout"
	default:
		return "RequestFailed"
	}
}

// cacheKey is <prefix>versions:<account>:<region>:<repo>:<hash of rule>; the
// rule is part of it so a changed rule is not served a stale list.
func cacheKey(prefix string, repo awsx.ECRRepoRef, rule spec.VersionRule) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%q\x00%q\x00%d", rule.Pattern, rule.Sort, rule.Limit)))
	return fmt.Sprintf("%sversions:%s:%s:%s:%s", prefix, repo.Account, repo.Region, repo.Name, hex.EncodeToString(sum[:8]))
}

// cacheGet and cacheSet ignore every Valkey failure: the cache only saves
// calls to ECR.
func (s *Service) cacheGet(ctx context.Context, key string) ([]Version, bool) {
	if s.cfg.Valkey == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, valkeyTimeout)
	defer cancel()
	raw, err := s.cfg.Valkey.Do(ctx, s.cfg.Valkey.B().Get().Key(key).Build()).AsBytes()
	if err != nil {
		return nil, false
	}
	var found []Version
	if json.Unmarshal(raw, &found) != nil {
		return nil, false
	}
	return found, true
}

func (s *Service) cacheSet(ctx context.Context, key string, found []Version) {
	if s.cfg.Valkey == nil {
		return
	}
	raw, err := json.Marshal(found)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, valkeyTimeout)
	defer cancel()
	s.cfg.Valkey.Do(ctx, s.cfg.Valkey.B().Set().Key(key).Value(string(raw)).Ex(cacheTTL).Build())
}
