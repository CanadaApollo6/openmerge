package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// QueueID identifies a durable queue.
type QueueID int64

// QueueKey identifies one installation/repository/base/lane partition. An empty
// Lane selects the default serial lane. All other fields must be nonempty.
type QueueKey struct {
	Installation, Repository, BaseBranch, Lane string
}

// Store is safe for concurrent workers. Close it when all workers have stopped.
type Store struct {
	pool *pgxpool.Pool
	// Tests synchronize contenders immediately before the blocking lock call.
	beforeQueueLock func()
}

// Open connects to PostgreSQL and applies embedded forward-only migrations.
// Exec mode avoids session-local prepared statements behind transaction poolers.
func Open(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect database: %w", err)
	}
	s := &Store{pool: pool}
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return s, nil
}

// Close releases the connection pool.
func (s *Store) Close() { s.pool.Close() }

// EnsureQueue returns the same ID for every occurrence of this queue key.
func (s *Store) EnsureQueue(ctx context.Context, key QueueKey) (QueueID, error) {
	if key.Installation == "" || key.Repository == "" || key.BaseBranch == "" {
		return 0, errors.New("installation, repository and base branch are required")
	}
	var id QueueID
	err := s.pool.QueryRow(ctx, `INSERT INTO queues(installation, repository, base, lane) VALUES ($1, $2, $3, $4)
		ON CONFLICT (installation, repository, base, lane) DO UPDATE SET lane = EXCLUDED.lane RETURNING id`,
		key.Installation, key.Repository, key.BaseBranch, key.Lane).Scan(&id)
	return id, err
}

// InitializeFence persists the forge's initial fence before admitting any events.
// Repeating the same token is harmless; changing it or initializing after the log
// has started is rejected. Subsequent fence changes come only from queue events.
// The queue package has no bootstrap event, so this immutable seed accompanies
// the base branch in the queue row, rather than manufacturing a landing result.
func (s *Store) InitializeFence(ctx context.Context, q QueueID, token string) error {
	if token == "" {
		return errors.New("initial fence must not be empty")
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.lockQueue(ctx, tx, q); err != nil {
			return err
		}
		result, err := tx.Exec(ctx, `UPDATE queues SET initial_fence = $2 WHERE id = $1 AND
			(initial_fence = $2 OR (initial_fence IS NULL AND NOT EXISTS (SELECT FROM events WHERE queue_id = $1)))`, q, token)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return errors.New("queue missing, already initialized with another fence, or event log already started")
		}
		return nil
	})
}

func (s *Store) lockQueue(ctx context.Context, tx pgx.Tx, q QueueID) error {
	if s.beforeQueueLock != nil {
		s.beforeQueueLock()
	}
	return lockQueue(ctx, tx, q)
}

func lockQueue(ctx context.Context, tx pgx.Tx, q QueueID) error {
	// A dedicated two-int namespace keeps queue hashes separate from migrations.
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1330464081, hashtext($1))`, fmt.Sprint(q))
	return err
}

// Append commits a delivery's events together, in order. Only the final event
// carries the delivery key, so deduplication returns the original final watermark.
// An empty key disables deduplication. Empty deliveries are rejected.
func (s *Store) Append(ctx context.Context, q QueueID, events []queue.Event, dedupeKey string) (int64, error) {
	return s.append(ctx, q, events, dedupeKey, false)
}

// ErrLandingStarted rejects a superseded executor before it calls the forge.
var ErrLandingStarted = errors.New("landing has already started or its batch is no longer testing")

func (s *Store) append(ctx context.Context, q QueueID, events []queue.Event, dedupeKey string, executorEmit bool) (int64, error) {
	if len(events) == 0 {
		return 0, errors.New("cannot append an empty delivery")
	}
	var watermark int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.lockQueue(ctx, tx, q); err != nil {
			return err
		}
		if dedupeKey != "" {
			err := tx.QueryRow(ctx, `SELECT id FROM events WHERE queue_id = $1 AND dedupe_key = $2`, q, dedupeKey).Scan(&watermark)
			if err == nil {
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		for i, event := range events {
			if start, ok := event.(queue.LandingStarted); ok && executorEmit {
				state, _, err := load(ctx, tx, q)
				if err != nil {
					return err
				}
				if state.Active == nil || state.Active.ID != start.BatchID || state.Active.Phase != queue.Testing {
					return ErrLandingStarted
				}
				// A lease can expire between BeginConsequential and this emit.
				// Do not let a paused Land start after a replacement cancelled
				// it or a conflicting action acquired its execution window.
				landKey := "batch/" + start.BatchID + "/land"
				var authorized bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM actions WHERE queue_id = $1 AND key = $2
					AND status = 'in_flight' AND lease_until > $3) AND NOT EXISTS
					(SELECT FROM actions WHERE queue_id = $1 AND key <> $2 AND status = 'in_flight' AND lease_until > $3)`, q, landKey, start.At).Scan(&authorized); err != nil {
					return err
				}
				if !authorized {
					return ErrClaimLost
				}
			}
			name, payload, err := queue.EncodeEvent(event)
			if err != nil {
				return err
			}
			var key *string
			if dedupeKey != "" && i == len(events)-1 {
				key = &dedupeKey
			}
			if err := tx.QueryRow(ctx, `INSERT INTO events(queue_id, type, payload, dedupe_key)
				VALUES ($1, $2, $3::jsonb, $4) RETURNING id`, q, name, string(payload), key).Scan(&watermark); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return watermark, nil
}

