package secrets

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/smithy-go"
)

// formatVersionKMS is the first byte of every ciphertext made by the KMS
// cipher, so it is told apart from a local one.
const formatVersionKMS byte = 2

// kmsAPI is the part of *kms.Client the cipher uses.
type kmsAPI interface {
	Encrypt(ctx context.Context, in *kms.EncryptInput, opts ...func(*kms.Options)) (*kms.EncryptOutput, error)
	Decrypt(ctx context.Context, in *kms.DecryptInput, opts ...func(*kms.Options)) (*kms.DecryptOutput, error)
}

type kmsCipher struct {
	client kmsAPI
	keyID  string
}

// NewKMS returns a cipher backed by an AWS KMS key. The purpose is passed to
// KMS as the encryption context {"purpose": purpose}, so KMS itself refuses to
// decrypt a value for another purpose. Decrypt also names the key, so a value
// made under another key is refused. Values are limited to KMS's 4096 bytes.
func NewKMS(client *kms.Client, keyID string) Cipher {
	return newKMSWith(client, keyID)
}

func newKMSWith(client kmsAPI, keyID string) Cipher {
	return &kmsCipher{client: client, keyID: keyID}
}

func (c *kmsCipher) Encrypt(ctx context.Context, purpose string, plaintext []byte) ([]byte, error) {
	out, err := c.client.Encrypt(ctx, &kms.EncryptInput{
		KeyId:             &c.keyID,
		Plaintext:         plaintext,
		EncryptionContext: map[string]string{"purpose": purpose},
	})
	if err != nil {
		return nil, fmt.Errorf("secrets: kms encrypt failed (%s)", awsCode(err))
	}
	return append([]byte{formatVersionKMS}, out.CiphertextBlob...), nil
}

func (c *kmsCipher) Decrypt(ctx context.Context, purpose string, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < 2 || ciphertext[0] != formatVersionKMS {
		return nil, errDecrypt
	}
	out, err := c.client.Decrypt(ctx, &kms.DecryptInput{
		KeyId:             &c.keyID,
		CiphertextBlob:    ciphertext[1:],
		EncryptionContext: map[string]string{"purpose": purpose},
	})
	if err != nil {
		return nil, errDecrypt
	}
	return out.Plaintext, nil
}

// awsCode is the AWS error code of err, or "unknown": the text of an SDK error
// can carry request details, the code cannot.
func awsCode(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode()
	}
	return "unknown"
}
