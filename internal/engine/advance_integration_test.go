//go:build integration

package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/yamdrake/internal/auth"
	"github.com/rcss-labs/yamdrake/internal/store"
	"github.com/rcss-labs/yamdrake/internal/wake"
)

// countTransitionLines counts the run-level lines that report a state change.
func countTransitionLines(lines []string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, "moved from") {
			n++
		}
	}
	return n
}

func hasLine(lines []string, parts ...string) bool {
	for _, l := range lines {
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(l, p)
		}
		if ok {
			return true
		}
	}
	return false
}

func TestCreateStoresSnapshotAndStartsResolving(t *testing.T) {
	h := newHarness(t, harnessOptions{approvalFor: []string{"dev"}})
	ctx := context.Background()

	run := h.create(h.starter, false, "s2", "s1")
	if run.State != "resolving" || run.Kind != "deploy" || run.Via != "web" || run.AutoStart || !run.ApprovalRequired {
		t.Fatalf("created run: %+v", run)
	}
	got := h.run(run.ID)
	source, err := store.GetJobSource(ctx, h.pool, h.sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.JobSnapshot) == "" || !strings.Contains(string(got.JobSnapshot), `"release"`) || string(source.LastGood) == "" {
		t.Fatalf("job snapshot not stored: %s", got.JobSnapshot)
	}
	if got.JobID != "release" || got.JobCommit != "jobcommit1" || got.InventoryCommit != "invcommit1" || got.InventorySnapshotID != h.snapshot.ID {
		t.Fatalf("what the run was planned from: %+v", got)
	}
	if got.Environment != testEnvironment || got.Label != testLabel || got.AWSAccount != testAWSAccount || got.FolderID != h.folderID || got.RequestedBy != h.starter.UserID {
		t.Fatalf("target of the run: %+v", got)
	}
	wantLocks := []string{
		"arn:aws:ecs:eu-west-1:111122223333:service/cluster/s1-staging",
		"arn:aws:ecs:eu-west-1:111122223333:service/cluster/s2-staging",
		"s3://cfg/staging/s1.toml",
		"s3://cfg/staging/s2.toml",
	}
	if !reflect.DeepEqual(got.LockResources, wantLocks) {
		t.Fatalf("lock resources = %v, want %v", got.LockResources, wantLocks)
	}
	if len(got.Selection) == 0 {
		t.Fatalf("selection not stored")
	}

	legs := h.legs(run.ID)
	if len(legs) != 5 || legs[0].Kind != "resolve" || legs[0].State != "queued" || legs[0].QueuedAt == nil {
		t.Fatalf("legs: %+v", legs)
	}
	steps, err := store.StepsOfRun(ctx, h.pool, run.ID)
	if err != nil || len(steps) != 2 {
		t.Fatalf("steps = %+v, %v", steps, err)
	}
	if !h.pub.has(wake.TopicLegs, run.ID) || !h.pub.has(wake.TopicRuns, run.ID) {
		t.Fatalf("no wake-up after create: %v", h.pub.events)
	}
	if lines := h.runLines(run.ID); len(lines) == 0 {
		t.Fatalf("no run-level line for the creation")
	}
	if n := len(h.audits("run.create")); n != 1 {
		t.Fatalf("%d run.create audit entries, want 1", n)
	}

	// A job whose approval labels do not include the environment's label needs no approval.
	other := newHarness(t, harnessOptions{approvalFor: []string{"prod"}})
	if run := other.create(other.starter, false, "s1"); run.ApprovalRequired {
		t.Fatalf("approval required for a label the job does not list")
	}
}

func TestCreateNeedsRunPermissionOnTarget(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	ctx := context.Background()
	create := func(p auth.Principal) error {
		_, err := h.eng.Create(ctx, p, h.sourceID, testEnvironment, selection("s1"), "web", false)
		return err
	}

	wrongScopes := map[string]store.Grant{
		"another label":       {Action: "run", Label: "prod"},
		"another environment": {Action: "run", Environment: "production"},
		"another folder":      {Action: "run", FolderID: h.folderID + 1000},
		"another action":      {Action: "cancel"},
	}
	for name, grant := range wrongScopes {
		p := h.stranger
		p.Grants = []store.Grant{grant}
		if err := create(p); !errors.Is(err, ErrForbidden) {
			t.Errorf("grant for %s: Create = %v, want ErrForbidden", name, err)
		}
	}
	if err := create(h.stranger); !errors.Is(err, ErrForbidden) {
		t.Errorf("no grant: Create = %v, want ErrForbidden", err)
	}
	var runs int
	if err := h.pool.QueryRow(ctx, "SELECT count(*) FROM runs").Scan(&runs); err != nil || runs != 0 {
		t.Fatalf("%d runs after refused creates (%v), want 0", runs, err)
	}

	scoped := h.stranger
	scoped.Grants = []store.Grant{{Action: "run", Label: testLabel, Environment: testEnvironment, FolderID: h.folderID}}
	if err := create(scoped); err != nil {
		t.Fatalf("grant covering the target: Create = %v", err)
	}
}

