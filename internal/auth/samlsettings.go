package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/yamdrake/internal/secrets"
	"github.com/rcss-labs/yamdrake/internal/store"
)

const (
	samlSettingsKey = "saml_settings"
	samlSPKeyKey    = "saml_sp_key"

	// SPKeyPurpose binds the ciphertext of the service provider's private key
	// to its use (see secrets.Cipher).
	SPKeyPurpose = "saml-sp-key"

	// Defaults for the attribute names the identity provider is asked for.
	DefaultGroupsAttribute = "groups"
	DefaultNameAttribute   = "displayName"

	samlRelayKeyKey = "saml_relay_key"
	// RelayKeyPurpose binds the ciphertext of the relay-state MAC key to its use.
	RelayKeyPurpose = "saml-relay-key"

	spKeyBits      = 2048
	spCertValidity = 10 * 365 * 24 * time.Hour
)

// SAMLSettings configure single sign-on. They live in instance_settings.
type SAMLSettings struct {
	Enabled bool `json:"enabled"`
	// IDPMetadataXML is the identity provider's metadata, stored as given: it
	// is read once by the administrator and never re-fetched at sign-in.
	IDPMetadataXML string `json:"idp_metadata_xml"`
	// EntityID is our name towards the identity provider. Empty means the
	// default, <public URL>/saml/metadata, which the caller fills in because
	// only it knows the public URL.
	EntityID string `json:"entity_id"`
	// GroupsAttribute names the attribute that lists the person's groups.
	GroupsAttribute string `json:"groups_attribute"`
	// EmailAttribute names the attribute that holds the email address; empty
	// means the NameID is the email address.
	EmailAttribute string `json:"email_attribute"`
	// NameAttribute names the attribute that holds the display name.
	NameAttribute string `json:"name_attribute"`
}

// LoadSAMLSettings returns the stored settings, or the defaults (disabled)
// when none are stored. Empty attribute names read as their defaults.
func LoadSAMLSettings(ctx context.Context, db store.DBTX) (SAMLSettings, error) {
	var raw []byte
	err := db.QueryRow(ctx, "SELECT value FROM instance_settings WHERE key = $1", samlSettingsKey).Scan(&raw)
	var s SAMLSettings
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return SAMLSettings{}, fmt.Errorf("load saml settings: %w", err)
	default:
		if err := json.Unmarshal(raw, &s); err != nil {
			return SAMLSettings{}, fmt.Errorf("load saml settings: decode: %w", err)
		}
	}
	if s.GroupsAttribute == "" {
		s.GroupsAttribute = DefaultGroupsAttribute
	}
	if s.NameAttribute == "" {
		s.NameAttribute = DefaultNameAttribute
	}
	return s, nil
}

// SaveSAMLSettings stores or replaces the settings. Pass a transaction to
// write an audit entry atomically with the change.
func SaveSAMLSettings(ctx context.Context, db store.DBTX, s SAMLSettings) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("save saml settings: %w", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO instance_settings (key, value) VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, samlSettingsKey, raw); err != nil {
		return fmt.Errorf("save saml settings: %w", err)
	}
	return nil
}

// storedSPKey is the document kept under the saml_sp_key setting: the private
// key encrypted, the certificate in the clear (it is published anyway).
type storedSPKey struct {
	Key  []byte `json:"key"`
	Cert string `json:"cert"`
}

// LoadSPKeyPair returns the service provider's signing key and certificate.
// The first call creates them (RSA 2048, self-signed, valid ten years) and
// stores the key encrypted with cipher under SPKeyPurpose. Concurrent first
// calls end up with one pair: the insert does nothing when a row exists and
// every caller reads the winner back.
func LoadSPKeyPair(ctx context.Context, db store.DBTX, cipher secrets.Cipher, now time.Time) (*rsa.PrivateKey, *x509.Certificate, error) {
	doc, found, err := readSPKey(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	if !found {
		fresh, err := newSPKey(ctx, cipher, now)
		if err != nil {
			return nil, nil, err
		}
		raw, err := json.Marshal(fresh)
		if err != nil {
			return nil, nil, fmt.Errorf("saml sp key: %w", err)
		}
		if _, err := db.Exec(ctx, "INSERT INTO instance_settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO NOTHING", samlSPKeyKey, raw); err != nil {
			return nil, nil, fmt.Errorf("saml sp key: store: %w", err)
		}
		if doc, found, err = readSPKey(ctx, db); err != nil {
			return nil, nil, err
		} else if !found {
			return nil, nil, errors.New("saml sp key: missing right after it was stored")
		}
	}
	plain, err := cipher.Decrypt(ctx, SPKeyPurpose, doc.Key)
	if err != nil {
		return nil, nil, fmt.Errorf("saml sp key: decrypt: %w", err)
	}
	keyBlock, _ := pem.Decode(plain)
	certBlock, _ := pem.Decode([]byte(doc.Cert))
	if keyBlock == nil || certBlock == nil {
		return nil, nil, errors.New("saml sp key: stored key or certificate is not PEM")
	}
	key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("saml sp key: %w", err)
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("saml sp key: %w", err)
	}
	return key, cert, nil
}

