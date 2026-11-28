package store

import (
	"context"
	"fmt"
	"time"
)

// LogLine is one line of a run's log. LegID 0 marks a line the orchestrator
// wrote about the run itself.
type LogLine struct {
	RunID, LegID int64
	Seq          int64
	At           time.Time
	Level, Text  string // Level: "info" | "ok" | "warn" | "error"
}

// AppendRunLine adds a run-level line (leg 0) numbered after the highest one.
// The caller holds the run row lock (LockRun), which serializes appenders;
// without it two callers could pick the same number and one would fail with
// ErrDuplicate.
func AppendRunLine(ctx context.Context, tx DBTX, runID int64, level, text string, at time.Time) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO log_lines (run_id, leg_id, seq, at, level, text)
		 SELECT $1, 0, coalesce(max(seq), 0) + 1, $2, $3, $4 FROM log_lines WHERE run_id = $1 AND leg_id = 0`,
		runID, dbTime(at), level, text)
	if err != nil {
		return fmt.Errorf("append run line: %w", mapError(err))
	}
	return nil
}

// InsertLogLines stores lines in one statement. A line whose (run, leg, seq)
// is already stored is dropped, so a repeated batch never duplicates.
func InsertLogLines(ctx context.Context, db DBTX, lines []LogLine) error {
	if len(lines) == 0 {
		return nil
	}
	runIDs := make([]int64, len(lines))
	legIDs := make([]int64, len(lines))
	seqs := make([]int64, len(lines))
	ats := make([]time.Time, len(lines))
	levels := make([]string, len(lines))
	texts := make([]string, len(lines))
	for i, l := range lines {
		runIDs[i], legIDs[i], seqs[i], ats[i], levels[i], texts[i] = l.RunID, l.LegID, l.Seq, dbTime(l.At), l.Level, l.Text
	}
	_, err := db.Exec(ctx,
		`INSERT INTO log_lines (run_id, leg_id, seq, at, level, text)
		 SELECT * FROM unnest($1::bigint[], $2::bigint[], $3::bigint[], $4::timestamptz[], $5::text[], $6::text[])
		 ON CONFLICT (run_id, leg_id, seq) DO NOTHING`,
		runIDs, legIDs, seqs, ats, levels, texts)
	if err != nil {
		return fmt.Errorf("insert log lines: %w", mapError(err))
	}
	return nil
}

// MaxLogSeq returns the highest stored sequence for (run, leg), 0 when none.
func MaxLogSeq(ctx context.Context, db DBTX, runID, legID int64) (int64, error) {
	var max int64
	err := db.QueryRow(ctx, "SELECT coalesce(max(seq), 0) FROM log_lines WHERE run_id = $1 AND leg_id = $2", runID, legID).Scan(&max)
	if err != nil {
		return 0, fmt.Errorf("max log seq: %w", mapError(err))
	}
	return max, nil
}