func TestCreateRefusesNotRunnableJob(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, harnessOptions{})
	if err := store.SaveJobProblems(ctx, h.pool, h.sourceID, []byte(`[{"message":"bad"}]`), "jobcommit2", false, h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.Create(ctx, h.starter, h.sourceID, testEnvironment, selection("s1"), "web", false); !errors.Is(err, ErrNotRunnable) {
		t.Fatalf("not runnable job: Create = %v, want ErrNotRunnable", err)
	}

	noInventory := newHarness(t, harnessOptions{noInventory: true})
	if _, err := noInventory.eng.Create(ctx, noInventory.starter, noInventory.sourceID, testEnvironment, selection("s1"), "web", false); !errors.Is(err, ErrNotRunnable) {
		t.Fatalf("no active inventory: Create = %v, want ErrNotRunnable", err)
	}

	// A bad selection is reported as a SelectionError carrying every problem.
	ok := newHarness(t, harnessOptions{})
	_, err := ok.eng.Create(ctx, ok.starter, ok.sourceID, testEnvironment, Selection{"g": {Version: "1.0", Services: map[string]string{"nope": ""}}}, "web", false)
	var selectionErr *SelectionError
	if !errors.As(err, &selectionErr) || len(selectionErr.Problems) == 0 {
		t.Fatalf("bad selection: Create = %v, want *SelectionError with problems", err)
	}
	_, err = ok.eng.Create(ctx, ok.starter, ok.sourceID, "no-such-environment", selection("s1"), "web", false)
	if !errors.As(err, &selectionErr) {
		t.Fatalf("unknown environment: Create = %v, want *SelectionError", err)
	}
}

