// Package awstest seeds and observes a Floci (local AWS emulator) for tests.
// It is for tests only: every helper fails the calling test on error.
//
// Tests that use it carry the build tag floci and read the emulator address
// from YUMDRAKE_TEST_AWS_ENDPOINT (make test-floci sets it).
package awstest

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/rcss-labs/yamdrake/internal/awsx"
)

// EndpointVariable names the environment variable that holds the emulator URL.
const EndpointVariable = "YUMDRAKE_TEST_AWS_ENDPOINT"

// World is one test's view of the shared Floci.
type World struct {
	// Endpoint is the emulator URL, for example http://localhost:4577.
	Endpoint string

	t *testing.T
}

// New returns a World for t. It fails the test when the endpoint variable is
// unset, so a missing Floci is never a silent skip.
func New(t *testing.T) *World {
	t.Helper()
	endpoint := strings.TrimRight(os.Getenv(EndpointVariable), "/")
	if endpoint == "" {
		t.Fatalf("%s is not set: start Floci (make dev-up) and run make test-floci", EndpointVariable)
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		t.Fatalf("%s is not a valid URL", EndpointVariable)
	}
	return &World{Endpoint: endpoint, t: t}
}

// randomAccounts remembers the account ids handed out by RandomAccountID: an
// id that is random and ours is a safe marker for removing what it created.
var randomAccounts sync.Map

// RandomAccountID returns a fresh 12-digit account id, so parallel runs never
// share a Floci account.
func RandomAccountID() string {
	n, err := rand.Int(rand.Reader, big.NewInt(900_000_000_000))
	if err != nil {
		panic(err)
	}
	id := fmt.Sprintf("%012d", n.Int64()+100_000_000_000)
	randomAccounts.Store(id, true)
	return id
}

// RandomSuffix returns 8 lowercase hex characters for resource names.
func RandomSuffix() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Account is one Floci account; its 12-digit id is also its access key id.
type Account struct {
	ID string

	w *World
}

// Account returns the account with the given 12-digit id.
func (w *World) Account(id string) *Account {
	w.t.Helper()
	if len(id) != 12 || strings.Trim(id, "0123456789") != "" {
		w.t.Fatalf("account id %q must be 12 digits", id)
	}
	return &Account{ID: id, w: w}
}

// Credentials are static credentials whose access key id is the account id.
func (a *Account) Credentials() aws.CredentialsProvider {
	return credentials.NewStaticCredentialsProvider(a.ID, "test", "")
}

// AWSConfig returns an SDK configuration for the account, pointed at Floci.
func (a *Account) AWSConfig(region string) aws.Config {
	return a.AWSConfigAt(region, a.w.Endpoint)
}

// AWSConfigAt is AWSConfig against another endpoint, such as a Proxy URL.
func (a *Account) AWSConfigAt(region, endpoint string) aws.Config {
	a.w.t.Helper()
	cfg, err := awsx.Config(context.Background(), region, a.Credentials(), awsx.Options{EndpointURL: endpoint})
	if err != nil {
		a.w.t.Fatalf("awstest: %v", err)
	}
	return cfg
}

// queryCall makes one signed Query-protocol POST (IAM, EC2) and returns the
// response body. The IAM and EC2 SDK modules are deliberately not dependencies.
func (a *Account) queryCall(service, region string, form url.Values) []byte {
	a.w.t.Helper()
	body := form.Encode()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, a.w.Endpoint+"/", strings.NewReader(body))
	if err != nil {
		a.w.t.Fatalf("awstest: build %s request: %v", service, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	creds, err := a.Credentials().Retrieve(context.Background())
	if err != nil {
		a.w.t.Fatalf("awstest: credentials: %v", err)
	}
	sum := sha256.Sum256([]byte(body))
	if err := v4.NewSigner().SignHTTP(context.Background(), creds, req, hex.EncodeToString(sum[:]), service, region, time.Now()); err != nil {
		a.w.t.Fatalf("awstest: sign %s request: %v", service, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.w.t.Fatalf("awstest: %s %s: %v", service, form.Get("Action"), err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		a.w.t.Fatalf("awstest: read %s response: %v", service, err)
	}
	if resp.StatusCode/100 != 2 {
		a.w.t.Fatalf("awstest: %s %s: HTTP %d: %s", service, form.Get("Action"), resp.StatusCode, out)
	}
	return out
}

// Role creates an IAM role that the account itself may assume and returns its ARN.
func (a *Account) Role(name string) (arn string) {
	a.w.t.Helper()
	trust := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::%s:root"},"Action":"sts:AssumeRole"}]}`, a.ID)
	out := a.queryCall("iam", "us-east-1", url.Values{
		"Action":                   {"CreateRole"},
		"Version":                  {"2010-05-08"},
		"RoleName":                 {name},
		"AssumeRolePolicyDocument": {trust},
	})
	var parsed struct {
		Arn string `xml:"CreateRoleResult>Role>Arn"`
	}
	if err := xml.Unmarshal(out, &parsed); err != nil || parsed.Arn == "" {
		a.w.t.Fatalf("awstest: CreateRole %s: no role ARN in response (%v): %s", name, err, out)
	}
	return parsed.Arn
}

// subnet finds a subnet of the region's default VPC with DescribeSubnets.
func (a *Account) subnet(region string) string {
	a.w.t.Helper()
	out := a.queryCall("ec2", region, url.Values{
		"Action":  {"DescribeSubnets"},
		"Version": {"2016-11-15"},
	})
	var parsed struct {
		Subnets []string `xml:"subnetSet>item>subnetId"`
	}
	if err := xml.Unmarshal(out, &parsed); err != nil || len(parsed.Subnets) == 0 {
		a.w.t.Fatalf("awstest: DescribeSubnets in %s: no subnet (%v): %s", region, err, out)
	}
	sort.Strings(parsed.Subnets)
	return parsed.Subnets[0]
}

// removeContainers force-removes the containers named by ids. It is only ever
// given ids that the caller itself caused (task ids it listed, or containers of its own random account).
func removeContainers(ids []string) {
	if len(ids) == 0 {
		return
	}
	args := append([]string{"rm", "-f"}, ids...)
	// Best effort: a container that is already gone is fine.
	_ = exec.Command("docker", args...).Run() //nolint:gosec // test helper: arguments are ours
}

// containerIDs lists container ids matching docker ps filters.
func containerIDs(filters ...string) []string {
	args := []string{"ps", "-aq"}
	for _, f := range filters {
		args = append(args, "--filter", f)
	}
	out, err := exec.Command("docker", args...).Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}

// dockerRun runs docker and fails the test with its output on error.
func (w *World) dockerRun(args ...string) string {
	w.t.Helper()
	var buf bytes.Buffer
	cmd := exec.Command("docker", args...) //nolint:gosec // test helper: arguments are ours
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		w.t.Fatalf("awstest: docker %s: %v\n%s", args[0], err, buf.String())
	}
	return strings.TrimSpace(buf.String())
}
