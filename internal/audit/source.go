package audit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const queueIdentitySQL = `SELECT q.id,q.base,q.lane,COALESCE(e.id,0) FROM queues q LEFT JOIN LATERAL (SELECT id FROM events WHERE queue_id=q.id ORDER BY id DESC LIMIT 1) e ON true WHERE q.installation=$1 AND q.repository=$2`

const sourcePageSQL = `WITH candidate AS MATERIALIZED (SELECT id,type,recorded_at,payload::text AS payload FROM events WHERE queue_id=$1 AND id>$2 AND id<=$3 AND ($4::timestamptz IS NULL OR recorded_at >= $4) AND ($5::timestamptz IS NULL OR recorded_at < $5) ORDER BY id LIMIT 32), sized AS (SELECT id,type,recorded_at,payload,sum(octet_length(payload)) OVER (ORDER BY id) AS page_bytes FROM candidate), bounded AS (SELECT id,type,recorded_at,payload,page_bytes,lag(page_bytes,1,0::bigint) OVER (ORDER BY id) AS prior_bytes FROM sized) SELECT id,type,recorded_at,CASE WHEN octet_length(payload)<=4194304 THEN payload ELSE NULL END FROM bounded WHERE page_bytes<=4194304 OR prior_bytes<=4194304 ORDER BY id`

const replayPageSQL = `WITH candidate AS MATERIALIZED (SELECT e.id,q.installation,q.repository,q.base,q.lane,e.type,e.recorded_at,e.payload::text AS payload FROM events e JOIN queues q ON q.id=e.queue_id WHERE e.queue_id=$1 AND e.id=ANY($2::bigint[]) ORDER BY e.id LIMIT 32), sized AS (SELECT id,installation,repository,base,lane,type,recorded_at,payload,sum(octet_length(payload)) OVER (ORDER BY id) AS page_bytes FROM candidate), bounded AS (SELECT id,installation,repository,base,lane,type,recorded_at,payload,page_bytes,lag(page_bytes,1,0::bigint) OVER (ORDER BY id) AS prior_bytes FROM sized) SELECT id,installation,repository,base,lane,type,recorded_at,CASE WHEN octet_length(payload)<=4194304 THEN payload ELSE NULL END FROM bounded WHERE page_bytes<=4194304 OR prior_bytes<=4194304 ORDER BY id`

const pageSize = 32
const zeroDigest = "0000000000000000000000000000000000000000000000000000000000000000"

// ErrSelectors identifies missing tenancy or invalid request bounds.
var ErrSelectors = errors.New("audit: invalid selectors or bounds")

// ErrTimeout identifies an expired snapshot without exposing connection details.
var ErrTimeout = errors.New("audit: export timed out")

var errSource = errors.New("audit: source snapshot unavailable or invalid")
var errIntegrity = errors.New("audit: export integrity check failed")

// Request selects one unambiguous installation and exact stored repository name.
// Empty Installation discovers tenancy and fails if more than one is present.
type Request struct {
	Installation string `json:"installation"`
	Repository   string `json:"repository"`
	From         string `json:"from"`
	Until        string `json:"until"`
}

// Key order is lexicographic UTF-8 bytes of installation, repository, base, lane.
// Database IDs never participate in the queue-key comparator.
type Key struct {
	Installation string `json:"installation"`
	Repository   string `json:"repository"`
	Base         string `json:"base"`
	Lane         string `json:"lane"`
}

// Source identifies immutable database metadata independently of the payload.
type Source struct {
	QueueID    int64  `json:"queue_id"`
	ID         int64  `json:"id"`
	Type       string `json:"type"`
	RecordedAt string `json:"recorded_at"`
	Queue      Key    `json:"queue"`
}

// Selection is independently hashed immutable metadata plus a canonical payload hash.
// Typed evidence metadata is part of frozen serialization v1.
type Selection struct {
	Source         Source      `json:"source"`
	PayloadSHA256  string      `json:"payload_sha256"`
	MetadataSHA256 string      `json:"metadata_sha256"`
	Role           string      `json:"role"`
	References     []Reference `json:"references"`
	Kinds          []string    `json:"evidence_kinds"`
}

type envelope struct {
	Kind           string          `json:"kind"`
	Source         Source          `json:"source"`
	Payload        json.RawMessage `json:"payload"`
	Role           string          `json:"role"`
	References     []Reference     `json:"references"`
	Kinds          []string        `json:"evidence_kinds"`
	PreviousSHA256 string          `json:"previous_sha256"`
}

type watermark struct {
	QueueID int64 `json:"queue_id"`
	Queue   Key   `json:"queue"`
	ID      int64 `json:"id"`
}

