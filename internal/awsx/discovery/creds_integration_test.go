//go:build integration

package discovery

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go"

	"github.com/rcss-labs/yamdrake/internal/awsx"
	"github.com/rcss-labs/yamdrake/internal/store"
	"github.com/rcss-labs/yamdrake/test/awstest"
)

func roleCredential(accountID, externalID string) store.AWSCredential {
	return store.AWSCredential{Method: "role", RoleARN: "arn:aws:iam::" + accountID + ":role/target", ExternalID: externalID}
}

// stubbedBase is a base configuration whose STS answers come from stub.
func stubbedBase(stub *awstest.STSStub) aws.Config {
	return aws.Config{Region: "us-east-1", HTTPClient: stub.Client(), RetryMaxAttempts: 1,
		Credentials: aws.AnonymousCredentials{}}
}

func TestUnknownAccountIsErrNotConnected(t *testing.T) {
	e := newEnv(t)
	p := New(e.pool, e.cipher, stubbedBase(&awstest.STSStub{}), awsx.Options{})
	_, err := p.For(context.Background(), "999999999999", "us-east-1", "yumdrake-test")
	if !errors.Is(err, ErrNotConnected) {
		t.Fatalf("For an unknown account = %v, want ErrNotConnected", err)
	}
	if !strings.Contains(err.Error(), "999999999999") {
		t.Errorf("error %q does not name the account", err)
	}
}

// Floci does not enforce ExternalId, so the stub is what proves it is sent.
func TestAssumeRoleSendsRoleExternalIDAndSessionName(t *testing.T) {
	e := newEnv(t)
	stub := &awstest.STSStub{}
	p := New(e.pool, e.cipher, stubbedBase(stub), awsx.Options{})
	e.createAccount(t, "111111111111", roleCredential("111111111111", "ext-"+awstest.RandomSuffix()), "", "")
	e.createAccount(t, "222222222222", roleCredential("222222222222", ""), "", "")

	cfg, err := p.For(context.Background(), "111111111111", "eu-west-1", "yumdrake-"+serviceName+"-run-42")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if cfg.Region != "eu-west-1" {
		t.Errorf("Region = %q, want eu-west-1", cfg.Region)
	}
	calls := stub.Calls()
	if len(calls) != 1 {
		t.Fatalf("AssumeRole calls = %d, want 1", len(calls))
	}
	got := calls[0]
	stored, err := store.AWSAccountByAccountID(context.Background(), e.pool, "111111111111")
	if err != nil {
		t.Fatalf("AWSAccountByAccountID: %v", err)
	}
	want := stored.Discovery
	if got.Get("RoleArn") != want.RoleARN || got.Get("ExternalId") != want.ExternalID || want.ExternalID == "" ||
		got.Get("RoleSessionName") != "yumdrake-"+serviceName+"-run-42" {
		t.Errorf("AssumeRole sent %v, want role %s, external id %s, session yumdrake-%s-run-42", got, want.RoleARN, want.ExternalID, serviceName)
	}

	if _, err := p.For(context.Background(), "222222222222", "us-east-1", "yumdrake-"+serviceName+"-run-43"); err != nil {
		t.Fatalf("For without external id: %v", err)
	}
	if _, has := stub.Calls()[1]["ExternalId"]; has {
		t.Errorf("AssumeRole sent an ExternalId for a role without one: %v", stub.Calls()[1])
	}
}

func TestAssumeRoleFailureNamesAccountAndCode(t *testing.T) {
	e := newEnv(t)
	stub := &awstest.STSStub{FailCode: "AccessDenied"}
	p := New(e.pool, e.cipher, stubbedBase(stub), awsx.Options{})
	e.createAccount(t, "333333333333", roleCredential("333333333333", "ext-secret-value"), "", "")

	_, err := p.For(context.Background(), "333333333333", "us-east-1", "yumdrake-test")
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "AccessDenied" {
		t.Fatalf("error = %v, want an AWS error with code AccessDenied", err)
	}
	for _, want := range []string{"333333333333", "AccessDenied"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "ext-secret-value") || strings.Contains(err.Error(), "stubbed failure") {
		t.Errorf("error %q repeats the external id or the AWS message", err)
	}
}

// Concurrent callers for one account make one AssumeRole call, not one each.
func TestConcurrentCallersShareOneAssumeRole(t *testing.T) {
	e := newEnv(t)
	stub := &awstest.STSStub{}
	p := New(e.pool, e.cipher, stubbedBase(stub), awsx.Options{})
	e.createAccount(t, "444444444444", roleCredential("444444444444", ""), "", "")

	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.For(context.Background(), "444444444444", "us-east-1", "yumdrake-test"); err != nil {
				t.Errorf("For: %v", err)
			}
		}()
	}
	wg.Wait()
	if n := len(stub.Calls()); n != 1 {
		t.Errorf("AssumeRole calls = %d, want 1", n)
	}
}

