package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Run is one deploy or restore of a job. Its state machine lives in the engine
// package; the store only enforces the list of valid values.
type Run struct {
	ID                         int64
	Kind                       string // "deploy" | "restore"
	JobSourceID                int64
	JobID                      string
	JobSnapshot                []byte // JSON spec.Job
	InventoryCommit, JobCommit string
	Environment, Label         string
	AWSAccount                 string
	FolderID                   int64
	Selection                  []byte // JSON engine.Selection
	Manifest                   []byte // JSON, nil until resolved
	ManifestID                 string
	ApprovalRequired           bool
	AutoStart                  bool
	State, WaitReason          string
	RequestedBy                int64
	Via                        string // "web" | "cli"
	ParentRunID                *int64
	CancelRequestedBy          *int64
	CreatedAt                  time.Time
	// StateChangedAt is when the run last went through UpdateRunState (or was
	// inserted). Planned and approval expiry measure from it.
	StateChangedAt                           time.Time
	StartedAt, FinishedAt, CancelRequestedAt *time.Time
}

// RunStep is one step of a run's plan, copied from the job when the run is
// created.
type RunStep struct {
	ID, RunID   int64
	Name, Use   string
	GroupID     string
	Position    int
	MaxParallel int
	Timeout     time.Duration
	Needs       []byte // JSON []spec.Need
}

// RunFilter selects runs for ListRuns. Zero fields mean "any".
type RunFilter struct {
	JobSourceID int64
	Environment string
	States      []string
	// Query matches, case-insensitively as a substring, the job id, label,
	// environment, manifest id or the run id.
	Query    string
	BeforeID int64 // only runs with a smaller id, for paging
	Limit    int   // default 50, at most 500
}

const (
	defaultRunListLimit = 50
	maxRunListLimit     = 500
)

// finalRunStates are the states a run never leaves.
const finalRunStatesSQL = `('succeeded', 'partially_succeeded', 'failed', 'cancelled', 'rejected', 'expired', 'invalidated')`

const runColumns = `id, kind, job_source_id, job_id, job_snapshot, inventory_commit, job_commit,
	environment, label, aws_account, folder_id, selection, manifest, manifest_id,
	approval_required, auto_start, state, wait_reason, requested_by, via, parent_run_id,
	cancel_requested_by, cancel_requested_at, created_at, state_changed_at, started_at, finished_at`

func scanRun(r rowScanner) (Run, error) {
	var x Run
	err := r.Scan(&x.ID, &x.Kind, &x.JobSourceID, &x.JobID, &x.JobSnapshot, &x.InventoryCommit, &x.JobCommit,
		&x.Environment, &x.Label, &x.AWSAccount, &x.FolderID, &x.Selection, &x.Manifest, &x.ManifestID,
		&x.ApprovalRequired, &x.AutoStart, &x.State, &x.WaitReason, &x.RequestedBy, &x.Via, &x.ParentRunID,
		&x.CancelRequestedBy, &x.CancelRequestedAt, &x.CreatedAt, &x.StateChangedAt, &x.StartedAt, &x.FinishedAt)
	return x, err
}

