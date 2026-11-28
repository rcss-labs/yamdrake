package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/yamdrake/internal/secrets"
	"github.com/rcss-labs/yamdrake/internal/store"
)

const (
	// slackSettingsKey is the instance_settings row that holds the Slack bot token.
	slackSettingsKey = "slack"
	// SlackTokenPurpose binds the token's ciphertext to this use.
	SlackTokenPurpose = "slack"
)

// storedSlack is the document kept under slackSettingsKey. The token is
// ciphertext; who set it and when are in the clear.
type storedSlack struct {
	Token []byte    `json:"token"`
	SetBy int64     `json:"set_by"`
	SetAt time.Time `json:"set_at"`
}

// SlackStatus says whether a token is stored, never what it is.
type SlackStatus struct {
	Configured bool
	SetAt      *time.Time
	SetBy      *int64
}

// SaveSlackToken encrypts and stores the bot token, replacing any earlier
// one. Pass a transaction to write an audit entry atomically with the change.
func SaveSlackToken(ctx context.Context, db store.DBTX, cipher secrets.Cipher, token string, setBy int64, at time.Time) error {
	ciphertext, err := cipher.Encrypt(ctx, SlackTokenPurpose, []byte(token))
	if err != nil {
		return errors.New("save slack token: encrypt")
	}
	raw, err := json.Marshal(storedSlack{Token: ciphertext, SetBy: setBy, SetAt: at.UTC()})
	if err != nil {
		return fmt.Errorf("save slack token: %w", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO instance_settings (key, value) VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, slackSettingsKey, raw); err != nil {
		return fmt.Errorf("save slack token: %w", err)
	}
	return nil
}

func loadStoredSlack(ctx context.Context, db store.DBTX) (storedSlack, bool, error) {
	var raw []byte
	err := db.QueryRow(ctx, "SELECT value FROM instance_settings WHERE key = $1", slackSettingsKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return storedSlack{}, false, nil
	}
	if err != nil {
		return storedSlack{}, false, fmt.Errorf("load slack settings: %w", err)
	}
	var doc storedSlack
	if err := json.Unmarshal(raw, &doc); err != nil {
		return storedSlack{}, false, errors.New("load slack settings: decode")
	}
	return doc, len(doc.Token) > 0, nil
}

// LoadSlackStatus reports whether a token is stored, when and by whom.
func LoadSlackStatus(ctx context.Context, db store.DBTX) (SlackStatus, error) {
	doc, ok, err := loadStoredSlack(ctx, db)
	if err != nil || !ok {
		return SlackStatus{}, err
	}
	at, by := doc.SetAt, doc.SetBy
	return SlackStatus{Configured: true, SetAt: &at, SetBy: &by}, nil
}

// TokenSource returns the Slack bot token, or "" when none is configured.
// Errors never contain the token.
type TokenSource func(ctx context.Context) (string, error)

// SlackTokenSource reads and decrypts the stored token on every call, so a
// token changed through the API takes effect at the next delivery round.
func SlackTokenSource(db store.DBTX, cipher secrets.Cipher) TokenSource {
	return func(ctx context.Context) (string, error) {
		doc, ok, err := loadStoredSlack(ctx, db)
		if err != nil || !ok {
			return "", err
		}
		plain, err := cipher.Decrypt(ctx, SlackTokenPurpose, doc.Token)
		if err != nil {
			return "", errors.New("decrypt slack token")
		}
		return string(plain), nil
	}
}
