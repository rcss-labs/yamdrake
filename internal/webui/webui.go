// Package webui serves the built web application embedded in the binary.
package webui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

// dist holds the output of `make web-build`. The all: prefix keeps .gitkeep in
// the embed so the package compiles before the first build.
//
//go:embed all:dist
var dist embed.FS

const (
	indexName     = "index.html"
	notBuiltText  = "The web application is not built. Run make web-build.\n"
	assetsPrefix  = "assets/"
	immutableType = "public, max-age=31536000, immutable"

	// contentSecurityPolicy allows scripts, styles, fonts and connections only
	// from the same origin. Images may also be data: URIs.
	contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; font-src 'self'; " +
		"connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'self'; " +
		"frame-ancestors 'none'; form-action 'self'"
)

// Handler serves the embedded build; unknown paths without a file extension
// get index.html.
func Handler() http.Handler {
	build, err := fs.Sub(dist, "dist")
	if err != nil {
		panic("webui: embedded dist directory is missing: " + err.Error()) // cannot happen: the embed directive names it
	}
	return newHandler(build)
}

// newHandler serves the build in fsys (rooted at the build directory).
func newHandler(fsys fs.FS) http.Handler {
	// index.html is the one file every app route needs; read it once.
	index, indexErr := fs.ReadFile(fsys, indexName)
	indexETag := ""
	if indexErr == nil {
		sum := sha256.Sum256(index)
		indexETag = `"` + hex.EncodeToString(sum[:8]) + `"`
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w.Header())

		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}

		name, ok := buildPath(r.URL.Path)
		// The mux keeps these paths away from the web handler, but it does not
		// treat an encoded slash as a separator (/api%2fv1/me), so check the
		// decoded, cleaned path here too.
		if !ok || isReserved("/"+name) {
			http.NotFound(w, r)
			return
		}

		if indexErr != nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusServiceUnavailable)
			if r.Method != http.MethodHead {
				_, _ = w.Write([]byte(notBuiltText))
			}
			return
		}

		if name != "" && name != indexName {
			if file, err := fsys.Open(name); err == nil {
				defer func() { _ = file.Close() }()
				if info, err := file.Stat(); err == nil && !info.IsDir() {
					if body, ok := file.(io.ReadSeeker); ok {
						serve(w, r, name, "", body)
						return
					}
					if data, err := io.ReadAll(file); err == nil {
						serve(w, r, name, "", bytes.NewReader(data))
						return
					}
				}
			}
			// A missing file with an extension is a missing file, not an app route.
			if path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
		}
		serve(w, r, indexName, indexETag, bytes.NewReader(index))
	})
}

// isReserved reports whether a cleaned, rooted path belongs to the API or the
// health endpoints rather than the web application.
func isReserved(rooted string) bool {
	if rooted == "/healthz" || rooted == "/readyz" {
		return true
	}
	for _, prefix := range []string{"/api", "/saml"} {
		if rooted == prefix || strings.HasPrefix(rooted, prefix+"/") {
			return true
		}
	}
	return false
}

// buildPath turns a request path into a name inside the build. It rejects
// backslashes and cleans the path against a rooted "/" so ".." cannot climb
// above the build; fs.FS implementations also refuse invalid names.
func buildPath(requestPath string) (string, bool) {
	if strings.ContainsRune(requestPath, '\\') {
		return "", false
	}
	name := strings.TrimPrefix(path.Clean("/"+requestPath), "/")
	if name != "" && !fs.ValidPath(name) {
		return "", false
	}
	return name, true
}

func serve(w http.ResponseWriter, r *http.Request, name, etag string, body io.ReadSeeker) {
	h := w.Header()
	h.Set("Content-Type", contentType(name))
	if strings.HasPrefix(name, assetsPrefix) {
		h.Set("Cache-Control", immutableType)
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	if etag != "" {
		h.Set("ETag", etag)
	}
	http.ServeContent(w, r, name, time.Time{}, body)
}

func contentType(name string) string {
	ext := strings.ToLower(path.Ext(name))
	switch ext {
	case ".woff2":
		return "font/woff2"
	case ".woff":
		return "font/woff"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}

func setSecurityHeaders(h http.Header) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", contentSecurityPolicy)
}
