//go:build integration

package api

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Text that PostgreSQL cannot store (NUL, invalid UTF-8) used to reach it and
// come back as a 500. Every string a request carries is refused first.
func TestControlCharactersAreRefusedEverywhere(t *testing.T) {
	h := newHarness(t)
	user := h.newUser("inputowner")
	sessionID := h.newSession(user.ID)
	cases := []struct {
		name         string
		method, path string
		body         any
		viaSession   bool
	}{
		{"group name", "POST", "/api/v1/admin/groups", map[string]any{"name": "a\x00b"}, false},
		{"group name with a newline", "POST", "/api/v1/admin/groups", map[string]any{"name": "a\nb"}, false},
		{"user email", "POST", "/api/v1/admin/users", map[string]any{"email": "a\x00@example.test", "name": "n", "source": "local", "password": shortPassword}, false},
		{"user name", "POST", "/api/v1/admin/users", map[string]any{"email": "ok@example.test", "name": "n\x00", "source": "local", "password": shortPassword}, false},
		{"grant environment", "POST", "/api/v1/admin/grants", map[string]any{"group_id": 1, "action": "run", "environment": "dev\x00"}, false},
		{"grant label", "POST", "/api/v1/admin/grants", map[string]any{"group_id": 1, "action": "run", "label": "dev\x00"}, false},
		{"folder name", "POST", "/api/v1/folders", map[string]any{"name": "f\x00"}, false},
		{"inventory repo", "PUT", "/api/v1/admin/inventory-source", map[string]any{"repo": "a/b\x00", "branch": "main", "path": "inv"}, false},
		{"connection owner", "POST", "/api/v1/admin/github-connections", map[string]any{"owner": "ac\x00me", "method": "token", "token": "t"}, false},
		{"sso entity id", "PUT", "/api/v1/admin/sso", map[string]any{"entity_id": "e\x00"}, false},
		{"token name", "POST", "/api/v1/tokens", map[string]any{"name": "t\x00"}, true},
		// The path and the query reach the database too.
		{"mapping name in the path", "PUT", "/api/v1/admin/idp-mappings/a%00b", map[string]any{"group_id": 1}, false},
		{"mapping name that is not UTF-8", "GET", "/api/v1/admin/idp-mappings/a%ffb", nil, false},
		{"service name in the path", "GET", "/api/v1/inventory/services/a%00b", nil, false},
		{"NUL in the query", "GET", "/api/v1/admin/audit?limit=1%00", nil, false},
	}
	for _, tc := range cases {
		r := sent{method: tc.method, path: tc.path, bearer: h.adminToken, body: tc.body}
		if tc.viaSession {
			r = sent{method: tc.method, path: tc.path, sessionID: sessionID, origin: publicURL, body: tc.body}
		}
		if rec := h.send(r); rec.Code != http.StatusBadRequest || errorCodeOf(rec) != "invalid" {
			t.Errorf("%s: %s, want 400 invalid", tc.name, failure(rec))
		}
	}
	// A multi-line field that is meant to be multi-line is still accepted past
	// the check (here it fails later, as invalid metadata, not as control text).
	rec := h.send(sent{method: "PUT", path: "/api/v1/admin/sso", bearer: h.adminToken,
		body: map[string]any{"idp_metadata_xml": "<a>\n\t<b/>\n</a>"}})
	if !strings.Contains(rec.Body.String(), "SAML metadata") {
		t.Errorf("multi-line metadata was refused as control text: %s", failure(rec))
	}
}

// The same email that makes any other wrong credential a 401 makes a NUL one a
// 401, not a 500, and answers byte for byte alike.
func TestLoginWithControlCharactersInTheEmailIsAnOrdinaryFailure(t *testing.T) {
	h := newHarness(t)
	reference := h.login(uniqueEmail(t, "nobody"), "not the password at all")
	h.expect(reference, http.StatusUnauthorized, "unauthorized")
	for name, email := range map[string]string{
		"NUL":                "a\x00@example.test",
		"NUL in a known one": strings.Replace(h.admin.Email, "@", "\x00@", 1),
		"newline":            "a\n@example.test",
		"not UTF-8":          "a\xff@example.test",
	} {
		rec := h.login(email, "not the password at all")
		if rec.Code != http.StatusUnauthorized || rec.Body.String() != reference.Body.String() {
			t.Errorf("%s: %s, want the same 401 as any wrong credential", name, failure(rec))
		}
	}
}

func TestGrantEnvironmentMustBeAnEnvironmentName(t *testing.T) {
	h := newHarness(t)
	groupID := h.createGroupVia("envgroup-"+uniqueEmail(t, "g"), false)
	grant := func(environment string) int {
		return h.asAdmin("POST", "/api/v1/admin/grants", map[string]any{"group_id": groupID, "action": "run", "environment": environment}).Code
	}
	for name, environment := range map[string]string{
		"100 kB":                strings.Repeat("a", 100*1024),
		"64 characters":         strings.Repeat("a", 64),
		"upper case":            "Prod",
		"leading hyphen":        "-dev",
		"slash":                 "dev/eu",
		"space":                 "dev eu",
		"trailing newline":      "dev\n",
		"non-ASCII look-alike":  "dеv", // a Cyrillic e
		"an escaped-looking id": url.QueryEscape("dev eu"),
	} {
		if code := grant(environment); code != http.StatusBadRequest {
			t.Errorf("environment %s: status %d, want 400", name, code)
		}
	}
	for _, environment := range []string{"dev", "eu-west-1", "a", strings.Repeat("a", 63)} {
		if code := grant(environment); code != http.StatusCreated {
			t.Errorf("environment %q: status %d, want 201", environment, code)
		}
	}
}

func TestAccessRoutesLimitFreeText(t *testing.T) {
	h := newHarness(t)
	long := strings.Repeat("n", 101)
	for name, tc := range map[string]struct {
		path string
		body map[string]any
	}{
		"user name over 100":  {"/api/v1/admin/users", map[string]any{"email": "a@example.test", "name": long, "source": "local", "password": shortPassword}},
		"user without a name": {"/api/v1/admin/users", map[string]any{"email": "b@example.test", "name": "  ", "source": "local", "password": shortPassword}},
		"email over 254":      {"/api/v1/admin/users", map[string]any{"email": strings.Repeat("a", 250) + "@example.test", "name": "n", "source": "local", "password": shortPassword}},
		"group name over 100": {"/api/v1/admin/groups", map[string]any{"name": long}},
		"sso entity id":       {"/api/v1/admin/sso", map[string]any{"entity_id": strings.Repeat("e", 100*1024)}},
		"sso groups attr":     {"/api/v1/admin/sso", map[string]any{"groups_attribute": strings.Repeat("e", 300)}},
		"sso email attr":      {"/api/v1/admin/sso", map[string]any{"email_attribute": strings.Repeat("e", 300)}},
		"sso name attr":       {"/api/v1/admin/sso", map[string]any{"name_attribute": strings.Repeat("e", 300)}},
	} {
		method := "POST"
		if strings.Contains(tc.path, "/sso") {
			method = "PUT"
		}
		if rec := h.send(sent{method: method, path: tc.path, bearer: h.adminToken, body: tc.body}); rec.Code != http.StatusBadRequest || errorCodeOf(rec) != "invalid" {
			t.Errorf("%s: %s, want 400 invalid", name, failure(rec))
		}
	}
}
