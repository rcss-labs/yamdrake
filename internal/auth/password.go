package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Hashing parameters for new passwords (OWASP-grade argon2id).
const (
	argonMemoryKiB = 64 * 1024
	argonTime      = 3
	argonThreads   = 2
	argonSaltLen   = 16
	argonKeyLen    = 32

	minPasswordLen = 12
)

// Upper bounds accepted from a stored hash, so a planted hash cannot make a
// sign-in allocate unbounded memory or spin for minutes.
const (
	maxStoredMemoryKiB = 256 * 1024
	maxStoredTime      = 10
	maxStoredThreads   = 16
	maxStoredSaltLen   = 64
	maxStoredKeyLen    = 64
	minStoredSaltLen   = 8
	minStoredKeyLen    = 16
)

var (
	// ErrWeakPassword is returned for a password shorter than 12 characters.
	ErrWeakPassword = errors.New("password must be at least 12 characters")
	// ErrDisabled is returned when the user exists but is disabled.
	ErrDisabled = errors.New("user is disabled")

	errMalformedHash = errors.New("malformed password hash")
)

// HashPassword returns an argon2id PHC string for password.
func HashPassword(password string) (string, error) {
	if utf8.RuneCountInString(password) < minPasswordLen {
		return "", ErrWeakPassword
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	return hashWithParams(password, salt, argonMemoryKiB, argonTime, argonThreads), nil
}

func hashWithParams(password string, salt []byte, memoryKiB, iterations uint32, threads uint8) string {
	key := argon2.IDKey([]byte(password), salt, iterations, memoryKiB, threads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, memoryKiB, iterations, threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

// VerifyPassword checks password against a PHC string produced by HashPassword.
// It uses the parameters stored in the hash and compares in constant time. A
// wrong password is (false, nil); a hash it cannot parse or that asks for
// absurd parameters is an error.
func VerifyPassword(hash, password string) (ok bool, err error) {
	memoryKiB, iterations, threads, salt, want, err := parseHash(hash)
	if err != nil {
		return false, err
	}
	keyLen := uint32(len(want)) //nolint:gosec // parseHash bounds the key length to 16..64 bytes
	got := argon2.IDKey([]byte(password), salt, iterations, memoryKiB, threads, keyLen)
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func parseHash(hash string) (memoryKiB, iterations uint32, threads uint8, salt, key []byte, err error) {
	// "$argon2id$v=19$m=..,t=..,p=..$salt$key" splits into 6 parts, the first empty.
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return 0, 0, 0, nil, nil, errMalformedHash
	}
	if parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return 0, 0, 0, nil, nil, errMalformedHash
	}
	var m, t, p uint64
	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return 0, 0, 0, nil, nil, errMalformedHash
	}
	for i, field := range []struct {
		prefix string
		dst    *uint64
	}{{"m=", &m}, {"t=", &t}, {"p=", &p}} {
		raw, found := strings.CutPrefix(params[i], field.prefix)
		if !found {
			return 0, 0, 0, nil, nil, errMalformedHash
		}
		v, perr := strconv.ParseUint(raw, 10, 32)
		if perr != nil {
			return 0, 0, 0, nil, nil, errMalformedHash
		}
		*field.dst = v
	}
	if t < 1 || t > maxStoredTime || p < 1 || p > maxStoredThreads ||
		m < 8*p || m > maxStoredMemoryKiB {
		return 0, 0, 0, nil, nil, errMalformedHash
	}
	salt, serr := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	key, kerr := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if serr != nil || kerr != nil ||
		len(salt) < minStoredSaltLen || len(salt) > maxStoredSaltLen ||
		len(key) < minStoredKeyLen || len(key) > maxStoredKeyLen {
		return 0, 0, 0, nil, nil, errMalformedHash
	}
	return uint32(m), uint32(t), uint8(p), salt, key, nil
}
