package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestHashAndVerifyPassword(t *testing.T) {
	t.Parallel()
	const password = "correct horse battery"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatal("hash does not start with the argon2id parameter segment")
	}
	if ok, err := VerifyPassword(hash, password); err != nil || !ok {
		t.Fatalf("VerifyPassword(right) = %v, %v; want true, nil", ok, err)
	}
	if ok, err := VerifyPassword(hash, "correct horse batterz"); err != nil || ok {
		t.Fatalf("VerifyPassword(wrong) = %v, %v; want false, nil", ok, err)
	}
	other, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword again: %v", err)
	}
	if other == hash {
		t.Fatal("two hashes of one password are identical; the salt is not random")
	}
}

func TestHashPasswordRejectsShort(t *testing.T) {
	t.Parallel()
	if _, err := HashPassword("elevenchars"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("11 characters: error = %v, want ErrWeakPassword", err)
	}
	// Length counts characters, not bytes: 12 multi-byte characters are enough.
	if _, err := HashPassword(strings.Repeat("é", 12)); err != nil {
		t.Fatalf("12 multi-byte characters: %v", err)
	}
	if _, err := HashPassword(strings.Repeat("é", 11)); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("11 multi-byte characters: error = %v, want ErrWeakPassword", err)
	}
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	t.Parallel()
	const salt, key = "c2FsdHNhbHRzYWx0c2FsdA", "a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2U"
	tests := map[string]string{
		"empty":              "",
		"not phc":            "hunter2hunter2",
		"wrong algorithm":    "$argon2i$v=19$m=65536,t=3,p=2$" + salt + "$" + key,
		"wrong version":      "$argon2id$v=18$m=65536,t=3,p=2$" + salt + "$" + key,
		"missing key":        "$argon2id$v=19$m=65536,t=3,p=2$" + salt,
		"bad parameters":     "$argon2id$v=19$m=x,t=3,p=2$" + salt + "$" + key,
		"bad salt encoding":  "$argon2id$v=19$m=65536,t=3,p=2$!!!$" + key,
		"empty key":          "$argon2id$v=19$m=65536,t=3,p=2$" + salt + "$",
		"absurd memory":      "$argon2id$v=19$m=4194304,t=3,p=2$" + salt + "$" + key,
		"absurd iterations":  "$argon2id$v=19$m=65536,t=1000,p=2$" + salt + "$" + key,
		"absurd parallelism": "$argon2id$v=19$m=65536,t=3,p=255$" + salt + "$" + key,
		"zero iterations":    "$argon2id$v=19$m=65536,t=0,p=2$" + salt + "$" + key,
		"trailing garbage":   "$argon2id$v=19$m=65536,t=3,p=2$" + salt + "$" + key + "$x",
	}
	for name, hash := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ok, err := VerifyPassword(hash, "correct horse battery")
			if err == nil || ok {
				t.Fatalf("VerifyPassword = %v, %v; want false and an error", ok, err)
			}
		})
	}
}

func TestVerifyUsesStoredParameters(t *testing.T) {
	t.Parallel()
	// Hash produced with cheaper parameters than the current ones must still verify.
	hash := hashWithParams("correct horse battery", []byte("0123456789abcdef"), 8*1024, 1, 1)
	if ok, err := VerifyPassword(hash, "correct horse battery"); err != nil || !ok {
		t.Fatalf("VerifyPassword(old parameters) = %v, %v; want true, nil", ok, err)
	}
	if ok, err := VerifyPassword(hash, "correct horse batterz"); err != nil || ok {
		t.Fatalf("VerifyPassword(old parameters, wrong) = %v, %v; want false, nil", ok, err)
	}
}