func TestHappyPathStateSequence(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	ctx := context.Background()

	run := h.create(h.starter, false, "s1", "s2")
	h.advance(run.ID)
	h.expectState(run.ID, "resolving") // the resolve leg has not finished
	h.resolved(run.ID)
	h.advance(run.ID)
	planned := h.expectState(run.ID, "planned")

	// The manifest is the resolve leg's result and its id is the hex SHA-256 of it.
	resolve := h.legByKind(run.ID, "resolve")
	sum := sha256.Sum256(resolve.Result)
	if planned.ManifestID != hex.EncodeToString(sum[:]) || string(planned.Manifest) != string(resolve.Result) {
		t.Fatalf("manifest %q / %q, want the resolve result %q hashed", planned.Manifest, planned.ManifestID, resolve.Result)
	}
	if len(h.locks(run.ID)) != 0 {
		t.Fatalf("a planned run holds locks")
	}

	if err := h.eng.Start(ctx, h.starter, run.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	running := h.expectState(run.ID, "running")
	if running.StartedAt == nil || running.WaitReason != "" {
		t.Fatalf("running run: %+v", running)
	}
	if got := h.locks(run.ID); !reflect.DeepEqual(got, running.LockResources) {
		t.Fatalf("locks held = %v, want exactly the run's resources %v", got, running.LockResources)
	}
	revalidate := h.legByKind(run.ID, "revalidate")
	if revalidate.State != "queued" || revalidate.QueuedAt == nil {
		t.Fatalf("revalidate leg: %+v", revalidate)
	}
	h.advance(run.ID) // waiting for revalidate: nothing changes
	for _, l := range h.legs(run.ID) {
		if l.Kind == "step" && l.State != "pending" {
			t.Fatalf("step leg %d is %s before revalidation finished", l.ID, l.State)
		}
	}

	h.setLeg(revalidate.ID, "succeeded")
	h.advance(run.ID)
	for _, service := range []string{"s1", "s2"} {
		if got := h.stepLeg(run.ID, "config", service).State; got != "queued" {
			t.Fatalf("config leg for %s is %s, want queued", service, got)
		}
		if got := h.stepLeg(run.ID, "deploy", service).State; got != "pending" {
			t.Fatalf("deploy leg for %s is %s, want pending until its config is done", service, got)
		}
	}
	for _, service := range []string{"s1", "s2"} {
		h.setLeg(h.stepLeg(run.ID, "config", service).ID, "succeeded")
	}
	h.advance(run.ID)
	for _, service := range []string{"s1", "s2"} {
		if got := h.stepLeg(run.ID, "deploy", service).State; got != "queued" {
			t.Fatalf("deploy leg for %s is %s, want queued", service, got)
		}
	}
	if len(h.locks(run.ID)) == 0 {
		t.Fatalf("locks released while legs still run")
	}
	for _, service := range []string{"s1", "s2"} {
		h.setLeg(h.stepLeg(run.ID, "deploy", service).ID, "succeeded")
	}
	h.advance(run.ID)
	done := h.expectState(run.ID, "succeeded")
	if done.FinishedAt == nil || len(h.locks(run.ID)) != 0 {
		t.Fatalf("finished run: %+v, locks %v", done, h.locks(run.ID))
	}

	lines := h.runLines(run.ID)
	for _, transition := range [][2]string{{"resolving", "planned"}, {"planned", "queued"}, {"queued", "running"}, {"running", "succeeded"}} {
		if !hasLine(lines, "moved from "+transition[0]+" to "+transition[1]) {
			t.Errorf("no run-level line for %s -> %s in %q", transition[0], transition[1], lines)
		}
	}
	if n := countTransitionLines(lines); n != 4 {
		t.Errorf("%d transition lines, want exactly 4: %q", n, lines)
	}
	h.advance(run.ID) // a final run: nothing happens
	if got := h.runLines(run.ID); len(got) != len(lines) {
		t.Fatalf("Advance on a final run wrote lines: %q", got)
	}
}

func TestPlannedRunWaitsForStartAndAutoStartDoesNot(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	ctx := context.Background()

	planned := h.planned(h.starter, "s1")
	for i := 0; i < 3; i++ {
		h.advance(planned.ID)
	}
	h.expectState(planned.ID, "planned")
	if len(h.locks(planned.ID)) != 0 {
		t.Fatalf("planned run holds locks")
	}
	if err := h.eng.Start(ctx, h.stranger, planned.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Start without run permission = %v, want ErrForbidden", err)
	}
	h.expectState(planned.ID, "planned")
	if err := h.eng.Start(ctx, h.starter, planned.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := h.eng.Start(ctx, h.starter, planned.ID); !errors.Is(err, ErrWrongState) {
		t.Fatalf("second Start = %v, want ErrWrongState", err)
	}
	if n := len(h.audits("run.start")); n != 1 {
		t.Fatalf("%d run.start audit entries, want 1 (the refused ones write none)", n)
	}

	// With auto-start the run skips planned: no person starts it.
	auto := h.create(h.starter, true, "s3")
	h.resolved(auto.ID)
	h.advance(auto.ID)
	h.expectState(auto.ID, "queued")
	h.advance(auto.ID)
	h.expectState(auto.ID, "running")
}

func TestPlannedRunExpiresAfterAnHour(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	ctx := context.Background()
	run := h.planned(h.starter, "s1")

	h.clock.Advance(59 * time.Minute)
	if err := h.eng.Expire(ctx, run.ID); err != nil {
		t.Fatalf("Expire: %v", err)
	}
	h.expectState(run.ID, "planned")

	h.clock.Advance(2 * time.Minute)
	if err := h.eng.Expire(ctx, run.ID); err != nil {
		t.Fatalf("Expire: %v", err)
	}
	expired := h.expectState(run.ID, "expired")
	if expired.FinishedAt == nil {
		t.Fatalf("expired run has no finish time")
	}
	if err := h.eng.Start(ctx, h.starter, run.ID); !errors.Is(err, ErrWrongState) {
		t.Fatalf("Start of an expired run = %v, want ErrWrongState", err)
	}
	if err := h.eng.Expire(ctx, run.ID); err != nil { // already final: nothing to do
		t.Fatalf("Expire of a final run: %v", err)
	}
}

func TestApprovalRequiredWaitsAndHoldsNoLocks(t *testing.T) {
	h := newHarness(t, harnessOptions{approvalFor: []string{"dev"}})
	ctx := context.Background()
	run := h.planned(h.starter, "s1")
	if !run.ApprovalRequired {
		t.Fatalf("run does not need approval")
	}
	if err := h.eng.Start(ctx, h.starter, run.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for i := 0; i < 3; i++ {
		h.advance(run.ID)
	}
	h.expectState(run.ID, "waiting_approval")
	if len(h.locks(run.ID)) != 0 {
		t.Fatalf("a run waiting for approval holds locks: %v", h.locks(run.ID))
	}
	for _, l := range h.legs(run.ID) {
		if l.Kind == "revalidate" {
			t.Fatalf("revalidate leg exists before approval")
		}
	}

	if err := h.eng.Approve(ctx, h.approver, run.ID, run.ManifestID, ""); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	approved := h.expectState(run.ID, "queued")
	entries := h.audits("run.approve")
	if len(entries) != 1 || !strings.Contains(string(entries[0].Detail), approved.ManifestID) || approved.ManifestID == "" {
		t.Fatalf("approve audit entries %+v, want one naming manifest %q", entries, approved.ManifestID)
	}
	h.advance(run.ID)
	h.expectState(run.ID, "running")
	if len(h.locks(run.ID)) == 0 {
		t.Fatalf("approved run took no locks")
	}
	if err := h.eng.Approve(ctx, h.approver, run.ID, run.ManifestID, ""); !errors.Is(err, store.ErrAlreadyDecided) {
		t.Fatalf("Approve of a running run = %v, want store.ErrAlreadyDecided", err)
	}
}

func TestApproveByStarterIsRefusedUnlessAdmin(t *testing.T) {
	h := newHarness(t, harnessOptions{approvalFor: []string{"dev"}})
	ctx := context.Background()
	start := func(p auth.Principal) store.Run {
		run := h.planned(p, "s1")
		if err := h.eng.Start(ctx, p, run.ID); err != nil {
			t.Fatalf("Start: %v", err)
		}
		return h.expectState(run.ID, "waiting_approval")
	}

	run := start(h.starterApprover)
	if err := h.eng.Approve(ctx, h.starterApprover, run.ID, run.ManifestID, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("starter approving own run = %v, want ErrForbidden", err)
	}
	if err := h.eng.Approve(ctx, h.stranger, run.ID, run.ManifestID, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("approval without the grant = %v, want ErrForbidden", err)
	}
	if err := h.eng.Reject(ctx, h.starterApprover, run.ID, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("starter rejecting own run = %v, want ErrForbidden", err)
	}
	h.expectState(run.ID, "waiting_approval")
	if n := len(h.audits("run.approve")) + len(h.audits("run.reject")); n != 0 {
		t.Fatalf("%d audit entries for refused approvals", n)
	}

	adminRun := start(h.admin)
	if err := h.eng.Approve(ctx, h.admin, adminRun.ID, adminRun.ManifestID, ""); err != nil {
		t.Fatalf("admin approving own run: %v", err)
	}
	h.expectState(adminRun.ID, "queued")
}

func TestRejectAndExpire(t *testing.T) {
	h := newHarness(t, harnessOptions{approvalFor: []string{"dev"}, approvalExpiry: 2 * time.Hour})
	ctx := context.Background()
	waiting := func() store.Run {
		run := h.planned(h.starter, "s1")
		if err := h.eng.Start(ctx, h.starter, run.ID); err != nil {
			t.Fatalf("Start: %v", err)
		}
		return h.expectState(run.ID, "waiting_approval")
	}

	rejected := waiting()
	if err := h.eng.Reject(ctx, h.approver, rejected.ID, ""); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if got := h.expectState(rejected.ID, "rejected"); got.EndReason == "" || got.WaitReason != "" {
		t.Fatalf("rejected run: end reason %q, wait reason %q", got.EndReason, got.WaitReason)
	}
	if err := h.eng.Reject(ctx, h.approver, rejected.ID, ""); !errors.Is(err, store.ErrAlreadyDecided) {
		t.Fatalf("second Reject = %v, want store.ErrAlreadyDecided", err)
	}
	before := len(h.runLines(rejected.ID))
	h.advance(rejected.ID) // a late result for a final run is harmless
	if got := len(h.runLines(rejected.ID)); got != before {
		t.Fatalf("Advance of a rejected run wrote lines")
	}
	if len(h.audits("run.reject")) != 1 {
		t.Fatalf("reject not audited exactly once")
	}

	expiring := waiting()
	h.clock.Advance(119 * time.Minute)
	if err := h.eng.Expire(ctx, expiring.ID); err != nil {
		t.Fatal(err)
	}
	h.expectState(expiring.ID, "waiting_approval") // the job's own expiry is 2h, not the default 24h
	h.clock.Advance(2 * time.Minute)
	if err := h.eng.Expire(ctx, expiring.ID); err != nil {
		t.Fatal(err)
	}
	if got := h.expectState(expiring.ID, "expired"); got.EndReason == "" {
		t.Fatalf("expired run has no end reason")
	}
}

func TestSecondRunWaitsForFirstRunsLocks(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	ctx := context.Background()

	a := h.running("s1")
	// B overlaps A on s1 and also wants s2, which nobody holds: it must take nothing.
	b := h.planned(h.starter, "s1", "s2")
	if err := h.eng.Start(ctx, h.starter, b.ID); err != nil {
		t.Fatalf("Start B: %v", err)
	}
	wantReason := "waiting for run #" + strconv.FormatInt(a.ID, 10)
	check := func(when string) {
		t.Helper()
		got := h.expectState(b.ID, "queued")
		if got.WaitReason != wantReason {
			t.Fatalf("%s: B waits with %q, want %q", when, got.WaitReason, wantReason)
		}
		if locks := h.locks(b.ID); len(locks) != 0 {
			t.Fatalf("%s: B holds %v while waiting (locks are all or nothing)", when, locks)
		}
		for _, l := range h.legs(b.ID) {
			if l.Kind == "revalidate" || (l.Kind == "step" && l.State != "pending") {
				t.Fatalf("%s: B has progressed: leg %+v", when, l)
			}
		}
	}
	h.advance(b.ID)
	check("while A is running")
	startedAt := h.run(b.ID).StateChangedAt
	h.advance(b.ID)
	if !h.run(b.ID).StateChangedAt.Equal(startedAt) {
		t.Fatalf("a repeated wait restarted the state clock")
	}

	// A's revalidation is unknown: A keeps its locks in needs_reconciliation.
	h.setLeg(h.legByKind(a.ID, "revalidate").ID, "unknown")
	h.advance(a.ID)
	h.expectState(a.ID, "needs_reconciliation")
	h.advance(b.ID)
	check("while A needs reconciliation")

	// A person settles it; A is final and releases its locks, so B proceeds.
	if err := h.eng.Reconcile(ctx, h.starter, a.ID, h.legByKind(a.ID, "revalidate").ID, "failed"); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	h.expectState(a.ID, "failed")
	h.advance(b.ID)
	if got := h.expectState(b.ID, "running"); got.WaitReason != "" {
		t.Fatalf("B still says %q while running", got.WaitReason)
	}
	if len(h.locks(b.ID)) != 4 {
		t.Fatalf("B holds %v, want all four of its resources", h.locks(b.ID))
	}
}

func TestInterleavingFromTheDesignCannotHappen(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	ctx := context.Background()

	a := h.running("s1")
	b := h.planned(h.starter, "s1")
	if err := h.eng.Start(ctx, h.starter, b.ID); err != nil {
		t.Fatal(err)
	}
	h.pastRevalidation(a.ID)
	configA := h.stepLeg(a.ID, "config", "s1")
	if configA.State != "queued" {
		t.Fatalf("A's config leg is %s, want queued", configA.State)
	}
	h.setLeg(configA.ID, "succeeded") // A's config is applied; its deploy is still to come
	h.advance(a.ID)
	for i := 0; i < 3; i++ {
		h.advance(b.ID)
	}
	h.expectState(b.ID, "queued")
	for _, l := range h.legs(b.ID) {
		if l.State == "queued" && l.Kind != "resolve" || l.State == "claimed" || l.State == "running" {
			t.Fatalf("B has a leg in flight while A still deploys the same service: %+v", l)
		}
	}

	h.setLeg(h.stepLeg(a.ID, "deploy", "s1").ID, "succeeded")
	h.advance(a.ID)
	h.expectState(a.ID, "succeeded")
	h.advance(b.ID)
	h.expectState(b.ID, "running")
	if got := h.legByKind(b.ID, "revalidate").State; got != "queued" {
		t.Fatalf("B's revalidate leg is %s, want queued", got)
	}
}

func TestRevalidateFailureInvalidatesAndReleasesLocks(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	run := h.running("s1", "s2")
	if len(h.locks(run.ID)) == 0 {
		t.Fatalf("running run holds no locks")
	}
	h.setLeg(h.legByKind(run.ID, "revalidate").ID, "failed")
	h.advance(run.ID)
	invalid := h.expectState(run.ID, "invalidated")
	if invalid.EndReason == "" || invalid.WaitReason != "" {
		t.Fatalf("invalidated run: end reason %q, wait reason %q", invalid.EndReason, invalid.WaitReason)
	}
	if invalid.FinishedAt == nil || len(h.locks(run.ID)) != 0 {
		t.Fatalf("invalidated run: %+v, locks %v", invalid, h.locks(run.ID))
	}
	for _, l := range h.legs(run.ID) {
		if l.Kind == "step" && l.State != "cancelled" {
			t.Fatalf("step leg %d is %s, want cancelled (nothing ran)", l.ID, l.State)
		}
	}
	if !hasLine(h.runLines(run.ID), "moved from running to invalidated") {
		t.Fatalf("no line for the invalidation: %q", h.runLines(run.ID))
	}
}

func TestTwoAdvancersNeverQueueALegTwice(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	ctx := context.Background()
	run := h.create(h.starter, true, "s1", "s2", "s3", "s4", "s5", "s6")

	var wg sync.WaitGroup
	var mu sync.Mutex
	var problems []string
	report := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		problems = append(problems, strings.TrimSpace(fmt.Sprintf(format, args...)))
	}
	deadline := time.Now().Add(30 * time.Second)
	finished := func() bool {
		mu.Lock()
		stop := len(problems) > 0 // a problem was found: stop everyone
		mu.Unlock()
		if stop {
			return true
		}
		var state string
		if err := h.pool.QueryRow(ctx, "SELECT state FROM runs WHERE id = $1", run.ID).Scan(&state); err != nil {
			report("read state: %v", err)
			return true
		}
		return isFinalRunState(state) || time.Now().After(deadline)
	}

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !finished() {
				if err := h.eng.Advance(ctx, run.ID); err != nil {
					report("Advance: %v", err)
					return
				}
			}
		}()
	}
	// The settler plays the runners: everything queued succeeds at once. It
	// also checks that a queued_at, once set, never changes.
	wg.Add(1)
	go func() {
		defer wg.Done()
		seen := map[int64]time.Time{}
		for !finished() {
			for _, l := range h.legs(run.ID) {
				if l.QueuedAt != nil {
					if first, ok := seen[l.ID]; ok && !first.Equal(*l.QueuedAt) {
						report("leg %d queued_at changed from %v to %v", l.ID, first, l.QueuedAt)
					}
					seen[l.ID] = *l.QueuedAt
				}
				if l.State == "queued" {
					_, err := h.pool.Exec(ctx, `UPDATE legs SET state = 'succeeded', result = '{"ok":true}'::jsonb, finished_at = now() WHERE id = $1 AND state = 'queued'`, l.ID)
					if err != nil {
						report("settle leg: %v", err)
					}
				}
			}
		}
	}()
	wg.Wait()
	if len(problems) > 0 {
		t.Fatalf("problems: %s", strings.Join(problems, "; "))
	}

	h.expectState(run.ID, "succeeded")
	legs := h.legs(run.ID)
	counts := map[string]int{}
	for _, l := range legs {
		counts[l.Kind]++
		if l.State != "succeeded" || l.QueuedAt == nil {
			t.Errorf("leg %+v: want succeeded with a queued_at", l)
		}
	}
	if counts["resolve"] != 1 || counts["revalidate"] != 1 || counts["step"] != 12 {
		t.Fatalf("legs by kind = %v, want 1 resolve, 1 revalidate, 12 steps", counts)
	}
	if len(h.locks(run.ID)) != 0 {
		t.Fatalf("locks left after the run finished")
	}
	lines := h.runLines(run.ID)
	if n := countTransitionLines(lines); n != 3 { // resolving->queued, queued->running, running->succeeded
		t.Fatalf("%d transition lines, want 3 (no transition applied twice): %q", n, lines)
	}
}

