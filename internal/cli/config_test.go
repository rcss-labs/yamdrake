package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfigMissingFileIsEmpty(t *testing.T) {
	got, err := LoadConfig(envMap(nil), filepath.Join(t.TempDir(), "none", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Server != "" || got.Token != "" {
		t.Errorf("config = %+v, want empty", got)
	}
}

func TestConfigPrecedenceFlagThenEnvThenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := SaveConfig(path, Config{Server: "https://file.example", Token: "file-token", TokenID: 4}); err != nil {
		t.Fatal(err)
	}

	fromFile, err := LoadConfig(envMap(nil), path)
	if err != nil {
		t.Fatal(err)
	}
	if fromFile.Server != "https://file.example" || fromFile.Token != "file-token" || fromFile.TokenID != 4 {
		t.Errorf("file config = %+v", fromFile)
	}

	fromEnv, err := LoadConfig(envMap(map[string]string{"YUMDRAKE_SERVER": "https://env.example", "YUMDRAKE_TOKEN": "env-token"}), path)
	if err != nil {
		t.Fatal(err)
	}
	if fromEnv.Server != "https://env.example" || fromEnv.Token != "env-token" || !fromEnv.TokenFromEnv || fromEnv.TokenID != 0 {
		t.Errorf("env config = %+v", fromEnv)
	}

	if got := fromEnv.WithServer("https://flag.example").Server; got != "https://flag.example" {
		t.Errorf("flag server = %q", got)
	}
	if got := fromEnv.WithServer("").Server; got != "https://env.example" {
		t.Errorf("empty flag changed the server to %q", got)
	}
}

func TestSaveConfigModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	path := filepath.Join(t.TempDir(), "yumdrake", "config.json")
	if err := SaveConfig(path, Config{Server: "https://x.example", Token: "secret"}); err != nil {
		t.Fatal(err)
	}
	file, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if file.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600", file.Mode().Perm())
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if dir.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, want 0700", dir.Mode().Perm())
	}
}

func TestLoadConfigRefusesFileReadableByOthers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"server":"https://x.example","token":"super-secret"}`), 0o644); err != nil { //nolint:gosec // the test needs a readable file
		t.Fatal(err)
	}
	_, err := LoadConfig(envMap(nil), path)
	if err == nil {
		t.Fatal("want an error for a config file readable by others")
	}
	if ExitCodeFor(err) != 2 {
		t.Errorf("exit code = %d, want 2", ExitCodeFor(err))
	}
	if !strings.Contains(err.Error(), "chmod 600") || strings.Contains(err.Error(), "super-secret") {
		t.Errorf("message = %q, want a chmod hint and no token", err.Error())
	}
}

func TestDefaultConfigPath(t *testing.T) {
	got, err := DefaultConfigPath(envMap(map[string]string{"XDG_CONFIG_HOME": "/xdg"}))
	if err != nil || got != filepath.Join("/xdg", "yumdrake", "config.json") {
		t.Errorf("xdg path = %q, %v", got, err)
	}
	got, err = DefaultConfigPath(envMap(map[string]string{"HOME": "/home/me"}))
	if err != nil {
		t.Skip("no home directory resolvable in this environment")
	}
	if !strings.HasSuffix(got, filepath.Join(".config", "yumdrake", "config.json")) {
		t.Errorf("home path = %q", got)
	}
}