type sourceEvent struct {
	source  Source
	payload []byte
}

// Reader owns only a read-only pool. Opening it does not migrate or acquire locks.
type Reader struct {
	// ScratchLimits must be set before use; zero selects bounded defaults.
	ScratchLimits ScratchLimits
	pool          *pgxpool.Pool
	timeout       time.Duration
}

// Open constructs a pool without migrations, locks, or write queries.
func Open(ctx context.Context, dsn string, timeout time.Duration) (*Reader, error) {
	if timeout < time.Millisecond {
		return nil, errSource
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errSource
	}
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.MaxConns = 2
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errSource
	}
	return &Reader{pool: p, timeout: timeout}, nil
}

// Close releases database connections.
func (r *Reader) Close() { r.pool.Close() }

func (r *Reader) snapshot(ctx context.Context, fn func(context.Context, pgx.Tx) error) (result error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	defer func() {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result = ErrTimeout
		}
	}()
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return errSource
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	budget, err := newScratchBudget(r.ScratchLimits)
	if err != nil {
		return err
	}
	ctx = context.WithValue(ctx, scratchBudgetKey{}, budget)
	ctx = context.WithValue(ctx, pinCursorKey{}, &pinCursor{})
	if err = fn(ctx, tx); err != nil {
		return err
	}
	if tx.Commit(ctx) != nil {
		return errSource
	}
	return nil
}

func parseRange(req Request) (*time.Time, *time.Time, error) {
	if req.Repository == "" {
		return nil, nil, errSource
	}
	var from, until *time.Time
	for _, item := range []struct {
		s string
		p **time.Time
	}{{req.From, &from}, {req.Until, &until}} {
		if item.s != "" {
			t, e := time.Parse(time.RFC3339Nano, item.s)
			if e != nil || t.UTC().Year() < 1 || t.UTC().Year() > 9999 {
				return nil, nil, ErrSelectors
			}
			*item.p = &t
		}
	}
	// Equal bounds are a valid empty half-open interval. This matters after
	// ceiling sub-microsecond request bounds to PostgreSQL's microsecond
	// timestamp precision: a non-empty source interval may normalize to [t,t).
	if from != nil && until != nil && from.After(*until) {
		return nil, nil, errSource
	}
	// DB timestamps are discrete microseconds. Ceil both bounds so pgx's
	// microsecond encoding preserves the exact half-open comparison, including
	// a valid interval lying entirely between two DB timestamp values.
	for _, bound := range []*time.Time{from, until} {
		if bound != nil {
			floor := bound.Truncate(time.Microsecond)
			if !floor.Equal(*bound) {
				floor = floor.Add(time.Microsecond)
			}
			if floor.UTC().Year() > 9999 {
				return nil, nil, ErrSelectors
			}
			*bound = floor
		}
	}
	return from, until, nil
}