func TestCancelWhileRunning(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	ctx := context.Background()
	run := h.running("s1", "s2")
	h.pastRevalidation(run.ID)
	inFlight := h.stepLeg(run.ID, "config", "s1")
	h.setLeg(inFlight.ID, "running") // a runner is working on it
	untouched := h.stepLeg(run.ID, "config", "s2")
	if untouched.State != "queued" {
		t.Fatalf("config s2 is %s, want queued", untouched.State)
	}

	if err := h.eng.Cancel(ctx, h.stranger, run.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Cancel without permission = %v, want ErrForbidden", err)
	}
	if err := h.eng.Cancel(ctx, h.starter, run.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	cancelling := h.expectState(run.ID, "cancelling")
	if cancelling.CancelRequestedBy == nil || *cancelling.CancelRequestedBy != h.starter.UserID || cancelling.CancelRequestedAt == nil {
		t.Fatalf("who and when not recorded: %+v", cancelling)
	}
	if got := h.stepLeg(run.ID, "config", "s1"); got.State != "running" || !got.CancelRequested {
		t.Fatalf("in-flight leg: %+v, want running and flagged", got)
	}
	if got := h.stepLeg(run.ID, "config", "s2").State; got != "cancelled" {
		t.Fatalf("queued leg is %s, want cancelled", got)
	}
	for _, service := range []string{"s1", "s2"} {
		if got := h.stepLeg(run.ID, "deploy", service).State; got != "cancelled" {
			t.Fatalf("pending deploy leg for %s is %s, want cancelled", service, got)
		}
	}
	if len(h.locks(run.ID)) == 0 {
		t.Fatalf("locks released while a leg is still in flight")
	}
	h.advance(run.ID)
	h.expectState(run.ID, "cancelling")

	h.setLeg(inFlight.ID, "succeeded") // the runner finished its file and stopped
	h.advance(run.ID)
	done := h.expectState(run.ID, "cancelled")
	if done.FinishedAt == nil || len(h.locks(run.ID)) != 0 {
		t.Fatalf("cancelled run: %+v, locks %v", done, h.locks(run.ID))
	}
	if err := h.eng.Cancel(ctx, h.starter, run.ID); !errors.Is(err, ErrWrongState) {
		t.Fatalf("Cancel of a final run = %v, want ErrWrongState", err)
	}
	if len(h.audits("run.cancel")) != 1 {
		t.Fatalf("cancel not audited exactly once")
	}
}

// TestCancelInEveryNonFinalState proves a person can cancel a run wherever it is.
func TestCancelInEveryNonFinalState(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		state string
		opts  harnessOptions
		build func(h *harness) store.Run
		// afterCancel is the state straight after Cancel.
		afterCancel string
		finish      func(h *harness, run store.Run)
	}{
		{state: "resolving", afterCancel: "cancelled",
			build: func(h *harness) store.Run { return h.create(h.starter, false, "s1") }},
		{state: "planned", afterCancel: "cancelled",
			build: func(h *harness) store.Run { return h.planned(h.starter, "s1") }},
		{state: "waiting_approval", opts: harnessOptions{approvalFor: []string{"dev"}}, afterCancel: "cancelled",
			build: func(h *harness) store.Run {
				run := h.planned(h.starter, "s1")
				if err := h.eng.Start(ctx, h.starter, run.ID); err != nil {
					t.Fatal(err)
				}
				return h.expectState(run.ID, "waiting_approval")
			}},
		{state: "queued", afterCancel: "cancelled",
			build: func(h *harness) store.Run {
				h.running("s1")
				run := h.planned(h.starter, "s1")
				if err := h.eng.Start(ctx, h.starter, run.ID); err != nil {
					t.Fatal(err)
				}
				return h.expectState(run.ID, "queued")
			}},
		{state: "running", afterCancel: "cancelling", // the queued revalidate leg is cancelled; the next advance settles the run
			build: func(h *harness) store.Run { return h.running("s1") },
			finish: func(h *harness, run store.Run) {
				h.advance(run.ID)
				h.expectState(run.ID, "cancelled")
			}},
		{state: "cancelling", afterCancel: "cancelling",
			build: func(h *harness) store.Run {
				run := h.running("s1")
				h.setLeg(h.legByKind(run.ID, "revalidate").ID, "running")
				if err := h.eng.Cancel(ctx, h.starter, run.ID); err != nil {
					t.Fatal(err)
				}
				return h.expectState(run.ID, "cancelling")
			},
			finish: func(h *harness, run store.Run) {
				h.setLeg(h.legByKind(run.ID, "revalidate").ID, "succeeded")
				h.advance(run.ID)
				h.expectState(run.ID, "cancelled")
			}},
		{state: "needs_reconciliation", afterCancel: "needs_reconciliation",
			build: func(h *harness) store.Run {
				run := h.running("s1")
				h.setLeg(h.legByKind(run.ID, "revalidate").ID, "unknown")
				h.advance(run.ID)
				return h.expectState(run.ID, "needs_reconciliation")
			},
			finish: func(h *harness, run store.Run) {
				if err := h.eng.Reconcile(ctx, h.starter, run.ID, h.legByKind(run.ID, "revalidate").ID, "failed"); err != nil {
					t.Fatal(err)
				}
				h.expectState(run.ID, "cancelled") // the person asked for it, and nothing succeeded
			}},
	}
	for _, c := range cases {
		t.Run(c.state, func(t *testing.T) {
			h := newHarness(t, c.opts)
			run := c.build(h)
			if run.State != c.state {
				t.Fatalf("built a run in %s, want %s", run.State, c.state)
			}
			if err := h.eng.Cancel(ctx, h.starter, run.ID); err != nil {
				t.Fatalf("Cancel in %s: %v", c.state, err)
			}
			got := h.expectState(run.ID, c.afterCancel)
			if got.CancelRequestedAt == nil {
				t.Fatalf("cancel request not recorded")
			}
			if c.afterCancel == "cancelled" {
				if len(h.locks(run.ID)) != 0 {
					t.Fatalf("cancelled run holds locks %v", h.locks(run.ID))
				}
				for _, l := range h.legs(run.ID) {
					if l.State == "pending" || l.State == "queued" {
						t.Fatalf("leg %+v survived the cancel", l)
					}
				}
			}
			if c.finish != nil {
				c.finish(h, run)
				if len(h.locks(run.ID)) != 0 {
					t.Fatalf("locks left after the run finished: %v", h.locks(run.ID))
				}
			}
		})
	}
}

