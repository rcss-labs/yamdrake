// Package cli holds what the yumdrake command line tool shares between
// commands: the configuration file, the API client and output helpers.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// Config is where the CLI talks to and how it signs in.
type Config struct {
	Server string `json:"server,omitempty"`
	Token  string `json:"token,omitempty"`
	// TokenID is the server's id of Token, known when `yumdrake login` made it.
	// It lets logout revoke exactly this token.
	TokenID int64 `json:"token_id,omitempty"`
	// TokenFromEnv is set when Token came from YUMDRAKE_TOKEN, not the file.
	TokenFromEnv bool `json:"-"`
	// InsecureHTTP allows plain http:// to a non-local host.
	InsecureHTTP bool `json:"-"`
}

// WithServer returns the config with server replaced by the --server flag
// value, when one was given.
func (c Config) WithServer(flagValue string) Config {
	if flagValue != "" {
		c.Server = flagValue
	}
	return c
}

// DefaultConfigPath is $XDG_CONFIG_HOME/yumdrake/config.json, else
// ~/.config/yumdrake/config.json.
func DefaultConfigPath(getenv func(string) string) (string, error) {
	if dir := getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "yumdrake", "config.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", &ExitError{Code: ExitUsage, Message: "cannot find the home directory; set XDG_CONFIG_HOME"}
	}
	return filepath.Join(home, ".config", "yumdrake", "config.json"), nil
}

// LoadConfig reads the file at path (the default path when empty) and applies
// the environment on top: YUMDRAKE_SERVER and YUMDRAKE_TOKEN win over the
// file. A missing file is an empty config. A file that other users can read is
// refused, since it holds the token. The --server flag is applied by the
// caller with WithServer.
func LoadConfig(getenv func(string) string, path string) (Config, error) {
	if path == "" {
		var err error
		if path, err = DefaultConfigPath(getenv); err != nil {
			return Config{}, err
		}
	}
	var cfg Config
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return Config{}, &ExitError{Code: ExitUsage, Message: fmt.Sprintf("cannot read the config file %s: %v", path, errors.Unwrap(err))}
	default:
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			return Config{}, &ExitError{Code: ExitUsage, Message: fmt.Sprintf(
				"the config file %s can be read by other users and holds your token; run: chmod 600 %s", path, path)}
		}
		data, err := os.ReadFile(path) //nolint:gosec // the user's own config path
		if err != nil {
			return Config{}, &ExitError{Code: ExitUsage, Message: fmt.Sprintf("cannot read the config file %s", path)}
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			// The decoder's message can quote file content, which holds the token.
			return Config{}, &ExitError{Code: ExitUsage, Message: fmt.Sprintf("the config file %s is not valid JSON", path)}
		}
	}
	if v := getenv("YUMDRAKE_SERVER"); v != "" {
		cfg.Server = v
	}
	if v := getenv("YUMDRAKE_TOKEN"); v != "" {
		cfg.Token, cfg.TokenID, cfg.TokenFromEnv = v, 0, true
	}
	cfg.InsecureHTTP = getenv("YUMDRAKE_INSECURE_HTTP") == "1"
	return cfg, nil
}

// SaveConfig writes the config to path with mode 0600, creating the directory
// with mode 0700. The write goes through a temporary file so a crash never
// leaves half a file.
func SaveConfig(path string, c Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return &ExitError{Code: ExitUsage, Message: fmt.Sprintf("cannot create %s: %v", dir, errors.Unwrap(err))}
	}
	// A directory made earlier with looser modes holds the token file: tighten
	// it when we own it (chmod fails otherwise, and then we leave it alone).
	if info, err := os.Stat(dir); err == nil && info.Mode().Perm()&0o077 != 0 {
		_ = os.Chmod(dir, 0o700) //nolint:gosec // a directory needs the execute bit
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return &ExitError{Code: ExitUsage, Message: fmt.Sprintf("cannot write in %s", dir)}
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return &ExitError{Code: ExitUsage, Message: fmt.Sprintf("cannot write %s", path)}
	}
	return nil
}
