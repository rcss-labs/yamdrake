//go:build integration && floci

package discovery

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	"github.com/rcss-labs/yumdrake/internal/awsx"
	"github.com/rcss-labs/yumdrake/internal/awsx/internal/credsource"
	"github.com/rcss-labs/yumdrake/internal/store"
	"github.com/rcss-labs/yumdrake/test/awstest"
)

// countingClient counts the HTTP requests made through it; the base
// configuration is used for STS only, so each request is an STS call.
type countingClient struct{ n atomic.Int64 }

func (c *countingClient) Do(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return http.DefaultClient.Do(r)
}

func callerAccount(t *testing.T, cfg aws.Config) string {
	t.Helper()
	identity, err := sts.NewFromConfig(cfg).GetCallerIdentity(context.Background(), &sts.GetCallerIdentityInput{})
	if err != nil {
		t.Fatalf("GetCallerIdentity: %v", err)
	}
	return aws.ToString(identity.Account)
}

func TestAssumeRoleYieldsTargetAccountIdentity(t *testing.T) {
	e := newEnv(t)
	world := awstest.New(t)
	home := world.Account(awstest.RandomAccountID())   // where the service itself runs
	target := world.Account(awstest.RandomAccountID()) // the connected account
	e.createAccount(t, target.ID, store.AWSCredential{Method: "role", RoleARN: target.Role("yumdrake-" + serviceName + "-" + awstest.RandomSuffix()), ExternalID: "ext"}, "", "")
	p := New(e.pool, e.cipher, home.AWSConfig("us-east-1"), awsx.Options{EndpointURL: world.Endpoint})

	cfg, err := p.For(context.Background(), target.ID, "eu-west-1", "yumdrake-"+serviceName+"-run-1")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if got := callerAccount(t, cfg); got != target.ID {
		t.Errorf("caller account = %s, want the connected account %s (not the service's own %s)", got, target.ID, home.ID)
	}
	if cfg.Region != "eu-west-1" {
		t.Errorf("Region = %q, want eu-west-1", cfg.Region)
	}
}

