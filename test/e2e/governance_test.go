//go:build e2e

package e2e

// End-to-end proof of approvals, freezes and Slack notifications. Each test
// starts the engine stack with the governance fixture (a prod environment that
// needs approval, a job that posts to #deploys), a fake Slack that records what
// the orchestrator's sender posts, and three people: an admin, a requester who
// may run dev and prod, and an approver who may approve prod. The runners
// execute the engine's fake steps against a fake.World that counts every
// pretend change, so "nothing ran" is a count of zero.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/yumdrake/test/e2e/fakeslack"
)

const (
	slackTestToken  = "xoxb-test" // fake value, accepted only by the fake Slack
	governancePass  = "governance passphrase 123"
	planChangedText = "The plan changed. Reload and review it again."
	// settleAfterLast is how long a test waits after the last expected message
	// before it checks that nothing else arrived: five notify intervals.
	settleAfterLast = time.Second
	messageWait     = 30 * time.Second
)

type governanceStack struct {
	*engineStack
	slack     *fakeslack.Server
	admin     caller
	requester caller
	approver  caller
}

type governanceRun struct {
	ID         int64  `json:"id"`
	State      string `json:"state"`
	WaitReason string `json:"wait_reason"`
	ManifestID string `json:"manifest_id"`
	Approval   *struct {
		Decision string `json:"decision"`
	} `json:"approval"`
}

type approvalItem struct {
	RunID      int64  `json:"run_id"`
	CanApprove bool   `json:"can_approve"`
	Reason     string `json:"reason"`
}

func startGovernance(t *testing.T) *governanceStack {
	t.Helper()
	slack := fakeslack.New(slackTestToken)
	t.Cleanup(slack.Close)
	s := startEngine(t, engineOpts{
		governance: true,
		orchestratorEnv: map[string]string{
			"YUMDRAKE_SLACK_API_URL":   slack.URL(),
			"YUMDRAKE_NOTIFY_INTERVAL": "200ms",
		},
	})
	g := &governanceStack{engineStack: s, slack: slack}
	g.admin = caller{t: t, base: s.api.base, session: s.api.session}
	g.admin.expect(http.StatusOK, "", http.MethodPut, "/api/v1/admin/slack", map[string]string{"token": slackTestToken})

	// Everyone signed in may view; grants cover the rest.
	g.requester = g.addPerson("requester", "Deployers", [2]string{"run", "dev"}, [2]string{"run", "prod"})
	g.approver = g.addPerson("approver", "Approvers", [2]string{"approve", "prod"})
	return g
}

// addPerson creates a local user in a new group holding the (action, label)
// grants and signs them in. The user's name is name, which is how Slack
// messages and approvals show them.
func (g *governanceStack) addPerson(name, group string, grants ...[2]string) caller {
	g.t.Helper()
	return addPerson(g.t, g.admin, name, group, grants...)
}

// addPerson is the same with any admin caller; the Floci governance tests use it too.
func addPerson(t *testing.T, admin caller, name, group string, grants ...[2]string) caller {
	t.Helper()
	var groupOut, userOut struct {
		ID int64 `json:"id"`
	}
	admin.expect(http.StatusCreated, "", http.MethodPost, "/api/v1/admin/groups", map[string]any{"name": group}).decode(t, "group", &groupOut)
	for _, grant := range grants {
		admin.expect(http.StatusCreated, "", http.MethodPost, "/api/v1/admin/grants",
			map[string]any{"group_id": groupOut.ID, "action": grant[0], "label": grant[1]})
	}
	email := name + "@governance.example.test"
	admin.expect(http.StatusCreated, "", http.MethodPost, "/api/v1/admin/users",
		map[string]any{"email": email, "name": name, "source": "local", "password": governancePass}).decode(t, "user", &userOut)
	admin.expect(http.StatusOK, "", http.MethodPatch, fmt.Sprintf("/api/v1/admin/users/%d", userOut.ID),
		map[string]any{"group_ids": []int64{groupOut.ID}})
	return signInAs(t, admin.base, email, governancePass)
}

// startRun has who create and start a run in env of two services (b1, m1).
func (g *governanceStack) startRun(who caller, env string) int64 {
	g.t.Helper()
	var created struct {
		ID int64 `json:"id"`
	}
	got := who.send(http.MethodPost, fmt.Sprintf("/api/v1/jobs/%d/runs", g.jobID),
		map[string]any{"environment": env, "selection": selection("v1.0.0", "b1", "m1"), "start": true})
	if got.status >= 300 {
		g.t.Fatalf("create run in %s: status %d code %q", env, got.status, got.code)
	}
	got.decode(g.t, "created run", &created)
	return created.ID
}

func (g *governanceStack) view(who caller, id int64) governanceRun {
	g.t.Helper()
	var run governanceRun
	who.expect(http.StatusOK, "", http.MethodGet, fmt.Sprintf("/api/v1/runs/%d", id), nil).decode(g.t, "run", &run)
	return run
}

