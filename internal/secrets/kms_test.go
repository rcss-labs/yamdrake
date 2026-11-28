package secrets

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
)

// stubKMS behaves like KMS for what the cipher relies on: a ciphertext is only
// decrypted by the key and with the encryption context it was made with. It
// records the contexts it was sent so a test can see them.
type stubKMS struct {
	encryptContexts []map[string]string
	decryptContexts []map[string]string
	keyIDs          []string
}

func (s *stubKMS) Encrypt(_ context.Context, in *kms.EncryptInput, _ ...func(*kms.Options)) (*kms.EncryptOutput, error) {
	s.encryptContexts = append(s.encryptContexts, maps.Clone(in.EncryptionContext))
	s.keyIDs = append(s.keyIDs, aws.ToString(in.KeyId))
	// "Ciphertext": the context and key bound to the data, reversed so the
	// plaintext does not appear in it.
	blob := append([]byte(in.EncryptionContext["purpose"]+"|"+aws.ToString(in.KeyId)+"|"), reverse(in.Plaintext)...)
	return &kms.EncryptOutput{CiphertextBlob: blob}, nil
}

func (s *stubKMS) Decrypt(_ context.Context, in *kms.DecryptInput, _ ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	s.decryptContexts = append(s.decryptContexts, maps.Clone(in.EncryptionContext))
	prefix := []byte(in.EncryptionContext["purpose"] + "|" + aws.ToString(in.KeyId) + "|")
	if !bytes.HasPrefix(in.CiphertextBlob, prefix) {
		return nil, errors.New("InvalidCiphertextException")
	}
	return &kms.DecryptOutput{Plaintext: reverse(in.CiphertextBlob[len(prefix):])}, nil
}

func reverse(b []byte) []byte {
	out := make([]byte, len(b))
	for i := range b {
		out[len(b)-1-i] = b[i]
	}
	return out
}

func TestKMSCipherSendsAndChecksThePurposeAsEncryptionContext(t *testing.T) {
	ctx := context.Background()
	stub := &stubKMS{}
	c := newKMSWith(stub, "key-1")

	sealed, err := c.Encrypt(ctx, "aws-discovery", []byte("secret-access-key"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Contains(sealed, []byte("secret-access-key")) {
		t.Error("ciphertext holds the plaintext")
	}
	if len(stub.encryptContexts) != 1 || len(stub.encryptContexts[0]) != 1 || stub.encryptContexts[0]["purpose"] != "aws-discovery" {
		t.Fatalf("Encrypt context = %v, want exactly {purpose: aws-discovery}", stub.encryptContexts)
	}
	if stub.keyIDs[0] != "key-1" {
		t.Errorf("Encrypt key id = %q", stub.keyIDs[0])
	}

	got, err := c.Decrypt(ctx, "aws-discovery", sealed)
	if err != nil || string(got) != "secret-access-key" {
		t.Fatalf("Decrypt = %q, %v", got, err)
	}
	if len(stub.decryptContexts) != 1 || stub.decryptContexts[0]["purpose"] != "aws-discovery" || len(stub.decryptContexts[0]) != 1 {
		t.Fatalf("Decrypt context = %v, want exactly {purpose: aws-discovery}", stub.decryptContexts)
	}

	// Another purpose sends another context, which KMS (here the stub) refuses;
	// the error stays generic.
	if got, err := c.Decrypt(ctx, "aws-deploy", sealed); err == nil || got != nil || err.Error() != errDecrypt.Error() {
		t.Fatalf("Decrypt with the wrong purpose = %q, %v; want the generic error", got, err)
	}
	if stub.decryptContexts[1]["purpose"] != "aws-deploy" {
		t.Errorf("the wrong-purpose Decrypt sent context %v", stub.decryptContexts[1])
	}
}

func TestKMSCipherRefusesForeignOrShortCiphertext(t *testing.T) {
	c := newKMSWith(&stubKMS{}, "key-1")
	local := newTestCipher(t)
	localSealed, err := local.Encrypt(context.Background(), "aws-deploy", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	for name, blob := range map[string][]byte{"empty": nil, "local format": localSealed, "version only": {formatVersionKMS}} {
		if got, err := c.Decrypt(context.Background(), "aws-deploy", blob); err == nil || got != nil {
			t.Errorf("%s: Decrypt = %q, %v; want an error", name, got, err)
		}
	}
}
