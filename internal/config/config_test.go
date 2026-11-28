package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func envFrom(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

const (
	validDatabaseURL = "postgres://u@db:5432/yumdrake"
	validValkeyAddr  = "valkey:6379"
	// base64 of 32 bytes of 0x2a; a test value only.
	validSecretKey = "KioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKio=" //nolint:gosec // test value
)

func requiredEnv() map[string]string {
	return map[string]string{
		"YUMDRAKE_DATABASE_URL": validDatabaseURL,
		"YUMDRAKE_VALKEY_ADDR":  validValkeyAddr,

		"YUMDRAKE_LOCAL_SECRET_KEY": validSecretKey,
		"YUMDRAKE_PUBLIC_URL":       "https://yumdrake.example.com",
	}
}

func TestLoadOrchestratorDefaults(t *testing.T) {
	cfg, err := LoadOrchestrator(envFrom(requiredEnv()))
	if err != nil {
		t.Fatalf("LoadOrchestrator() error = %v", err)
	}
	if cfg.DatabaseURL != validDatabaseURL || cfg.ValkeyAddr != validValkeyAddr {
		t.Errorf("required values not carried over: %+v", cfg.Common)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info", cfg.LogLevel)
	}
	if cfg.ShutdownTimeout != 20*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 20s", cfg.ShutdownTimeout)
	}
	if cfg.StartupTimeout != 60*time.Second {
		t.Errorf("StartupTimeout = %v, want 60s", cfg.StartupTimeout)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if !cfg.MigrateOnStart {
		t.Error("MigrateOnStart = false, want true")
	}
	if cfg.MigrateTimeout != 10*time.Minute {
		t.Errorf("MigrateTimeout = %v, want 10m", cfg.MigrateTimeout)
	}
}

func TestLoadOrchestratorMigrateTimeoutOverride(t *testing.T) {
	env := requiredEnv()
	env["YUMDRAKE_MIGRATE_TIMEOUT"] = "45m"
	cfg, err := LoadOrchestrator(envFrom(env))
	if err != nil {
		t.Fatalf("LoadOrchestrator() error = %v", err)
	}
	if cfg.MigrateTimeout != 45*time.Minute {
		t.Errorf("MigrateTimeout = %v, want 45m", cfg.MigrateTimeout)
	}
}

func TestLoadOrchestratorOverrides(t *testing.T) {
	env := map[string]string{
		"YUMDRAKE_DATABASE_URL":     "postgresql://user@db.example:5433/other?sslmode=require",
		"YUMDRAKE_VALKEY_ADDR":      "cache.example:6380",
		"YUMDRAKE_LOCAL_SECRET_KEY": validSecretKey,
		"YUMDRAKE_LOG_LEVEL":        "debug",
		"YUMDRAKE_SHUTDOWN_TIMEOUT": "5s",
		"YUMDRAKE_STARTUP_TIMEOUT":  "2m",
		"YUMDRAKE_HTTP_ADDR":        "0.0.0.0:9090",
		"YUMDRAKE_MIGRATE_ON_START": "false",
		"YUMDRAKE_PUBLIC_URL":       "http://localhost:8080",
	}
	cfg, err := LoadOrchestrator(envFrom(env))
	if err != nil {
		t.Fatalf("LoadOrchestrator() error = %v", err)
	}
	if cfg.DatabaseURL != env["YUMDRAKE_DATABASE_URL"] {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.ValkeyAddr != "cache.example:6380" {
		t.Errorf("ValkeyAddr = %q", cfg.ValkeyAddr)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want debug", cfg.LogLevel)
	}
	if cfg.ShutdownTimeout != 5*time.Second {
		t.Errorf("ShutdownTimeout = %v", cfg.ShutdownTimeout)
	}
	if cfg.StartupTimeout != 2*time.Minute {
		t.Errorf("StartupTimeout = %v", cfg.StartupTimeout)
	}
	if cfg.HTTPAddr != "0.0.0.0:9090" {
		t.Errorf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.MigrateOnStart {
		t.Error("MigrateOnStart = true, want false")
	}
}

func TestLoadRunnerOverrides(t *testing.T) {
	env := requiredEnv()
	env["YUMDRAKE_HEALTH_ADDR"] = "0.0.0.0:9091"
	env["YUMDRAKE_RUNNER_ID"] = "runner-7"
	cfg, err := LoadRunner(envFrom(env))
	if err != nil {
		t.Fatalf("LoadRunner() error = %v", err)
	}
	if cfg.HealthAddr != "0.0.0.0:9091" || cfg.RunnerID != "runner-7" {
		t.Errorf("got HealthAddr=%q RunnerID=%q", cfg.HealthAddr, cfg.RunnerID)
	}
}

func TestLoadReportsEveryProblemAtOnce(t *testing.T) {
	env := map[string]string{
		"YUMDRAKE_VALKEY_ADDR":      validValkeyAddr,
		"YUMDRAKE_LOCAL_SECRET_KEY": validSecretKey,
		"YUMDRAKE_LOG_LEVEL":        "loud",
		"YUMDRAKE_SHUTDOWN_TIMEOUT": "-1s",
	}
	_, err := LoadOrchestrator(envFrom(env))
	if err == nil {
		t.Fatal("LoadOrchestrator() error = nil, want error")
	}
	for _, name := range []string{"YUMDRAKE_DATABASE_URL", "YUMDRAKE_LOG_LEVEL", "YUMDRAKE_SHUTDOWN_TIMEOUT"} {
		if !strings.Contains(err.Error(), "config: "+name+": ") {
			t.Errorf("error does not report %s: %v", name, err)
		}
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	cases := map[string]string{
		"YUMDRAKE_VALKEY_ADDR":      "no-port",
		"YUMDRAKE_STARTUP_TIMEOUT":  "0s",
		"YUMDRAKE_MIGRATE_TIMEOUT":  "soon",
		"YUMDRAKE_HTTP_ADDR":        "host:99999",
		"YUMDRAKE_MIGRATE_ON_START": "yes",
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			env := requiredEnv()
			env[name] = value
			_, err := LoadOrchestrator(envFrom(env))
			if err == nil || !strings.Contains(err.Error(), "config: "+name+": ") {
				t.Errorf("error = %v, want a problem with %s", err, name)
			}
		})
	}
}

func TestLoadErrorNeverContainsDatabasePassword(t *testing.T) {
	env := requiredEnv()
	env["YUMDRAKE_DATABASE_URL"] = "postgres://u:s3cret@host/db?sslmode=bogus%zz"
	_, err := LoadOrchestrator(envFrom(env))
	if err == nil {
		t.Fatal("LoadOrchestrator() error = nil, want error")
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Errorf("error leaks the password: %v", err)
	}
	if !strings.Contains(err.Error(), "config: YUMDRAKE_DATABASE_URL: ") {
		t.Errorf("error does not name the variable: %v", err)
	}
}

func TestLoadRunnerDefaultsRunnerIDToHostname(t *testing.T) {
	want, err := os.Hostname()
	if err != nil {
		t.Skipf("hostname unavailable: %v", err)
	}
	cfg, err := LoadRunner(envFrom(requiredEnv()))
	if err != nil {
		t.Fatalf("LoadRunner() error = %v", err)
	}
	if cfg.RunnerID != want {
		t.Errorf("RunnerID = %q, want %q", cfg.RunnerID, want)
	}
}

func TestRedactURL(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"password replaced", "postgres://u:s3cret@h:5432/db?sslmode=require", "postgres://u:REDACTED@h:5432/db?sslmode=require"},
		{"no password unchanged", "postgres://h:5432/db", "postgres://h:5432/db"},
		{"unparseable", "::not a url", "invalid URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactURL(tc.in); got != tc.want {
				t.Errorf("RedactURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRedactURLQueryPassword(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"query password", "postgres://u@h:5432/db?password=s3cret&sslmode=require", "postgres://u@h:5432/db?password=REDACTED&sslmode=require"},
		{"percent-encoded query password", "postgres://u@h/db?password=p%40ss%2Fword", "postgres://u@h/db?password=REDACTED"},
		{"percent-encoded user-info password", "postgres://u:p%40ss%2Fword@h/db", "postgres://u:REDACTED@h/db"},
		{"both forms", "postgres://u:one@h/db?password=two", "postgres://u:REDACTED@h/db?password=REDACTED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactURL(tc.in)
			if got != tc.want {
				t.Errorf("RedactURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestScrubRemovesEncodedAndPlainPassword(t *testing.T) {
	cases := []struct{ name, url, password string }{
		{"user-info", "postgres://u:p%40ss%2Fw%20rd@h:5432/db", "p@ss/w rd"},
		{"query", "postgres://u@h:5432/db?password=p%40ss%2Fw%20rd", "p@ss/w rd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := "dial failed for " + tc.url + "; saw " + tc.password +
				" and p%40ss%2Fw%20rd and p%40ss%2Fw+rd and p@ss%2Fw%20rd"
			got := Scrub(text, tc.url)
			for _, leaked := range []string{tc.password, "p%40ss%2Fw%20rd", "p%40ss%2Fw+rd", "p@ss%2Fw%20rd", "ss/w"} {
				if strings.Contains(got, leaked) {
					t.Errorf("Scrub() = %q, still contains %q", got, leaked)
				}
			}
			if !strings.Contains(got, "REDACTED") || !strings.Contains(got, "dial failed for") {
				t.Errorf("Scrub() = %q, want surrounding text kept and REDACTED present", got)
			}
		})
	}
	if got := Scrub("nothing secret", "postgres://u@h/db"); got != "nothing secret" {
		t.Errorf("Scrub() without a password = %q, want text unchanged", got)
	}
}

func TestLoadRunnerDefaultsHealthAddrToLoopback(t *testing.T) {
	cfg, err := LoadRunner(envFrom(requiredEnv()))
	if err != nil {
		t.Fatalf("LoadRunner() error = %v", err)
	}
	if cfg.HealthAddr != "127.0.0.1:8081" {
		t.Errorf("HealthAddr = %q, want 127.0.0.1:8081 so the runner is not reachable from outside its task by default", cfg.HealthAddr)
	}
}

func TestValidateHostPort(t *testing.T) {
	for _, ok := range []string{":8080", "127.0.0.1:8081", "[::1]:9"} {
		if err := ValidateHostPort(ok); err != nil {
			t.Errorf("ValidateHostPort(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "nope", "host:0", "host:70000", "host:abc"} {
		if err := ValidateHostPort(bad); err == nil {
			t.Errorf("ValidateHostPort(%q) = nil, want an error", bad)
		}
	}
}

func TestSecretAndSyncDefaults(t *testing.T) {
	cfg, err := LoadOrchestrator(envFrom(requiredEnv()))
	if err != nil {
		t.Fatalf("LoadOrchestrator() error = %v", err)
	}
	if cfg.SecretsMode != "local" {
		t.Errorf("SecretsMode = %q, want local", cfg.SecretsMode)
	}
	if got := cfg.LocalSecretKey.Reveal(); got != strings.Repeat("*", 32) {
		t.Errorf("LocalSecretKey did not decode to the 32 key bytes")
	}
	if cfg.SyncInterval != 60*time.Second {
		t.Errorf("SyncInterval = %v, want 60s", cfg.SyncInterval)
	}
	if cfg.GitHubAPIURL != "https://api.github.com" {
		t.Errorf("GitHubAPIURL = %q", cfg.GitHubAPIURL)
	}
	runner, err := LoadRunner(envFrom(requiredEnv()))
	if err != nil {
		t.Fatalf("LoadRunner() error = %v", err)
	}
	if runner.SecretsMode != "local" || runner.LocalSecretKey.Reveal() != strings.Repeat("*", 32) {
		t.Error("runner does not load the secrets settings")
	}
}

func TestSecretAndSyncRules(t *testing.T) {
	tests := []struct {
		name    string
		set     map[string]string
		wantVar string // empty means valid
	}{
		{"key missing", map[string]string{"YUMDRAKE_LOCAL_SECRET_KEY": ""}, "YUMDRAKE_LOCAL_SECRET_KEY"},
		{"key not base64", map[string]string{"YUMDRAKE_LOCAL_SECRET_KEY": "not base64 !!"}, "YUMDRAKE_LOCAL_SECRET_KEY"},
		{"key wrong length", map[string]string{"YUMDRAKE_LOCAL_SECRET_KEY": "c2hvcnQ="}, "YUMDRAKE_LOCAL_SECRET_KEY"}, //nolint:gosec // test value
		{"mode not local", map[string]string{"YUMDRAKE_SECRETS_MODE": "kms"}, "YUMDRAKE_SECRETS_MODE"},
		{"interval too short", map[string]string{"YUMDRAKE_SYNC_INTERVAL": "9s"}, "YUMDRAKE_SYNC_INTERVAL"},
		{"interval minimum", map[string]string{"YUMDRAKE_SYNC_INTERVAL": "10s"}, ""},
		{"interval not duration", map[string]string{"YUMDRAKE_SYNC_INTERVAL": "soon"}, "YUMDRAKE_SYNC_INTERVAL"},
		{"github url ftp", map[string]string{"YUMDRAKE_GITHUB_API_URL": "ftp://example.com"}, "YUMDRAKE_GITHUB_API_URL"},
		{"github url no host", map[string]string{"YUMDRAKE_GITHUB_API_URL": "http://"}, "YUMDRAKE_GITHUB_API_URL"},
		{"github url http", map[string]string{"YUMDRAKE_GITHUB_API_URL": "http://127.0.0.1:9999"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := requiredEnv()
			for k, v := range tc.set {
				env[k] = v
			}
			_, err := LoadOrchestrator(envFrom(env))
			if tc.wantVar == "" {
				if err != nil {
					t.Fatalf("error = %v, want none", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantVar) {
				t.Fatalf("error = %v, want it to name %s", err, tc.wantVar)
			}
		})
	}
}

func TestRunnerRejectsBadSecretKey(t *testing.T) {
	env := requiredEnv()
	env["YUMDRAKE_LOCAL_SECRET_KEY"] = "c2hvcnQ="
	if _, err := LoadRunner(envFrom(env)); err == nil {
		t.Fatal("LoadRunner() accepted a 5-byte key")
	}
}

func TestSecretsNeverAppearInErrorsOrFormatting(t *testing.T) {
	badKey := "c2VjcmV0LWtleS12YWx1ZS10b28tc2hvcnQ="
	env := requiredEnv()
	env["YUMDRAKE_LOCAL_SECRET_KEY"] = badKey
	env["YUMDRAKE_SYNC_INTERVAL"] = "bad"
	_, err := LoadOrchestrator(envFrom(env))
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, secret := range []string{badKey, "secret-key-value"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error text contains a secret value: %q", err.Error())
		}
	}

	env = requiredEnv()
	cfg, err := LoadOrchestrator(envFrom(env))
	if err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("config", "config", cfg, "key", cfg.LocalSecretKey)
	rendered := []string{
		buf.String(),
		fmt.Sprintf("%v", cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg),
		fmt.Sprint(cfg.LocalSecretKey),
	}
	for _, text := range rendered {
		for _, secret := range []string{validSecretKey, strings.Repeat("*", 32)} {
			if strings.Contains(text, secret) {
				t.Errorf("formatted output leaks a secret: %q", text)
			}
		}
	}
}

func TestPublicURLRules(t *testing.T) {
	tests := []struct {
		name, value string
		wantErr     bool
		want        string
	}{
		{"missing", "", true, ""},
		{"https host", "https://yumdrake.example.com", false, "https://yumdrake.example.com"},
		{"https with port", "https://yumdrake.example.com:8443", false, "https://yumdrake.example.com:8443"},
		{"trailing slash dropped", "https://yumdrake.example.com/", false, "https://yumdrake.example.com"},
		{"http on a public host", "http://yumdrake.example.com", true, ""},
		{"http on localhost", "http://localhost:8080", false, "http://localhost:8080"},
		{"http on 127.0.0.1", "http://127.0.0.1:8080", false, "http://127.0.0.1:8080"},
		{"http on a lookalike host", "http://localhost.example.com", true, ""},
		{"other scheme", "ftp://localhost", true, ""},
		{"no host", "https://", true, ""},
		{"path", "https://yumdrake.example.com/app", true, ""},
		{"query", "https://yumdrake.example.com/?a=b", true, ""},
		{"user info", "https://user:pw@yumdrake.example.com", true, ""},
		{"not a url", "::::", true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := requiredEnv()
			env["YUMDRAKE_PUBLIC_URL"] = tc.value
			cfg, err := LoadOrchestrator(envFrom(env))
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "YUMDRAKE_PUBLIC_URL") {
					t.Fatalf("error = %v, want it to name YUMDRAKE_PUBLIC_URL", err)
				}
				if tc.value != "" && strings.Contains(err.Error(), "pw@") {
					t.Errorf("error text echoes the user info: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v, want none", err)
			}
			if cfg.PublicURL != tc.want {
				t.Errorf("PublicURL = %q, want %q", cfg.PublicURL, tc.want)
			}
		})
	}
}

func TestValkeyKeyPrefix(t *testing.T) {
	cfg, err := LoadOrchestrator(envFrom(requiredEnv()))
	if err != nil || cfg.ValkeyKeyPrefix != "" {
		t.Fatalf("default prefix = %q, %v; want empty", cfg.ValkeyKeyPrefix, err)
	}
	env := requiredEnv()
	env["YUMDRAKE_VALKEY_KEY_PREFIX"] = "blue:"
	if cfg, err = LoadOrchestrator(envFrom(env)); err != nil || cfg.ValkeyKeyPrefix != "blue:" {
		t.Fatalf("prefix = %q, %v; want blue:", cfg.ValkeyKeyPrefix, err)
	}
	for _, bad := range []string{"has space", "wild*", strings.Repeat("a", 65)} {
		env["YUMDRAKE_VALKEY_KEY_PREFIX"] = bad
		if _, err := LoadOrchestrator(envFrom(env)); err == nil || !strings.Contains(err.Error(), "YUMDRAKE_VALKEY_KEY_PREFIX") {
			t.Errorf("prefix %q: error = %v, want it to name the variable", bad, err)
		}
	}
}

func TestFakeStepsRefusedOutsideDev(t *testing.T) {
	cases := []struct {
		name      string
		env       map[string]string
		wantError string // substring; empty means the load must succeed
	}{
		{"prod with fakes", map[string]string{"YUMDRAKE_ENV": "prod", "YUMDRAKE_FAKE_STEPS": "true"}, "YUMDRAKE_FAKE_STEPS"},
		{"default env with fakes", map[string]string{"YUMDRAKE_FAKE_STEPS": "true"}, "YUMDRAKE_FAKE_STEPS"},
		{"dev with fakes", map[string]string{"YUMDRAKE_ENV": "dev", "YUMDRAKE_FAKE_STEPS": "true"}, ""},
		{"prod without fakes", map[string]string{"YUMDRAKE_ENV": "prod"}, ""},
		{"unknown env", map[string]string{"YUMDRAKE_ENV": "staging"}, "YUMDRAKE_ENV"},
		{"fake flag not a bool", map[string]string{"YUMDRAKE_ENV": "dev", "YUMDRAKE_FAKE_STEPS": "yes"}, "YUMDRAKE_FAKE_STEPS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := requiredEnv()
			for k, v := range tc.env {
				env[k] = v
			}
			cfg, err := LoadRunner(envFrom(env))
			if tc.wantError == "" {
				if err != nil {
					t.Fatalf("LoadRunner() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("LoadRunner() = %+v, %v; want an error naming %s", cfg, err, tc.wantError)
			}
		})
	}
}

func TestLoadRunnerEngineDefaultsAndOverrides(t *testing.T) {
	cfg, err := LoadRunner(envFrom(requiredEnv()))
	if err != nil {
		t.Fatalf("LoadRunner() error = %v", err)
	}
	if cfg.Env != "prod" || cfg.FakeSteps || cfg.Concurrency != 8 ||
		cfg.Lease != 30*time.Second || cfg.Heartbeat != 10*time.Second || cfg.Poll != 2*time.Second {
		t.Errorf("defaults = %+v", cfg)
	}
	env := requiredEnv()
	env["YUMDRAKE_RUNNER_CONCURRENCY"] = "3"
	env["YUMDRAKE_LEASE"] = "1m"
	env["YUMDRAKE_HEARTBEAT"] = "20s"
	env["YUMDRAKE_RUNNER_POLL"] = "500ms"
	cfg, err = LoadRunner(envFrom(env))
	if err != nil {
		t.Fatalf("LoadRunner() error = %v", err)
	}
	if cfg.Concurrency != 3 || cfg.Lease != time.Minute || cfg.Heartbeat != 20*time.Second || cfg.Poll != 500*time.Millisecond {
		t.Errorf("overrides = %+v", cfg)
	}
}

func TestLoadRunnerRefusesHeartbeatNotShorterThanLease(t *testing.T) {
	for _, heartbeat := range []string{"30s", "45s"} {
		env := requiredEnv()
		env["YUMDRAKE_HEARTBEAT"] = heartbeat // the default lease is 30s
		if _, err := LoadRunner(envFrom(env)); err == nil || !strings.Contains(err.Error(), "YUMDRAKE_HEARTBEAT") {
			t.Errorf("heartbeat %s: error = %v, want one naming YUMDRAKE_HEARTBEAT", heartbeat, err)
		}
	}
	env := requiredEnv()
	env["YUMDRAKE_RUNNER_CONCURRENCY"] = "0"
	if _, err := LoadRunner(envFrom(env)); err == nil || !strings.Contains(err.Error(), "YUMDRAKE_RUNNER_CONCURRENCY") {
		t.Errorf("concurrency 0: error = %v", err)
	}
}

func TestLoadOrchestratorLogArchiveDir(t *testing.T) {
	cfg, err := LoadOrchestrator(envFrom(requiredEnv()))
	if err != nil || cfg.LogArchiveDir != "" {
		t.Fatalf("default: dir = %q, err = %v; want archiving off", cfg.LogArchiveDir, err)
	}
	env := requiredEnv()
	env["YUMDRAKE_LOG_ARCHIVE_DIR"] = "/var/lib/yumdrake/logs"
	if cfg, err = LoadOrchestrator(envFrom(env)); err != nil || cfg.LogArchiveDir != "/var/lib/yumdrake/logs" {
		t.Fatalf("absolute: dir = %q, err = %v", cfg.LogArchiveDir, err)
	}
	env["YUMDRAKE_LOG_ARCHIVE_DIR"] = "relative/dir"
	if _, err = LoadOrchestrator(envFrom(env)); err == nil || !strings.Contains(err.Error(), "YUMDRAKE_LOG_ARCHIVE_DIR") {
		t.Fatalf("relative path: err = %v, want a complaint naming the variable", err)
	}
}
