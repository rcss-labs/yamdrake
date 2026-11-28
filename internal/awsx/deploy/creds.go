// Package deploy builds the deployment AWS credentials of a connected
// account. Only the runner uses it. It is the one place that reads the
// deployment secret from the database and opens it (encryption purpose
// awsx.PurposeDeploy); the orchestrator binary must never import it, and the
// boundary test in internal/awsx fails if it does or if any package it
// depends on decrypts that purpose or reads that column.
//
// For the role method the separation from discovery rests on the distinct IAM
// identities of the orchestrator and the runner and on the trust policy of the
// deploy role (design spec D8); the code boundary only keeps the orchestrator
// from reaching a deployment key stored in the database.
package deploy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rcss-labs/yumdrake/internal/awsx"
	"github.com/rcss-labs/yumdrake/internal/awsx/internal/credsource"
	"github.com/rcss-labs/yumdrake/internal/secrets"
	"github.com/rcss-labs/yumdrake/internal/store"
)

// ErrNotConnected means no connected AWS account has that account id.
var ErrNotConnected = errors.New("deploy: AWS account is not connected")

// Provider builds configurations from the stored deployment credentials.
type Provider struct {
	pool   *pgxpool.Pool
	cipher secrets.Cipher
	src    *credsource.Source
}

// New returns a Provider. base is the runner's own AWS identity, from which
// roles are assumed; nothing is resolved until For is called, so a service
// without AWS credentials still starts.
func New(pool *pgxpool.Pool, c secrets.Cipher, base aws.Config, o awsx.Options) *Provider {
	return &Provider{pool: pool, cipher: c, src: credsource.New("deploy", base, o, nil)}
}

// For returns a configuration for region that signs in to accountID with its
// deployment credential. A role is assumed with the stored external id and
// sessionName (yumdrake-runner-run-<id> for a run); a key is used as stored.
// An empty region means the base configuration's. It returns ErrNotConnected
// for an unknown account and a *credsource.Error, which names the account and
// the AWS error code and nothing secret, for an AWS failure.
func (p *Provider) For(ctx context.Context, accountID, region, sessionName string) (aws.Config, error) {
	return p.build(ctx, accountID, region, sessionName, p.src.Config)
}

// ForUncached is For without the cache: every call asks AWS for new
// credentials and stores nothing. The connection test uses it, so a trust
// policy broken since the last call shows at once.
func (p *Provider) ForUncached(ctx context.Context, accountID, region, sessionName string) (aws.Config, error) {
	return p.build(ctx, accountID, region, sessionName, p.src.ConfigUncached)
}

type configBuilder func(context.Context, credsource.Request, func(context.Context) (string, error)) (aws.Config, error)

func (p *Provider) build(ctx context.Context, accountID, region, sessionName string, config configBuilder) (aws.Config, error) {
	account, err := store.AWSAccountByAccountID(ctx, p.pool, accountID)
	if errors.Is(err, store.ErrNotFound) {
		return aws.Config{}, fmt.Errorf("account %s: %w", accountID, ErrNotConnected)
	}
	if err != nil {
		return aws.Config{}, fmt.Errorf("deploy credentials for account %s: %w", accountID, err)
	}
	secret := func(ctx context.Context) (string, error) {
		ciphertext, err := readDeploySecret(ctx, p.pool, accountID)
		if err != nil {
			return "", credsource.ErrSecretRead
		}
		plain, err := p.cipher.Decrypt(ctx, awsx.PurposeDeploy, ciphertext)
		if err != nil {
			return "", credsource.ErrSecretDecrypt
		}
		return string(plain), nil
	}
	return config(ctx, credsource.Request{
		AccountID: accountID, Region: region, SessionName: sessionName, Credential: account.Deploy,
	}, secret)
}

// readDeploySecret returns the ciphertext of the deployment secret access key
// (nil for the role method). It is unexported on purpose: this is the only
// reader of the column outside the administration API's insert.
func readDeploySecret(ctx context.Context, pool *pgxpool.Pool, accountID string) ([]byte, error) {
	var ciphertext []byte
	err := pool.QueryRow(ctx, "SELECT deploy_secret_ciphertext FROM aws_accounts WHERE account_id = $1", accountID).Scan(&ciphertext)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotConnected
	}
	if err != nil {
		return nil, fmt.Errorf("read deploy secret: %w", err)
	}
	return ciphertext, nil
}

// WithLogger sets the logger credential failures are written to, the app's
// structured logger, and returns p. Without it the default logger is used.
func (p *Provider) WithLogger(log *slog.Logger) *Provider {
	p.src.WithLogger(log)
	return p
}
