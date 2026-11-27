package config

import (
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
)

func requiredEnv() map[string]string {
	return map[string]string{
		"YUMDRAKE_DATABASE_URL": validDatabaseURL,
		"YUMDRAKE_VALKEY_ADDR":  validValkeyAddr,
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
}

func TestLoadOrchestratorOverrides(t *testing.T) {
	env := map[string]string{
		"YUMDRAKE_DATABASE_URL":     "postgresql://user@db.example:5433/other?sslmode=require",
		"YUMDRAKE_VALKEY_ADDR":      "cache.example:6380",
		"YUMDRAKE_LOG_LEVEL":        "debug",
		"YUMDRAKE_SHUTDOWN_TIMEOUT": "5s",
		"YUMDRAKE_STARTUP_TIMEOUT":  "2m",
		"YUMDRAKE_HTTP_ADDR":        "0.0.0.0:9090",
		"YUMDRAKE_MIGRATE_ON_START": "false",
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
