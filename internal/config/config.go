// Package config loads and validates the environment configuration of the
// Yumdrake orchestrator and runner.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Common holds the settings shared by both service binaries.
type Common struct {
	DatabaseURL     string
	ValkeyAddr      string
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
	StartupTimeout  time.Duration
}

// Orchestrator holds the settings of the orchestrator service.
type Orchestrator struct {
	Common
	HTTPAddr       string
	MigrateOnStart bool
	// MigrateTimeout bounds the schema migration, separately from StartupTimeout.
	MigrateTimeout time.Duration
}

// Runner holds the settings of the runner service.
type Runner struct {
	Common
	HealthAddr string
	RunnerID   string
}

const redactedPassword = "REDACTED"

// loader collects problems so that every one is reported at once.
type loader struct {
	getenv   func(string) string
	problems []error
}

func (l *loader) fail(variable, reason string) {
	l.problems = append(l.problems, fmt.Errorf("config: %s: %s", variable, reason))
}

func (l *loader) err() error {
	return errors.Join(l.problems...)
}

func (l *loader) loadCommon() Common {
	var c Common

	c.DatabaseURL = l.getenv("YUMDRAKE_DATABASE_URL")
	if reason := validateDatabaseURL(c.DatabaseURL); reason != "" {
		l.fail("YUMDRAKE_DATABASE_URL", reason)
	}

	c.ValkeyAddr = l.getenv("YUMDRAKE_VALKEY_ADDR")
	if c.ValkeyAddr == "" {
		l.fail("YUMDRAKE_VALKEY_ADDR", "is required")
	} else if reason := validateHostPort(c.ValkeyAddr); reason != "" {
		l.fail("YUMDRAKE_VALKEY_ADDR", reason)
	}

	c.LogLevel = l.logLevel("YUMDRAKE_LOG_LEVEL", "info")
	c.ShutdownTimeout = l.duration("YUMDRAKE_SHUTDOWN_TIMEOUT", "20s")
	c.StartupTimeout = l.duration("YUMDRAKE_STARTUP_TIMEOUT", "60s")
	return c
}

// LoadOrchestrator reads the orchestrator configuration from getenv.
func LoadOrchestrator(getenv func(string) string) (Orchestrator, error) {
	l := &loader{getenv: getenv}
	var cfg Orchestrator
	cfg.Common = l.loadCommon()
	cfg.HTTPAddr = l.hostPort("YUMDRAKE_HTTP_ADDR", ":8080")

	cfg.MigrateTimeout = l.duration("YUMDRAKE_MIGRATE_TIMEOUT", "10m")

	switch raw := l.valueOr("YUMDRAKE_MIGRATE_ON_START", "true"); raw {
	case "true":
		cfg.MigrateOnStart = true
	case "false":
		cfg.MigrateOnStart = false
	default:
		l.fail("YUMDRAKE_MIGRATE_ON_START", `must be "true" or "false"`)
	}

	if err := l.err(); err != nil {
		return Orchestrator{}, err
	}
	return cfg, nil
}

// LoadRunner reads the runner configuration from getenv.
func LoadRunner(getenv func(string) string) (Runner, error) {
	l := &loader{getenv: getenv}
	var cfg Runner
	cfg.Common = l.loadCommon()
	cfg.HealthAddr = l.hostPort("YUMDRAKE_HEALTH_ADDR", "127.0.0.1:8081")

	cfg.RunnerID = getenv("YUMDRAKE_RUNNER_ID")
	if cfg.RunnerID == "" {
		hostname, err := os.Hostname()
		if err != nil || hostname == "" {
			l.fail("YUMDRAKE_RUNNER_ID", "is not set and the host name could not be determined")
		}
		cfg.RunnerID = hostname
	}

	if err := l.err(); err != nil {
		return Runner{}, err
	}
	return cfg, nil
}

func (l *loader) valueOr(variable, fallback string) string {
	if v := l.getenv(variable); v != "" {
		return v
	}
	return fallback
}

func (l *loader) logLevel(variable, fallback string) slog.Level {
	switch l.valueOr(variable, fallback) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	l.fail(variable, "must be one of debug, info, warn, error")
	return slog.LevelInfo
}

