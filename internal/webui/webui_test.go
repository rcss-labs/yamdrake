package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const wantCSP = "default-src 'self'; script-src 'self'; style-src 'self'; font-src 'self'; connect-src 'self'; " +
	"img-src 'self' data:; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'"

const indexHTML = "<!doctype html><title>Yumdrake</title><div id=root></div>"

func builtFS() fstest.MapFS {
	return fstest.MapFS{
		".gitkeep":              {Data: nil},
		"index.html":            {Data: []byte(indexHTML)},
		"assets/index-abc.js":   {Data: []byte("console.log(1)")},
		"assets/index-abc.css":  {Data: []byte("body{}")},
		"assets/plex-abc.woff2": {Data: []byte("font")},
		"favicon.svg":           {Data: []byte("<svg/>")},
		"secret.txt":            {Data: []byte("outside")},
	}
}

func get(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func assertSecurityHeaders(t *testing.T, label string, rec *httptest.ResponseRecorder) {
	t.Helper()
	want := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "same-origin",
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": wantCSP,
	}
	for name, value := range want {
		if got := rec.Header().Get(name); got != value {
			t.Errorf("%s: %s = %q, want %q", label, name, got, value)
		}
	}
}

func TestServesIndexForAppRoutes(t *testing.T) {
	h := newHandler(builtFS())
	for _, target := range []string{"/", "/jobs/3?tab=definition", "/admin/users", "/sign-in", "/jobs"} {
		rec := get(t, h, http.MethodGet, target)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status %d, want 200", target, rec.Code)
		}
		if rec.Body.String() != indexHTML {
			t.Errorf("GET %s: body %q, want index.html", target, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("GET %s: Cache-Control %q, want no-cache", target, got)
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
			t.Errorf("GET %s: Content-Type %q, want text/html", target, got)
		}
	}
	for _, target := range []string{"/assets/missing.js", "/favicon.ico", "/nope.css"} {
		rec := get(t, h, http.MethodGet, target)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", target, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "Yumdrake") {
			t.Errorf("GET %s: served index.html for a missing file", target)
		}
	}
}

func TestOnlyGetAndHeadAreServed(t *testing.T) {
	h := newHandler(builtFS())
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := get(t, h, method, "/jobs")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status %d, want 405", method, rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("%s: Allow %q, want %q", method, got, "GET, HEAD")
		}
		assertSecurityHeaders(t, method, rec)
	}
	rec := get(t, h, http.MethodHead, "/jobs")
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD: status %d with %d body bytes, want 200 and none", rec.Code, rec.Body.Len())
	}
}

func TestAssetCachingAndSecurityHeaders(t *testing.T) {
	h := newHandler(builtFS())
	assets := map[string]string{
		"/assets/index-abc.js":   "text/javascript",
		"/assets/index-abc.css":  "text/css",
		"/assets/plex-abc.woff2": "font/woff2",
	}
	for target, wantType := range assets {
		rec := get(t, h, http.MethodGet, target)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d, want 200", target, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Errorf("GET %s: Cache-Control %q", target, got)
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, wantType) {
			t.Errorf("GET %s: Content-Type %q, want %s", target, got, wantType)
		}
		assertSecurityHeaders(t, target, rec)
	}

	index := get(t, h, http.MethodGet, "/index.html")
	if got := index.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("index.html: Cache-Control %q, want no-cache", got)
	}

	for _, target := range []string{"/", "/jobs/3", "/missing.js", "/favicon.svg"} {
		assertSecurityHeaders(t, target, get(t, h, http.MethodGet, target))
	}
}

func TestReports503WithoutBuild(t *testing.T) {
	const want = "The web application is not built. Run make web-build."
	h := newHandler(fstest.MapFS{".gitkeep": {}})
	for _, target := range []string{"/", "/jobs", "/assets/x.js"} {
		rec := get(t, h, http.MethodGet, target)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("GET %s: status %d, want 503", target, rec.Code)
		}
		if got := strings.TrimSpace(rec.Body.String()); got != want {
			t.Errorf("GET %s: body %q, want %q", target, got, want)
		}
		assertSecurityHeaders(t, target, rec)
	}
}

func TestPathsCannotEscapeTheBuild(t *testing.T) {
	// The build lives in dist/; secret.txt sits next to it, outside the tree
	// the handler is given.
	root := fstest.MapFS{
		"dist/index.html":          {Data: []byte(indexHTML)},
		"dist/assets/index-abc.js": {Data: []byte("console.log(1)")},
		"secret.txt":               {Data: []byte("outside")},
	}
	build, err := fs.Sub(root, "dist")
	if err != nil {
		t.Fatal(err)
	}
	h := newHandler(build)
	for _, target := range []string{
		"/../secret.txt",
		"/assets/../../secret.txt",
		"/%2e%2e/secret.txt",
		"/assets/%2e%2e/%2e%2e/secret.txt",
		"/assets/..%2f..%2fsecret.txt",
		`/assets\..\..\secret.txt`,
		"/assets/%5c..%5c..%5csecret.txt",
		"//secret.txt",
	} {
		rec := get(t, h, http.MethodGet, target)
		if strings.Contains(rec.Body.String(), "outside") {
			t.Errorf("GET %s: served a file outside the build", target)
		}
		assertSecurityHeaders(t, target, rec)
	}
	// Backslashes are never path separators here.
	if rec := get(t, h, http.MethodGet, `/assets%5cindex-abc.js`); rec.Code != http.StatusNotFound {
		t.Errorf("backslash path: status %d, want 404", rec.Code)
	}
}