// unknownRun builds a running run whose config leg for s1 ended unknown, with
// everything else done; it returns in needs_reconciliation holding its locks.
func unknownRun(t *testing.T, h *harness) (store.Run, store.Leg) {
	t.Helper()
	run := h.running("s1", "s2")
	h.pastRevalidation(run.ID)
	unknown := h.stepLeg(run.ID, "config", "s1")
	h.setLeg(unknown.ID, "unknown")
	h.setLeg(h.stepLeg(run.ID, "config", "s2").ID, "succeeded")
	h.advance(run.ID) // s1's deploy is skipped, s2's deploy is queued
	if got := h.stepLeg(run.ID, "deploy", "s1").State; got != "skipped" {
		t.Fatalf("deploy s1 is %s, want skipped (its config is unknown)", got)
	}
	h.setLeg(h.stepLeg(run.ID, "deploy", "s2").ID, "succeeded")
	h.advance(run.ID)
	return h.expectState(run.ID, "needs_reconciliation"), unknown
}

func TestUnknownLegGivesNeedsReconciliationAndKeepsLocks(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	run, _ := unknownRun(t, h)
	if got := h.locks(run.ID); !reflect.DeepEqual(got, run.LockResources) {
		t.Fatalf("locks = %v, want all kept: %v", got, run.LockResources)
	}
	if run.FinishedAt != nil {
		t.Fatalf("needs_reconciliation is not final")
	}
	for i := 0; i < 3; i++ {
		h.advance(run.ID)
	}
	h.expectState(run.ID, "needs_reconciliation")
	for _, l := range h.legs(run.ID) {
		if l.Kind == "revalidate" && l.ID != h.legByKind(run.ID, "revalidate").ID {
			t.Fatalf("a second revalidate leg appeared")
		}
	}
}

