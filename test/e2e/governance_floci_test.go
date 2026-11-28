//go:build e2e && floci

package e2e

// End-to-end proof of approvals, freezes and Slack notifications with the real
// copy-config and ecs-deploy steps against the Floci AWS emulator. It is the
// counterpart of governance_test.go, which runs the engine's fake steps: here
// "deployed" means the ECS service points at a new task definition running the
// planned image and the destination bucket holds the new config, and "did
// nothing" means Floci shows no new task definition revision and no new object.
// The people and the fake Slack are the same as there.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/yamdrake/test/e2e/fakeslack"
)

// flociGovernance is the Floci stack (dev-1 and prod-1 seeded, real steps)
// with a job that needs approval in prod and posts to #deploys.
type flociGovernance struct {
	*flociStack
	slack     *fakeslack.Server
	admin     caller
	requester caller
	approver  caller
}

func startFlociGovernance(t *testing.T) *flociGovernance {
	t.Helper()
	slack := fakeslack.New(slackTestToken)
	t.Cleanup(slack.Close)
	s := startFloci(t, flociOpts{
		seed:       []string{devEnv, prodEnv},
		real:       true,
		governance: true,
		orchestratorEnv: map[string]string{
			"YUMDRAKE_SLACK_API_URL":   slack.URL(),
			"YUMDRAKE_NOTIFY_INTERVAL": "200ms",
		},
	})
	for _, e := range s.envs {
		s.staleDestinations(e)
	}
	g := &flociGovernance{flociStack: s, slack: slack}
	g.admin = caller{t: t, base: s.api.base, session: s.api.session}
	g.admin.expect(http.StatusOK, "", http.MethodPut, "/api/v1/admin/slack", map[string]string{"token": slackTestToken})
	g.requester = addPerson(t, g.admin, "requester", "Deployers", [2]string{"run", "dev"}, [2]string{"run", "prod"}, [2]string{"approve", "prod"})
	g.approver = addPerson(t, g.admin, "approver", "Approvers", [2]string{"approve", "prod"})
	return g
}

// startRun has who create and start a run of the three services in env.
func (g *flociGovernance) startRun(who caller, env string) int64 {
	g.t.Helper()
	services := map[string]string{}
	for _, name := range planServices {
		services[name] = ""
	}
	var created struct {
		ID int64 `json:"id"`
	}
	got := who.send(http.MethodPost, fmt.Sprintf("/api/v1/jobs/%d/runs", g.jobID), map[string]any{
		"environment": env,
		"selection":   map[string]any{"all": map[string]any{"version": planVersion, "services": services}},
		"start":       true,
	})
	if got.status >= 300 {
		g.t.Fatalf("create run in %s: status %d code %q", env, got.status, got.code)
	}
	got.decode(g.t, "created run", &created)
	return created.ID
}

func (g *flociGovernance) view(who caller, id int64) governanceRun {
	g.t.Helper()
	var run governanceRun
	who.expect(http.StatusOK, "", http.MethodGet, fmt.Sprintf("/api/v1/runs/%d", id), nil).decode(g.t, "run", &run)
	return run
}

// waitState waits for the run to be in state; the approval and freeze waits
// happen before any step, so they are short.
func (g *flociGovernance) waitState(id int64, state string) governanceRun {
	g.t.Helper()
	var last governanceRun
	eventually(g.t, runWait, fmt.Sprintf("run %d to be %s", id, state), func() bool {
		last = g.view(g.admin, id)
		return last.State == state
	})
	return last
}

func (g *flociGovernance) messagesOf(id int64) []string {
	return slackMessagesOf(g.t, g.slack, id)
}

func (g *flociGovernance) expectMessages(id int64, prefixes ...string) []string {
	g.t.Helper()
	return expectSlackMessages(g.t, g.slack, id, prefixes...)
}

// ---- what Floci holds ----

// awsState is what one environment's account holds that a deployment changes.
type awsState struct {
	taskDefs  map[string]string   // service -> task definition it points at
	revisions map[string][]string // service -> active revisions
	objects   map[string]string   // destination key -> sha256 of its content
}

func (g *flociGovernance) state(e *seededEnv) awsState {
	g.t.Helper()
	st := awsState{taskDefs: map[string]string{}, revisions: map[string][]string{}, objects: map[string]string{}}
	for _, name := range planServices {
		st.taskDefs[name] = g.serviceTaskDefinition(e, name)
		st.revisions[name] = g.revisions(e, name)
	}
	for _, key := range []string{"web.env", "web.toml", "api.env"} {
		if body, ok := e.acct.Object(e.dstBucket, key); ok {
			st.objects[key] = sha(body)
		}
	}
	return st
}

