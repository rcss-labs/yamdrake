package logs

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Line is one stored log line. LegID 0 marks a line about the run itself.
type Line struct {
	RunID, LegID, Seq int64
	At                time.Time
	Level, Text       string
}

// Filter narrows a read. Zero fields do not filter. A step or service filter
// excludes run-level lines (they belong to no step or service).
type Filter struct {
	StepID                *int64
	Service, Level, Query string // Query: case-insensitive substring of the text
}

// Gap is a missing run of sequence numbers inside one leg.
type Gap struct {
	LegID, FromSeq, ToSeq int64
	Note                  string // why the lines are missing, when known
}

// Page is one answer of Read. Next resumes right after the last line
// returned, whatever the filter.
type Page struct {
	Lines []Line
	Next  Cursor
	Gaps  []Gap
}

// Cursor is an opaque position in a run's log. It encodes the last sequence
// seen per leg. Lines of different legs are committed in an order unrelated to
// their timestamps, so a position in time could skip a line committed late;
// the sequence of each leg cannot, because a leg's batches are committed in
// order and each batch is contiguous.
type Cursor string

// ErrBadCursor is returned for a cursor that is not one Read produced.
var ErrBadCursor = errors.New("invalid log cursor")

const (
	cursorVersion = "v1;"
	// A cursor lists the legs read so far. These bounds keep a hostile or
	// corrupt cursor from costing more than a bounded amount of work.
	maxCursorLegs = 2048
	maxCursorLen  = 128 * 1024

	defaultPageLimit = 200
	maxPageLimit     = 1000
)

func encodeCursor(positions map[int64]int64) (Cursor, error) {
	if len(positions) == 0 {
		return "", nil
	}
	if len(positions) > maxCursorLegs {
		return "", fmt.Errorf("log cursor: run has more than %d legs with lines", maxCursorLegs)
	}
	legs := make([]int64, 0, len(positions))
	for leg := range positions {
		legs = append(legs, leg)
	}
	sort.Slice(legs, func(i, j int) bool { return legs[i] < legs[j] })
	var b strings.Builder
	b.WriteString(cursorVersion)
	for i, leg := range legs {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatInt(leg, 10))
		b.WriteByte(':')
		b.WriteString(strconv.FormatInt(positions[leg], 10))
	}
	return Cursor(base64.RawURLEncoding.EncodeToString([]byte(b.String()))), nil
}

// decodeCursor validates c strictly: only the canonical encoding of a set of
// (leg >= 0, seq >= 1) pairs is accepted.
func decodeCursor(c Cursor) (map[int64]int64, error) {
	if c == "" {
		return map[int64]int64{}, nil
	}
	if len(c) > maxCursorLen {
		return nil, fmt.Errorf("%w: too long", ErrBadCursor)
	}
	raw, err := base64.RawURLEncoding.DecodeString(string(c))
	if err != nil {
		return nil, fmt.Errorf("%w: not base64", ErrBadCursor)
	}
	body, ok := strings.CutPrefix(string(raw), cursorVersion)
	if !ok {
		return nil, fmt.Errorf("%w: unknown version", ErrBadCursor)
	}
	entries := strings.Split(body, ",")
	if len(entries) > maxCursorLegs {
		return nil, fmt.Errorf("%w: too many legs", ErrBadCursor)
	}
	out := make(map[int64]int64, len(entries))
	prev := int64(-1)
	for _, entry := range entries {
		legText, seqText, found := strings.Cut(entry, ":")
		if !found {
			return nil, fmt.Errorf("%w: malformed entry", ErrBadCursor)
		}
		leg, err1 := strconv.ParseInt(legText, 10, 64)
		seq, err2 := strconv.ParseInt(seqText, 10, 64)
		if err1 != nil || err2 != nil || strconv.FormatInt(leg, 10) != legText || strconv.FormatInt(seq, 10) != seqText {
			return nil, fmt.Errorf("%w: malformed number", ErrBadCursor)
		}
		if leg < 0 || seq < 1 || leg <= prev {
			return nil, fmt.Errorf("%w: out of range or unsorted", ErrBadCursor)
		}
		prev = leg
		out[leg] = seq
	}
	return out, nil
}

// querier is what pgxpool.Pool and pgx.Tx share for reading.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Reader reads a run's log from PostgreSQL and the archive.
type Reader struct {
	pool    *pgxpool.Pool
	archive Archive
}

// NewReader returns a Reader. archive may be nil when archiving is off.
func NewReader(pool *pgxpool.Pool, a Archive) *Reader {
	return &Reader{pool: pool, archive: a}
}

// candidate is a line that may go in the page, with the key it is ordered by.
// order is the leg's running maximum of line times: it keeps each leg's lines
// in sequence order even if a clock stepped back, so a page is always a
// prefix of every leg and the cursor never skips a line.
type candidate struct {
	Line
	order time.Time
}

