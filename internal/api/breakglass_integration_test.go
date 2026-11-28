//go:build integration

package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/yamdrake/internal/spec"
	"github.com/rcss-labs/yamdrake/internal/store"
)

// addJobWithChannel registers a job with the labels and Slack channel given.
func (h *harness) addJobWithChannel(folderID int64, id, channel string, labels ...string) {
	h.t.Helper()
	job := spec.Job{ID: id, Title: id, Labels: labels, SlackChannel: channel}
	source, err := store.CreateJobSource(context.Background(), h.pool, store.JobSource{FolderID: folderID, Repo: "acme/jobs", Branch: "main", Path: id + ".yaml"})
	if err != nil {
		h.t.Fatalf("CreateJobSource: %v", err)
	}
	if err := store.SaveJobGood(context.Background(), h.pool, source.ID, mustMarshal(h.t, job), "", job.ID, job.Title, "jc1", time.Now()); err != nil {
		h.t.Fatalf("SaveJobGood: %v", err)
	}
}

func TestBreakGlassSignInIsLoggedAndAnnouncedToProductionChannels(t *testing.T) {
	h := newHarness(t)
	folder := h.createFolder("services")
	h.addJobWithChannel(folder, "release-prod", "#deploys-prod", "preprod", "prod")
	h.addJobWithChannel(folder, "release-prod-2", "#deploys-prod", "prod") // the same channel is told once
	h.addJobWithChannel(folder, "audit-prod", "#audit-prod", "prod")
	h.addJobWithChannel(folder, "release-dev", "#deploys-dev", "dev")
	user := h.newUser("breakglass")

	h.expect(h.login(user.Email, testPassword), http.StatusOK, "")

	if out := h.logs.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "break-glass sign-in") || !strings.Contains(out, user.Email) {
		t.Errorf("service log lacks a warn line naming the break-glass account:\n%s", out)
	}
	rows, err := h.pool.Query(context.Background(), `SELECT channel, kind, run_id, convert_from(payload, 'UTF8') FROM notifications ORDER BY channel`)
	if err != nil {
		t.Fatalf("read notifications: %v", err)
	}
	defer rows.Close()
	var channels []string
	for rows.Next() {
		var channel, kind, payload string
		var runID *int64
		if err := rows.Scan(&channel, &kind, &runID, &payload); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if kind != "break_glass_sign_in" || runID != nil || !strings.Contains(payload, user.Email) {
			t.Errorf("notification to %s = kind %q, run %v, payload %s; want a run-less break-glass message naming %s", channel, kind, runID, payload, user.Email)
		}
		channels = append(channels, channel)
	}
	if got := strings.Join(channels, ","); got != "#audit-prod,#deploys-prod" {
		t.Errorf("channels told = %q, want #audit-prod,#deploys-prod", got)
	}
}