func (g *governanceStack) approvals(who caller) []approvalItem {
	g.t.Helper()
	var out struct {
		Approvals []approvalItem `json:"approvals"`
	}
	who.expect(http.StatusOK, "", http.MethodGet, "/api/v1/approvals", nil).decode(g.t, "approvals", &out)
	return out.Approvals
}

// nothingRan fails when any fake leg changed the pretend world.
func (g *governanceStack) nothingRan() {
	g.t.Helper()
	if keys := g.world.Keys(); len(keys) != 0 {
		g.t.Fatalf("legs ran although the run must not have started: %d changes, first %q", len(keys), keys[0])
	}
}

// messagesOf is the texts posted for one run, in arrival order. The link at
// the end of each message names the run.
func (g *governanceStack) messagesOf(runID int64) []string {
	return slackMessagesOf(g.t, g.slack, runID)
}

func slackMessagesOf(t *testing.T, slack *fakeslack.Server, runID int64) []string {
	var texts []string
	suffix := fmt.Sprintf("/runs/%d", runID)
	for _, m := range slack.Messages() {
		if strings.HasSuffix(m.Text, suffix) {
			if m.Channel != "#deploys" {
				t.Errorf("message posted to %q, want #deploys", m.Channel)
			}
			texts = append(texts, m.Text)
		}
	}
	return texts
}

// expectMessages waits until the run has at least len(prefixes) messages, then
// checks that they are exactly these, in order: each text starts with its
// prefix. It waits a moment first so a message that should not exist has time
// to arrive. It returns the texts.
func (g *governanceStack) expectMessages(runID int64, prefixes ...string) []string {
	g.t.Helper()
	return expectSlackMessages(g.t, g.slack, runID, prefixes...)
}

func expectSlackMessages(t *testing.T, slack *fakeslack.Server, runID int64, prefixes ...string) []string {
	t.Helper()
	eventually(t, messageWait, fmt.Sprintf("%d Slack messages for run %d", len(prefixes), runID), func() bool {
		return len(slackMessagesOf(t, slack, runID)) >= len(prefixes)
	})
	time.Sleep(settleAfterLast)
	got := slackMessagesOf(t, slack, runID)
	if len(got) != len(prefixes) {
		t.Fatalf("run %d has %d Slack messages, want %d: %q", runID, len(got), len(prefixes), got)
	}
	for i, prefix := range prefixes {
		if !strings.HasPrefix(got[i], prefix) {
			t.Fatalf("run %d message %d = %q, want it to start with %q", runID, i+1, got[i], prefix)
		}
	}
	return got
}

func TestProdRunWaitsIsApprovedAndDeploys(t *testing.T) {
	g := startGovernance(t)
	id := g.startRun(g.requester, "env-prod")

	g.waitRun(id, "waiting_approval")
	g.nothingRan()

	view := g.view(g.requester, id)
	if view.Approval == nil || view.Approval.Decision != "" {
		t.Fatalf("run view approval = %+v, want an open approval", view.Approval)
	}
	if view.ManifestID == "" {
		t.Fatal("run view has no manifest_id")
	}

	// The requester sees the request but may not decide it; the approver may.
	listed := func(who caller) approvalItem {
		t.Helper()
		for _, item := range g.approvals(who) {
			if item.RunID == id {
				return item
			}
		}
		t.Fatalf("run %d is not in the approvals list", id)
		return approvalItem{}
	}
	if own := listed(g.requester); own.CanApprove || own.Reason != "You do not have approve permission in prod." {
		t.Errorf("requester's approval item = %+v, want can_approve false with reason %q", own, "You do not have approve permission in prod.")
	}
	if other := listed(g.approver); !other.CanApprove {
		t.Errorf("approver's approval item = %+v, want can_approve true", other)
	}
	path := fmt.Sprintf("/api/v1/runs/%d/approve", id)
	g.requester.expect(http.StatusForbidden, "forbidden", http.MethodPost, path, map[string]string{"manifest_id": view.ManifestID})
	g.nothingRan()

	g.approver.expect(http.StatusOK, "", http.MethodPost, path, map[string]string{"manifest_id": view.ManifestID, "comment": "looks right"})
	done := g.waitRun(id, "succeeded")
	if done.State != "succeeded" {
		t.Fatalf("run state = %s", done.State)
	}

	got := g.expectMessages(id, "requester wants to run", "Approved by approver", "Started:", "Succeeded:")
	if !strings.Contains(got[3], "2 of 2 services updated.") {
		t.Errorf("finish message = %q, want it to say 2 of 2 services updated.", got[3])
	}
}

// If approving did not compare the manifest, a plan changed after the approver
// looked at it would still deploy: this test fails then, because the call
// would answer 200 and the run would go on to run.
func TestApproveStalePlanRefused(t *testing.T) {
	g := startGovernance(t)
	id := g.startRun(g.requester, "env-prod")
	g.waitRun(id, "waiting_approval")
	manifest := g.view(g.approver, id).ManifestID

	g.approver.expect(http.StatusConflict, "conflict", http.MethodPost, fmt.Sprintf("/api/v1/runs/%d/approve", id),
		map[string]string{"manifest_id": "stale-" + manifest})
	// The error text is part of the contract.
	got := g.approver.send(http.MethodPost, fmt.Sprintf("/api/v1/runs/%d/approve", id), map[string]string{"manifest_id": "stale-" + manifest})
	if !strings.Contains(string(got.body), planChangedText) {
		t.Errorf("stale approval error does not say %q", planChangedText)
	}

	holdsFor(t, 2*time.Second, "the run staying in waiting_approval with nothing run", func() bool {
		return g.view(g.admin, id).State == "waiting_approval" && len(g.world.Keys()) == 0
	})
	if view := g.view(g.admin, id); view.Approval == nil || view.Approval.Decision != "" {
		t.Errorf("approval after a refused approve = %+v, want still open", view.Approval)
	}
}