// Read returns up to limit lines of the run after the cursor (empty = from the
// start), ordered by time, then leg, then sequence, and the gaps found in the
// legs it covered. Archived legs are merged in.
func (r *Reader) Read(ctx context.Context, runID int64, f Filter, after Cursor, limit int) (Page, error) {
	return r.read(ctx, runID, f, after, limit, nil)
}

// archiveCache holds decoded archived legs, by leg id, between the pages of one
// read sequence. A nil cache decodes afresh every time.
type archiveCache map[int64][]archivedLine

// Session reads one run's log page after page for one consumer, such as an
// event stream. It remembers each archived leg it has decoded until the cursor
// has passed the leg's last line, so a read of the whole log decompresses every
// archived leg once. A Session is not safe for concurrent use.
type Session struct {
	r     *Reader
	runID int64
	cache archiveCache
}

// NewSession starts a paged read of the run's log.
func (r *Reader) NewSession(runID int64) *Session {
	return &Session{r: r, runID: runID, cache: archiveCache{}}
}

// Read is Reader.Read for the session's run.
func (s *Session) Read(ctx context.Context, f Filter, after Cursor, limit int) (Page, error) {
	return s.r.read(ctx, s.runID, f, after, limit, s.cache)
}

func (r *Reader) read(ctx context.Context, runID int64, f Filter, after Cursor, limit int, cache archiveCache) (Page, error) {
	cursor, err := decodeCursor(after)
	if err != nil {
		return Page{}, err
	}
	if limit <= 0 {
		limit = defaultPageLimit
	}
	if limit > maxPageLimit {
		limit = maxPageLimit
	}

	// One snapshot for every statement, so an archive that commits mid-read
	// cannot make a leg appear twice or not at all.
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Page{}, fmt.Errorf("read log: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	archived, err := r.archivedLegs(ctx, tx, runID)
	if err != nil {
		return Page{}, err
	}
	cands, err := r.queryRows(ctx, tx, runID, f, cursor, limit, archived)
	if err != nil {
		return Page{}, err
	}
	archiveGaps := map[int64][]Gap{} // per archived leg, every gap after the cursor
	lost := map[int64]Gap{}          // archived legs whose object is gone
	if len(archived) > 0 {
		archCands, gaps, err := r.readArchived(ctx, tx, runID, f, cursor, archived, limit, cache, cands, lost)
		if err != nil {
			return Page{}, err
		}
		cands = append(cands, archCands...)
		archiveGaps = gaps
	}
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if !a.order.Equal(b.order) {
			return a.order.Before(b.order)
		}
		if a.LegID != b.LegID {
			return a.LegID < b.LegID
		}
		return a.Seq < b.Seq
	})
	if len(cands) > limit {
		cands = cands[:limit]
	}

	page := Page{Lines: make([]Line, len(cands))}
	covered := map[int64]int64{} // leg -> highest sequence returned
	for i, c := range cands {
		page.Lines[i] = c.Line
		covered[c.LegID] = c.Seq
	}
	next := make(map[int64]int64, len(cursor)+len(covered))
	for leg, seq := range cursor {
		next[leg] = seq
	}
	for leg, seq := range covered {
		next[leg] = seq
	}
	for leg, g := range lost {
		next[leg] = g.ToSeq
	}
	if page.Next, err = encodeCursor(next); err != nil {
		return Page{}, err
	}
	// Forget archived legs the cursor has read to the end.
	for leg := range cache {
		if ref, ok := archived[leg]; !ok || next[leg] >= ref.LastSeq {
			delete(cache, leg)
		}
	}

	if page.Gaps, err = r.gaps(ctx, tx, runID, cursor, covered, archived, archiveGaps); err != nil {
		return Page{}, err
	}
	for _, g := range lost {
		if g.Note != "" {
			page.Gaps = append(page.Gaps, g)
		}
	}
	sort.Slice(page.Gaps, func(i, j int) bool {
		if page.Gaps[i].LegID != page.Gaps[j].LegID {
			return page.Gaps[i].LegID < page.Gaps[j].LegID
		}
		return page.Gaps[i].FromSeq < page.Gaps[j].FromSeq
	})
	return page, nil
}

