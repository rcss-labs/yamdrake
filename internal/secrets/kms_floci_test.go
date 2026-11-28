//go:build floci

package secrets_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/rcss-labs/yamdrake/internal/secrets"
	"github.com/rcss-labs/yamdrake/test/awstest"
)

func TestKMSCipherRoundTripAndWrongPurposeFails(t *testing.T) {
	ctx := context.Background()
	world := awstest.New(t)
	account := world.Account(awstest.RandomAccountID())
	keyID := account.KMSKey("us-east-1")
	cipher := secrets.NewKMS(kms.NewFromConfig(account.AWSConfig("us-east-1")), keyID)

	sealed, err := cipher.Encrypt(ctx, "aws-discovery", []byte("wJalrXUtnFEMI/K7MDENG/bPxRfiCY"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	got, err := cipher.Decrypt(ctx, "aws-discovery", sealed)
	if err != nil || string(got) != "wJalrXUtnFEMI/K7MDENG/bPxRfiCY" {
		t.Fatalf("Decrypt = %q, %v", got, err)
	}
	if got, err := cipher.Decrypt(ctx, "aws-deploy", sealed); err == nil {
		t.Fatalf("Decrypt under another purpose succeeded with %q", got)
	}
}
