// Package credsource turns one stored AWS credential (a role to assume or a
// static key) into an aws.Config, with a per-account cache. It is shared by
// internal/awsx/discovery and internal/awsx/deploy and knows nothing about
// either: it never reads or decrypts a secret itself, the caller hands it a
// loader for the one secret of its own purpose.
//
// It sits under internal/awsx/internal so that only the two services can use
// it. For the role method the separation between discovery and deployment
// rests on the distinct IAM identities of the orchestrator and the runner and
// on the trust policy of the deploy role (design spec D8), not on this code:
// the code boundary (internal/awsx/boundary_test.go) only keeps the
// orchestrator from reaching a deployment key stored in the database.
package credsource

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	"github.com/rcss-labs/yumdrake/internal/awsx"
	"github.com/rcss-labs/yumdrake/internal/store"
)

const (
	// refreshMargin is how long before expiry a cached credential is replaced.
	// Static keys are re-read this often too, so a rotated key is picked up.
	refreshMargin = 5 * time.Minute
	// assumeDuration is the longest session a chained role allows.
	assumeDuration = 3600
	// defaultRegion is used when neither the caller nor the base configuration
	// names one; STS answers from any region.
	defaultRegion = "us-east-1"
)

// Request names the credential to build.
type Request struct {
	AccountID, Region, SessionName string
	Credential                     store.AWSCredential
}

// Causes a secret loader reports when the secret cannot be obtained. Only the
// category is logged: the underlying error could carry key material.
var (
	ErrSecretRead    = errors.New("credsource: secret could not be read")
	ErrSecretDecrypt = errors.New("credsource: secret could not be decrypted")
)

// Error is a failure to obtain credentials. It names the service and the
// account and carries the AWS error code, never a message from AWS, a key, a
// secret or a session token. It satisfies smithy.APIError so callers that
// report AWS error codes need no special case.
// RequestID is the AWS request id when AWS answered, for support cases.
type Error struct{ Service, AccountID, Code, RequestID string }

func (e *Error) Error() string {
	text := fmt.Sprintf("%s credentials for account %s: AWS error %s", e.Service, e.AccountID, e.Code)
	if e.RequestID != "" {
		text += " (request id " + e.RequestID + ")"
	}
	return text
}

// ErrorCode is the AWS error code, "Timeout" or "RequestFailed" when AWS gave none.
func (e *Error) ErrorCode() string { return e.Code }

// ErrorMessage is deliberately the same as Error.
func (e *Error) ErrorMessage() string { return e.Error() }

// ErrorFault reports the caller as the likely origin.
func (e *Error) ErrorFault() smithy.ErrorFault { return smithy.FaultUnknown }

// Source builds and caches credentials for one service.
type Source struct {
	service string
	base    aws.Config
	now     func() time.Time
	logger  *slog.Logger // nil: slog.Default()

	mu      sync.Mutex
	entries map[key]*entry
}

type key struct{ account, region, session string }

// entry is one cached credential. Its own lock lets callers for different
// accounts proceed while callers for one account wait for a single fetch.
type entry struct {
	mu          sync.Mutex
	fingerprint string
	creds       aws.Credentials
	refreshAt   time.Time
	have        bool
}

// New returns a Source for the named service ("discovery" or "deploy"). base
// is the service's own identity: the credentials it assumes roles from. now is
// the clock; nil means time.Now.
func New(service string, base aws.Config, o awsx.Options, now func() time.Time) *Source {
	base = base.Copy()
	if base.BaseEndpoint == nil && o.EndpointURL != "" {
		base.BaseEndpoint = aws.String(o.EndpointURL)
	}
	if now == nil {
		now = time.Now
	}
	return &Source{service: service, base: base, now: now, entries: map[key]*entry{}}
}

// WithLogger sets where failures are logged (the app's structured logger) and
// returns s. Call it before the Source is shared.
func (s *Source) WithLogger(log *slog.Logger) *Source {
	s.logger = log
	return s
}

// Config returns a configuration for req.Region that signs with the
// credential. secret loads the decrypted secret access key; it is called only
// for the key method and only when the cache has nothing fresh. The returned
// credentials provider refreshes itself from the cache, so a long-lived
// configuration keeps working past the first credentials' expiry.
//
// Failures are reported eagerly, here, as *Error.
func (s *Source) Config(ctx context.Context, req Request, secret func(context.Context) (string, error)) (aws.Config, error) {
	region := req.Region
	if region == "" {
		region = s.base.Region
	}
	if region == "" {
		region = defaultRegion
	}
	req.Region = region
	e := s.entry(key{req.AccountID, region, req.SessionName})
	provider := aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
		return s.credentials(ctx, e, req, secret)
	})
	if _, err := provider(ctx); err != nil {
		s.forget(key{req.AccountID, region, req.SessionName}, e)
		return aws.Config{}, err
	}
	cfg := s.base.Copy()
	cfg.Region = region
	cfg.Credentials = provider
	return cfg, nil
}

// ConfigUncached is Config without the cache: it always asks AWS for new
// credentials and stores nothing, so a caller that must see AWS as it is now
// (the connection test) never gets an earlier answer. The configuration it
// returns holds those credentials until they expire.
func (s *Source) ConfigUncached(ctx context.Context, req Request, secret func(context.Context) (string, error)) (aws.Config, error) {
	if req.Region == "" {
		req.Region = s.base.Region
	}
	if req.Region == "" {
		req.Region = defaultRegion
	}
	creds, err := s.fetch(ctx, req, secret)
	if err != nil {
		return aws.Config{}, err
	}
	cfg := s.base.Copy()
	cfg.Region = req.Region
	cfg.Credentials = aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) { return creds, nil })
	return cfg, nil
}