func TestKeyMethodUsesStoredKey(t *testing.T) {
	e := newEnv(t)
	world := awstest.New(t)
	home := world.Account(awstest.RandomAccountID())
	target := world.Account(awstest.RandomAccountID())
	counter := &countingClient{}
	base := home.AWSConfig("us-east-1")
	base.HTTPClient = counter
	// The Floci access key id is the account id and the secret is "test".
	e.createAccount(t, target.ID, store.AWSCredential{Method: "key", AccessKeyID: target.ID}, "test", ownPurpose)
	p := New(e.pool, e.cipher, base, awsx.Options{EndpointURL: world.Endpoint})

	cfg, err := p.For(context.Background(), target.ID, "us-east-1", "yumdrake-"+serviceName+"-run-1")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	creds, err := cfg.Credentials.Retrieve(context.Background())
	if err != nil || creds.AccessKeyID != target.ID || creds.SecretAccessKey != "test" || creds.SessionToken != "" {
		t.Errorf("credentials = key %q, token %q, %v; want the stored key and no session token", creds.AccessKeyID, creds.SessionToken, err)
	}
	if n := counter.n.Load(); n != 0 {
		t.Errorf("the key method made %d STS calls, want none", n)
	}
	if got := callerAccount(t, cfg); got != target.ID {
		t.Errorf("caller account = %s, want %s", got, target.ID)
	}
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

func TestCredentialsAreCachedUntilNearExpiry(t *testing.T) {
	e := newEnv(t)
	world := awstest.New(t)
	home := world.Account(awstest.RandomAccountID())
	target := world.Account(awstest.RandomAccountID())
	counter := &countingClient{}
	base := home.AWSConfig("us-east-1")
	base.HTTPClient = counter
	e.createAccount(t, target.ID, store.AWSCredential{Method: "role", RoleARN: target.Role("yumdrake-" + serviceName + "-" + awstest.RandomSuffix())}, "", "")
	clock := &fakeClock{now: time.Now()}
	p := New(e.pool, e.cipher, base, awsx.Options{EndpointURL: world.Endpoint})
	p.src = credsource.New(serviceName, base, awsx.Options{EndpointURL: world.Endpoint}, clock.Now)
	ctx := context.Background()
	session := "yumdrake-" + serviceName + "-run-1"
	calls := func() int64 { return counter.n.Load() }

	first, err := p.For(ctx, target.ID, "us-east-1", session)
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	creds, err := first.Credentials.Retrieve(ctx)
	if err != nil || !creds.CanExpire {
		t.Fatalf("Retrieve = %+v, %v; want expiring credentials", creds, err)
	}
	if calls() != 1 {
		t.Fatalf("STS calls after the first For = %d, want 1", calls())
	}

	// Well before expiry: the same credentials, no new call, also through the
	// provider of a configuration handed out earlier.
	clock.set(creds.Expires.Add(-6 * time.Minute))
	if _, err := p.For(ctx, target.ID, "us-east-1", session); err != nil {
		t.Fatalf("For: %v", err)
	}
	if again, err := first.Credentials.Retrieve(ctx); err != nil || again.AccessKeyID != creds.AccessKeyID {
		t.Fatalf("Retrieve = %+v, %v; want the cached credentials", again, err)
	}
	if calls() != 1 {
		t.Errorf("STS calls 6 minutes before expiry = %d, want 1 (cached)", calls())
	}

	// Inside the last 5 minutes: new credentials, on For and on an old
	// configuration (Floci issues the same expiry again, so the fake clock is
	// inside the window of the new credentials too; only the first refresh is
	// counted).
	clock.set(creds.Expires.Add(-4 * time.Minute))
	if _, err := p.For(ctx, target.ID, "us-east-1", session); err != nil {
		t.Fatalf("For: %v", err)
	}
	if calls() != 2 {
		t.Errorf("STS calls 4 minutes before expiry = %d, want 2 (refreshed)", calls())
	}
	if refreshed, err := first.Credentials.Retrieve(ctx); err != nil || refreshed.AccessKeyID == creds.AccessKeyID {
		t.Errorf("an earlier configuration still serves the expiring credentials %q (%v)", refreshed.AccessKeyID, err)
	}
}

func TestErrorNamesCodeAndNeverTheSecret(t *testing.T) {
	e := newEnv(t)
	world := awstest.New(t)
	home := world.Account(awstest.RandomAccountID())
	target := world.Account(awstest.RandomAccountID())
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	const externalID = "ext-secret-0123456789"
	const secret = "wrong-purpose-secret-0123456789"
	const keyID = "AKIAWRONGPURPOSE0001"
	missing := "arn:aws:iam::" + target.ID + ":role/does-not-exist-" + awstest.RandomSuffix()
	e.createAccount(t, target.ID, store.AWSCredential{Method: "role", RoleARN: missing, ExternalID: externalID}, "", "")
	keyed := world.Account(awstest.RandomAccountID())
	e.createAccount(t, keyed.ID, store.AWSCredential{Method: "key", AccessKeyID: keyID}, secret, otherPurpose)
	p := New(e.pool, e.cipher, home.AWSConfig("us-east-1"), awsx.Options{EndpointURL: world.Endpoint})

	_, roleErr := p.For(context.Background(), target.ID, "us-east-1", "yumdrake-test")
	var apiErr smithy.APIError
	if !errors.As(roleErr, &apiErr) || apiErr.ErrorCode() == "" {
		t.Fatalf("error for a role that does not exist = %v, want an AWS error with a code", roleErr)
	}
	if !strings.Contains(roleErr.Error(), target.ID) || !strings.Contains(roleErr.Error(), apiErr.ErrorCode()) {
		t.Errorf("error %q must name the account %s and the code %s", roleErr, target.ID, apiErr.ErrorCode())
	}
	_, keyErr := p.For(context.Background(), keyed.ID, "us-east-1", "yumdrake-test")
	if keyErr == nil {
		t.Fatal("a key sealed under the other purpose was accepted")
	}
	for _, text := range []string{roleErr.Error(), keyErr.Error(), logs.String()} {
		for _, bad := range []string{externalID, secret, keyID} {
			if strings.Contains(text, bad) {
				t.Errorf("text %q contains %q", text, bad)
			}
		}
	}
}
