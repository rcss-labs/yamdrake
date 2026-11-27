// Package secrets encrypts credentials before they are stored.
package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// formatVersionLocal is the first byte of every ciphertext made by the local
// cipher. It lets a future format (or the KMS cipher) be told apart.
const formatVersionLocal byte = 1

const keySize = 32

// errDecrypt is deliberately generic: it never carries plaintext or key
// material, and does not say whether the purpose, the data or the key was wrong.
var errDecrypt = errors.New("secrets: cannot decrypt value")

// Cipher encrypts and decrypts small secrets. The purpose (for example
// "github-connection") is bound into the ciphertext, so a value encrypted for
// one purpose cannot be decrypted for another.
type Cipher interface {
	Encrypt(ctx context.Context, purpose string, plaintext []byte) ([]byte, error)
	Decrypt(ctx context.Context, purpose string, ciphertext []byte) ([]byte, error)
}

type localCipher struct {
	aead cipher.AEAD
}

// NewLocal returns an AES-256-GCM cipher for development and tests. key must
// be exactly 32 bytes. Ciphertext layout: version byte, nonce, sealed data.
func NewLocal(key []byte) (Cipher, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("secrets: local key must be %d bytes, got %d", keySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("secrets: cannot initialise the local cipher")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("secrets: cannot initialise the local cipher")
	}
	return &localCipher{aead: aead}, nil
}

func (c *localCipher) Encrypt(_ context.Context, purpose string, plaintext []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, errors.New("secrets: cannot generate a nonce")
	}
	out := make([]byte, 0, 1+len(nonce)+len(plaintext)+c.aead.Overhead())
	out = append(out, formatVersionLocal)
	out = append(out, nonce...)
	// The version byte is part of the authenticated data so it cannot be altered.
	return c.aead.Seal(out, nonce, plaintext, additionalData(formatVersionLocal, purpose)), nil
}

func (c *localCipher) Decrypt(_ context.Context, purpose string, ciphertext []byte) ([]byte, error) {
	nonceSize := c.aead.NonceSize()
	if len(ciphertext) < 1+nonceSize+c.aead.Overhead() || ciphertext[0] != formatVersionLocal {
		return nil, errDecrypt
	}
	nonce := ciphertext[1 : 1+nonceSize]
	plaintext, err := c.aead.Open(nil, nonce, ciphertext[1+nonceSize:], additionalData(ciphertext[0], purpose))
	if err != nil {
		return nil, errDecrypt
	}
	return plaintext, nil
}

func additionalData(version byte, purpose string) []byte {
	return append([]byte{version}, purpose...)
}