func (l *loader) duration(variable, fallback string) time.Duration {
	d, err := time.ParseDuration(l.valueOr(variable, fallback))
	if err != nil {
		l.fail(variable, "must be a Go duration such as 20s")
		return 0
	}
	if d <= 0 {
		l.fail(variable, "must be greater than zero")
		return 0
	}
	return d
}

func (l *loader) hostPort(variable, fallback string) string {
	value := l.valueOr(variable, fallback)
	if reason := validateHostPort(value); reason != "" {
		l.fail(variable, reason)
	}
	return value
}

// ValidateHostPort reports whether value is host:port with a numeric port in
// 1-65535. An empty host is allowed.
func ValidateHostPort(value string) error {
	if reason := validateHostPort(value); reason != "" {
		return errors.New(reason)
	}
	return nil
}

// validateHostPort returns a reason when value is not host:port with a numeric
// port in 1-65535. An empty host is allowed.
func validateHostPort(value string) string {
	_, port, err := net.SplitHostPort(value)
	if err != nil {
		return "must be host:port"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "port must be a number between 1 and 65535"
	}
	return ""
}

// validateDatabaseURL returns a reason that never contains any part of raw,
// because raw may hold a password.
func validateDatabaseURL(raw string) string {
	if raw == "" {
		return "is required"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "is not a valid URL"
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "must use the postgres:// or postgresql:// scheme"
	}
	if u.Host == "" {
		return "must include a host"
	}
	if _, err := url.ParseQuery(u.RawQuery); err != nil {
		return "has an invalid query string"
	}
	return ""
}

// RedactURL replaces the password in raw with REDACTED, whether it is given
// as user info or as a "password" query parameter. It returns "invalid URL"
// when raw cannot be parsed, so a bad value is never echoed.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "invalid URL"
	}
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			u.User = url.UserPassword(u.User.Username(), redactedPassword)
		}
	}
	u.RawQuery = redactQueryPasswords(u.RawQuery)
	return u.String()
}

// redactQueryPasswords replaces the value of every "password" parameter in
// rawQuery, leaving the order and encoding of the other parameters untouched.
func redactQueryPasswords(rawQuery string) string {
	if rawQuery == "" {
		return rawQuery
	}
	pairs := strings.Split(rawQuery, "&")
	for i, pair := range pairs {
		name, _, found := strings.Cut(pair, "=")
		if !found {
			continue
		}
		if decoded, err := url.QueryUnescape(name); err == nil && decoded == "password" {
			pairs[i] = name + "=" + redactedPassword
		}
	}
	return strings.Join(pairs, "&")
}

// Scrub removes the connection URL and every form of its password from text:
// the plain password, and any mix of percent-encoded characters ("%40" or
// "%2f" for "@" or "/", "+" or "%20" for a space). Driver errors may echo any
// of these, so callers use Scrub on error text before logging or returning it.
func Scrub(text, rawURL string) string {
	if rawURL == "" {
		return text
	}
	text = strings.ReplaceAll(text, rawURL, RedactURL(rawURL))
	u, err := url.Parse(rawURL)
	if err != nil {
		return text
	}
	var passwords []string
	if u.User != nil {
		if password, ok := u.User.Password(); ok {
			passwords = append(passwords, password)
		}
	}
	passwords = append(passwords, u.Query()["password"]...)
	for _, password := range passwords {
		if password == "" {
			continue
		}
		text = passwordPattern(password).ReplaceAllLiteralString(text, redactedPassword)
	}
	return text
}

// passwordPattern matches password where each character may appear literally
// or as percent escapes of its UTF-8 bytes (any hex case), and a space may
// also appear as "+".
func passwordPattern(password string) *regexp.Regexp {
	var pattern strings.Builder
	for _, r := range password {
		var escaped strings.Builder
		for _, b := range []byte(string(r)) {
			fmt.Fprintf(&escaped, "%%(?i:%02x)", b)
		}
		alternatives := []string{regexp.QuoteMeta(string(r)), escaped.String()}
		if r == ' ' {
			alternatives = append(alternatives, `\+`)
		}
		pattern.WriteString("(?:" + strings.Join(alternatives, "|") + ")")
	}
	return regexp.MustCompile(pattern.String())
}
