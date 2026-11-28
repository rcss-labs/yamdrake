//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/rcss-labs/yamdrake/internal/manifest"
	"github.com/rcss-labs/yamdrake/internal/store"
)

// planFixtureValue stands for a value from an env file. The runner never puts
// one in a manifest; the test plants it in fields the plan must not serve.
const planFixtureValue = "DB_PASSWORD=hunter2-fixture-value"

func (f *runFixture) storePlan(runID int64, m manifest.Manifest) {
	f.t.Helper()
	m.ID = manifest.Identity(m)
	if err := store.SetManifest(context.Background(), f.pool, runID, mustMarshal(f.t, m), m.ID); err != nil {
		f.t.Fatalf("SetManifest: %v", err)
	}
}

func TestPlanEndpointServesRedactedManifest(t *testing.T) {
	f := newRunFixture(t)
	id := f.newRun(f.jobID, devEnvironment, "s1", "s2")
	f.storePlan(id, manifest.Manifest{
		RunID: id, Environment: devEnvironment, Label: "dev", AWSAccount: "111122223333",
		InventoryCommit: "inv-commit", JobCommit: "job-commit",
		Services: []manifest.Service{
			{
				Name: "s1", Group: "g", Version: "1.0", ImageDigest: "sha256:aaaa",
				ECSService: "arn:aws:ecs:eu-west-1:1:service/c/s1", ECRRepository: "1.dkr.ecr/s1",
				PreviousTaskDefinition: "arn:aws:ecs:eu-west-1:1:task-definition/s1:7", PreviousVersion: "0.9",
				Config: []manifest.ConfigItem{
					{
						Kind: "env", Name: "app", Source: "acme/cfg/app.env", Destination: "s3://cfg/s1.env",
						Branch: "dev", Commit: "c0ffee", Fallback: true, SHA256: "cafe0001",
						ArtifactKey: "manifests/1/s1/env/secret-artifact-key/" + planFixtureValue, PreviousArtifactKey: "manifests/1/s1/env/previous/secret-previous-key",
						PreviousExists: true, PreviousSHA256: "cafe0002", Change: "changed",
						Keys: &manifest.KeyDiff{Added: []string{"NEW_KEY"}, Removed: []string{"OLD_KEY"}, Changed: []string{"CHANGED_KEY"}},
					},
					{Kind: "toml", Name: "settings", Source: "s3://cfg/src.toml", Destination: "s3://cfg/s1.toml", SHA256: "cafe0003", Change: "unchanged", SourceVersionID: "v-id"},
				},
			},
			{Name: "s2", Version: "2.0", ImageDigest: "sha256:bbbb", ECSService: "arn:aws:ecs:eu-west-1:1:service/c/s2"},
		},
		Checks: []manifest.Check{{Name: "image exists", Service: "s1", Status: "passed", Detail: "sha256:aaaa"}},
	})

	code, raw := f.doRaw("GET", runPath(id, "/plan"), f.adminToken, nil)
	if code != http.StatusOK {
		t.Fatalf("GET plan = %d %s", code, raw)
	}
	body := string(raw)
	for _, leaked := range []string{"secret-artifact-key", "secret-previous-key", "artifact_key", "hunter2", planFixtureValue} {
		if strings.Contains(body, leaked) {
			t.Errorf("the plan body contains %q: %s", leaked, body)
		}
	}
	if found := nullSlices(reflect.TypeFor[planView](), raw, ""); len(found) > 0 {
		t.Errorf("plan has null where a list belongs: %v", found)
	}

	var got planView
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID == "" || got.RunID != id || got.Environment != devEnvironment || got.Label != "dev" {
		t.Errorf("plan header = %+v", got)
	}
	if len(got.Services) != 2 || got.Services[0].Name != "s1" || got.Services[0].Group != "g" || got.Services[0].Version != "1.0" ||
		got.Services[0].ImageDigest != "sha256:aaaa" || got.Services[0].PreviousVersion != "0.9" ||
		got.Services[0].PreviousTaskDefinition != "arn:aws:ecs:eu-west-1:1:task-definition/s1:7" {
		t.Fatalf("services = %+v", got.Services)
	}
	env := got.Services[0].Config[0]
	if env.Kind != "env" || env.Name != "app" || env.Branch != "dev" || !env.Fallback || env.Change != "changed" ||
		!reflect.DeepEqual(env.Keys.Added, []string{"NEW_KEY"}) || !reflect.DeepEqual(env.Keys.Removed, []string{"OLD_KEY"}) || !reflect.DeepEqual(env.Keys.Changed, []string{"CHANGED_KEY"}) {
		t.Errorf("env item = %+v", env)
	}
	if toml := got.Services[0].Config[1]; toml.Kind != "toml" || toml.Change != "unchanged" || toml.Keys != nil ||
		toml.SHA256 != "cafe0003" || toml.SourceVersionID != "v-id" || toml.Destination != "s3://cfg/s1.toml" {
		t.Errorf("toml item = %+v, want no keys and its checksum", toml)
	}
	if env.SHA256 != "cafe0001" || env.PreviousSHA256 != "cafe0002" || !env.PreviousExists || env.Commit != "c0ffee" || env.Destination != "s3://cfg/s1.env" {
		t.Errorf("env item = %+v, want checksums, commit and destination", env)
	}
	if got.AWSAccount != "111122223333" || got.InventoryCommit != "inv-commit" || got.JobCommit != "job-commit" ||
		got.Services[0].ECSService == "" || got.Services[0].ECRRepository != "1.dkr.ecr/s1" {
		t.Errorf("plan = %+v, want account, commits, ECS service and repository", got)
	}
	if len(got.Checks) != 1 || got.Checks[0].Status != "passed" || got.Checks[0].Service != "s1" {
		t.Errorf("checks = %+v", got.Checks)
	}
}

func TestPlanEndpoint404BeforeResolved(t *testing.T) {
	f := newRunFixture(t)
	id := f.newRun(f.jobID, devEnvironment, "s1")
	if code, _ := f.get(f.adminToken, runPath(id, "/plan")); code != http.StatusNotFound {
		t.Errorf("plan of an unresolved run = %d, want 404", code)
	}
	if code, _ := f.get(f.adminToken, runPath(999999, "/plan")); code != http.StatusNotFound {
		t.Errorf("plan of an unknown run = %d, want 404", code)
	}
	if code, _ := f.get("", runPath(id, "/plan")); code != http.StatusUnauthorized {
		t.Errorf("plan without credentials = %d, want 401", code)
	}
}
