//go:build integration

package api

import (
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"
)

func expiryIn(d time.Duration) string { return time.Now().UTC().Add(d).Format(time.RFC3339) }

func (h *harness) createFreeze(body map[string]any) map[string]any {
	h.t.Helper()
	return h.mustDo("POST", "/api/v1/admin/freezes", body, http.StatusCreated)
}

func TestCreateFreezeReturnsTheFreezeAndAudits(t *testing.T) {
	h := newHarness(t)
	created := h.createFreeze(map[string]any{"label": "dev", "reason": "quarter-end", "expires_at": expiryIn(48 * time.Hour)})
	for _, key := range []string{"id", "label", "environment", "reason", "created_by", "created_by_name", "created_at", "expires_at", "ended_at", "ended_by_name", "active"} {
		if _, ok := created[key]; !ok {
			t.Errorf("freeze response lacks %q: %v", key, created)
		}
	}
	if len(created) != 11 {
		t.Errorf("freeze response has %d keys, want exactly the 11 the web app reads: %v", len(created), created)
	}
	if created["label"] != "dev" || created["environment"] != "" || created["reason"] != "quarter-end" || created["active"] != true {
		t.Errorf("unexpected freeze %v", created)
	}
	if created["ended_at"] != nil || created["ended_by_name"] != "" {
		t.Errorf("a new freeze has ended: %v", created)
	}
	if got := int64(created["created_by"].(float64)); got != h.admin.ID {
		t.Errorf("created_by = %d, want %d", got, h.admin.ID)
	}
	if created["created_by_name"] != h.admin.Name {
		t.Errorf("created_by_name = %v, want %q", created["created_by_name"], h.admin.Name)
	}
	if !slices.Contains(h.auditActions(), "freeze.create") {
		t.Errorf("no freeze.create audit entry: %v", h.auditActions())
	}
}

func TestCreateFreezeValidation(t *testing.T) {
	h := newHarness(t)
	valid := func() map[string]any {
		return map[string]any{"environment": "staging", "reason": "hold", "expires_at": expiryIn(time.Hour)}
	}
	cases := map[string]func(m map[string]any){
		"no reason":          func(m map[string]any) { delete(m, "reason") },
		"blank reason":       func(m map[string]any) { m["reason"] = "   " },
		"both scopes":        func(m map[string]any) { m["label"] = "dev" },
		"neither scope":      func(m map[string]any) { delete(m, "environment") },
		"expiry in the past": func(m map[string]any) { m["expires_at"] = expiryIn(-time.Minute) },
		"expiry beyond 30d":  func(m map[string]any) { m["expires_at"] = expiryIn(31 * 24 * time.Hour) },
		"no expiry":          func(m map[string]any) { delete(m, "expires_at") },
		"expiry not a time":  func(m map[string]any) { m["expires_at"] = "tomorrow" },
		"bad environment":    func(m map[string]any) { m["environment"] = "Not An Environment" },
	}
	for name, mutate := range cases {
		body := valid()
		mutate(body)
		code, got := h.do("POST", "/api/v1/admin/freezes", body)
		if code != http.StatusBadRequest || errorCode(t, got) != "invalid" {
			t.Errorf("%s: got %d %v, want 400 invalid", name, code, got)
		}
	}
	if slices.Contains(h.auditActions(), "freeze.create") {
		t.Errorf("a refused freeze was audited")
	}
	// Just inside the cap is accepted.
	body := valid()
	body["expires_at"] = expiryIn(30*24*time.Hour - time.Minute)
	h.createFreeze(body)
}

func TestLiftFreezeEndsItAndAudits(t *testing.T) {
	h := newHarness(t)
	created := h.createFreeze(map[string]any{"environment": "staging", "reason": "hold", "expires_at": expiryIn(time.Hour)})
	path := "/api/v1/admin/freezes/" + strconv.FormatInt(int64(created["id"].(float64)), 10)

	h.mustDo("DELETE", path, nil, http.StatusNoContent)
	if !slices.Contains(h.auditActions(), "freeze.lift") {
		t.Errorf("no freeze.lift audit entry: %v", h.auditActions())
	}
	// A second lift finds nothing active.
	code, got := h.do("DELETE", path, nil)
	if code != http.StatusNotFound || errorCode(t, got) != "not_found" {
		t.Errorf("second lift = %d %v, want 404 not_found", code, got)
	}
	code, _ = h.do("DELETE", "/api/v1/admin/freezes/999999", nil)
	if code != http.StatusNotFound {
		t.Errorf("lift of an unknown freeze = %d, want 404", code)
	}
}

func TestListFreezesSplitsActiveAndRecent(t *testing.T) {
	h := newHarness(t)
	empty := h.mustDo("GET", "/api/v1/freezes", nil, http.StatusOK)
	for _, key := range []string{"active", "ended"} {
		if list, ok := empty[key].([]any); !ok || len(list) != 0 {
			t.Errorf("empty %s = %v, want []", key, empty[key])
		}
	}

	keep := h.createFreeze(map[string]any{"label": "dev", "reason": "stays", "expires_at": expiryIn(time.Hour)})
	lifted := h.createFreeze(map[string]any{"label": "dev", "reason": "lifted", "expires_at": expiryIn(time.Hour)})
	h.mustDo("DELETE", "/api/v1/admin/freezes/"+strconv.FormatInt(int64(lifted["id"].(float64)), 10), nil, http.StatusNoContent)

	got := h.mustDo("GET", "/api/v1/freezes", nil, http.StatusOK)
	active, ended := got["active"].([]any), got["ended"].([]any)
	if len(active) != 1 || active[0].(map[string]any)["id"] != keep["id"] || active[0].(map[string]any)["active"] != true {
		t.Errorf("active = %v, want only the freeze that stays", active)
	}
	if len(ended) != 1 || ended[0].(map[string]any)["id"] != lifted["id"] || ended[0].(map[string]any)["active"] != false {
		t.Errorf("ended = %v, want only the lifted freeze", ended)
	}
	if ended[0].(map[string]any)["ended_at"] == nil || ended[0].(map[string]any)["ended_by_name"] != h.admin.Name {
		t.Errorf("a lifted freeze lacks ended_at or names its lifter: %v", ended[0])
	}
	if _, present := got["recent"]; present {
		t.Errorf("the list still has a recent key: %v", got)
	}
}

func TestFreezeRoutesNeedManageAccessToWrite(t *testing.T) {
	h := newHarness(t)
	user := h.newUser("viewer")
	token, _ := h.newToken(user.ID)
	body := map[string]any{"environment": "staging", "reason": "hold", "expires_at": expiryIn(time.Hour)}
	h.expect(h.send(sent{method: "POST", path: "/api/v1/admin/freezes", bearer: token, body: body}), http.StatusForbidden, "forbidden")
	h.expect(h.send(sent{method: "DELETE", path: "/api/v1/admin/freezes/1", bearer: token}), http.StatusForbidden, "forbidden")
	// Anyone who may view can list them.
	h.expect(h.send(sent{method: "GET", path: "/api/v1/freezes", bearer: token}), http.StatusOK, "")
	if slices.Contains(h.auditActions(), "freeze.create") {
		t.Errorf("a refused request was audited")
	}
}
