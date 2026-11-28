//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/yamdrake/internal/running"
	"github.com/rcss-labs/yamdrake/internal/spec"
)

// fakeRunning answers with a fixed matrix and counts how it was asked.
type fakeRunning struct {
	mu            sync.Mutex
	cached, fresh int
}

func (f *fakeRunning) matrix(inv spec.Inventory) running.Matrix {
	m := running.Matrix{Environments: inv.Environments, Rows: []running.Row{}}
	for _, name := range inv.ServiceNames() {
		cells := make([]running.Cell, len(inv.Environments))
		for i := range cells {
			cells[i] = running.Cell{State: "ok", Version: "1.4.2", TaskDefinition: name + ":3", ByYumdrake: true, RunID: 12,
				ReadAt: time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)}
		}
		m.Rows = append(m.Rows, running.Row{Service: name, Cells: cells})
	}
	return m
}

func (f *fakeRunning) Matrix(_ context.Context, inv spec.Inventory) (running.Matrix, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cached++
	return f.matrix(inv), nil
}

func (f *fakeRunning) MatrixFresh(_ context.Context, inv spec.Inventory) (running.Matrix, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fresh++
	return f.matrix(inv), nil
}

func TestRunningEndpoint(t *testing.T) {
	h := newHarness(t)
	fake := &fakeRunning{}
	h.rebuild(func(d *Deps) { d.Running = fake })

	// No inventory yet: the same answer as the version lists give.
	h.expect(h.send(sent{method: "GET", path: "/api/v1/versions/running", bearer: h.adminToken}), http.StatusConflict, "conflict")

	h.pushInventory("c1")
	h.pushJob("j1")
	h.setInventorySource()

	code, raw := h.doRaw("GET", "/api/v1/versions/running", h.adminToken, nil)
	if code != http.StatusOK {
		t.Fatalf("GET running = %d: %s", code, raw)
	}
	var body struct {
		Environments []map[string]any `json:"environments"`
		Rows         []struct {
			Service string           `json:"service"`
			Cells   []map[string]any `json:"cells"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode: %v: %s", err, raw)
	}
	if len(body.Environments) == 0 || len(body.Rows) != 2 || body.Rows[0].Service != "backend-api" {
		t.Fatalf("body = %s, want the inventory's environments and one row per service sorted by name", raw)
	}
	for _, field := range []string{"state", "version", "task_definition", "by_yumdrake", "run_id", "last_deploy_failed", "behind", "error", "read_at"} {
		if _, ok := body.Rows[0].Cells[0][field]; !ok {
			t.Errorf("cell has no %q field: %v", field, body.Rows[0].Cells[0])
		}
	}
	if len(body.Rows[0].Cells) != len(body.Environments) {
		t.Errorf("cells = %d, environments = %d, want one cell per environment", len(body.Rows[0].Cells), len(body.Environments))
	}
	if fake.cached != 1 || fake.fresh != 0 {
		t.Errorf("reads = %d cached, %d fresh, want 1 and 0", fake.cached, fake.fresh)
	}

	// refresh=1 bypasses the cache, and only for people who manage connections.
	if code, raw := h.doRaw("GET", "/api/v1/versions/running?refresh=1", h.adminToken, nil); code != http.StatusOK {
		t.Fatalf("refresh as admin = %d: %s", code, raw)
	}
	if fake.fresh != 1 {
		t.Errorf("fresh reads = %d, want 1", fake.fresh)
	}
	plain := h.newUser("runningplain")
	plainToken, _ := h.newToken(plain.ID)
	h.expect(h.send(sent{method: "GET", path: "/api/v1/versions/running?refresh=1", bearer: plainToken}), http.StatusForbidden, "forbidden")
	if fake.fresh != 1 {
		t.Errorf("fresh reads = %d after a refused refresh, want still 1", fake.fresh)
	}
	if code, raw := h.doRaw("GET", "/api/v1/versions/running", plainToken, nil); code != http.StatusOK {
		t.Errorf("plain read as a viewer = %d: %s", code, raw)
	}
	if code, _ := h.doRaw("GET", "/api/v1/versions/running", "", nil); code != http.StatusUnauthorized {
		t.Errorf("without a token = %d, want 401", code)
	}
}

func TestRunningEndpointWithTheDefaultReader(t *testing.T) {
	h := newHarness(t)
	h.rebuild(func(d *Deps) { d.Running = nil; d.Discovery = nil })
	h.pushInventory("c1")
	h.pushJob("j1")
	h.setInventorySource()
	// The default reader is built from the default discovery, so the page
	// answers even with no AWS account connected: every cell says why not.
	code, raw := h.doRaw("GET", "/api/v1/versions/running", h.adminToken, nil)
	if code != http.StatusOK {
		t.Fatalf("GET running with the default reader = %d: %s", code, raw)
	}
	var body struct {
		Rows []struct {
			Cells []struct {
				State string `json:"state"`
				Error string `json:"error"`
			} `json:"cells"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, row := range body.Rows {
		for _, c := range row.Cells {
			if c.State == "ok" {
				t.Errorf("a cell is ok with no AWS account connected: %s", raw)
			}
		}
	}
}
