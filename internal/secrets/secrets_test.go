package secrets

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func testKey() []byte { return bytes.Repeat([]byte{7}, 32) }

func newTestCipher(t *testing.T) Cipher {
	t.Helper()
	c, err := NewLocal(testKey())
	if err != nil {
		t.Fatalf("NewLocal() error = %v", err)
	}
	return c
}

func TestRoundTrip(t *testing.T) {
	c := newTestCipher(t)
	ctx := context.Background()
	for _, plaintext := range [][]byte{[]byte("ghp_exampletoken"), {}, bytes.Repeat([]byte("x"), 5000)} {
		sealed, err := c.Encrypt(ctx, "github-connection", plaintext)
		if err != nil {
			t.Fatalf("Encrypt() error = %v", err)
		}
		got, err := c.Decrypt(ctx, "github-connection", sealed)
		if err != nil {
			t.Fatalf("Decrypt() error = %v", err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Errorf("round trip of %d bytes returned %d different bytes", len(plaintext), len(got))
		}
	}
}

func TestSamePlaintextEncryptsDifferently(t *testing.T) {
	c := newTestCipher(t)
	a, errA := c.Encrypt(context.Background(), "p", []byte("same"))
	b, errB := c.Encrypt(context.Background(), "p", []byte("same"))
	if errA != nil || errB != nil {
		t.Fatalf("Encrypt() errors = %v, %v", errA, errB)
	}
	if bytes.Equal(a, b) {
		t.Error("two encryptions of the same plaintext are identical")
	}
}

func TestWrongPurposeFails(t *testing.T) {
	c := newTestCipher(t)
	sealed, err := c.Encrypt(context.Background(), "github-connection", []byte("secret-value"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Decrypt(context.Background(), "other-purpose", sealed)
	if err == nil {
		t.Fatal("Decrypt() with a different purpose succeeded")
	}
	if got != nil {
		t.Errorf("Decrypt() returned %d bytes with an error", len(got))
	}
}

func TestTamperedCiphertextFails(t *testing.T) {
	c := newTestCipher(t)
	sealed, err := c.Encrypt(context.Background(), "p", []byte("secret-value"))
	if err != nil {
		t.Fatal(err)
	}
	// Version byte, a nonce byte, and the last (tag) byte.
	for _, pos := range []int{0, 1, len(sealed) - 1} {
		tampered := bytes.Clone(sealed)
		tampered[pos] ^= 0x01
		got, err := c.Decrypt(context.Background(), "p", tampered)
		if err == nil {
			t.Errorf("flipping byte %d: Decrypt() succeeded", pos)
		}
		if got != nil {
			t.Errorf("flipping byte %d: Decrypt() returned plaintext with an error", pos)
		}
		if err != nil && strings.Contains(err.Error(), "secret-value") {
			t.Errorf("error text contains plaintext: %v", err)
		}
	}
}

func TestTruncatedCiphertextFails(t *testing.T) {
	c := newTestCipher(t)
	sealed, err := c.Encrypt(context.Background(), "p", []byte("secret-value"))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, 1, 5, 13, len(sealed) - 1} {
		got, err := c.Decrypt(context.Background(), "p", sealed[:n])
		if err == nil {
			t.Errorf("truncated to %d bytes: Decrypt() succeeded", n)
		}
		if got != nil {
			t.Errorf("truncated to %d bytes: plaintext returned with an error", n)
		}
	}
}

func TestNewLocalRejectsWrongKeyLength(t *testing.T) {
	for _, n := range []int{16, 33} {
		if _, err := NewLocal(make([]byte, n)); err == nil {
			t.Errorf("NewLocal(%d bytes) succeeded, want an error", n)
		}
	}
}