func TestRejectedRunDoesNothing(t *testing.T) {
	g := startGovernance(t)
	id := g.startRun(g.requester, "env-prod")
	g.waitRun(id, "waiting_approval")

	g.approver.expect(http.StatusOK, "", http.MethodPost, fmt.Sprintf("/api/v1/runs/%d/reject", id), map[string]string{"comment": "not now"})
	g.waitRun(id, "rejected")
	g.expectMessages(id, "requester wants to run", "Rejected by approver")
	g.nothingRan()
}

func TestDevRunNeedsNoApproval(t *testing.T) {
	g := startGovernance(t)
	id := g.startRun(g.requester, "env-a")

	g.waitRun(id, "succeeded")
	if view := g.view(g.requester, id); view.Approval != nil {
		t.Errorf("dev run has an approval: %+v", view.Approval)
	}
	if listed := g.approvals(g.approver); len(listed) != 0 {
		t.Errorf("approvals list = %+v, want empty", listed)
	}
	g.expectMessages(id, "Started:", "Succeeded:")
}

// The freeze must hold the approved run back before any leg starts. Without
// the freeze check the run would reach running within a second and the world
// would change during the two-second hold.
func TestFreezeBlocksStartThenReleases(t *testing.T) {
	g := startGovernance(t)
	var freeze struct {
		ID int64 `json:"id"`
	}
	g.admin.expect(http.StatusCreated, "", http.MethodPost, "/api/v1/admin/freezes", map[string]any{
		"environment": "env-prod",
		"reason":      "release week",
		"expires_at":  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}).decode(t, "freeze", &freeze)

	id := g.startRun(g.requester, "env-prod")
	g.waitRun(id, "waiting_approval")
	manifest := g.view(g.approver, id).ManifestID
	g.approver.expect(http.StatusOK, "", http.MethodPost, fmt.Sprintf("/api/v1/runs/%d/approve", id), map[string]string{"manifest_id": manifest})

	eventually(t, 30*time.Second, "the approved run to wait on the freeze", func() bool {
		run := g.view(g.admin, id)
		return run.State == "queued" && strings.HasPrefix(run.WaitReason, "frozen: release week, until")
	})
	holdsFor(t, 2*time.Second, "the frozen run staying queued with no leg run", func() bool {
		return g.view(g.admin, id).State == "queued" && len(g.world.Keys()) == 0
	})
	for _, text := range g.messagesOf(id) {
		if strings.HasPrefix(text, "Started:") {
			t.Fatalf("a Started message was posted while the run was frozen: %q", text)
		}
	}

	g.admin.expect(http.StatusNoContent, "", http.MethodDelete, fmt.Sprintf("/api/v1/admin/freezes/%d", freeze.ID), nil)
	g.waitRun(id, "succeeded")
	g.expectMessages(id, "requester wants to run", "Approved by approver", "Started:", "Succeeded:")
}

// Slack being down must not touch runs, and the messages must still arrive
// once it is back. The sender backs off from one minute, which is too long for
// a test, so the test pulls next_attempt_at of the pending rows forward itself,
// as an operator waiting out the backoff would see happen on its own.
func TestSlackOutageDoesNotAffectRuns(t *testing.T) {
	g := startGovernance(t)
	g.slack.SetFailing(http.StatusInternalServerError)

	first := g.startRun(g.requester, "env-a")
	second := g.startRun(g.requester, "env-b")
	g.waitRun(first, "succeeded")
	g.waitRun(second, "succeeded")

	// The outage was really hit: a row was tried and is waiting for its retry.
	eventually(t, messageWait, "a notification to have failed an attempt and be pending", func() bool {
		var n int
		err := g.pool.QueryRow(t.Context(), `SELECT count(*) FROM notifications WHERE state = 'pending' AND attempts >= 1`).Scan(&n)
		return err == nil && n >= 1
	})
	if sent := g.slack.Messages(); len(sent) != 0 {
		t.Fatalf("fake Slack recorded %d messages during the outage", len(sent))
	}

	g.slack.SetFailing(0)
	eventually(t, messageWait, "every message of both runs to arrive", func() bool {
		_, _ = g.pool.Exec(t.Context(), `UPDATE notifications SET next_attempt_at = now() WHERE state = 'pending'`)
		return len(g.messagesOf(first)) >= 2 && len(g.messagesOf(second)) >= 2
	})
	g.expectMessages(first, "Started:", "Succeeded:")
	g.expectMessages(second, "Started:", "Succeeded:")
}
