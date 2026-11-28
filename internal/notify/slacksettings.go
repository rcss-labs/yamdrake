package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/yumdrake/internal/secrets"
	"github.com/rcss-labs/yumdrake/internal/store"
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
	// InvalidAt is when Slack first refused the token; absent while it works.
	// Storing a new token writes a fresh document without it.
	InvalidAt *time.Time `json:"invalid_at,omitempty"`
}

// SlackStatus says whether a token is stored, never what it is.
type SlackStatus struct {
	Configured bool
	SetAt      *time.Time
	SetBy      *int64
	// InvalidSince is when Slack first refused the stored token (nil while it
	// works or is unset). Sending is paused until a new token is stored.
	InvalidSince *time.Time
	// ciphertext identifies the stored token, to guard MarkSlackTokenInvalid.
	ciphertext []byte
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

// MarkSlackTokenInvalid records that Slack refused the stored token, at the
// given time. It only acts when the stored token is still the one that was
// refused (status is what the sender read before posting), so a token an admin
// set in the meantime is not marked. It reports whether it changed anything.
func MarkSlackTokenInvalid(ctx context.Context, db store.DBTX, refused SlackStatus, at time.Time) (bool, error) {
	if !refused.Configured || len(refused.ciphertext) == 0 {
		return false, nil
	}
	stamp, err := json.Marshal(at.UTC())
	if err != nil {
		return false, fmt.Errorf("mark slack token invalid: %w", err)
	}
	guard, err := json.Marshal(refused.ciphertext)
	if err != nil {
		return false, fmt.Errorf("mark slack token invalid: %w", err)
	}
	tag, err := db.Exec(ctx, `UPDATE instance_settings
SET value = value || jsonb_build_object('invalid_at', $2::jsonb), updated_at = now()
WHERE key = $1 AND value->'token' = $3::jsonb AND NOT value ? 'invalid_at'`, slackSettingsKey, string(stamp), string(guard))
	if err != nil {
		return false, fmt.Errorf("mark slack token invalid: %w", err)
	}
	return tag.RowsAffected() > 0, nil
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
	return SlackStatus{Configured: true, SetAt: &at, SetBy: &by, InvalidSince: doc.InvalidAt, ciphertext: doc.Token}, nil
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