// requireUnchanged fails unless Floci holds exactly what it held before: the
// same task definition and revisions per service, the same destination files.
func (g *flociGovernance) requireUnchanged(e *seededEnv, before awsState) {
	g.t.Helper()
	after := g.state(e)
	for _, name := range planServices {
		if after.taskDefs[name] != before.taskDefs[name] {
			g.t.Errorf("%s now points at %s, want it untouched at %s", name, after.taskDefs[name], before.taskDefs[name])
		}
		if len(after.revisions[name]) != len(before.revisions[name]) {
			g.t.Errorf("%s has revisions %v, want %v", name, after.revisions[name], before.revisions[name])
		}
	}
	for key, digest := range before.objects {
		if after.objects[key] != digest {
			g.t.Errorf("destination %s changed", key)
		}
	}
}

// requireDeployed fails unless every service points at a new task definition
// running the planned image and the destinations hold the new config.
func (g *flociGovernance) requireDeployed(e *seededEnv, before awsState) {
	g.t.Helper()
	for _, name := range planServices {
		now := g.serviceTaskDefinition(e, name)
		if now == before.taskDefs[name] {
			g.t.Errorf("%s still points at its old task definition %s", name, now)
		}
		if revs := g.revisions(e, name); len(revs) != len(before.revisions[name])+1 {
			g.t.Errorf("%s has revisions %v, want exactly one more than before (%d)", name, revs, len(before.revisions[name]))
		}
		g.requireImageOf(e, name, now, e.services[name].digest)
	}
	g.requireDestination(e, "web.env", webEnvNew)
	g.requireDestination(e, "web.toml", webToml)
	g.requireDestination(e, "api.env", apiEnv)
}

// ---- the tests ----

func TestProdRunWaitsIsApprovedAndDeploysOnFloci(t *testing.T) {
	g := startFlociGovernance(t)
	prod := g.envs[prodEnv]
	before := g.state(prod)
	id := g.startRun(g.requester, prodEnv)

	g.waitState(id, "waiting_approval")
	g.requireUnchanged(prod, before)
	view := g.view(g.requester, id)
	if view.Approval == nil || view.Approval.Decision != "" {
		t.Fatalf("run view approval = %+v, want an open approval", view.Approval)
	}
	if view.ManifestID == "" {
		t.Fatal("run view has no manifest_id")
	}

	path := fmt.Sprintf("/api/v1/runs/%d/approve", id)
	g.requester.expect(http.StatusForbidden, "forbidden", http.MethodPost, path, map[string]string{"manifest_id": view.ManifestID})
	g.requireUnchanged(prod, before)

	g.approver.expect(http.StatusOK, "", http.MethodPost, path, map[string]string{"manifest_id": view.ManifestID, "comment": "looks right"})
	g.expectDeploy(id, "succeeded")
	g.requireDeployed(prod, before)

	got := g.expectMessages(id, "requester wants to run", "Approved by approver", "Started:", "Succeeded:")
	if !strings.Contains(got[3], "3 of 3 services updated.") {
		t.Errorf("finish message = %q, want it to say 3 of 3 services updated.", got[3])
	}
}

// If approving did not compare the manifest, an approval of a plan that is not
// the run's would still deploy. The approver reviewed the plan of a first run;
// somebody then deployed web by hand, so the second run, planned afterwards,
// has another manifest. Approving it with the first run's id must be refused
// and change nothing in AWS.
func TestApproveStalePlanRefusedOnFloci(t *testing.T) {
	g := startFlociGovernance(t)
	prod := g.envs[prodEnv]
	first := g.startRun(g.requester, prodEnv)
	g.waitState(first, "waiting_approval")
	reviewed := g.view(g.approver, first).ManifestID

	g.deployByHand(prod, "web")
	before := g.state(prod)
	second := g.startRun(g.requester, prodEnv)
	g.waitState(second, "waiting_approval")
	current := g.view(g.approver, second).ManifestID
	if current == reviewed {
		t.Fatalf("the manifest id did not change after the hand deployment (%s); the test proves nothing", current)
	}

	path := fmt.Sprintf("/api/v1/runs/%d/approve", second)
	g.approver.expect(http.StatusConflict, "conflict", http.MethodPost, path, map[string]string{"manifest_id": reviewed})
	got := g.approver.send(http.MethodPost, path, map[string]string{"manifest_id": reviewed})
	if !strings.Contains(string(got.body), planChangedText) {
		t.Errorf("stale approval error does not say %q", planChangedText)
	}

	holdsFor(t, 2*time.Second, "the run staying in waiting_approval with AWS untouched", func() bool {
		return g.view(g.admin, second).State == "waiting_approval"
	})
	g.requireUnchanged(prod, before)
	if view := g.view(g.admin, second); view.Approval == nil || view.Approval.Decision != "" {
		t.Errorf("approval after a refused approve = %+v, want still open", view.Approval)
	}
}