func readSPKey(ctx context.Context, db store.DBTX) (storedSPKey, bool, error) {
	var raw []byte
	err := db.QueryRow(ctx, "SELECT value FROM instance_settings WHERE key = $1", samlSPKeyKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return storedSPKey{}, false, nil
	}
	if err != nil {
		return storedSPKey{}, false, fmt.Errorf("saml sp key: read: %w", err)
	}
	var doc storedSPKey
	if err := json.Unmarshal(raw, &doc); err != nil {
		return storedSPKey{}, false, fmt.Errorf("saml sp key: decode: %w", err)
	}
	return doc, true, nil
}

func newSPKey(ctx context.Context, cipher secrets.Cipher, now time.Time) (storedSPKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, spKeyBits)
	if err != nil {
		return storedSPKey{}, fmt.Errorf("saml sp key: generate: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return storedSPKey{}, fmt.Errorf("saml sp key: serial: %w", err)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "yumdrake-saml-sp"},
		// Backdated a little so a slightly slow identity provider clock still
		// accepts a certificate made a moment ago.
		NotBefore: now.Add(-5 * time.Minute),
		NotAfter:  now.Add(spCertValidity),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return storedSPKey{}, fmt.Errorf("saml sp key: certificate: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	encrypted, err := cipher.Encrypt(ctx, SPKeyPurpose, keyPEM)
	if err != nil {
		return storedSPKey{}, fmt.Errorf("saml sp key: encrypt: %w", err)
	}
	return storedSPKey{
		Key:  encrypted,
		Cert: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
	}, nil
}

// LoadSPCertificate returns the service provider's certificate without
// decrypting the private key, for the public metadata endpoint. If no pair
// exists yet it creates one first.
func LoadSPCertificate(ctx context.Context, db store.DBTX, cipher secrets.Cipher, now time.Time) (*x509.Certificate, error) {
	doc, found, err := readSPKey(ctx, db)
	if err != nil {
		return nil, err
	}
	if !found {
		_, cert, err := LoadSPKeyPair(ctx, db, cipher, now)
		return cert, err
	}
	block, _ := pem.Decode([]byte(doc.Cert))
	if block == nil {
		return nil, errors.New("saml sp key: stored certificate is not PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

// LoadRelayKey returns the 32-byte key that authenticates the relay-state
// cookie. It is random, created on first use (concurrent first calls end up
// with one key) and stored encrypted under RelayKeyPurpose.
func LoadRelayKey(ctx context.Context, db store.DBTX, cipher secrets.Cipher) ([]byte, error) {
	read := func() ([]byte, bool, error) {
		var raw []byte
		err := db.QueryRow(ctx, "SELECT value FROM instance_settings WHERE key = $1", samlRelayKeyKey).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("saml relay key: read: %w", err)
		}
		var doc struct {
			Key []byte `json:"key"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, false, fmt.Errorf("saml relay key: decode: %w", err)
		}
		return doc.Key, true, nil
	}
	encrypted, found, err := read()
	if err != nil {
		return nil, err
	}
	if !found {
		fresh := make([]byte, 32)
		if _, err := rand.Read(fresh); err != nil {
			return nil, fmt.Errorf("saml relay key: %w", err)
		}
		sealed, err := cipher.Encrypt(ctx, RelayKeyPurpose, fresh)
		if err != nil {
			return nil, fmt.Errorf("saml relay key: encrypt: %w", err)
		}
		raw, _ := json.Marshal(map[string][]byte{"key": sealed})
		if _, err := db.Exec(ctx, "INSERT INTO instance_settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO NOTHING", samlRelayKeyKey, raw); err != nil {
			return nil, fmt.Errorf("saml relay key: store: %w", err)
		}
		if encrypted, found, err = read(); err != nil {
			return nil, err
		} else if !found {
			return nil, errors.New("saml relay key: missing right after it was stored")
		}
	}
	key, err := cipher.Decrypt(ctx, RelayKeyPurpose, encrypted)
	if err != nil {
		return nil, fmt.Errorf("saml relay key: decrypt: %w", err)
	}
	return key, nil
}
