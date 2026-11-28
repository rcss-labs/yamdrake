//go:build integration

package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/rcss-labs/yamdrake/internal/manifest"
)

// sampleManifest is what a resolve leg would report for the run.
func sampleManifest(runID int64, checks ...manifest.Check) manifest.Manifest {
	m := manifest.Manifest{
		RunID: runID, Environment: testEnvironment, Label: testLabel, AWSAccount: testAWSAccount,
		InventoryCommit: "inv-commit", JobCommit: "job-commit",
		Services: []manifest.Service{{
			Name: "s1", Version: "1.0", ImageDigest: "sha256:aaaa", ECSService: "arn:aws:ecs:eu-west-1:1:service/c/s1",
			Config: []manifest.ConfigItem{{Kind: "toml", Name: "app", Destination: "s3://cfg/s1.toml", SHA256: "bbbb", Change: "new"}},
		}},
		Checks: checks,
	}
	m.ID = manifest.Identity(m)
	return m
}

// settleResolve stands in for the runner: it settles the resolve leg with the manifest as its result.
func (h *harness) settleResolve(runID int64, state string, m manifest.Manifest) {
	h.t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		h.t.Fatalf("marshal manifest: %v", err)
	}
	leg := h.legByKind(runID, "resolve")
	if _, err := h.pool.Exec(context.Background(),
		`UPDATE legs SET state = $2, result = $3, finished_at = now() WHERE id = $1`, leg.ID, state, raw); err != nil {
		h.t.Fatalf("settle resolve leg: %v", err)
	}
}

func TestManifestStoredWhenResolveSucceeds(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	run := h.create(h.starter, false, "s1")
	h.advance(run.ID)
	m := sampleManifest(run.ID, manifest.Check{Name: "image exists", Service: "s1", Status: "passed"})
	h.settleResolve(run.ID, "succeeded", m)
	h.advance(run.ID)

	planned := h.expectState(run.ID, "planned")
	if planned.ManifestID == "" || len(planned.Manifest) == 0 {
		t.Fatalf("planned run has manifest id %q and %d bytes; both must be set", planned.ManifestID, len(planned.Manifest))
	}
	// The id is the manifest's own identity (a hash over the pinned inputs), not a hash of the stored bytes.
	if planned.ManifestID != m.ID || planned.ManifestID != manifest.Identity(m) {
		t.Fatalf("manifest id = %q, want the manifest identity %q", planned.ManifestID, m.ID)
	}
	sum := sha256.Sum256(h.legByKind(run.ID, "resolve").Result)
	if planned.ManifestID == hex.EncodeToString(sum[:]) {
		t.Fatal("manifest id is the hash of the result bytes; it must be the manifest identity")
	}
	var stored manifest.Manifest
	if err := json.Unmarshal(planned.Manifest, &stored); err != nil || stored.ID != m.ID || len(stored.Services) != 1 {
		t.Fatalf("stored manifest %s (%v), want the resolve result", planned.Manifest, err)
	}
}

func TestManifestStoredWhenPreflightFailsAndRunIsFailed(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	run := h.create(h.starter, false, "s1")
	h.advance(run.ID)
	m := sampleManifest(run.ID, manifest.Check{Name: "image exists", Service: "s1", Status: "failed", Detail: "tag not found"})
	h.settleResolve(run.ID, "failed", m)
	h.advance(run.ID)

	failed := h.expectState(run.ID, "failed")
	if failed.ManifestID != m.ID || len(failed.Manifest) == 0 {
		t.Fatalf("failed run has manifest id %q (%d bytes), want %q so the plan with its failed checks can be shown", failed.ManifestID, len(failed.Manifest), m.ID)
	}
}

func TestResolveFailedWithoutAResultStoresNoManifest(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	run := h.create(h.starter, false, "s1")
	h.advance(run.ID)
	leg := h.legByKind(run.ID, "resolve")
	if _, err := h.pool.Exec(context.Background(), `UPDATE legs SET state = 'failed', finished_at = now() WHERE id = $1`, leg.ID); err != nil {
		t.Fatalf("settle: %v", err)
	}
	h.advance(run.ID)
	failed := h.expectState(run.ID, "failed")
	if failed.ManifestID != "" || len(failed.Manifest) != 0 {
		t.Fatalf("manifest %q / %d bytes, want none when the leg produced no result", failed.ManifestID, len(failed.Manifest))
	}
}