func TestRejectedRunDoesNothingOnFloci(t *testing.T) {
	g := startFlociGovernance(t)
	prod := g.envs[prodEnv]
	before := g.state(prod)
	id := g.startRun(g.requester, prodEnv)
	g.waitState(id, "waiting_approval")

	g.approver.expect(http.StatusOK, "", http.MethodPost, fmt.Sprintf("/api/v1/runs/%d/reject", id), map[string]string{"comment": "not now"})
	g.waitState(id, "rejected")
	g.expectMessages(id, "requester wants to run", "Rejected by approver")
	g.requireUnchanged(prod, before)
	for _, leg := range g.deployRun(id).Legs {
		if leg.Kind == "step" && leg.State != "pending" && leg.State != "cancelled" && leg.State != "skipped" {
			t.Errorf("step leg %s/%s is %q, want it never to have run", leg.Step, leg.Service, leg.State)
		}
	}
}

func TestDevRunNeedsNoApprovalOnFloci(t *testing.T) {
	g := startFlociGovernance(t)
	dev := g.envs[devEnv]
	before := g.state(dev)
	id := g.startRun(g.requester, devEnv)

	g.expectDeploy(id, "succeeded")
	g.requireDeployed(dev, before)
	if view := g.view(g.requester, id); view.Approval != nil {
		t.Errorf("dev run has an approval: %+v", view.Approval)
	}
	var out struct {
		Approvals []approvalItem `json:"approvals"`
	}
	g.approver.expect(http.StatusOK, "", http.MethodGet, "/api/v1/approvals", nil).decode(t, "approvals", &out)
	if len(out.Approvals) != 0 {
		t.Errorf("approvals list = %+v, want empty", out.Approvals)
	}
	g.expectMessages(id, "Started:", "Succeeded:")
}

// The freeze must hold the approved run back before any step starts. Without
// the freeze check the run would deploy within seconds of the approval, and
// Floci would show a new revision during the hold.
func TestFreezeBlocksStartThenReleasesOnFloci(t *testing.T) {
	g := startFlociGovernance(t)
	prod := g.envs[prodEnv]
	before := g.state(prod)
	var freeze struct {
		ID int64 `json:"id"`
	}
	g.admin.expect(http.StatusCreated, "", http.MethodPost, "/api/v1/admin/freezes", map[string]any{
		"environment": prodEnv,
		"reason":      "release week",
		"expires_at":  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}).decode(t, "freeze", &freeze)

	id := g.startRun(g.requester, prodEnv)
	g.waitState(id, "waiting_approval")
	manifest := g.view(g.approver, id).ManifestID
	g.approver.expect(http.StatusOK, "", http.MethodPost, fmt.Sprintf("/api/v1/runs/%d/approve", id), map[string]string{"manifest_id": manifest})

	eventually(t, 30*time.Second, "the approved run to wait on the freeze", func() bool {
		run := g.view(g.admin, id)
		return run.State == "queued" && strings.HasPrefix(run.WaitReason, "frozen: release week, until")
	})
	holdsFor(t, 3*time.Second, "the frozen run staying queued with AWS untouched", func() bool {
		return g.view(g.admin, id).State == "queued"
	})
	g.requireUnchanged(prod, before)
	for _, text := range g.messagesOf(id) {
		if strings.HasPrefix(text, "Started:") {
			t.Fatalf("a Started message was posted while the run was frozen: %q", text)
		}
	}

	g.admin.expect(http.StatusNoContent, "", http.MethodDelete, fmt.Sprintf("/api/v1/admin/freezes/%d", freeze.ID), nil)
	g.expectDeploy(id, "succeeded")
	g.requireDeployed(prod, before)
	g.expectMessages(id, "requester wants to run", "Approved by approver", "Started:", "Succeeded:")
}

// Slack being down must not touch runs, and the messages must still arrive
// once it is back. The sender backs off from one minute, so the test pulls
// next_attempt_at of the pending rows forward itself. The runs are two dev
// runs one after the other: a second run planned before the first deployed
// would be invalidated by it.
func TestSlackOutageDoesNotAffectRunsOnFloci(t *testing.T) {
	g := startFlociGovernance(t)
	dev := g.envs[devEnv]
	before := g.state(dev)
	g.slack.SetFailing(http.StatusInternalServerError)

	first := g.startRun(g.requester, devEnv)
	g.expectDeploy(first, "succeeded")
	g.requireDeployed(dev, before)
	second := g.startRun(g.requester, devEnv)
	g.expectDeploy(second, "succeeded")

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