// queryRows returns the first limit matching rows that lie after the cursor.
// The window runs before the filter so every source computes the same order
// regardless of the filter.
func (r *Reader) queryRows(ctx context.Context, q querier, runID int64, f Filter, cursor map[int64]int64, limit int, archived map[int64]archiveRef) ([]candidate, error) {
	legIDs, seqs := cursorArrays(cursor)
	archivedIDs := make([]int64, 0, len(archived))
	for leg := range archived {
		archivedIDs = append(archivedIDs, leg)
	}
	var rows pgx.Rows
	var err error
	if f == (Filter{}) {
		// No filter: a page can only hold the first `limit` rows of each leg (a
		// leg's rows leave in sequence order with a non-decreasing sort key), so
		// read just those per leg. Reading all the rows after the cursor would
		// make a paged read of N lines cost O(N) per page.
		rows, err = q.Query(ctx, `
WITH leg_ids AS (
    SELECT id AS leg_id FROM legs WHERE run_id = $1
    UNION SELECT 0
),
after AS (
    SELECT t.leg_id, t.seq, t.at, t.level, t.text,
           max(t.at) OVER (PARTITION BY t.leg_id ORDER BY t.seq) AS ord
    FROM leg_ids g
    LEFT JOIN unnest($2::bigint[], $3::bigint[]) AS c(leg_id, seq) ON c.leg_id = g.leg_id
    CROSS JOIN LATERAL (
        SELECT l.leg_id, l.seq, l.at, l.level, l.text FROM log_lines l
        WHERE l.run_id = $1 AND l.leg_id = g.leg_id AND l.seq > coalesce(c.seq, 0)
        ORDER BY l.seq LIMIT $4
    ) t
    WHERE g.leg_id <> ALL($5::bigint[])
)
SELECT leg_id, seq, at, level, text, ord FROM after ORDER BY ord, leg_id, seq LIMIT $4`,
			runID, legIDs, seqs, limit, archivedIDs)
	} else {
		rows, err = q.Query(ctx, `
WITH after AS (
    SELECT l.leg_id, l.seq, l.at, l.level, l.text,
           max(l.at) OVER (PARTITION BY l.leg_id ORDER BY l.seq) AS ord
    FROM log_lines l
    LEFT JOIN unnest($2::bigint[], $3::bigint[]) AS c(leg_id, seq) ON c.leg_id = l.leg_id
    WHERE l.run_id = $1 AND l.seq > coalesce(c.seq, 0) AND l.leg_id <> ALL($9::bigint[])
)
SELECT a.leg_id, a.seq, a.at, a.level, a.text, a.ord
FROM after a
LEFT JOIN legs g ON g.id = a.leg_id
WHERE ($4::bigint IS NULL OR g.step_id = $4)
  AND ($5 = '' OR g.service = $5)
  AND ($6 = '' OR a.level = $6)
  AND ($7 = '' OR strpos(lower(a.text), lower($7)) > 0)
ORDER BY a.ord, a.leg_id, a.seq
LIMIT $8`,
			runID, legIDs, seqs, f.StepID, f.Service, f.Level, f.Query, limit, archivedIDs)
	}
	if err != nil {
		return nil, fmt.Errorf("read log lines: %w", err)
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		c := candidate{Line: Line{RunID: runID}}
		if err := rows.Scan(&c.LegID, &c.Seq, &c.At, &c.Level, &c.Text, &c.order); err != nil {
			return nil, fmt.Errorf("scan log line: %w", err)
		}
		c.At = c.At.UTC()
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read log lines: %w", err)
	}
	return out, nil
}

// gaps reports the missing sequence ranges, per leg, between the cursor and
// the last line returned. Gaps are computed on all rows of the leg, ignoring
// the filter, so a filter neither hides nor invents one.
func (r *Reader) gaps(ctx context.Context, q querier, runID int64, cursor, covered map[int64]int64, archived map[int64]archiveRef, archiveGaps map[int64][]Gap) ([]Gap, error) {
	var legIDs, lows, highs []int64
	var out []Gap
	for leg, high := range covered {
		if _, isArchived := archived[leg]; isArchived {
			for _, g := range archiveGaps[leg] {
				if g.ToSeq <= high {
					out = append(out, g)
				}
			}
			continue
		}
		legIDs, lows, highs = append(legIDs, leg), append(lows, cursor[leg]), append(highs, high)
	}
	if len(legIDs) > 0 {
		rows, err := q.Query(ctx, `
SELECT leg_id, prev + 1, seq - 1 FROM (
    SELECT l.leg_id, l.seq, lag(l.seq, 1, c.lo) OVER (PARTITION BY l.leg_id ORDER BY l.seq) AS prev
    FROM log_lines l
    JOIN unnest($2::bigint[], $3::bigint[], $4::bigint[]) AS c(leg_id, lo, hi) ON c.leg_id = l.leg_id
    WHERE l.run_id = $1 AND l.seq > c.lo AND l.seq <= c.hi
) t WHERE seq - prev > 1`, runID, legIDs, lows, highs)
		if err != nil {
			return nil, fmt.Errorf("find log gaps: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var g Gap
			if err := rows.Scan(&g.LegID, &g.FromSeq, &g.ToSeq); err != nil {
				return nil, fmt.Errorf("scan log gap: %w", err)
			}
			out = append(out, g)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("find log gaps: %w", err)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LegID != out[j].LegID {
			return out[i].LegID < out[j].LegID
		}
		return out[i].FromSeq < out[j].FromSeq
	})
	return out, nil
}

func cursorArrays(cursor map[int64]int64) (legs, seqs []int64) {
	legs, seqs = make([]int64, 0, len(cursor)), make([]int64, 0, len(cursor))
	for leg, seq := range cursor {
		legs, seqs = append(legs, leg), append(seqs, seq)
	}
	return legs, seqs
}
