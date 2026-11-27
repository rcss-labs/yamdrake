// Package sync polls GitHub and keeps the stored inventory and job definitions
// current. The inventory is activated one commit at a time as a whole, and each
// job keeps its last good version when a newer commit is invalid.
//
// Two kinds of failure are kept apart on purpose. A source error (network,
// 5xx, rate limit, missing connection, unreadable branch) says nothing about
// the YAML, so it changes no stored row and is returned. A definitions error
// (a file that does not parse or validate) is a result: it is stored as
// problems next to the commit that caused it.
package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rcss-labs/yamdrake/internal/spec"
	"github.com/rcss-labs/yamdrake/internal/store"
)

// Source reads a repository. It is satisfied by *github.Client.
type Source interface {
	HeadCommit(ctx context.Context, repo, branch string) (string, error)
	ListFiles(ctx context.Context, repo, commit, dir string) ([]string, error)
	ReadFile(ctx context.Context, repo, commit, path string) ([]byte, error)
}

// SourceFor looks up (and decrypts) the connection for a repository owner.
type SourceFor func(ctx context.Context, owner string) (Source, error)

// Engine syncs the inventory and the job sources.
type Engine struct {
	pool    *pgxpool.Pool
	sources SourceFor
	log     *slog.Logger
	now     func() time.Time
}

// NewEngine builds an engine. now supplies every stored timestamp.
func NewEngine(pool *pgxpool.Pool, sources SourceFor, log *slog.Logger, now func() time.Time) *Engine {
	return &Engine{pool: pool, sources: sources, log: log, now: now}
}

const noInventoryMessage = "no active inventory yet"

// SyncJob brings one job source up to date with the head of its branch.
func (e *Engine) SyncJob(ctx context.Context, jobSourceID int64) error {
	job, err := store.GetJobSource(ctx, e.pool, jobSourceID)
	if err != nil {
		return err
	}
	at := e.now()

	snapshot, haveInventory, err := store.ActiveSnapshot(ctx, e.pool)
	if err != nil {
		return err
	}
	if !haveInventory {
		// The problem is stored without a commit so that every round reads the
		// file again until an inventory exists.
		problems := spec.Problems{{File: job.Path, Message: noInventoryMessage}}
		return e.saveJobProblems(ctx, e.pool, job, problems, "", false, at)
	}
	var inventory spec.Inventory
	if err := json.Unmarshal(snapshot.Inventory, &inventory); err != nil {
		return fmt.Errorf("decode active inventory: %w", err)
	}

	source, err := e.sourceForRepo(ctx, job.Repo)
	if err != nil {
		return e.sourceError("job source", err, "job_source_id", job.ID, "repo", job.Repo)
	}
	head, err := source.HeadCommit(ctx, job.Repo, job.Branch)
	if err != nil {
		return e.sourceError("read job branch head", err, "job_source_id", job.ID, "repo", job.Repo, "branch", job.Branch)
	}

	// Unchanged means: nothing new at the head and the file was last checked
	// against the inventory that is active now. A job re-validated at
	// activation, or read since, has checked_at at or after ActivatedAt.
	if head == newestSeenCommit(job) && job.CheckedAt != nil && !job.CheckedAt.Before(snapshot.ActivatedAt) {
		return store.TouchJobChecked(ctx, e.pool, job.ID, at)
	}

	data, err := source.ReadFile(ctx, job.Repo, head, job.Path)
	if err != nil {
		return e.sourceError("read job file", err, "job_source_id", job.ID, "repo", job.Repo, "commit", head)
	}
	parsed, problems := spec.ParseJob(job.Path, data)
	if len(problems) == 0 {
		problems = spec.ValidateJob(parsed, inventory)
	}
	if len(problems) > 0 {
		// Keep LastGood; the job stays runnable only if it already was.
		return e.saveJobProblems(ctx, e.pool, job, problems, head, job.Runnable && job.LastGood != nil, at)
	}

	definition, err := json.Marshal(parsed)
	if err != nil {
		return fmt.Errorf("encode job %q: %w", parsed.ID, err)
	}
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin job save: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	err = store.SaveJobGood(ctx, tx, job.ID, definition, parsed.ID, parsed.Title, head, at)
	if err == nil {
		err = store.SaveJobProblems(ctx, tx, job.ID, nil, head, true, at)
	}
	if errors.Is(err, store.ErrDuplicate) {
		_ = tx.Rollback(ctx)
		duplicate := spec.Problems{{File: job.Path, Line: 0, Message: fmt.Sprintf("job id %q is already used by another job source", parsed.ID)}}
		return e.saveJobProblems(ctx, e.pool, job, duplicate, head, job.Runnable && job.LastGood != nil, at)
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit job save: %w", err)
	}
	return nil
}

// newestSeenCommit is the newest commit read for the job: the one the stored
// problems belong to, or else the last good one.
func newestSeenCommit(job store.JobSource) string {
	if job.Problems != nil {
		return job.ProblemsCommit
	}
	return job.LastGoodCommit
}

func (e *Engine) saveJobProblems(ctx context.Context, db store.DBTX, job store.JobSource, problems spec.Problems,
	commit string, runnable bool, at time.Time) error {
	encoded, err := json.Marshal(problems.Sorted())
	if err != nil {
		return fmt.Errorf("encode problems: %w", err)
	}
	return store.SaveJobProblems(ctx, db, job.ID, encoded, commit, runnable, at)
}

// revalidateJobs checks every job's last good definition against a newly
// activated inventory. It runs inside the activation transaction.
//
// Rule: a job whose newest commit is its last good one (no problems, or
// problems recorded for that same commit) is re-validated: valid means
// runnable with problems cleared, invalid means not runnable with the reasons
// and LastGood kept. A job whose newest commit is a different, rejected one
// keeps those problems untouched, because they describe what its author must
// fix; only its runnable flag follows the last good definition. A job that has
// never had a good read has nothing to validate.
func (e *Engine) revalidateJobs(ctx context.Context, tx store.DBTX, inventory spec.Inventory, at time.Time) error {
	jobs, err := store.ListJobSources(ctx, tx)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if job.LastGood == nil {
			continue
		}
		var lastGood spec.Job
		if err := json.Unmarshal(job.LastGood, &lastGood); err != nil {
			return fmt.Errorf("decode last good of job source %d: %w", job.ID, err)
		}
		problems := spec.ValidateJob(lastGood, inventory)
		ownProblems := job.Problems != nil && job.ProblemsCommit != job.LastGoodCommit
		switch {
		case ownProblems:
			err = store.SetJobRunnable(ctx, tx, job.ID, len(problems) == 0)
		case len(problems) == 0:
			err = store.SaveJobProblems(ctx, tx, job.ID, nil, job.LastGoodCommit, true, at)
		default:
			err = e.saveJobProblems(ctx, tx, job, problems, job.LastGoodCommit, false, at)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// sourceForRepo resolves the connection for the owner part of "owner/name".
func (e *Engine) sourceForRepo(ctx context.Context, repo string) (Source, error) {
	owner, _, _ := strings.Cut(repo, "/")
	return e.sources(ctx, owner)
}

// sourceError logs a failure to reach the source at warn level and returns it
// wrapped. Nothing is stored for it.
func (e *Engine) sourceError(what string, err error, attrs ...any) error {
	e.log.Warn("sync source error", append(attrs, "step", what, "error", err.Error())...)
	return fmt.Errorf("%s: %w", what, err)
}
