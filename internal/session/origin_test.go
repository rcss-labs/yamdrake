package session

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCookieAttributes(t *testing.T) {
	created := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	rec := httptest.NewRecorder()
	SetCookie(rec, Session{ID: "abc", CreatedAt: created})

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, want 1", len(cookies))
	}
	c := cookies[0]
	if c.Name != "yumdrake_session" || c.Value != "abc" {
		t.Fatalf("cookie name/value = %q/%q", c.Name, c.Value)
	}
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Fatalf("attributes: secure=%v httponly=%v samesite=%v path=%q", c.Secure, c.HttpOnly, c.SameSite, c.Path)
	}
	if !c.Expires.Equal(created.Add(24 * time.Hour)) {
		t.Fatalf("Expires = %v, want creation + 24h", c.Expires)
	}

	clear := httptest.NewRecorder()
	ClearCookie(clear)
	cc := clear.Result().Cookies()
	if len(cc) != 1 || cc[0].Name != "yumdrake_session" || cc[0].Value != "" || cc[0].MaxAge >= 0 {
		t.Fatalf("ClearCookie did not expire the cookie: %+v", cc)
	}
	if !cc[0].Secure || !cc[0].HttpOnly || cc[0].SameSite != http.SameSiteLaxMode || cc[0].Path != "/" {
		t.Fatal("ClearCookie must keep the same attributes")
	}
}

func TestIDFromRequest(t *testing.T) {
	valid := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" // 43 base64url chars
	cases := []struct {
		name   string
		cookie string
		want   bool
	}{
		{"valid", valid, true},
		{"too short", "abc", false},
		{"too long", valid + "A", false},
		{"not base64url", valid[:42] + "+", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.AddCookie(&http.Cookie{Name: CookieName, Value: tc.cookie}) //nolint:gosec // request cookie in a test
			id, ok := IDFromRequest(r)
			if ok != tc.want || (ok && id != tc.cookie) {
				t.Fatalf("IDFromRequest = %q, %v; want ok=%v", id, ok, tc.want)
			}
		})
	}
	if _, ok := IDFromRequest(httptest.NewRequest("GET", "/", nil)); ok {
		t.Fatal("request without cookie must report no id")
	}
}

func TestSameOrigin(t *testing.T) {
	const public = "https://yumdrake.example.com"
	cases := []struct {
		name    string
		method  string
		origin  string
		fetch   string
		publicU string
		want    bool
	}{
		{"GET any origin", "GET", "https://evil.example", "", public, true},
		{"HEAD", "HEAD", "https://evil.example", "", public, true},
		{"OPTIONS", "OPTIONS", "https://evil.example", "", public, true},
		{"POST matching origin", "POST", public, "", public, true},
		{"POST other origin", "POST", "https://evil.example", "", public, false},
		{"POST other scheme", "POST", "http://yumdrake.example.com", "", public, false},
		{"POST other port", "POST", "https://yumdrake.example.com:8443", "", public, false},
		{"POST origin null", "POST", "null", "", public, false},
		{"POST mismatching origin ignores same-origin fetch header", "POST", "https://evil.example", "same-origin", public, false},
		{"POST no origin same-origin", "POST", "", "same-origin", public, true},
		{"POST no origin cross-site", "POST", "", "cross-site", public, false},
		{"POST no origin same-site", "POST", "", "same-site", public, false},
		{"POST neither header", "POST", "", "", public, false},
		{"DELETE neither header", "DELETE", "", "", public, false},
		{"localhost with port", "POST", "http://localhost:8080", "", "http://localhost:8080", true},
		{"unparseable public url", "POST", public, "", "://bad", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "/api/v1/x", nil)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.fetch != "" {
				r.Header.Set("Sec-Fetch-Site", tc.fetch)
			}
			if got := SameOrigin(r, tc.publicU); got != tc.want {
				t.Fatalf("SameOrigin = %v, want %v", got, tc.want)
			}
		})
	}
}
