// Package webui serves the built web application embedded in the binary.
package webui

import (
	"bytes"
	"embed"
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w.Header())

		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}

		index, err := fs.ReadFile(fsys, indexName)
		if err != nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusServiceUnavailable)
			if r.Method != http.MethodHead {
				_, _ = w.Write([]byte(notBuiltText))
			}
			return
		}

		name, ok := buildPath(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if name != "" && name != indexName {
			if data, err := fs.ReadFile(fsys, name); err == nil {
				serve(w, r, name, data)
				return
			}
			// A missing file with an extension is a missing file, not an app route.
			if path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
		}
		serve(w, r, indexName, index)
	})
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

func serve(w http.ResponseWriter, r *http.Request, name string, data []byte) {
	h := w.Header()
	h.Set("Content-Type", contentType(name))
	if strings.HasPrefix(name, assetsPrefix) {
		h.Set("Cache-Control", immutableType)
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
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