func TestReconcileToSucceededFinishesRun(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	ctx := context.Background()
	run, unknown := unknownRun(t, h)

	if err := h.eng.Reconcile(ctx, h.stranger, run.ID, unknown.ID, "succeeded"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Reconcile without restore = %v, want ErrForbidden", err)
	}
	if err := h.eng.Reconcile(ctx, h.starter, run.ID, unknown.ID, "maybe"); err == nil {
		t.Fatalf("Reconcile accepted the outcome %q", "maybe")
	}
	if err := h.eng.Reconcile(ctx, h.starter, run.ID, h.stepLeg(run.ID, "deploy", "s2").ID, "succeeded"); !errors.Is(err, ErrWrongState) {
		t.Fatalf("Reconcile of a settled leg = %v, want ErrWrongState", err)
	}
	h.expectState(run.ID, "needs_reconciliation")
	if len(h.audits("run.reconcile")) != 0 {
		t.Fatalf("refused reconciliations were audited")
	}

	if err := h.eng.Reconcile(ctx, h.starter, run.ID, unknown.ID, "succeeded"); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// s1's deploy was skipped and reconciliation never resumes it: partial.
	done := h.expectState(run.ID, "partially_succeeded")
	if done.FinishedAt == nil || len(h.locks(run.ID)) != 0 {
		t.Fatalf("finished run: %+v, locks %v", done, h.locks(run.ID))
	}
	if got := h.stepLeg(run.ID, "config", "s1").State; got != "succeeded" {
		t.Fatalf("reconciled leg is %s", got)
	}
	entries := h.audits("run.reconcile")
	if len(entries) != 1 || !strings.Contains(string(entries[0].Detail), `"succeeded"`) {
		t.Fatalf("reconcile audit = %+v, want one entry with the person's choice", entries)
	}
	if err := h.eng.Reconcile(ctx, h.starter, run.ID, unknown.ID, "failed"); !errors.Is(err, ErrWrongState) {
		t.Fatalf("Reconcile of a final run = %v, want ErrWrongState", err)
	}
}

