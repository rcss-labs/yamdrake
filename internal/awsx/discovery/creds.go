// Package discovery builds the read-only AWS credentials of a connected
// account. The orchestrator uses it; it can open only the discovery secret
// (encryption purpose awsx.PurposeDiscovery) and has no way to reach the
// deployment credential, which lives in internal/awsx/deploy. The boundary
// test in internal/awsx keeps it that way.
package discovery

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rcss-labs/yamdrake/internal/awsx"
	"github.com/rcss-labs/yamdrake/internal/awsx/credsource"
	"github.com/rcss-labs/yamdrake/internal/secrets"
	"github.com/rcss-labs/yamdrake/internal/store"
)

// ErrNotConnected means no connected AWS account has that account id.
var ErrNotConnected = errors.New("discovery: AWS account is not connected")

// Provider builds configurations from the stored discovery credentials.
type Provider struct {
	pool   *pgxpool.Pool
	cipher secrets.Cipher
	src    *credsource.Source
}

// New returns a Provider. base is the orchestrator's own AWS identity, from
// which roles are assumed; nothing is resolved until For is called, so a
// service without AWS credentials still starts.
func New(pool *pgxpool.Pool, c secrets.Cipher, base aws.Config, o awsx.Options) *Provider {
	return &Provider{pool: pool, cipher: c, src: credsource.New("discovery", base, o, nil)}
}

// For returns a configuration for region that signs in to accountID with its
// discovery credential. A role is assumed with the stored external id and
// sessionName (yumdrake-orchestrator-run-<id> for a run); a key is used as
// stored. An empty region means the base configuration's. It returns
// ErrNotConnected for an unknown account and a *credsource.Error, which names
// the account and the AWS error code and nothing secret, for an AWS failure.
func (p *Provider) For(ctx context.Context, accountID, region, sessionName string) (aws.Config, error) {
	account, err := store.AWSAccountByAccountID(ctx, p.pool, accountID)
	if errors.Is(err, store.ErrNotFound) {
		return aws.Config{}, fmt.Errorf("account %s: %w", accountID, ErrNotConnected)
	}
	if err != nil {
		return aws.Config{}, fmt.Errorf("discovery credentials for account %s: %w", accountID, err)
	}
	secret := func(ctx context.Context) (string, error) {
		ciphertext, err := store.DiscoverySecret(ctx, p.pool, accountID)
		if err != nil {
			return "", err
		}
		plain, err := p.cipher.Decrypt(ctx, awsx.PurposeDiscovery, ciphertext)
		return string(plain), err
	}
	return p.src.Config(ctx, credsource.Request{
		AccountID: accountID, Region: region, SessionName: sessionName, Credential: account.Discovery,
	}, secret)
}
