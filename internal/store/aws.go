package store

import (
	"context"
	"fmt"
	"time"
)

// AWSCredential says how Yumdrake signs in to an AWS account. Method "role"
// uses RoleARN (and ExternalID when set); method "key" uses AccessKeyID and a
// secret access key that is stored only as ciphertext, outside this struct.
// The API shows AccessKeyID by its last 4 characters only.
type AWSCredential struct {
	Method, RoleARN, ExternalID string // Method: "role" | "key"
	AccessKeyID                 string
}

// AWSAccount is a connected AWS account. Discovery is read-only and is used by
// the orchestrator; Deploy is used by the runner. No secret is part of it.
type AWSAccount struct {
	ID                int64
	Name, AccountID   string
	Discovery, Deploy AWSCredential
	CreatedAt         time.Time
}

const awsAccountColumns = `id, name, account_id,
	discovery_method, discovery_role_arn, discovery_external_id, discovery_access_key_id,
	deploy_method, deploy_role_arn, deploy_external_id, deploy_access_key_id, created_at`

func scanAWSAccount(row interface{ Scan(...any) error }) (AWSAccount, error) {
	var a AWSAccount
	err := row.Scan(&a.ID, &a.Name, &a.AccountID,
		&a.Discovery.Method, &a.Discovery.RoleARN, &a.Discovery.ExternalID, &a.Discovery.AccessKeyID,
		&a.Deploy.Method, &a.Deploy.RoleARN, &a.Deploy.ExternalID, &a.Deploy.AccessKeyID, &a.CreatedAt)
	return a, err
}

// CreateAWSAccount stores an account with the already-encrypted secret access
// keys of its credentials (nil for the role method). It returns ErrDuplicate
// when the name or the account id is taken and ErrInvalid when a credential
// breaks the rules of its method.
func CreateAWSAccount(ctx context.Context, db DBTX, a AWSAccount, discoverySecret, deploySecret []byte) (AWSAccount, error) {
	row := db.QueryRow(ctx,
		`INSERT INTO aws_accounts (name, account_id,
			discovery_method, discovery_role_arn, discovery_external_id, discovery_access_key_id, discovery_secret_ciphertext,
			deploy_method, deploy_role_arn, deploy_external_id, deploy_access_key_id, deploy_secret_ciphertext)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING `+awsAccountColumns,
		a.Name, a.AccountID,
		a.Discovery.Method, a.Discovery.RoleARN, a.Discovery.ExternalID, a.Discovery.AccessKeyID, discoverySecret,
		a.Deploy.Method, a.Deploy.RoleARN, a.Deploy.ExternalID, a.Deploy.AccessKeyID, deploySecret)
	created, err := scanAWSAccount(row)
	if err != nil {
		return AWSAccount{}, fmt.Errorf("create aws account: %w", mapError(err))
	}
	return created, nil
}

// ListAWSAccounts returns all accounts ordered by name, without secrets.
func ListAWSAccounts(ctx context.Context, db DBTX) ([]AWSAccount, error) {
	rows, err := db.Query(ctx, "SELECT "+awsAccountColumns+" FROM aws_accounts ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("list aws accounts: %w", err)
	}
	defer rows.Close()
	var out []AWSAccount
	for rows.Next() {
		a, err := scanAWSAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("scan aws account: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list aws accounts: %w", err)
	}
	return out, nil
}

// AWSAccountByID returns the account with that row id, or ErrNotFound.
func AWSAccountByID(ctx context.Context, db DBTX, id int64) (AWSAccount, error) {
	a, err := scanAWSAccount(db.QueryRow(ctx, "SELECT "+awsAccountColumns+" FROM aws_accounts WHERE id = $1", id))
	if err != nil {
		return AWSAccount{}, fmt.Errorf("get aws account: %w", mapError(err))
	}
	return a, nil
}

// AWSAccountByAccountID returns the account with that 12-digit AWS account id,
// or ErrNotFound.
func AWSAccountByAccountID(ctx context.Context, db DBTX, accountID string) (AWSAccount, error) {
	a, err := scanAWSAccount(db.QueryRow(ctx, "SELECT "+awsAccountColumns+" FROM aws_accounts WHERE account_id = $1", accountID))
	if err != nil {
		return AWSAccount{}, fmt.Errorf("get aws account: %w", mapError(err))
	}
	return a, nil
}

// DiscoverySecret returns the ciphertext of the discovery secret access key of
// the account (nil for the role method), or ErrNotFound.
//
// There is deliberately no counterpart for the deployment secret: it is read
// only inside internal/awsx/deploy, so that code which runs in the
// orchestrator cannot call it. See internal/awsx/boundary_test.go.
func DiscoverySecret(ctx context.Context, db DBTX, accountID string) ([]byte, error) {
	var secret []byte
	if err := db.QueryRow(ctx, "SELECT discovery_secret_ciphertext FROM aws_accounts WHERE account_id = $1", accountID).Scan(&secret); err != nil {
		return nil, fmt.Errorf("get aws secret: %w", mapError(err))
	}
	return secret, nil
}
