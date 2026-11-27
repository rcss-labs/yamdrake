//go:build integration

package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/rcss-labs/yamdrake/internal/spec"
)

// nullSlices returns the JSON paths in raw where the Go type t declares a slice
// but the document has null. The web application iterates every list, so null
// is a bug. json.RawMessage and []byte are not lists.
func nullSlices(t reflect.Type, raw json.RawMessage, path string) []string {
	isNull := strings.TrimSpace(string(raw)) == "null"
	for t.Kind() == reflect.Pointer {
		if isNull {
			return nil
		}
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return nil
		}
		if isNull {
			return []string{path}
		}
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return nil
		}
		var found []string
		for i, item := range items {
			found = append(found, nullSlices(t.Elem(), item, path+"["+itoa(int64(i))+"]")...)
		}
		return found
	case reflect.Map:
		var items map[string]json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return nil
		}
		var found []string
		for key, item := range items {
			found = append(found, nullSlices(t.Elem(), item, path+"."+key)...)
		}
		return found
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return nil
		}
		return structNullSlices(t, fields, path)
	}
	return nil
}

func structNullSlices(t reflect.Type, fields map[string]json.RawMessage, path string) []string {
	var found []string
	for i := range t.NumField() {
		field := t.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if field.Anonymous && name == "" {
			found = append(found, structNullSlices(field.Type, fields, path)...)
			continue
		}
		if name == "-" || !field.IsExported() {
			continue
		}
		if name == "" {
			name = field.Name
		}
		if value, ok := fields[name]; ok {
			found = append(found, nullSlices(field.Type, value, path+"."+name)...)
		}
	}
	return found
}

// Every list a Plan 2 or sign-in route returns is [] when empty, never null,
// on an empty database and on a populated one.
func TestNoRouteReturnsNullForAList(t *testing.T) {
	h := newHarness(t)
	type route struct {
		path string
		into reflect.Type
	}
	routes := []route{
		{"/api/v1/me", reflect.TypeFor[principalResponse]()},
		{"/api/v1/inventory", reflect.TypeFor[inventoryResponse]()},
		{"/api/v1/folders", reflect.TypeFor[struct {
			Folders []folderResponse `json:"folders"`
		}]()},
		{"/api/v1/jobs", reflect.TypeFor[struct {
			Jobs []jobSummary `json:"jobs"`
		}]()},
		{"/api/v1/admin/github-connections", reflect.TypeFor[struct {
			Connections []connectionResponse `json:"connections"`
		}]()},
	}
	check := func(stage string, routes []route) {
		t.Helper()
		for _, r := range routes {
			code, raw := h.doRaw("GET", r.path, h.adminToken, nil)
			if code != http.StatusOK {
				t.Fatalf("%s: GET %s = %d", stage, r.path, code)
			}
			if found := nullSlices(r.into, raw, ""); len(found) > 0 {
				t.Errorf("%s: GET %s has null where a list belongs: %v", stage, r.path, found)
			}
		}
	}
	check("empty database", routes)

	h.pushInventory("c1")
	h.pushJob("j1")
	h.setInventorySource()
	folder := h.createFolder("Backends")
	created := h.registerJob(folder)
	h.mustDo("POST", "/api/v1/admin/github-connections", map[string]any{"owner": "acme", "method": "token", "token": "ghp_x"}, http.StatusCreated)
	populated := append(routes,
		route{"/api/v1/inventory/services/orders", reflect.TypeFor[spec.Service]()},
		route{"/api/v1/inventory/services/backend-api", reflect.TypeFor[spec.Service]()},
		route{"/api/v1/jobs/" + itoa(int64(created["id"].(float64))), reflect.TypeFor[jobDetail]()},
	)
	check("populated database", populated)
	// The stored job definition is raw JSON in the response type; walk it as a spec.Job.
	_, raw := h.doRaw("GET", "/api/v1/jobs/"+itoa(int64(created["id"].(float64))), h.adminToken, nil)
	var detail struct {
		Definition json.RawMessage `json:"definition"`
	}
	_ = json.Unmarshal(raw, &detail)
	if found := nullSlices(reflect.TypeFor[spec.Job](), detail.Definition, ".definition"); len(found) > 0 {
		t.Errorf("job definition has null where a list belongs: %v", found)
	}
}

// /me and the sign-in answer carry exactly the fields the web application reads.
func TestPrincipalResponsesHaveTheWebContractShape(t *testing.T) {
	h := newHarness(t)
	user := h.newUser("shape")
	h.grant(user.ID, "manage_jobs")
	keys := func(m map[string]any) string {
		var names []string
		for k := range m {
			names = append(names, k)
		}
		sort.Strings(names)
		return strings.Join(names, ",")
	}
	check := func(what string, raw []byte) {
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("%s: decode: %v", what, err)
		}
		if got := keys(body); got != "admin,email,grants,id,name" {
			t.Errorf("%s fields = %s", what, got)
		}
		grants, _ := body["grants"].([]any)
		if len(grants) != 1 {
			t.Fatalf("%s grants = %v, want one", what, body["grants"])
		}
		if got := keys(grants[0].(map[string]any)); got != "action,environment,folder_id,label" {
			t.Errorf("%s grant fields = %s", what, got)
		}
	}
	rec := h.login(user.Email, testPassword)
	h.expect(rec, http.StatusOK, "")
	check("login", rec.Body.Bytes())
	token, _ := h.newToken(user.ID)
	me := h.send(sent{method: "GET", path: "/api/v1/me", bearer: token})
	h.expect(me, http.StatusOK, "")
	check("/me", me.Body.Bytes())
}
