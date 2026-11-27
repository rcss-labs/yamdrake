package config

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"
)

const (
	secretsModeLocal      = "local"
	localSecretKeyBytes   = 32
	minSyncInterval       = 10 * time.Second
	defaultGitHubAPIURL   = "https://api.github.com"
	redactedSecretDisplay = "REDACTED"
)

// Secret holds a sensitive value. It prints as REDACTED through fmt, slog and
// JSON, so a configuration struct can be logged without leaking it. Use
// Reveal where the real value is needed.
type Secret string

// Reveal returns the underlying value.
func (s Secret) Reveal() string { return string(s) }

// String implements fmt.Stringer.
func (Secret) String() string { return redactedSecretDisplay }

// GoString implements fmt.GoStringer, used by %#v.
func (Secret) GoString() string { return redactedSecretDisplay }

// LogValue implements slog.LogValuer.
func (Secret) LogValue() slog.Value { return slog.StringValue(redactedSecretDisplay) }

// MarshalText keeps the value out of JSON and other text encodings.
func (Secret) MarshalText() ([]byte, error) { return []byte(redactedSecretDisplay), nil }

// SecretSettings is read by both binaries.
type SecretSettings struct {
	SecretsMode    string
	LocalSecretKey Secret // the decoded 32 key bytes
}

// SyncSettings is read by the orchestrator only.
type SyncSettings struct {
	SyncInterval time.Duration
	GitHubAPIURL string
}

func (l *loader) loadSecretSettings() SecretSettings {
	var s SecretSettings
	s.SecretsMode = l.valueOr("YUMDRAKE_SECRETS_MODE", secretsModeLocal)
	if s.SecretsMode != secretsModeLocal {
		l.fail("YUMDRAKE_SECRETS_MODE", `must be "local"`)
		return s
	}
	// The messages below never include the value: it is key material.
	raw := l.getenv("YUMDRAKE_LOCAL_SECRET_KEY")
	if raw == "" {
		l.fail("YUMDRAKE_LOCAL_SECRET_KEY", "is required when YUMDRAKE_SECRETS_MODE is local")
		return s
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		l.fail("YUMDRAKE_LOCAL_SECRET_KEY", "must be standard base64")
		return s
	}
	if len(key) != localSecretKeyBytes {
		l.fail("YUMDRAKE_LOCAL_SECRET_KEY", fmt.Sprintf("must decode to exactly %d bytes", localSecretKeyBytes))
		return s
	}
	s.LocalSecretKey = Secret(key)
	return s
}

func (l *loader) loadSyncSettings() SyncSettings {
	var s SyncSettings

	s.SyncInterval = l.duration("YUMDRAKE_SYNC_INTERVAL", "60s")
	if s.SyncInterval > 0 && s.SyncInterval < minSyncInterval {
		l.fail("YUMDRAKE_SYNC_INTERVAL", "must be at least 10s")
	}

	s.GitHubAPIURL = l.valueOr("YUMDRAKE_GITHUB_API_URL", defaultGitHubAPIURL)
	if reason := validateHTTPURL(s.GitHubAPIURL); reason != "" {
		l.fail("YUMDRAKE_GITHUB_API_URL", reason)
	}
	return s
}

// validateHTTPURL returns a reason that never contains raw.
func validateHTTPURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "is not a valid URL"
	}
	if scheme := strings.ToLower(u.Scheme); scheme != "http" && scheme != "https" {
		return "must use the http:// or https:// scheme"
	}
	if u.Host == "" {
		return "must include a host"
	}
	return ""
}
