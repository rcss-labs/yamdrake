//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/rcss-labs/yumdrake/internal/versions"
)

// repoImages answers DescribeImages with fixed tags per repository name.
type repoImages map[string][]string

func (r repoImages) DescribeImages(_ context.Context, in *ecr.DescribeImagesInput, _ ...func(*ecr.Options)) (*ecr.DescribeImagesOutput, error) {
	var out ecr.DescribeImagesOutput
	for i, tag := range r[aws.ToString(in.RepositoryName)] {
		out.ImageDetails = append(out.ImageDetails, ecrtypes.ImageDetail{
			ImageDigest:   aws.String("sha256:" + tag),
			ImageTags:     []string{tag},
			ImagePushedAt: aws.Time(time.Date(2026, 1, 1, i, 0, 0, 0, time.UTC)),
		})
	}
	return &out, nil
}

type anyAccount struct{}

func (anyAccount) For(_ context.Context, _, region, _ string) (aws.Config, error) {
	return aws.Config{Region: region}, nil
}

func TestVersionsEndpoint(t *testing.T) {
	h := newHarness(t)
	h.rebuild(func(d *Deps) {
		d.Versions = versions.New(versions.Config{
			Discovery: anyAccount{},
			NewClient: func(aws.Config) versions.ECR {
				return repoImages{"backend-api": {"1.4.1", "1.4.2", "latest"}, "orders": {"1.4.2", "2.0.0"}}
			},
		})
	})
	h.pushInventory("c1")
	h.pushJob("j1")
	h.setInventorySource()
	folder := h.createFolder("Backends")
	created := h.registerJob(folder)
	path := "/api/v1/jobs/" + itoa(int64(created["id"].(float64))) + "/versions"

	type groupBody struct {
		Common      []string                    `json:"common"`
		Services    map[string][]map[string]any `json:"services"`
		Unavailable map[string]string           `json:"unavailable"`
	}
	fetch := func(query string) (int, struct {
		Groups map[string]groupBody `json:"groups"`
	}, []byte) {
		t.Helper()
		code, raw := h.doRaw("GET", path+query, h.adminToken, nil)
		var body struct {
			Groups map[string]groupBody `json:"groups"`
		}
		_ = json.Unmarshal(raw, &body)
		return code, body, raw
	}

	// prod-01 has both services.
	code, body, raw := fetch("?environment=prod-01")
	if code != http.StatusOK {
		t.Fatalf("GET versions = %d: %s", code, raw)
	}
	backends := body.Groups["backends"]
	if len(backends.Services["backend-api"]) != 2 || backends.Services["backend-api"][0]["tag"] != "1.4.2" {
		t.Errorf("backend-api = %v, want [1.4.2 1.4.1] (latest filtered, newest first)", backends.Services["backend-api"])
	}
	for _, field := range []string{"tag", "digest", "pushed_at"} {
		if _, ok := backends.Services["backend-api"][0][field]; !ok {
			t.Errorf("version has no %q field: %v", field, backends.Services["backend-api"][0])
		}
	}
	if len(backends.Unavailable) != 0 {
		t.Errorf("backends unavailable = %v, want none", backends.Unavailable)
	}

	// hetzner-01 has backend-api only: orders is reported, the group is not null anywhere.
	code, body, raw = fetch("?environment=hetzner-01")
	if code != http.StatusOK {
		t.Fatalf("GET versions hetzner-01 = %d: %s", code, raw)
	}
	micro := body.Groups["microservices"]
	if micro.Unavailable["orders"] != "not in this environment" {
		t.Errorf("orders unavailable = %v, want not in this environment", micro.Unavailable)
	}
	if strings.Contains(string(raw), "null") {
		t.Errorf("response has a null: %s", raw)
	}
	if micro.Common == nil || micro.Services == nil {
		t.Errorf("empty group has null list or map: %s", raw)
	}

	// Errors.
	h.expect(h.send(sent{method: "GET", path: "/api/v1/jobs/999999/versions?environment=prod-01", bearer: h.adminToken}), http.StatusNotFound, "not_found")
	h.expect(h.send(sent{method: "GET", path: path + "?environment=nope", bearer: h.adminToken}), http.StatusBadRequest, "invalid")
	h.expect(h.send(sent{method: "GET", path: path, bearer: h.adminToken}), http.StatusBadRequest, "invalid")
	if code, _ := h.doRaw("GET", path+"?environment=prod-01", "", nil); code != http.StatusUnauthorized {
		t.Errorf("without a token = %d, want 401", code)
	}
}
