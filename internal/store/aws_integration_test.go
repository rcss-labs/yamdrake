//go:build integration

package store

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAWSAccountRoundTripStoresOnlyCiphertext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	discoveryCipher := []byte{0x00, 0xfe, 'd', 'i', 's', 'c'}
	deployCipher := []byte{0x01, 0xff, 'd', 'e', 'p', 'l'}

	created, err := CreateAWSAccount(ctx, pool, AWSAccount{
		Name: "prod", AccountID: "123456789012",
		Discovery: AWSCredential{Method: "key", AccessKeyID: "AKIADISCOVERY00001234"},
		Deploy:    AWSCredential{Method: "role", RoleARN: "arn:aws:iam::123456789012:role/deploy", ExternalID: "ext-1"},
	}, discoveryCipher, nil)
	if err != nil {
		t.Fatalf("CreateAWSAccount: %v", err)
	}
	if created.ID == 0 || created.CreatedAt.IsZero() || created.Discovery.AccessKeyID != "AKIADISCOVERY00001234" || created.Deploy.RoleARN == "" {
		t.Fatalf("created = %+v", created)
	}

	// The role method stores no secret; the key method stores the ciphertext it was given.
	gotDiscovery, err := DiscoverySecret(ctx, pool, "123456789012")
	if err != nil || !bytes.Equal(gotDiscovery, discoveryCipher) {
		t.Fatalf("DiscoverySecret = %v, %v; want the ciphertext", gotDiscovery, err)
	}
	if got := deployCiphertext(t, pool, "123456789012"); got != nil {
		t.Fatalf("deploy ciphertext of a role account = %v; want nil", got)
	}

	// The same ciphertext columns are separate: a deploy key secret is not the discovery one.
	if _, err := CreateAWSAccount(ctx, pool, AWSAccount{
		Name: "dev", AccountID: "210987654321",
		Discovery: AWSCredential{Method: "role", RoleARN: "arn:aws:iam::210987654321:role/read"},
		Deploy:    AWSCredential{Method: "key", AccessKeyID: "AKIADEPLOY0000005678"},
	}, nil, deployCipher); err != nil {
		t.Fatalf("CreateAWSAccount dev: %v", err)
	}
	if got := deployCiphertext(t, pool, "210987654321"); !bytes.Equal(got, deployCipher) {
		t.Fatalf("deploy ciphertext = %v, want the one stored", got)
	}
	if got, err := DiscoverySecret(ctx, pool, "210987654321"); err != nil || got != nil {
		t.Fatalf("DiscoverySecret of a role account = %v, %v; want nil", got, err)
	}

	list, err := ListAWSAccounts(ctx, pool)
	if err != nil || len(list) != 2 || list[0].Name != "dev" || list[1].Name != "prod" {
		t.Fatalf("ListAWSAccounts = %+v, %v; want dev, prod by name", list, err)
	}
	byID, err := AWSAccountByAccountID(ctx, pool, "123456789012")
	if err != nil || byID.ID != created.ID || byID.Deploy.ExternalID != "ext-1" {
		t.Fatalf("AWSAccountByAccountID = %+v, %v", byID, err)
	}
	byKey, err := AWSAccountByID(ctx, pool, created.ID)
	if err != nil || byKey.AccountID != "123456789012" {
		t.Fatalf("AWSAccountByID = %+v, %v", byKey, err)
	}
	if _, err := AWSAccountByAccountID(ctx, pool, "999999999999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown account error = %v, want ErrNotFound", err)
	}
	if _, err := DiscoverySecret(ctx, pool, "999999999999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DiscoverySecret of an unknown account = %v, want ErrNotFound", err)
	}

	// A key credential without its secret is refused by the database too.
	if _, err := CreateAWSAccount(ctx, pool, AWSAccount{
		Name: "broken", AccountID: "555555555555",
		Discovery: AWSCredential{Method: "key", AccessKeyID: "AKIA"}, Deploy: AWSCredential{Method: "role", RoleARN: "arn:aws:iam::555555555555:role/x"},
	}, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("key credential without a secret error = %v, want ErrInvalid", err)
	}
}

func TestDuplicateAccountIDIsErrDuplicate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	role := func(account string) AWSCredential {
		return AWSCredential{Method: "role", RoleARN: "arn:aws:iam::" + account + ":role/x"}
	}
	if _, err := CreateAWSAccount(ctx, pool, AWSAccount{Name: "one", AccountID: "111111111111", Discovery: role("111111111111"), Deploy: role("111111111111")}, nil, nil); err != nil {
		t.Fatalf("first create: %v", err)
	}
	same := func(name, account string) error {
		_, err := CreateAWSAccount(ctx, pool, AWSAccount{Name: name, AccountID: account, Discovery: role(account), Deploy: role(account)}, nil, nil)
		return err
	}
	if err := same("two", "111111111111"); !errors.Is(err, ErrDuplicate) {
		t.Errorf("same account id error = %v, want ErrDuplicate", err)
	}
	if err := same("one", "222222222222"); !errors.Is(err, ErrDuplicate) {
		t.Errorf("same name error = %v, want ErrDuplicate", err)
	}
	if err := same("three", "not-twelve"); !errors.Is(err, ErrInvalid) {
		t.Errorf("malformed account id error = %v, want ErrInvalid", err)
	}
}

// deployCiphertext reads the deploy column directly: the store has no reader
// for it on purpose (only internal/awsx/deploy reads it).
func deployCiphertext(t *testing.T, pool *pgxpool.Pool, accountID string) []byte {
	t.Helper()
	var ciphertext []byte
	if err := pool.QueryRow(context.Background(), "SELECT deploy_secret_ciphertext FROM aws_accounts WHERE account_id = $1", accountID).Scan(&ciphertext); err != nil {
		t.Fatalf("read the deploy ciphertext: %v", err)
	}
	return ciphertext
}
