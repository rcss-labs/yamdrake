package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "ghp_supersecrettoken"

func newTokenClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, srv.Client(), Credentials{Method: "token", Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTokenAuthSendsBearer(t *testing.T) {
	var got http.Header
	c := newTokenClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = fmt.Fprint(w, `{"sha":"abc123"}`)
	}))
	if _, err := c.HeadCommit(context.Background(), "o/r", "main"); err != nil {
		t.Fatal(err)
	}
	if got.Get("Authorization") != "Bearer "+testToken {
		t.Errorf("Authorization = %q", got.Get("Authorization"))
	}
	if got.Get("Accept") != "application/vnd.github+json" {
		t.Errorf("Accept = %q", got.Get("Accept"))
	}
	if got.Get("User-Agent") != "yumdrake" {
		t.Errorf("User-Agent = %q", got.Get("User-Agent"))
	}
}

func TestHeadCommit(t *testing.T) {
	c := newTokenClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/repos/o/r/commits/feat/x" {
			t.Errorf("path = %s", r.URL.EscapedPath())
		}
		_, _ = fmt.Fprint(w, `{"sha":"deadbeef"}`)
	}))
	sha, err := c.HeadCommit(context.Background(), "o/r", "feat/x")
	if err != nil || sha != "deadbeef" {
		t.Fatalf("sha=%q err=%v", sha, err)
	}
}

func TestListFilesFetchesOnlyTheSubTreeOfDir(t *testing.T) {
	c := newTokenClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/git/trees/sha1:jobs/team" || r.URL.Query().Get("recursive") != "1" {
			t.Errorf("url = %s", r.URL)
		}
		_, _ = fmt.Fprint(w, `{"truncated":false,"tree":[
			{"path":"sub","type":"tree"},
			{"path":"a.yaml","type":"blob"},
			{"path":"sub/b.yaml","type":"blob"}]}`)
	}))
	got, err := c.ListFiles(context.Background(), "o/r", "sha1", "/jobs/team/")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "jobs/team/a.yaml,jobs/team/sub/b.yaml" {
		t.Errorf("got %v", got)
	}
}

func TestListFilesWholeRepositoryWhenDirIsEmpty(t *testing.T) {
	c := newTokenClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/git/trees/sha1" || r.URL.Query().Get("recursive") != "1" {
			t.Errorf("url = %s", r.URL)
		}
		_, _ = fmt.Fprint(w, `{"truncated":false,"tree":[{"path":"jobs","type":"tree"},{"path":"jobs/a.yaml","type":"blob"},{"path":"b.yaml","type":"blob"}]}`)
	}))
	got, err := c.ListFiles(context.Background(), "o/r", "sha1", "")
	if err != nil || strings.Join(got, ",") != "jobs/a.yaml,b.yaml" {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestListFilesTruncatedIsAnError(t *testing.T) {
	for _, dir := range []string{"", "inventory"} {
		c := newTokenClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, `{"truncated":true,"tree":[{"path":"a","type":"blob"}]}`)
		}))
		_, err := c.ListFiles(context.Background(), "o/r", "sha1", dir)
		if err == nil || !strings.Contains(err.Error(), "too large") {
			t.Errorf("dir %q: err = %v, want a clear truncated-tree error", dir, err)
		}
	}
}

func TestReadFileAtCommit(t *testing.T) {
	c := newTokenClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/repos/o/r/contents/dir/my%20file.yaml" || r.URL.Query().Get("ref") != "sha1" {
			t.Errorf("url = %s", r.URL)
		}
		enc := base64.StdEncoding.EncodeToString([]byte("hello: world\n"))
		// The real API wraps base64 at 60 columns.
		enc = enc[:8] + "\n" + enc[8:]
		_, _ = fmt.Fprintf(w, `{"type":"file","encoding":"base64","content":%q}`, enc)
	}))
	b, err := c.ReadFile(context.Background(), "o/r", "sha1", "dir/my file.yaml")
	if err != nil || string(b) != "hello: world\n" {
		t.Fatalf("b=%q err=%v", b, err)
	}
}

func TestBranchExists(t *testing.T) {
	c := newTokenClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/branches/main") {
			_, _ = fmt.Fprint(w, `{"name":"main"}`)
			return
		}
		http.NotFound(w, r)
	}))
	ok, err := c.BranchExists(context.Background(), "o/r", "main")
	if err != nil || !ok {
		t.Fatalf("main: ok=%v err=%v", ok, err)
	}
	ok, err = c.BranchExists(context.Background(), "o/r", "nope")
	if err != nil || ok {
		t.Fatalf("nope: ok=%v err=%v", ok, err)
	}
}

func TestNotFoundMapsToErrNotFound(t *testing.T) {
	c := newTokenClient(t, http.NotFoundHandler())
	if _, err := c.HeadCommit(context.Background(), "o/r", "main"); !errors.Is(err, ErrNotFound) {
		t.Errorf("HeadCommit: %v", err)
	}
	if _, err := c.ReadFile(context.Background(), "o/r", "sha", "x.yaml"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReadFile: %v", err)
	}
}

