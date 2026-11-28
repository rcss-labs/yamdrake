package main

import (
	"encoding/json"
	"time"

	"github.com/rcss-labs/yumdrake/internal/spec"
)

// The shapes below are the JSON of the server's /api/v1 routes (internal/api),
// reduced to the fields the command-line tool uses.

type jobSummary struct {
	ID             int64    `json:"id"`
	JobID          string   `json:"job_id"`
	Title          string   `json:"title"`
	FolderID       int64    `json:"folder_id"`
	Folder         string   `json:"folder"`
	Repo           string   `json:"repo"`
	Branch         string   `json:"branch"`
	Path           string   `json:"path"`
	Runnable       bool     `json:"runnable"`
	Commit         string   `json:"commit"`
	ProblemCount   int      `json:"problem_count"`
	SyncError      string   `json:"sync_error"`
	Services       int      `json:"services"`
	ApprovalLabels []string `json:"approval_labels"`
	// LastRun is added by the CLI from the run list.
	LastRun *lastRun `json:"last_run"`
}

type lastRun struct {
	ID    int64  `json:"id"`
	State string `json:"state"`
}

type jobDetail struct {
	jobSummary
	Definition     *spec.Job     `json:"definition"`
	Source         string        `json:"source"`
	Problems       spec.Problems `json:"problems"`
	ProblemsCommit string        `json:"problems_commit"`
}

type runSummary struct {
	ID          int64      `json:"id"`
	Kind        string     `json:"kind"`
	JobSourceID int64      `json:"job_source_id"`
	JobID       string     `json:"job_id"`
	Environment string     `json:"environment"`
	Label       string     `json:"label"`
	State       string     `json:"state"`
	WaitReason  string     `json:"wait_reason"`
	EndReason   string     `json:"end_reason"`
	ManifestID  string     `json:"manifest_id"`
	RequestedBy int64      `json:"requested_by"`
	Via         string     `json:"via"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
}

type runStep struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Use   string `json:"use"`
	Group string `json:"group"`
}

type runLeg struct {
	ID            int64  `json:"id"`
	StepID        *int64 `json:"step_id"`
	Step          string `json:"step"`
	Kind          string `json:"kind"`
	Service       string `json:"service"`
	Version       string `json:"version"`
	State         string `json:"state"`
	Attempt       int    `json:"attempt"`
	FailureShort  string `json:"failure_short"`
	FailureDetail string `json:"failure_detail"`
}

type runFailure struct {
	LegID   int64  `json:"leg_id"`
	Service string `json:"service"`
	Step    string `json:"step"`
	Kind    string `json:"kind"`
	State   string `json:"state"`
	Short   string `json:"short"`
	Detail  string `json:"detail"`
}

// runApproval is the open or last approval on a run (Plan 7).
type runApproval struct {
	RequestedBy int64      `json:"requested_by"`
	RequestedAt *time.Time `json:"requested_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
	Decision    string     `json:"decision"`
	Comment     string     `json:"comment"`
}

type runDetail struct {
	runSummary
	AWSAccount       string       `json:"aws_account"`
	ApprovalRequired bool         `json:"approval_required"`
	CancelRequested  bool         `json:"cancel_requested"`
	Approval         *runApproval `json:"approval"`
	Steps            []runStep    `json:"steps"`
	Legs             []runLeg     `json:"legs"`
	Summary          struct {
		Counts   map[string]int `json:"counts"`
		Failures []runFailure   `json:"failures"`
	} `json:"summary"`
}

type logLine struct {
	LegID int64     `json:"leg_id"`
	Seq   int64     `json:"seq"`
	At    time.Time `json:"at"`
	Level string    `json:"level"`
	Text  string    `json:"text"`
}

type logGap struct {
	LegID   int64 `json:"leg_id"`
	FromSeq int64 `json:"from_seq"`
	ToSeq   int64 `json:"to_seq"`
}

type logPage struct {
	Lines []logLine `json:"lines"`
	Next  string    `json:"next"`
	Gaps  []logGap  `json:"gaps"`
}

// versionEntry is one image tag of a service (Plan 5 versions.Version).
type versionEntry struct {
	Tag      string    `json:"tag"`
	Digest   string    `json:"digest"`
	PushedAt time.Time `json:"pushed_at"`
}

type groupVersions struct {
	Common      []string                  `json:"common"`
	Services    map[string][]versionEntry `json:"services"`
	Unavailable map[string]string         `json:"unavailable"`
}

type jobVersions struct {
	Groups map[string]groupVersions `json:"groups"`
}

// runningCell and runningMatrix are Plan 9's running versions.
type runningCell struct {
	State            string `json:"state"`
	Version          string `json:"version"`
	TaskDefinition   string `json:"task_definition"`
	ByYumdrake       bool   `json:"by_yumdrake"`
	RunID            int64  `json:"run_id"`
	LastDeployFailed bool   `json:"last_deploy_failed"`
	Behind           bool   `json:"behind"`
	Error            string `json:"error"`
}

type runningRow struct {
	Service string        `json:"service"`
	Cells   []runningCell `json:"cells"`
}

type runningMatrix struct {
	Environments []spec.Environment `json:"environments"`
	Rows         []runningRow       `json:"rows"`
}

// approvalItem is a row of GET /approvals (Plan 7).
type approvalItem struct {
	RunID           int64           `json:"run_id"`
	JobID           string          `json:"job_id"`
	Environment     string          `json:"environment"`
	RequestedByName string          `json:"requested_by_name"`
	RequestedAt     *time.Time      `json:"requested_at"`
	ExpiresAt       *time.Time      `json:"expires_at"`
	Versions        json.RawMessage `json:"versions"`
	CanApprove      bool            `json:"can_approve"`
	Reason          string          `json:"reason"`
}

// manifest is the redacted manifest of GET /runs/{id}/plan (Plan 5 Task 6,
// served by Task 9): the Go type's fields in snake_case.
type manifest struct {
	ID          string            `json:"id"`
	RunID       int64             `json:"run_id"`
	Environment string            `json:"environment"`
	Label       string            `json:"label"`
	Services    []manifestService `json:"services"`
	Checks      []manifestCheck   `json:"checks"`
}

type manifestService struct {
	Name                   string           `json:"name"`
	Group                  string           `json:"group"`
	Version                string           `json:"version"`
	ImageDigest            string           `json:"image_digest"`
	PreviousTaskDefinition string           `json:"previous_task_definition"`
	PreviousVersion        string           `json:"previous_version"`
	TargetTaskDefinition   string           `json:"target_task_definition"`
	Config                 []manifestConfig `json:"config"`
}

type manifestConfig struct {
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	Branch      string   `json:"branch"`
	Fallback    bool     `json:"fallback"`
	Change      string   `json:"change"`
	Keys        *keyDiff `json:"keys"`
	NotRestored string   `json:"not_restored"`
}

type keyDiff struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
	Changed []string `json:"changed"`
}

type manifestCheck struct {
	Name    string `json:"name"`
	Service string `json:"service"`
	Status  string `json:"status"`
	Detail  string `json:"detail"`
}
