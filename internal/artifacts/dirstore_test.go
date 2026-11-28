package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rcss-labs/yamdrake/internal/secrets"
)

func testCipher(t *testing.T) secrets.Cipher {
	t.Helper()
	c, err := secrets.NewLocal([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	return c
}

func newTestStore(t *testing.T) (Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewDir(dir, testCipher(t))
	if err != nil {
		t.Fatalf("NewDir: %v", err)
	}
	return s, dir
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestDirPutGetRoundTripAndChecksum(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	data := []byte("services:\n  api: {}\n")

	key, sum, err := s.Put(ctx, 7, "api", "manifest", "app.yaml", false, data)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if want := "manifests/7/api/manifest/app.yaml"; key != want {
		t.Errorf("key = %q, want %q", key, want)
	}
	if want := sha256Hex(data); sum != want {
		t.Errorf("sha256 = %q, want %q", sum, want)
	}
	got, err := s.Get(ctx, key, sum)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("Get returned %q, want %q", got, data)
	}
}

func TestDirPreviousCopiesUseSeparateKeys(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	currentKey, currentSum, err := s.Put(ctx, 7, "api", "manifest", "app.yaml", false, []byte("current"))
	if err != nil {
		t.Fatalf("Put current: %v", err)
	}
	previousKey, previousSum, err := s.Put(ctx, 7, "api", "manifest", "app.yaml", true, []byte("previous"))
	if err != nil {
		t.Fatalf("Put previous: %v", err)
	}
	if currentKey == previousKey {
		t.Fatalf("current and previous share key %q", currentKey)
	}
	if want := "manifests/7/api/manifest/previous/app.yaml"; previousKey != want {
		t.Errorf("previous key = %q, want %q", previousKey, want)
	}
	if got, err := s.Get(ctx, currentKey, currentSum); err != nil || string(got) != "current" {
		t.Errorf("current Get = %q, %v", got, err)
	}
	if got, err := s.Get(ctx, previousKey, previousSum); err != nil || string(got) != "previous" {
		t.Errorf("previous Get = %q, %v", got, err)
	}
}

func TestDirBytesOnDiskAreNotPlaintext(t *testing.T) {
	s, dir := newTestStore(t)
	marker := []byte("plaintext-marker-7f3a")

	key, _, err := s.Put(context.Background(), 1, "svc", "kind", "name", false, marker)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(key))) //nolint:gosec // the test reads its own temp dir
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if bytes.Contains(raw, marker) {
		t.Fatal("stored file contains the plaintext")
	}
}

func TestDirCorruptFileIsErrCorrupt(t *testing.T) {
	s, dir := newTestStore(t)
	ctx := context.Background()

	key, sum, err := s.Put(ctx, 1, "svc", "kind", "name", false, []byte("config body"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	path := filepath.Join(dir, filepath.FromSlash(key))
	raw, err := os.ReadFile(path) //nolint:gosec // the test reads its own temp dir
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	raw[len(raw)-1] ^= 0x01
	if err := os.WriteFile(path, raw, 0o600); err != nil { //nolint:gosec // the test writes its own temp dir

		t.Fatalf("corrupt stored file: %v", err)
	}
	if _, err := s.Get(ctx, key, sum); !errors.Is(err, ErrCorrupt) {
		t.Errorf("Get of a flipped byte: err = %v, want ErrCorrupt", err)
	}

	goodKey, _, err := s.Put(ctx, 2, "svc", "kind", "name", false, []byte("another body"))
	if err != nil {
		t.Fatalf("Put second: %v", err)
	}
	if _, err := s.Get(ctx, goodKey, sha256Hex([]byte("not the body"))); !errors.Is(err, ErrCorrupt) {
		t.Errorf("Get with a wrong checksum: err = %v, want ErrCorrupt", err)
	}
}

func TestDirRefusesKeysOutsideItsDirectory(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "store")
	s, err := NewDir(dir, testCipher(t))
	if err != nil {
		t.Fatalf("NewDir: %v", err)
	}
	sentinel := filepath.Join(parent, "x")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	keys := []string{
		"manifests/../x",
		"../manifests/1/a/b/c",
		"/etc/passwd",
		"manifests/1/a/./b",
		"other/1/a/b/c",
		"manifests\\..\\x",
	}
	for _, key := range keys {
		if _, err := s.Get(context.Background(), key, ""); err == nil {
			t.Errorf("Get(%q) succeeded, want an error", key)
		}
	}
	got, err := os.ReadFile(sentinel) //nolint:gosec // the test reads its own temp dir
	if err != nil || string(got) != "keep" {
		t.Errorf("sentinel outside the store changed: %q, %v", got, err)
	}
}

func TestDirPutRejectsBadComponents(t *testing.T) {
	s, _ := newTestStore(t)
	bad := []string{"", "a/b", ".", "a..b", "a\x00b", "a\\b"}
	for _, v := range bad {
		if _, _, err := s.Put(context.Background(), 1, v, "kind", "name", false, []byte("x")); err == nil {
			t.Errorf("service %q accepted", v)
		}
		if _, _, err := s.Put(context.Background(), 1, "svc", v, "name", false, []byte("x")); err == nil {
			t.Errorf("kind %q accepted", v)
		}
		if _, _, err := s.Put(context.Background(), 1, "svc", "kind", v, false, []byte("x")); err == nil {
			t.Errorf("name %q accepted", v)
		}
	}
}

func TestDirInterruptedWriteLeavesNoFileUnderFinalName(t *testing.T) {
	s, dir := newTestStore(t)
	s.(*dirStore).beforeRename = func(string) error { return errors.New("simulated crash") }

	key, _, err := s.Put(context.Background(), 1, "svc", "kind", "name", false, []byte("body"))
	if err == nil {
		t.Fatal("Put succeeded although the write was interrupted")
	}
	if _, statErr := os.Stat(filepath.Join(dir, filepath.FromSlash("manifests/1/svc/kind/name"))); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("final file exists after an interrupted write (key %q): %v", key, statErr)
	}
	leftover := false
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), ".tmp-") {
			leftover = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if leftover {
		t.Error("a .tmp- file was left behind")
	}
}

func TestDirFilesAreOwnerOnly(t *testing.T) {
	s, dir := newTestStore(t)
	key, _, err := s.Put(context.Background(), 1, "svc", "kind", "name", false, []byte("body"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(key)))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 600", perm)
	}
}

func TestDirPutRefusesDataOverTheLimit(t *testing.T) {
	s, _ := newTestStore(t)
	data := make([]byte, MaxObjectBytes+1)
	if _, _, err := s.Put(context.Background(), 1, "svc", "kind", "name", false, data); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Put over the limit: err = %v, want ErrTooLarge", err)
	}
}