// A secret sealed for the other service's purpose is never opened here.
func TestSecretOfTheOtherPurposeIsRejected(t *testing.T) {
	e := newEnv(t)
	p := New(e.pool, e.cipher, stubbedBase(&awstest.STSStub{}), awsx.Options{})
	const secret = "other-purpose-secret-0123456789"
	e.createAccount(t, "555555555555", store.AWSCredential{Method: "key", AccessKeyID: "AKIAOTHERPURPOSE1234"}, secret, otherPurpose)

	cfg, err := p.For(context.Background(), "555555555555", "us-east-1", "yumdrake-test")
	if err == nil {
		creds, _ := cfg.Credentials.Retrieve(context.Background())
		t.Fatalf("For accepted a secret sealed under %s, got key %q", otherPurpose, creds.AccessKeyID)
	}
	for _, bad := range []string{secret, "AKIAOTHERPURPOSE1234"} {
		if strings.Contains(err.Error(), bad) {
			t.Errorf("error %q contains %q", err, bad)
		}
	}
	if !strings.Contains(err.Error(), "555555555555") {
		t.Errorf("error %q does not name the account", err)
	}
}

// captureLogs routes the default logger into the returned buffer for the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

// A failure is diagnosable from the error and the log: account, AWS code and
// request id, and never the external id, a secret or a session token.
func TestFailureCarriesRequestIDInErrorAndLog(t *testing.T) {
	e := newEnv(t)
	logs := captureLogs(t)
	stub := &awstest.STSStub{FailCode: "AccessDenied", RequestID: "req-7f3a-0001"}
	p := New(e.pool, e.cipher, stubbedBase(stub), awsx.Options{})
	e.createAccount(t, "666666666666", roleCredential("666666666666", "ext-secret-value"), "", "")

	_, err := p.For(context.Background(), "666666666666", "us-east-1", "yumdrake-test")
	if err == nil {
		t.Fatal("For succeeded against a failing STS")
	}
	for name, text := range map[string]string{"error": err.Error(), "log": logs.String()} {
		for _, want := range []string{"666666666666", "AccessDenied", "req-7f3a-0001"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s %q does not contain %q", name, text, want)
			}
		}
		for _, bad := range []string{"ext-secret-value", "stub-secret", "stub-token", "stubbed failure"} {
			if strings.Contains(text, bad) {
				t.Errorf("%s %q contains %q", name, text, bad)
			}
		}
	}
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("log %q is not a warning", logs.String())
	}
}

// An unreadable secret is logged with its category (read or decrypt), never
// the cause text, the key id or the secret.
func TestUnreadableSecretLogsItsCategory(t *testing.T) {
	e := newEnv(t)
	logs := captureLogs(t)
	p := New(e.pool, e.cipher, stubbedBase(&awstest.STSStub{}), awsx.Options{})
	const secret = "category-secret-0123456789"
	e.createAccount(t, "777777777777", store.AWSCredential{Method: "key", AccessKeyID: "AKIACATEGORY0000001"}, secret, otherPurpose)

	_, err := p.For(context.Background(), "777777777777", "us-east-1", "yumdrake-test")
	if err == nil || !strings.Contains(err.Error(), "SecretUnavailable") {
		t.Fatalf("For = %v, want SecretUnavailable", err)
	}
	if !strings.Contains(logs.String(), "777777777777") || !strings.Contains(logs.String(), "cause=decrypt") {
		t.Errorf("log %q must name the account and cause=decrypt", logs.String())
	}
	for _, bad := range []string{secret, "AKIACATEGORY0000001"} {
		if strings.Contains(logs.String(), bad) || strings.Contains(err.Error(), bad) {
			t.Errorf("log or error contains %q", bad)
		}
	}
}

// The connection test must see AWS now, not credentials cached by an earlier
// call: a trust policy broken since then has to show.
func TestUncachedCallsAssumeRoleEveryTime(t *testing.T) {
	e := newEnv(t)
	stub := &awstest.STSStub{}
	p := New(e.pool, e.cipher, stubbedBase(stub), awsx.Options{})
	e.createAccount(t, "888888888888", roleCredential("888888888888", ""), "", "")
	ctx := context.Background()

	for range 2 {
		if _, err := p.For(ctx, "888888888888", "us-east-1", "yumdrake-test"); err != nil {
			t.Fatalf("For: %v", err)
		}
	}
	if n := len(stub.Calls()); n != 1 {
		t.Fatalf("cached For made %d AssumeRole calls, want 1", n)
	}
	for range 2 {
		if _, err := p.ForUncached(ctx, "888888888888", "us-east-1", "yumdrake-test"); err != nil {
			t.Fatalf("ForUncached: %v", err)
		}
	}
	if n := len(stub.Calls()); n != 3 {
		t.Errorf("AssumeRole calls after two ForUncached = %d, want 3", n)
	}
	stub.FailCode = "AccessDenied" // the trust policy breaks
	if _, err := p.ForUncached(ctx, "888888888888", "us-east-1", "yumdrake-test"); err == nil {
		t.Error("ForUncached reported success on a failing STS because of a cached credential")
	}
}