func TestCheckAgainRequeuesLeg(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	ctx := context.Background()
	run, unknown := unknownRun(t, h)
	// The leg has resume data and an operation record from its first attempts.
	if _, err := h.pool.Exec(ctx, `UPDATE legs SET resume = '{"cursor":7}'::jsonb WHERE id = $1`, unknown.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx,
		`INSERT INTO operations (leg_id, seq, name, key, state, at) VALUES ($1, 1, 'copy', 'copy-s1', 'intended', now())`, unknown.ID); err != nil {
		t.Fatal(err)
	}

	if err := h.eng.CheckAgain(ctx, h.stranger, run.ID, unknown.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("CheckAgain without restore = %v, want ErrForbidden", err)
	}
	if err := h.eng.CheckAgain(ctx, h.starter, run.ID, unknown.ID); err != nil {
		t.Fatalf("CheckAgain: %v", err)
	}
	leg := h.stepLeg(run.ID, "config", "s1")
	if leg.ID != unknown.ID || leg.State != "queued" || leg.QueuedAt == nil || string(leg.Resume) != `{"cursor": 7}` {
		t.Fatalf("requeued leg: %+v (resume %s)", leg, leg.Resume)
	}
	var operations int
	if err := h.pool.QueryRow(ctx, "SELECT count(*) FROM operations WHERE leg_id = $1", unknown.ID).Scan(&operations); err != nil || operations != 1 {
		t.Fatalf("operations of the leg: %d, %v; want 1 kept", operations, err)
	}
	if err := h.eng.CheckAgain(ctx, h.starter, run.ID, unknown.ID); !errors.Is(err, ErrWrongState) {
		t.Fatalf("CheckAgain of a queued leg = %v, want ErrWrongState", err)
	}
	if !h.pub.has(wake.TopicLegs, run.ID) {
		t.Fatalf("no wake-up for the requeued leg")
	}

	// While the leg is unsettled the run stays put: no running, no new locks, no revalidate.
	for i := 0; i < 3; i++ {
		h.advance(run.ID)
	}
	h.expectState(run.ID, "needs_reconciliation")
	if len(h.locks(run.ID)) == 0 {
		t.Fatalf("locks dropped while the check is pending")
	}
	h.setLeg(unknown.ID, "succeeded")
	h.advance(run.ID)
	h.expectState(run.ID, "partially_succeeded")
	if len(h.locks(run.ID)) != 0 {
		t.Fatalf("locks left")
	}
}

