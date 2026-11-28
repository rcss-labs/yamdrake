//go:build integration

package deploy

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rcss-labs/yumdrake/internal/awsx"
	"github.com/rcss-labs/yumdrake/internal/db"
	"github.com/rcss-labs/yumdrake/internal/secrets"
	"github.com/rcss-labs/yumdrake/internal/store"
	"github.com/rcss-labs/yumdrake/internal/testutil/testdb"
)

// Per-package parts of the credential tests, which are otherwise identical in
// internal/awsx/discovery and internal/awsx/deploy.
const (
	serviceName  = "deploy"
	ownPurpose   = awsx.PurposeDeploy    // the purpose this package opens
	otherPurpose = awsx.PurposeDiscovery // the purpose it must refuse
)

type env struct {
	pool   *pgxpool.Pool
	cipher secrets.Cipher
}

func newEnv(t *testing.T) env {
	t.Helper()
	ctx := context.Background()
	url := testdb.New(t)
	if err := db.Migrate(ctx, url); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)
	cipher, err := secrets.NewLocal([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	return env{pool: pool, cipher: cipher}
}

// createAccount stores an account whose credential for this package's service
// is credential, with secret sealed under purpose (nothing when secret is
// empty). The other service's credential is an unrelated role.
func (e env) createAccount(t *testing.T, accountID string, credential store.AWSCredential, secret, purpose string) {
	t.Helper()
	var sealed []byte
	if secret != "" {
		var err error
		if sealed, err = e.cipher.Encrypt(context.Background(), purpose, []byte(secret)); err != nil {
			t.Fatalf("Encrypt: %v", err)
		}
	}
	unrelated := store.AWSCredential{Method: "role", RoleARN: "arn:aws:iam::" + accountID + ":role/unrelated"}
	row := store.AWSAccount{Name: "account-" + accountID, AccountID: accountID, Discovery: unrelated, Deploy: unrelated}
	row.Deploy = credential
	if _, err := store.CreateAWSAccount(context.Background(), e.pool, row, nil, sealed); err != nil {
		t.Fatalf("CreateAWSAccount: %v", err)
	}
}