func TestRateLimitMapsToRateLimitErrorWithRetryAfter(t *testing.T) {
	cases := map[string]func(w http.ResponseWriter){
		"403 reset header": func(w http.ResponseWriter) {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprint(time.Now().Add(90*time.Second).Unix()))
			w.WriteHeader(http.StatusForbidden)
		},
		"429 retry-after": func(w http.ResponseWriter) {
			w.Header().Set("Retry-After", "90")
			w.WriteHeader(http.StatusTooManyRequests)
		},
		"403 retry-after": func(w http.ResponseWriter) {
			w.Header().Set("Retry-After", "90")
			w.WriteHeader(http.StatusForbidden)
		},
	}
	for name, respond := range cases {
		t.Run(name, func(t *testing.T) {
			c := newTokenClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { respond(w) }))
			_, err := c.HeadCommit(context.Background(), "o/r", "main")
			var rl *RateLimitError
			if !errors.As(err, &rl) {
				t.Fatalf("err = %v", err)
			}
			if rl.RetryAfter < 80*time.Second || rl.RetryAfter > 91*time.Second {
				t.Errorf("RetryAfter = %v", rl.RetryAfter)
			}
		})
	}
}

func TestPlain403IsOrdinaryError(t *testing.T) {
	c := newTokenClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	_, err := c.HeadCommit(context.Background(), "o/r", "main")
	var rl *RateLimitError
	if err == nil || errors.As(err, &rl) || errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestServerErrorDoesNotLeakToken(t *testing.T) {
	c := newTokenClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, "echo: "+r.Header.Get("Authorization")) //nolint:gosec // test server deliberately echoes the credential
	}))
	_, err := c.HeadCommit(context.Background(), "o/r", "main")
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("err = %v", err)
	}

	// Network failure path: closed server.
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c2, err := New(url, nil, Credentials{Method: "token", Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c2.HeadCommit(context.Background(), "o/r", "main")
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("network err = %v", err)
	}
}

func TestMalformedRepoIsRejected(t *testing.T) {
	c := newTokenClient(t, http.NotFoundHandler())
	for _, repo := range []string{"", "owner", "a/b/c", "/b", "a/", "a/../b"} {
		if _, err := c.HeadCommit(context.Background(), repo, "main"); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("repo %q: err = %v", repo, err)
		}
	}
}

func TestConditionalRequestReusesCachedResultOn304(t *testing.T) {
	var n atomic.Int32
	c := newTokenClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("ETag", `"v1"`)
			_, _ = fmt.Fprint(w, `{"sha":"cafe"}`)
			return
		}
		if r.Header.Get("If-None-Match") != `"v1"` {
			t.Errorf("If-None-Match = %q", r.Header.Get("If-None-Match"))
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	for i := 0; i < 2; i++ {
		sha, err := c.HeadCommit(context.Background(), "o/r", "main")
		if err != nil || sha != "cafe" {
			t.Fatalf("call %d: sha=%q err=%v", i, sha, err)
		}
	}
	if n.Load() != 2 {
		t.Errorf("requests = %d", n.Load())
	}
}

func TestListFilesConditionalRequest(t *testing.T) {
	var n atomic.Int32
	c := newTokenClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("ETag", `"t1"`)
			_, _ = fmt.Fprint(w, `{"tree":[{"path":"a.yaml","type":"blob"}]}`)
			return
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	for i := 0; i < 2; i++ {
		got, err := c.ListFiles(context.Background(), "o/r", "s", "")
		if err != nil || len(got) != 1 || got[0] != "a.yaml" {
			t.Fatalf("call %d: %v %v", i, got, err)
		}
	}
}

func TestConcurrentUseIsSafe(t *testing.T) {
	c := newTokenClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		_, _ = fmt.Fprint(w, `{"sha":"cafe"}`)
	}))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if _, err := c.HeadCommit(context.Background(), "o/r", "main"); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
}

