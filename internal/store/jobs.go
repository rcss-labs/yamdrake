package store

import (
	"context"
	"fmt"
	"time"
)

// JobSource is one YAML job file tracked in a repository.
type JobSource struct {
	ID                  int64
	FolderID            int64
	Repo, Branch, Path  string // Repo is "owner/name"
	JobID, Title        string // empty until first good read
	LastGood            []byte // JSON of spec.Job, nil until first good read
	LastGoodCommit      string
	Runnable            bool
	Problems            []byte // JSON of spec.Problems for the newest commit, nil when clean
	ProblemsCommit      string
	CheckedAt, SyncedAt *time.Time
}

const jobSourceColumns = `id, folder_id, repo, branch, path, job_id, title, last_good, last_good_commit,
	runnable, problems, problems_commit, checked_at, synced_at`

func scanJobSource(row interface{ Scan(...any) error }) (JobSource, error) {
	var s JobSource
	err := row.Scan(&s.ID, &s.FolderID, &s.Repo, &s.Branch, &s.Path, &s.JobID, &s.Title, &s.LastGood,
		&s.LastGoodCommit, &s.Runnable, &s.Problems, &s.ProblemsCommit, &s.CheckedAt, &s.SyncedAt)
	return s, err
}

// CreateJobSource registers a source in a folder. It returns ErrDuplicate when
// (repo, branch, path) is already tracked.
func CreateJobSource(ctx context.Context, db DBTX, s JobSource) (JobSource, error) {
	row := db.QueryRow(ctx,
		`INSERT INTO job_sources (folder_id, repo, branch, path) VALUES ($1, $2, $3, $4)
		 RETURNING `+jobSourceColumns, s.FolderID, s.Repo, s.Branch, s.Path)
	created, err := scanJobSource(row)
	if err != nil {
		return JobSource{}, fmt.Errorf("create job source: %w", mapError(err))
	}
	return created, nil
}

// ListJobSources returns all sources ordered by id.
func ListJobSources(ctx context.Context, db DBTX) ([]JobSource, error) {
	rows, err := db.Query(ctx, "SELECT "+jobSourceColumns+" FROM job_sources ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("list job sources: %w", err)
	}
	defer rows.Close()
	var out []JobSource
	for rows.Next() {
		s, err := scanJobSource(rows)
		if err != nil {
			return nil, fmt.Errorf("scan job source: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetJobSource returns ErrNotFound when id does not exist.
func GetJobSource(ctx context.Context, db DBTX, id int64) (JobSource, error) {
	s, err := scanJobSource(db.QueryRow(ctx, "SELECT "+jobSourceColumns+" FROM job_sources WHERE id = $1", id))
	if err != nil {
		return JobSource{}, fmt.Errorf("get job source: %w", mapError(err))
	}
	return s, nil
}

// MoveJobSource changes the folder. It returns ErrNotFound when the source does
// not exist.
func MoveJobSource(ctx context.Context, db DBTX, id, folderID int64) error {
	tag, err := db.Exec(ctx, "UPDATE job_sources SET folder_id = $2 WHERE id = $1", id, folderID)
	if err != nil {
		return fmt.Errorf("move job source: %w", mapError(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("move job source: %w", ErrNotFound)
	}
	return nil
}

// SaveJobGood stores the last good definition and sets synced_at and
// checked_at to at. It does not touch runnable or problems: a valid read calls
// SaveJobProblems(..., nil, commit, true, at) in the same transaction. It
// returns ErrDuplicate when jobID belongs to another source and ErrNotFound
// when the source does not exist.
func SaveJobGood(ctx context.Context, db DBTX, id int64, job []byte, jobID, title, commit string, at time.Time) error {
	tag, err := db.Exec(ctx,
		`UPDATE job_sources SET last_good = $2, job_id = $3, title = $4, last_good_commit = $5,
		        synced_at = $6, checked_at = $6
		 WHERE id = $1`, id, job, jobID, title, commit, at)
	if err != nil {
		return fmt.Errorf("save job good: %w", mapError(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("save job good: %w", ErrNotFound)
	}
	return nil
}

// SaveJobProblems sets problems (nil stores SQL NULL), problems_commit,
// runnable and checked_at.
func SaveJobProblems(ctx context.Context, db DBTX, id int64, problems []byte, commit string, runnable bool, at time.Time) error {
	tag, err := db.Exec(ctx,
		`UPDATE job_sources SET problems = $2, problems_commit = $3, runnable = $4, checked_at = $5
		 WHERE id = $1`, id, problems, commit, runnable, at)
	if err != nil {
		return fmt.Errorf("save job problems: %w", mapError(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("save job problems: %w", ErrNotFound)
	}
	return nil
}

// TouchJobChecked records a successful poll that found nothing new.
func TouchJobChecked(ctx context.Context, db DBTX, id int64, at time.Time) error {
	tag, err := db.Exec(ctx, "UPDATE job_sources SET checked_at = $2 WHERE id = $1", id, at)
	if err != nil {
		return fmt.Errorf("touch job checked: %w", mapError(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("touch job checked: %w", ErrNotFound)
	}
	return nil
}

// SetJobRunnable changes only the runnable flag; checked_at, problems and the
// stored definition stay as they are.
func SetJobRunnable(ctx context.Context, db DBTX, id int64, runnable bool) error {
	tag, err := db.Exec(ctx, "UPDATE job_sources SET runnable = $2 WHERE id = $1", id, runnable)
	if err != nil {
		return fmt.Errorf("set job runnable: %w", mapError(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("set job runnable: %w", ErrNotFound)
	}
	return nil
}