// forget drops the entry of a fetch that failed, so failures do not pile up.
func (s *Source) forget(k key, e *entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries[k] == e && e.mu.TryLock() {
		defer e.mu.Unlock()
		if !e.have {
			delete(s.entries, k)
		}
	}
}

// entry returns the cache entry of k, dropping long-expired ones so a runner
// that uses a session name per run does not grow without bound.
func (s *Source) entry(k key) *entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for other, e := range s.entries {
		if other != k && e.stale(now.Add(-time.Hour)) {
			delete(s.entries, other)
		}
	}
	e, ok := s.entries[k]
	if !ok {
		e = &entry{}
		s.entries[k] = e
	}
	return e
}

// stale reports an entry nobody holds that never got credentials or whose
// credentials ran out before t.
func (e *entry) stale(t time.Time) bool {
	if !e.mu.TryLock() {
		return false // in use
	}
	defer e.mu.Unlock()
	return !e.have || e.refreshAt.Before(t)
}

func (s *Source) credentials(ctx context.Context, e *entry, req Request, secret func(context.Context) (string, error)) (aws.Credentials, error) {
	c := req.Credential
	fingerprint := c.Method + "|" + c.RoleARN + "|" + c.ExternalID + "|" + c.AccessKeyID
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.have && e.fingerprint == fingerprint && s.now().Before(e.refreshAt) {
		return e.creds, nil
	}
	creds, err := s.fetch(ctx, req, secret)
	if err != nil {
		return aws.Credentials{}, err
	}
	e.fingerprint, e.creds, e.have = fingerprint, creds, true
	e.refreshAt = s.now().Add(refreshMargin) // static key
	if creds.CanExpire {
		e.refreshAt = creds.Expires.Add(-refreshMargin)
	}
	return creds, nil
}

func (s *Source) fetch(ctx context.Context, req Request, secret func(context.Context) (string, error)) (aws.Credentials, error) {
	c := req.Credential
	switch c.Method {
	case "key":
		plain, err := secret(ctx)
		if err != nil {
			cause := "unknown"
			switch {
			case errors.Is(err, ErrSecretRead):
				cause = "read"
			case errors.Is(err, ErrSecretDecrypt):
				cause = "decrypt"
			}
			return aws.Credentials{}, s.failure(req, "SecretUnavailable", "cause", cause)
		}
		return aws.Credentials{AccessKeyID: c.AccessKeyID, SecretAccessKey: plain, Source: "yumdrake-" + s.service + "-key"}, nil
	case "role":
		return s.assume(ctx, req)
	default:
		return aws.Credentials{}, s.failure(req, "UnsupportedMethod", "method", c.Method)
	}
}

func (s *Source) assume(ctx context.Context, req Request) (aws.Credentials, error) {
	c := req.Credential
	cfg := s.base.Copy()
	cfg.Region = req.Region
	input := &sts.AssumeRoleInput{
		RoleArn:         aws.String(c.RoleARN),
		RoleSessionName: aws.String(req.SessionName),
		DurationSeconds: aws.Int32(assumeDuration),
	}
	if c.ExternalID != "" {
		input.ExternalId = aws.String(c.ExternalID)
	}
	out, err := sts.NewFromConfig(cfg).AssumeRole(ctx, input)
	if err != nil {
		return aws.Credentials{}, s.failureFrom(req, err)
	}
	got := out.Credentials
	if got == nil || got.AccessKeyId == nil || got.SecretAccessKey == nil || got.SessionToken == nil || got.Expiration == nil {
		return aws.Credentials{}, s.failure(req, "RequestFailed")
	}
	return aws.Credentials{
		AccessKeyID: *got.AccessKeyId, SecretAccessKey: *got.SecretAccessKey, SessionToken: *got.SessionToken,
		Source: "yumdrake-" + s.service + "-assume-role", CanExpire: true, Expires: *got.Expiration,
	}, nil
}

// failure builds the error and logs it with the details that help diagnosis
// (extra is key/value pairs of non-secret facts).
func (s *Source) failure(req Request, code string, extra ...any) *Error {
	return s.log(&Error{Service: s.service, AccountID: req.AccountID, Code: code}, extra...)
}

func (s *Source) failureFrom(req Request, err error) *Error {
	failure := &Error{Service: s.service, AccountID: req.AccountID, Code: errorCode(err)}
	var withID interface{ ServiceRequestID() string }
	if errors.As(err, &withID) {
		failure.RequestID = withID.ServiceRequestID()
	}
	return s.log(failure)
}

func (s *Source) log(e *Error, extra ...any) *Error {
	attrs := append([]any{"aws_service", e.Service, "account", e.AccountID, "code", e.Code, "request_id", e.RequestID}, extra...)
	logger := s.logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn("AWS credentials could not be obtained", attrs...)
	return e
}

// errorCode is the AWS error code of err; the text of an SDK error can carry
// request details, so only the code is kept.
func errorCode(err error) string {
	var apiErr smithy.APIError
	switch {
	case errors.As(err, &apiErr):
		return apiErr.ErrorCode()
	case errors.Is(err, context.DeadlineExceeded):
		return "Timeout"
	}
	return "RequestFailed"
}