func TestReleaseLocksIsAdminOnlyAndAudited(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	ctx := context.Background()
	run, unknown := unknownRun(t, h)

	for _, p := range []auth.Principal{h.starter, h.approver, h.stranger} {
		if err := h.eng.ReleaseLocks(ctx, p, run.ID); !errors.Is(err, ErrForbidden) {
			t.Fatalf("ReleaseLocks by %s = %v, want ErrForbidden", p.Name, err)
		}
	}
	h.expectState(run.ID, "needs_reconciliation")
	if len(h.locks(run.ID)) == 0 {
		t.Fatalf("a refused release freed the locks")
	}

	// A leg being checked again could still run: the locks must stay until it settles.
	if err := h.eng.CheckAgain(ctx, h.starter, run.ID, unknown.ID); err != nil {
		t.Fatalf("CheckAgain: %v", err)
	}
	if err := h.eng.ReleaseLocks(ctx, h.admin, run.ID); !errors.Is(err, ErrWrongState) {
		t.Fatalf("ReleaseLocks with a queued check = %v, want ErrWrongState", err)
	}
	h.setLeg(unknown.ID, "running")
	if err := h.eng.ReleaseLocks(ctx, h.admin, run.ID); !errors.Is(err, ErrWrongState) {
		t.Fatalf("ReleaseLocks with a running check = %v, want ErrWrongState", err)
	}
	h.expectState(run.ID, "needs_reconciliation")
	if len(h.locks(run.ID)) == 0 {
		t.Fatalf("a refused release freed the locks")
	}
	h.setLeg(unknown.ID, "unknown")

	if err := h.eng.ReleaseLocks(ctx, h.admin, run.ID); err != nil {
		t.Fatalf("ReleaseLocks: %v", err)
	}
	failed := h.expectState(run.ID, "failed")
	if failed.EndReason != "locks released by admin without reconciliation" || failed.WaitReason != "" || failed.FinishedAt == nil {
		t.Fatalf("failed run: %+v", failed)
	}
	if len(h.locks(run.ID)) != 0 {
		t.Fatalf("locks still held")
	}
	if !hasLine(h.runLines(run.ID), "locks released by admin without reconciliation") {
		t.Fatalf("no run-level line: %q", h.runLines(run.ID))
	}
	entries := h.audits("run.release_locks")
	if len(entries) != 1 || entries[0].ActorID == nil || *entries[0].ActorID != h.admin.UserID {
		t.Fatalf("audit = %+v, want one entry by the admin", entries)
	}
	if err := h.eng.ReleaseLocks(ctx, h.admin, run.ID); !errors.Is(err, ErrWrongState) {
		t.Fatalf("second ReleaseLocks = %v, want ErrWrongState", err)
	}

	running := h.running("s3")
	if err := h.eng.ReleaseLocks(ctx, h.admin, running.ID); !errors.Is(err, ErrWrongState) {
		t.Fatalf("ReleaseLocks of a running run = %v, want ErrWrongState", err)
	}
}

func TestEveryPersonActionIsAudited(t *testing.T) {
	h := newHarness(t, harnessOptions{approvalFor: []string{"dev"}})
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	approved := h.planned(h.starter, "s1")
	must(h.eng.Start(ctx, h.starter, approved.ID))
	must(h.eng.Approve(ctx, h.approver, approved.ID, approved.ManifestID, ""))
	rejected := h.planned(h.starter, "s2")
	must(h.eng.Start(ctx, h.starter, rejected.ID))
	must(h.eng.Reject(ctx, h.approver, rejected.ID, ""))
	cancelled := h.planned(h.starter, "s3")
	must(h.eng.Cancel(ctx, h.starter, cancelled.ID))

	// check again, then reconcile
	first, unknown := unknownRun(t, h)
	must(h.eng.CheckAgain(ctx, h.starter, first.ID, unknown.ID))
	h.setLeg(unknown.ID, "unknown")
	h.advance(first.ID)
	must(h.eng.Reconcile(ctx, h.starter, first.ID, unknown.ID, "failed"))
	// release locks
	second, _ := unknownRun(t, h)
	must(h.eng.ReleaseLocks(ctx, h.admin, second.ID))

	want := map[string]struct {
		actor int64
		count int
	}{
		"run.create":        {h.starter.UserID, 5},
		"run.start":         {h.starter.UserID, 4},
		"run.approve":       {h.approver.UserID, 3},
		"run.reject":        {h.approver.UserID, 1},
		"run.cancel":        {h.starter.UserID, 1},
		"run.check_again":   {h.starter.UserID, 1},
		"run.reconcile":     {h.starter.UserID, 1},
		"run.release_locks": {h.admin.UserID, 1},
	}
	for action, w := range want {
		entries := h.audits(action)
		if len(entries) != w.count {
			t.Errorf("%s: %d audit entries, want %d", action, len(entries), w.count)
			continue
		}
		for _, e := range entries {
			if e.ActorID == nil || e.TargetType != "run" || e.TargetID == "" {
				t.Errorf("%s entry: %+v", action, e)
			}
			if w.actor != h.approver.UserID && *e.ActorID != w.actor { // approvals may come from the approver or the admin
				t.Errorf("%s entry by user %d, want %d", action, *e.ActorID, w.actor)
			}
		}
	}
}

// TestPerEnvironmentLimitCountsOtherRunsLegs shows the limits count the legs of
// other runs in the same environment.
func TestPerEnvironmentLimitCountsOtherRunsLegs(t *testing.T) {
	h := newHarness(t, harnessOptions{limits: Limits{PerEnvironment: 2, PerAccount: 20}})
	a := h.running("s1")
	h.pastRevalidation(a.ID) // one config leg of A is queued
	b := h.running("s2", "s3")
	h.pastRevalidation(b.ID)
	queued := 0
	for _, l := range h.legs(b.ID) {
		if l.Kind == "step" && l.State == "queued" {
			queued++
		}
	}
	if queued != 1 {
		t.Fatalf("B queued %d step legs, want 1: the limit of 2 per environment minus A's 1", queued)
	}
}

// TestCancelOfAClaimedLegFlagsItInsteadOfFailing covers the race where a runner
// claims a queued leg between Advance reading it and cancelling it.
func TestCancelOfAClaimedLegFlagsItInsteadOfFailing(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	ctx := context.Background()
	run := h.running("s1")
	revalidate := h.legByKind(run.ID, "revalidate")
	h.setLeg(revalidate.ID, "claimed") // the runner won the race

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	locked, err := store.LockRun(ctx, tx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var o outcome
	// The decision was made while the leg was still queued.
	if err := h.eng.apply(ctx, tx, locked, nil, Decision{CancelLegs: []int64{revalidate.ID}}, &o); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got := h.legByKind(run.ID, "revalidate")
	if got.State != "claimed" || !got.CancelRequested {
		t.Fatalf("leg = %+v, want still claimed and flagged", got)
	}
}
