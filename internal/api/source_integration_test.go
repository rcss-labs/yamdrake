//go:build integration

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// registerJobAt registers another job file of the fake jobs repository.
func (h *harness) registerJobAt(folderID int64, path string) map[string]any {
	h.t.Helper()
	return h.mustDo("POST", "/api/v1/jobs", map[string]any{
		"folder_id": folderID, "repo": jobsRepo, "branch": "main", "path": path,
	}, http.StatusCreated)
}

func TestJobAndServiceIncludeSource(t *testing.T) {
	h := newHarness(t)
	h.pushInventory("c1")
	h.setInventorySource()
	h.pushJob("j1")
	folder := h.createFolder("Backends")
	id := int64(h.registerJob(folder)["id"].(float64))

	jobText := string(fixture(t, "jobs/deploy-backend.yaml"))
	job := h.mustDo("GET", fmt.Sprintf("/api/v1/jobs/%d", id), nil, http.StatusOK)
	if job["source"] != jobText {
		t.Errorf("job source = %q, want the file served by GitHub", job["source"])
	}
	service := h.mustDo("GET", "/api/v1/inventory/services/orders", nil, http.StatusOK)
	if want := string(fixture(t, "services/orders.yaml")); service["source"] != want {
		t.Errorf("service source = %q, want %q", service["source"], want)
	}

	// A newer commit with a problem must not replace the last good text.
	h.src.put(jobsRepo, "j2", jobPath, []byte("apiVersion: yumdrake/v1\nkind: Job\njob: deploy-backend\nbogus: true\n"))
	h.src.setHead(jobsRepo, "main", "j2")
	job = h.mustDo("POST", fmt.Sprintf("/api/v1/jobs/%d/sync", id), nil, http.StatusOK)
	if job["problems_commit"] != "j2" {
		t.Fatalf("problems_commit = %v, want j2 (the bad commit was read)", job["problems_commit"])
	}
	if job["source"] != jobText {
		t.Errorf("job source after a bad commit = %q, want the last good text", job["source"])
	}

	// A job that was never good has no text.
	h.src.put(jobsRepo, "j2", "jobs/broken.yaml", []byte("apiVersion: yumdrake/v1\nkind: Job\nbogus: true\n"))
	never := h.registerJobAt(folder, "jobs/broken.yaml")
	if source, present := never["source"]; !present || source != "" {
		t.Errorf("never-good job source = %#v, want the empty string", source)
	}

	// An inventory commit with a problem keeps the active service text.
	h.pushInventory("c2")
	h.src.put(invRepo, "c2", invPath+"/services/orders.yaml", []byte("apiVersion: yumdrake/v1\nkind: Service\nname: orders\nbogus: true\n"))
	h.mustDo("POST", "/api/v1/inventory/sync", nil, http.StatusOK)
	service = h.mustDo("GET", "/api/v1/inventory/services/orders", nil, http.StatusOK)
	if want := string(fixture(t, "services/orders.yaml")); service["source"] != want {
		t.Errorf("service source after a rejected commit = %q, want the active commit's text", service["source"])
	}
}

func TestJobListIncludesServicesAndApprovalLabels(t *testing.T) {
	h := newHarness(t)
	h.pushInventory("c1")
	h.setInventorySource()
	h.pushJob("j1")
	folder := h.createFolder("Backends")
	h.registerJob(folder) // two groups, two services, approval on prod

	lite := strings.Replace(string(fixture(t, "jobs/deploy-backend.yaml")), "job: deploy-backend", "job: deploy-lite", 1)
	if i, j := strings.Index(lite, "approval:"), strings.Index(lite, "notify:"); i < 0 || j < i {
		t.Fatal("fixture layout changed: no approval block before notify")
	} else {
		lite = lite[:i] + lite[j:]
	}
	h.src.put(jobsRepo, "j1", "jobs/lite.yaml", []byte(lite))
	h.registerJobAt(folder, "jobs/lite.yaml")

	h.src.put(jobsRepo, "j1", "jobs/broken.yaml", []byte("apiVersion: yumdrake/v1\nkind: Job\nbogus: true\n"))
	h.registerJobAt(folder, "jobs/broken.yaml")

	code, raw := h.doRaw("GET", "/api/v1/jobs", h.adminToken, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/v1/jobs = %d", code)
	}
	body := string(raw)
	if got := strings.Count(body, `"approval_labels":[]`); got != 2 {
		t.Errorf(`"approval_labels":[] appears %d times, want 2 (no approval, never good): %s`, got, body)
	}
	if !strings.Contains(body, `"approval_labels":["prod"]`) {
		t.Errorf(`want "approval_labels":["prod"] for the approving job: %s`, body)
	}

	var list struct {
		Jobs []struct {
			Path           string   `json:"path"`
			Services       int      `json:"services"`
			ApprovalLabels []string `json:"approval_labels"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{jobPath: 2, "jobs/lite.yaml": 2, "jobs/broken.yaml": 0}
	if len(list.Jobs) != len(want) {
		t.Fatalf("jobs = %+v, want %d", list.Jobs, len(want))
	}
	for _, job := range list.Jobs {
		if job.Services != want[job.Path] {
			t.Errorf("%s services = %d, want %d", job.Path, job.Services, want[job.Path])
		}
	}
}

func TestInventorySourceRoundTrips(t *testing.T) {
	h := newHarness(t)

	empty := h.mustDo("GET", "/api/v1/admin/inventory-source", nil, http.StatusOK)
	for _, field := range []string{"repo", "branch", "path"} {
		if value, present := empty[field]; !present || value != "" {
			t.Errorf("%s with nothing stored = %#v, want the empty string", field, value)
		}
	}

	put := map[string]string{"repo": invRepo, "branch": "main", "path": invPath}
	h.mustDo("PUT", "/api/v1/admin/inventory-source", put, http.StatusOK)
	got := h.mustDo("GET", "/api/v1/admin/inventory-source", nil, http.StatusOK)
	for field, want := range put {
		if got[field] != want {
			t.Errorf("%s = %v, want %s", field, got[field], want)
		}
	}

	plain, _ := h.newToken(h.newUser("viewer").ID)
	if code, _ := h.doRaw("GET", "/api/v1/admin/inventory-source", plain, nil); code != http.StatusForbidden {
		t.Errorf("GET without manage_connections = %d, want 403", code)
	}
}
