package app

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/rcss-labs/yamdrake/internal/config"
)

func TestNewCipherLocalWorksWithoutAWS(t *testing.T) {
	cfg := config.Common{SecretSettings: config.SecretSettings{SecretsMode: "local", LocalSecretKey: config.Secret(strings.Repeat("k", 32))}}
	c, err := newCipher(cfg, aws.Config{})
	if err != nil {
		t.Fatalf("newCipher: %v", err)
	}
	sealed, err := c.Encrypt(context.Background(), "p", []byte("v"))
	if err != nil || len(sealed) == 0 {
		t.Fatalf("Encrypt = %v, %v", sealed, err)
	}
}

func TestNewCipherKMSNeedsARegionAndNamesNoKey(t *testing.T) {
	cfg := config.Common{SecretSettings: config.SecretSettings{SecretsMode: "kms", KMSKeyID: "alias/very-private-name"}}
	if _, err := newCipher(cfg, aws.Config{}); err == nil || strings.Contains(err.Error(), "very-private-name") {
		t.Fatalf("kms without a region: error = %v, want one that does not name the key", err)
	}
	if c, err := newCipher(cfg, aws.Config{Region: "eu-west-1"}); err != nil || c == nil {
		t.Fatalf("kms with a region = %v, %v", c, err)
	}
}