// InsertRun stores a run with its steps and legs in one transaction (a
// savepoint when db is already a transaction), so a failure leaves nothing.
//
// A leg's StepID, when not nil, is an index into steps (the ids do not exist
// yet); the stored leg points at the stored step. A zero r.CreatedAt means
// now; CreatedAt and StateChangedAt both start at that time. ErrInvalid for a
// value the CHECK constraints refuse or a step index out of range,
// ErrInvalidReference for an unknown job source, folder or user.
func InsertRun(ctx context.Context, db DBTX, r Run, steps []RunStep, legs []Leg) (Run, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return Run{}, fmt.Errorf("insert run: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var created any // NULL makes the column default (now()) apply to both times
	if !r.CreatedAt.IsZero() {
		created = dbTime(r.CreatedAt)
	}
	row := tx.QueryRow(ctx,
		`INSERT INTO runs (kind, job_source_id, job_id, job_snapshot, inventory_commit, job_commit,
			environment, label, aws_account, folder_id, selection, manifest, manifest_id,
			approval_required, auto_start, state, wait_reason, requested_by, via, parent_run_id,
			created_at, state_changed_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20,
			coalesce($21::timestamptz, now()), coalesce($21::timestamptz, now()))
		 RETURNING `+runColumns,
		r.Kind, r.JobSourceID, r.JobID, r.JobSnapshot, r.InventoryCommit, r.JobCommit,
		r.Environment, r.Label, r.AWSAccount, r.FolderID, r.Selection, r.Manifest, r.ManifestID,
		r.ApprovalRequired, r.AutoStart, r.State, r.WaitReason, r.RequestedBy, r.Via, r.ParentRunID, created)
	run, err := scanRun(row)
	if err != nil {
		return Run{}, fmt.Errorf("insert run: %w", mapError(err))
	}

	stepIDs := make([]int64, len(steps))
	for i, s := range steps {
		err := tx.QueryRow(ctx,
			`INSERT INTO run_steps (run_id, name, use, group_id, position, max_parallel, timeout_ns, needs)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, coalesce($8::jsonb, '[]'::jsonb)) RETURNING id`,
			run.ID, s.Name, s.Use, s.GroupID, s.Position, s.MaxParallel, int64(s.Timeout), s.Needs).Scan(&stepIDs[i])
		if err != nil {
			return Run{}, fmt.Errorf("insert run step %q: %w", s.Name, mapError(err))
		}
	}
	for i, l := range legs {
		l.RunID = run.ID
		if l.StepID != nil {
			idx := *l.StepID
			if idx < 0 || idx >= int64(len(stepIDs)) {
				return Run{}, fmt.Errorf("insert run leg %d: %w: step index %d out of range", i, ErrInvalid, idx)
			}
			id := stepIDs[idx]
			l.StepID = &id
		}
		if _, err := InsertLeg(ctx, tx, l); err != nil {
			return Run{}, fmt.Errorf("insert run leg %d: %w", i, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Run{}, fmt.Errorf("insert run: %w", mapError(err))
	}
	return run, nil
}

// GetRun returns ErrNotFound for an unknown id.
func GetRun(ctx context.Context, db DBTX, id int64) (Run, error) {
	r, err := scanRun(db.QueryRow(ctx, "SELECT "+runColumns+" FROM runs WHERE id = $1", id))
	if err != nil {
		return Run{}, fmt.Errorf("get run: %w", mapError(err))
	}
	return r, nil
}

// LockRun reads a run and locks its row until the transaction ends (SELECT ...
// FOR UPDATE). Every state change of a run starts with this, so two
// orchestrators never advance one run at the same time. ErrNotFound for an
// unknown id.
func LockRun(ctx context.Context, tx DBTX, id int64) (Run, error) {
	r, err := scanRun(tx.QueryRow(ctx, "SELECT "+runColumns+" FROM runs WHERE id = $1 FOR UPDATE", id))
	if err != nil {
		return Run{}, fmt.Errorf("lock run: %w", mapError(err))
	}
	return r, nil
}

// ListRuns returns runs newest first (by id).
func ListRuns(ctx context.Context, db DBTX, f RunFilter) ([]Run, error) {
	var where []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if f.JobSourceID != 0 {
		add("job_source_id = $%d", f.JobSourceID)
	}
	if f.Environment != "" {
		add("environment = $%d", f.Environment)
	}
	if len(f.States) > 0 {
		add("state = ANY($%d)", f.States)
	}
	if f.Query != "" {
		args = append(args, "%"+escapeLike(f.Query)+"%")
		n := len(args)
		where = append(where, fmt.Sprintf(
			`(job_id ILIKE $%[1]d OR label ILIKE $%[1]d OR environment ILIKE $%[1]d OR manifest_id ILIKE $%[1]d OR id::text ILIKE $%[1]d)`, n))
	}
	if f.BeforeID != 0 {
		add("id < $%d", f.BeforeID)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = defaultRunListLimit
	}
	if limit > maxRunListLimit {
		limit = maxRunListLimit
	}
	args = append(args, limit)
	query := "SELECT " + runColumns + " FROM runs"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", len(args))

	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", mapError(err))
	}
	defer rows.Close()
	runs := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("list runs: %w", mapError(err))
		}
		runs = append(runs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list runs: %w", mapError(err))
	}
	return runs, nil
}

// escapeLike makes s match literally inside a LIKE pattern.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ActiveRunIDs returns, in ascending order, every run not in a final state.
func ActiveRunIDs(ctx context.Context, db DBTX) ([]int64, error) {
	rows, err := db.Query(ctx, "SELECT id FROM runs WHERE state NOT IN "+finalRunStatesSQL+" ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("active run ids: %w", mapError(err))
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("active run ids: %w", mapError(err))
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("active run ids: %w", mapError(err))
	}
	return ids, nil
}

// UpdateRunState sets the state and wait reason and stamps state_changed_at
// with at (every call, even when the state is unchanged). started_at is set
// the first time the run is running, finished_at when it enters a final state.
// The caller holds the run lock (LockRun) and decides whether the move is
// legal; the store only checks the state is a known value (ErrInvalid).
func UpdateRunState(ctx context.Context, tx DBTX, id int64, state, waitReason string, at time.Time) error {
	tag, err := tx.Exec(ctx,
		`UPDATE runs SET state = $2, wait_reason = $3, state_changed_at = $4,
			started_at = CASE WHEN $2 = 'running' THEN coalesce(started_at, $4) ELSE started_at END,
			finished_at = CASE WHEN $2 IN `+finalRunStatesSQL+` THEN $4 ELSE finished_at END
		 WHERE id = $1`, id, state, waitReason, dbTime(at))
	if err != nil {
		return fmt.Errorf("update run state: %w", mapError(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("update run state: %w", ErrNotFound)
	}
	return nil
}

// SetManifest stores the resolved plan and its id.
func SetManifest(ctx context.Context, tx DBTX, id int64, manifest []byte, manifestID string) error {
	tag, err := tx.Exec(ctx, "UPDATE runs SET manifest = $2, manifest_id = $3 WHERE id = $1", id, manifest, manifestID)
	if err != nil {
		return fmt.Errorf("set manifest: %w", mapError(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("set manifest: %w", ErrNotFound)
	}
	return nil
}

// RequestCancel records who asked for the run to be cancelled and when. The
// first request wins; later ones change nothing and are not an error.
func RequestCancel(ctx context.Context, tx DBTX, id, by int64, at time.Time) error {
	tag, err := tx.Exec(ctx,
		`UPDATE runs SET cancel_requested_by = coalesce(cancel_requested_by, $2),
			cancel_requested_at = coalesce(cancel_requested_at, $3) WHERE id = $1`, id, by, dbTime(at))
	if err != nil {
		return fmt.Errorf("request cancel: %w", mapError(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("request cancel: %w", ErrNotFound)
	}
	return nil
}

// StepsOfRun returns the run's steps in position order.
func StepsOfRun(ctx context.Context, db DBTX, runID int64) ([]RunStep, error) {
	rows, err := db.Query(ctx,
		`SELECT id, run_id, name, use, group_id, position, max_parallel, timeout_ns, needs
		 FROM run_steps WHERE run_id = $1 ORDER BY position`, runID)
	if err != nil {
		return nil, fmt.Errorf("steps of run: %w", mapError(err))
	}
	defer rows.Close()
	steps := []RunStep{}
	for rows.Next() {
		var s RunStep
		var timeout int64
		if err := rows.Scan(&s.ID, &s.RunID, &s.Name, &s.Use, &s.GroupID, &s.Position, &s.MaxParallel, &timeout, &s.Needs); err != nil {
			return nil, fmt.Errorf("steps of run: %w", mapError(err))
		}
		s.Timeout = time.Duration(timeout)
		steps = append(steps, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("steps of run: %w", mapError(err))
	}
	return steps, nil
}