// Emit returns the executor callback. A nil error means the event has committed.
// Use a worker context whose lifetime covers execution and result persistence.
func (s *Store) Emit(ctx context.Context, q QueueID) func(queue.Event) error {
	return func(e queue.Event) error {
		_, err := s.append(ctx, q, []queue.Event{e}, "", true)
		return err
	}
}

// Load derives state from the immutable bootstrap and every committed event.
func (s *Store) Load(ctx context.Context, q QueueID) (queue.State, int64, error) {
	var state queue.State
	var watermark int64
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		state, watermark, err = load(ctx, tx, q)
		return err
	})
	return state, watermark, err
}

func load(ctx context.Context, tx pgx.Tx, q QueueID) (queue.State, int64, error) {
	var state queue.State
	if err := tx.QueryRow(ctx, `SELECT base, COALESCE(initial_fence, '') FROM queues WHERE id = $1`, q).Scan(&state.BaseBranch, &state.Fence); err != nil {
		return state, 0, err
	}
	rows, err := tx.Query(ctx, `SELECT id, payload FROM events
		WHERE queue_id = $1 ORDER BY id`, q)
	if err != nil {
		return state, 0, err
	}
	defer rows.Close()
	var watermark int64
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&watermark, &payload); err != nil {
			return state, 0, err
		}
		event, err := queue.DecodeEvent(payload)
		if err != nil {
			return state, 0, fmt.Errorf("event %d: %w", watermark, err)
		}
		state = queue.Apply(state, event)
	}
	return state, watermark, rows.Err()
}

// PlannedAction pins an intended effect to its historical event watermark.
type PlannedAction struct {
	QueueID   QueueID
	Key       string
	Action    queue.Action
	Watermark int64
}

// Plan serializes planning and writes the pure planner's actions atomically.
// Cancelled keys are reactivated with current inputs; done and poisoned keys stay
// terminal. It returns unclaimed work, quarantining undecodable rows for operators.
func (s *Store) Plan(ctx context.Context, q QueueID, cfg queue.Config, now time.Time) ([]PlannedAction, error) {
	var result []PlannedAction
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.lockQueue(ctx, tx, q); err != nil {
			return err
		}
		state, watermark, err := load(ctx, tx, q)
		if err != nil {
			return err
		}
		for _, action := range queue.Plan(cfg, state, now) {
			name, payload, err := queue.EncodeAction(action)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO actions(queue_id, key, type, payload, watermark)
				VALUES ($1, $2, $3, $4::jsonb, $5) ON CONFLICT (queue_id, key) DO UPDATE
				SET status = 'planned', type = EXCLUDED.type, payload = EXCLUDED.payload,
					watermark = EXCLUDED.watermark, error = NULL, updated_at = now()
				WHERE actions.status = 'cancelled'`, q, action.Key(), name, string(payload), watermark); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, `SELECT id, queue_id, key, payload, watermark FROM actions WHERE queue_id = $1 AND status = 'planned' ORDER BY id FOR UPDATE`, q)
		if err != nil {
			return err
		}
		records, err := pgx.CollectRows(rows, scanAction)
		if err != nil {
			return err
		}
		for _, record := range records {
			action, err := restoreAction(record)
			if err != nil {
				if err := poisonAction(ctx, tx, record.id, err); err != nil {
					return err
				}
				continue
			}
			result = append(result, action)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

type actionRecord struct {
	id        int64
	queue     QueueID
	key       string
	payload   []byte
	watermark int64
}

func scanAction(row pgx.CollectableRow) (actionRecord, error) {
	var a actionRecord
	err := row.Scan(&a.id, &a.queue, &a.key, &a.payload, &a.watermark)
	return a, err
}

func restoreAction(record actionRecord) (PlannedAction, error) {
	action, err := queue.DecodeAction(record.payload)
	return PlannedAction{QueueID: record.queue, Key: record.key, Action: action, Watermark: record.watermark}, err
}

func poisonAction(ctx context.Context, tx pgx.Tx, id int64, cause error) error {
	_, err := tx.Exec(ctx, `UPDATE actions SET status = 'poisoned', error = $2,
		attempt_id = NULL, lease_until = NULL, updated_at = now() WHERE id = $1`, id, cause.Error())
	return err
}

// PoisonedAction retains the row identity, original payload and decoding error
// so operators can diagnose and repair quarantined work without losing evidence.
type PoisonedAction struct {
	ID        int64
	QueueID   QueueID
	Key       string
	Watermark int64
	Payload   []byte
	Error     string
}

// Poisoned lists quarantined actions in insertion order, across all queues.
func (s *Store) Poisoned(ctx context.Context) ([]PoisonedAction, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, queue_id, key, watermark, payload, error
		FROM actions WHERE status = 'poisoned' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (PoisonedAction, error) {
		var a PoisonedAction
		err := row.Scan(&a.ID, &a.QueueID, &a.Key, &a.Watermark, &a.Payload, &a.Error)
		return a, err
	})
}
