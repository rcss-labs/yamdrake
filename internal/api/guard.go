package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// guard lets a request through only when it carries the bootstrap token as a
// bearer token. This is temporary: it goes away when sign-in arrives.
//
// Both sides are hashed first so the comparison takes the same time whatever
// the length of what the caller sent.
func guard(token string, next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		got := sha256.Sum256([]byte(presented))
		if !ok || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="yumdrake"`)
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "a valid bootstrap token is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