func resolve(ctx context.Context, tx pgx.Tx, req Request) (Request, error) {
	from, until, err := parseRange(req)
	if err != nil {
		return req, err
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT installation COLLATE "C" FROM queues WHERE repository=$1 AND ($2='' OR installation=$2) ORDER BY installation COLLATE "C" LIMIT 2`, req.Repository, req.Installation)
	if err != nil {
		return req, errSource
	}
	defer rows.Close()
	var found []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) != nil {
			return req, errSource
		}
		found = append(found, s)
	}
	if rows.Err() != nil {
		return req, errSource
	}
	if len(found) != 1 {
		return req, ErrSelectors
	}
	req.Installation = found[0]
	if from != nil {
		req.From = stamp(*from)
	}
	if until != nil {
		req.Until = stamp(*until)
	}
	return req, nil
}

// queues takes one indexed inventory snapshot and sorts it on disk using the
// same bounded merge as sources. The inventory spool stays in private staging.
func queues(ctx context.Context, tx pgx.Tx, req Request, dir string, visit func(watermark) error) error {
	input := filepath.Join(dir, "queue-input")
	sorted := filepath.Join(dir, "queue-sorted")
	e := spoolFile(input, func(out io.Writer) error {
		rows, e := tx.Query(ctx, queueIdentitySQL, req.Installation, req.Repository)
		if e != nil {
			return errSource
		}
		defer rows.Close()
		for rows.Next() {
			w := watermark{Queue: Key{Installation: req.Installation, Repository: req.Repository}}
			if rows.Scan(&w.QueueID, &w.Queue.Base, &w.Queue.Lane, &w.ID) != nil {
				return errSource
			}
			if e = spoolWrite(out, spoolRecord{Source: Source{QueueID: w.QueueID, ID: w.ID, Queue: w.Queue}, Payload: []byte(`{}`)}); e != nil {
				return e
			}
		}
		if rows.Err() != nil {
			return errSource
		}
		return nil
	})
	if e != nil {
		return e
	}
	if e = sortSpool(dir, input, sorted); e != nil {
		return e
	}
	defer func() { _ = os.Remove(input) }()
	defer func() { _ = os.Remove(sorted) }()
	c, e := openSpool(sorted)
	if e != nil {
		return e
	}
	defer func() { _ = c.f.Close() }()
	for c.ok {
		s := c.value.Source
		if e = visit(watermark{QueueID: s.QueueID, Queue: s.Queue, ID: s.ID}); e != nil {
			return e
		}
		if e = c.next(); e != nil {
			return e
		}
	}
	return nil
}

// keyLess compares identity components as UTF-8 bytes. Go string comparison is
// deliberately used instead of a database collation, so the order is stable
// across database locale/ICU settings.
func keyLess(a, b Key) bool {
	for _, p := range [][2]string{{a.Installation, b.Installation}, {a.Repository, b.Repository}, {a.Base, b.Base}, {a.Lane, b.Lane}} {
		if p[0] != p[1] {
			return p[0] < p[1]
		}
	}
	return false
}

func scan(ctx context.Context, tx pgx.Tx, w watermark, req Request, visit func(sourceEvent) error) error {
	from, until, err := parseRange(req)
	if err != nil {
		return err
	}
	var after int64
	for {
		rows, err := tx.Query(ctx, sourcePageSQL, w.QueueID, after, w.ID, from, until)
		if err != nil {
			return errSource
		}
		// Consume the cursor before callbacks issue any queries on this transaction.
		page := make([]sourceEvent, 0, pageSize)
		pageBytes := 0
		split := false
		for rows.Next() {
			e := sourceEvent{source: Source{QueueID: w.QueueID, Queue: w.Queue}}
			var at time.Time
			if rows.Scan(&e.source.ID, &e.source.Type, &at, &e.payload) != nil {
				rows.Close()
				return errSource
			}
			e.source.RecordedAt = stamp(at)
			if len(e.payload) == 0 || len(e.payload) > MaxBytes {
				rows.Close()
				return errCanonical
			}
			if len(page) > 0 && pageBytes+len(e.payload) > MaxBytes {
				split = true
				break
			}
			page = append(page, e)
			pageBytes += len(e.payload)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return errSource
		}
		for _, e := range page {
			if err = visit(e); err != nil {
				return err
			}
			after = e.source.ID
		}
		if len(page) == 0 || (!split && len(page) < pageSize) {
			return nil
		}
	}
}
func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000Z") }

// fetchPage retrieves only the selected IDs; visibility gaps never expand selection.
func fetchPage(ctx context.Context, tx pgx.Tx, selected []Selection, emit func(sourceEvent, *Selection) error) error {
	if len(selected) == 0 {
		return nil
	}
	queueID := selected[0].Source.QueueID
	for _, s := range selected {
		if s.Source.QueueID != queueID {
			return errIntegrity
		}
	}
	remaining := selected
	for len(remaining) > 0 {
		ids := make([]int64, len(remaining))
		for i, s := range remaining {
			ids[i] = s.Source.ID
		}
		rows, err := tx.Query(ctx, replayPageSQL, queueID, ids)
		if err != nil {
			return errSource
		}
		events := make([]sourceEvent, 0, pageSize)
		pageBytes := 0
		split := false
		for rows.Next() {
			e := sourceEvent{source: Source{QueueID: queueID}}
			var at time.Time
			if rows.Scan(&e.source.ID, &e.source.Queue.Installation, &e.source.Queue.Repository, &e.source.Queue.Base, &e.source.Queue.Lane, &e.source.Type, &at, &e.payload) != nil {
				rows.Close()
				return errSource
			}
			if len(events) >= len(remaining) || e.source.ID != remaining[len(events)].Source.ID {
				rows.Close()
				return errIntegrity
			}
			e.source.RecordedAt = stamp(at)
			if len(e.payload) == 0 || len(e.payload) > MaxBytes {
				rows.Close()
				return errCanonical
			}
			if len(events) >= pageSize || len(events) > 0 && pageBytes+len(e.payload) > MaxBytes {
				split = true
				break
			}
			events = append(events, e)
			pageBytes += len(e.payload)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return errSource
		}
		if !split && len(events) == pageSize && len(events) < len(remaining) {
			split = true
		}
		if len(events) == 0 || (!split && len(events) != len(remaining)) {
			return errIntegrity
		}
		for i, e := range events {
			if err = emit(e, &remaining[i]); err != nil {
				return err
			}
		}
		remaining = remaining[len(events):]
	}
	return nil
}