func TestNewValidatesCredentials(t *testing.T) {
	key := newTestKey(t)
	pkcs8, _ := x509.MarshalPKCS8PrivateKey(key)
	pkcs8PEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
	bad := []Credentials{
		{Method: "token"},
		{Method: "app", InstallationID: 1, PrivateKeyPEM: keyPEM(key)},
		{Method: "app", AppID: 1, PrivateKeyPEM: keyPEM(key)},
		{Method: "app", AppID: 1, InstallationID: 1, PrivateKeyPEM: []byte("not pem")},
		{Method: "other", Token: "x"},
	}
	for i, cr := range bad {
		if _, err := New("http://localhost", nil, cr); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
	if _, err := New("", nil, Credentials{Method: "token", Token: "x"}); err == nil {
		t.Error("empty baseURL accepted")
	}
	if _, err := New("http://localhost", nil, Credentials{Method: "app", AppID: 1, InstallationID: 2, PrivateKeyPEM: pkcs8PEM}); err != nil {
		t.Errorf("PKCS#8: %v", err)
	}
	if _, err := New("http://localhost", nil, Credentials{Method: "app", AppID: 1, InstallationID: 2, PrivateKeyPEM: keyPEM(key)}); err != nil {
		t.Errorf("PKCS#1: %v", err)
	}
}

func newTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func keyPEM(key *rsa.PrivateKey) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

// verifyJWT checks the RS256 signature and returns the claims.
func verifyJWT(t *testing.T, jwt string, pub *rsa.PublicKey) map[string]any {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt has %d parts", len(parts))
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("bad signature: %v", err)
	}
	hdr, _ := base64.RawURLEncoding.DecodeString(parts[0])
	var h map[string]any
	_ = json.Unmarshal(hdr, &h)
	if h["alg"] != "RS256" {
		t.Errorf("alg = %v", h["alg"])
	}
	body, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	if err := json.Unmarshal(body, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func TestAppAuthSignsJWTAndCachesInstallationToken(t *testing.T) {
	key := newTestKey(t)
	var exchanges atomic.Int32
	var ttl atomic.Int64
	ttl.Store(int64(10 * time.Minute))
	var apiAuth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations/77/access_tokens" && r.Method == http.MethodPost {
			n := exchanges.Add(1)
			jwt := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			claims := verifyJWT(t, jwt, &key.PublicKey)
			if fmt.Sprint(claims["iss"]) != "42" {
				t.Errorf("iss = %v", claims["iss"])
			}
			if life := claims["exp"].(float64) - claims["iat"].(float64); life != 540 {
				t.Errorf("lifetime = %v", life)
			}
			exp := time.Now().Add(time.Duration(ttl.Load())).UTC().Format(time.RFC3339)
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"token":"inst-token-%d","expires_at":%q}`, n, exp)
			return
		}
		apiAuth.Store(r.Header.Get("Authorization"))
		_, _ = fmt.Fprint(w, `{"sha":"abc"}`)
	}))
	defer srv.Close()
	c, err := New(srv.URL, srv.Client(), Credentials{Method: "app", AppID: 42, InstallationID: 77, PrivateKeyPEM: keyPEM(key)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := c.HeadCommit(ctx, "o/r", "main"); err != nil {
			t.Fatal(err)
		}
	}
	if exchanges.Load() != 1 {
		t.Fatalf("exchanges = %d, want 1", exchanges.Load())
	}
	if apiAuth.Load() != "Bearer inst-token-1" {
		t.Errorf("API call auth = %v", apiAuth.Load())
	}

	// A token expiring in 30 seconds is within the one-minute renewal window.
	ttl.Store(int64(30 * time.Second))
	c.appTokenMu.Lock()
	c.appToken = ""
	c.appTokenMu.Unlock()
	if _, err := c.HeadCommit(ctx, "o/r", "main"); err != nil { // exchange #2, short-lived
		t.Fatal(err)
	}
	if _, err := c.HeadCommit(ctx, "o/r", "main"); err != nil { // stale, exchange #3
		t.Fatal(err)
	}
	if exchanges.Load() != 3 {
		t.Fatalf("exchanges = %d, want 3", exchanges.Load())
	}
}

func TestAppExchangeFailureDoesNotLeakKey(t *testing.T) {
	key := newTestKey(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, r.Header.Get("Authorization")) //nolint:gosec // test server deliberately echoes the credential
	}))
	defer srv.Close()
	c, err := New(srv.URL, srv.Client(), Credentials{Method: "app", AppID: 1, InstallationID: 2, PrivateKeyPEM: keyPEM(key)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.HeadCommit(context.Background(), "o/r", "main")
	if err == nil || strings.Contains(err.Error(), "eyJ") || strings.Contains(err.Error(), "PRIVATE KEY") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallationExchangeFailureIsNotErrNotFound(t *testing.T) {
	key := newTestKey(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	c, err := New(srv.URL, srv.Client(), Credentials{Method: "app", AppID: 1, InstallationID: 2, PrivateKeyPEM: keyPEM(key)})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := c.BranchExists(context.Background(), "o/r", "main")
	if err == nil || ok || errors.Is(err, ErrNotFound) {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if _, err := c.HeadCommit(context.Background(), "o/r", "main"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("HeadCommit err=%v", err)
	}
}

func TestETagCacheStaysBoundedAcrossCommits(t *testing.T) {
	c := newTokenClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"`+r.URL.Path+`"`)
		_, _ = fmt.Fprint(w, `{"sha":"x","tree":[{"path":"a","type":"blob"}]}`)
	}))
	for i := 0; i < 50; i++ {
		if _, err := c.ListFiles(context.Background(), "o/r", fmt.Sprintf("commit%d", i), "d"); err != nil {
			t.Fatal(err)
		}
	}
	c.etagMu.Lock()
	n := len(c.etags)
	c.etagMu.Unlock()
	if n > 1 {
		t.Errorf("cache entries = %d, want 1 for one repo", n)
	}
}
