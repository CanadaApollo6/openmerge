package audit

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const sourcePageSQL = `SELECT id,type,recorded_at,CASE WHEN octet_length(payload::text)<=4194304 THEN payload ELSE NULL END FROM events WHERE queue_id=$1 AND id>$2 AND id<=$3 AND ($4::timestamptz IS NULL OR recorded_at >= $4) AND ($5::timestamptz IS NULL OR recorded_at < $5) ORDER BY id LIMIT 32`

const pageSize = 32
const zeroDigest = "0000000000000000000000000000000000000000000000000000000000000000"

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
// Role and References are reserved now so 10a-2 can express closure without a v1 change.
type Selection struct {
	Source         Source   `json:"source"`
	PayloadSHA256  string   `json:"payload_sha256"`
	MetadataSHA256 string   `json:"metadata_sha256"`
	Role           string   `json:"role"`
	References     []Source `json:"references"`
}

type envelope struct {
	Kind           string          `json:"kind"`
	Source         Source          `json:"source"`
	Payload        json.RawMessage `json:"payload"`
	Role           string          `json:"role"`
	References     []Source        `json:"references"`
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
	pool    *pgxpool.Pool
	timeout time.Duration
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

func (r *Reader) snapshot(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return errSource
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
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
			if e != nil {
				return nil, nil, errSource
			}
			*item.p = &t
		}
	}
	if from != nil && until != nil && !from.Before(*until) {
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
			*bound = floor
		}
	}
	return from, until, nil
}

func resolve(ctx context.Context, tx pgx.Tx, req Request) (Request, error) {
	if _, _, err := parseRange(req); err != nil {
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
	if rows.Err() != nil || len(found) != 1 {
		return req, errSource
	}
	req.Installation = found[0]
	return req, nil
}

// queues uses the existing identity unique index; no canonical/lifecycle table is
// consulted, so drained/retired queues and all retained source kinds remain visible.
// Keyset pages bound queue discovery as well as event reads.
func queues(ctx context.Context, tx pgx.Tx, req Request, visit func(watermark) error) error {
	var base, lane string
	first := true
	for {
		rows, err := tx.Query(ctx, `SELECT id,base,lane FROM queues WHERE installation=$1 AND repository=$2 AND ($3 OR (base COLLATE "C",lane COLLATE "C")>($4 COLLATE "C",$5 COLLATE "C")) ORDER BY base COLLATE "C",lane COLLATE "C" LIMIT 32`, req.Installation, req.Repository, first, base, lane)
		if err != nil {
			return errSource
		}
		var page []watermark
		for rows.Next() {
			w := watermark{Queue: Key{Installation: req.Installation, Repository: req.Repository}}
			if rows.Scan(&w.QueueID, &w.Queue.Base, &w.Queue.Lane) != nil {
				rows.Close()
				return errSource
			}
			page = append(page, w)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return errSource
		}
		for _, w := range page {
			if tx.QueryRow(ctx, `SELECT COALESCE(max(id),0) FROM events WHERE queue_id=$1`, w.QueueID).Scan(&w.ID) != nil {
				return errSource
			}
			if err = visit(w); err != nil {
				return err
			}
			base, lane = w.Queue.Base, w.Queue.Lane
			first = false
		}
		if len(page) < pageSize {
			return nil
		}
	}
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
		var page []sourceEvent
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
			page = append(page, e)
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
		if len(page) < pageSize {
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
	ids := make([]int64, len(selected))
	for i, s := range selected {
		ids[i] = s.Source.ID
	}
	rows, err := tx.Query(ctx, `SELECT e.id,q.installation,q.repository,q.base,q.lane,e.type,e.recorded_at,CASE WHEN octet_length(e.payload::text)<=4194304 THEN e.payload ELSE NULL END FROM events e JOIN queues q ON q.id=e.queue_id WHERE e.queue_id=$1 AND e.id=ANY($2::bigint[]) ORDER BY e.id`, selected[0].Source.QueueID, ids)
	if err != nil {
		return errSource
	}
	var events []sourceEvent
	for rows.Next() {
		e := sourceEvent{source: Source{QueueID: selected[0].Source.QueueID}}
		var at time.Time
		if rows.Scan(&e.source.ID, &e.source.Queue.Installation, &e.source.Queue.Repository, &e.source.Queue.Base, &e.source.Queue.Lane, &e.source.Type, &at, &e.payload) != nil {
			rows.Close()
			return errSource
		}
		e.source.RecordedAt = stamp(at)
		if len(e.payload) == 0 || len(e.payload) > MaxBytes {
			rows.Close()
			return errCanonical
		}
		events = append(events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(events) != len(selected) {
		return errIntegrity
	}
	for i, e := range events {
		if err = emit(e, &selected[i]); err != nil {
			return err
		}
	}
	return nil
}
