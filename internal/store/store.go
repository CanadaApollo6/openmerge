package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
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
	id, _, err := s.CanonicalQueue(ctx, key)
	return id, err
}

// CanonicalQueue atomically finds or registers a partition, preserving the first
// stored repository spelling, or a durably chosen historical variant. Conflicting
// histories must settle before admission. Every registration takes the same lock, so
// differently cased discoveries cannot split a queue's history or fence.
func (s *Store) CanonicalQueue(ctx context.Context, key QueueKey) (QueueID, QueueKey, error) {
	if key.Installation == "" || key.Repository == "" || key.BaseBranch == "" {
		return 0, key, errors.New("installation, repository and base branch are required")
	}
	var id QueueID
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Registration uses its own namespace, separate from queue event locks.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1330464082, hashtext($1 || '/' || lower($2) || '/' || $3 || '/' || $4))`, key.Installation, key.Repository, key.BaseBranch, key.Lane); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id, repository FROM queues WHERE installation=$1 AND lower(repository)=lower($2) AND base=$3 AND lane=$4 ORDER BY id`, key.Installation, key.Repository, key.BaseBranch, key.Lane)
		if err != nil {
			return err
		}
		type match struct {
			id         QueueID
			repository string
		}
		matches, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (match, error) {
			var m match
			err := row.Scan(&m.id, &m.repository)
			return m, err
		})
		if err != nil {
			return err
		}
		if len(matches) == 0 {
			if err := tx.QueryRow(ctx, `INSERT INTO queues(installation, repository, base, lane) VALUES ($1,$2,$3,$4) RETURNING id`, key.Installation, key.Repository, key.BaseBranch, key.Lane).Scan(&id); err != nil {
				return err
			}
			matches = append(matches, match{id: id, repository: key.Repository})
		}
		variantIDs := make([]int64, len(matches))
		for i, m := range matches {
			variantIDs[i] = int64(m.id)
		}
		var cachedID QueueID
		var cachedVariants []int64
		err = tx.QueryRow(ctx, `SELECT queue_id, variant_ids FROM canonical_queues WHERE installation=$1 AND repository=lower($2) AND base=$3 AND lane=$4`, key.Installation, key.Repository, key.BaseBranch, key.Lane).Scan(&cachedID, &cachedVariants)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil && slices.Equal(variantIDs, cachedVariants) {
			for _, m := range matches {
				if m.id == cachedID {
					id, key.Repository = m.id, m.repository
					return nil
				}
			}
			return errors.New("canonical queue is absent from its variant set")
		}
		chosen := matches[0]
		live := 0
		var latestFence int64
		for _, m := range matches {
			if len(matches) == 1 {
				break
			}
			state, watermark, err := load(ctx, tx, m.id)
			if err != nil {
				return err
			}
			if state.Active != nil && state.Active.Phase == queue.Landing {
				return fmt.Errorf("case-variant queue histories: landing settlement pending for queue %d", m.id)
			}
			protected := slices.ContainsFunc(state.Blocks(queue.Config{}), func(block queue.Block) bool {
				return block.Source != queue.RepositoryAccess && block.Source != queue.ConfigSource && block.Source != "lifecycle"
			})
			if len(state.Entries) > 0 || state.Active != nil || protected {
				live++
				chosen = m
			} else if live == 0 && state.Fence != "" && watermark >= latestFence {
				chosen, latestFence = m, watermark
			}
		}
		if live > 1 {
			return errors.New("conflicting case-variant queue histories have live state; operator reconciliation required (docs/operating.md#case-variant-history-recovery)")
		}
		id, key.Repository = chosen.id, chosen.repository
		_, err = tx.Exec(ctx, `INSERT INTO canonical_queues(installation,repository,base,lane,queue_id,variant_ids) VALUES ($1,lower($2),$3,$4,$5,$6)
			ON CONFLICT (installation,repository,base,lane) DO UPDATE SET queue_id=EXCLUDED.queue_id, variant_ids=EXCLUDED.variant_ids`, key.Installation, key.Repository, key.BaseBranch, key.Lane, id, variantIDs)
		return err
	})
	return id, key, err
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
	return s.append(ctx, q, events, dedupeKey, nil, nil)
}

// ErrStale rejects a start whose action is no longer in the current plan.
var ErrStale = errors.New("action is no longer authorized to start")

func (s *Store) append(ctx context.Context, q QueueID, events []queue.Event, dedupeKey string, execution *emission, observation *record) (int64, error) {
	if len(events) == 0 && observation == nil {
		return 0, errors.New("cannot append an empty delivery")
	}
	var watermark int64
	var stale bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.lockQueue(ctx, tx, q); err != nil {
			return err
		}
		if observation != nil {
			var previous []byte
			err := tx.QueryRow(ctx, `SELECT value FROM observations WHERE queue_id = $1 AND key = $2`, q, observation.key).Scan(&previous)
			if err == nil && string(previous) == string(observation.value) {
				return nil
			}
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO observations(queue_id, key, value) VALUES ($1, $2, $3)
				ON CONFLICT (queue_id, key) DO UPDATE SET value = EXCLUDED.value`, q, observation.key, observation.value); err != nil {
				return err
			}
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
			_, resetPoison := event.(queue.ResumeRequested)
			if cleared, ok := event.(queue.BlockSourceCleared); ok && cleared.Source == queue.ActionPoison && cleared.Validate() == nil {
				state, _, err := load(ctx, tx, q)
				if err != nil {
					return err
				}
				resetPoison = cleared.Basis == state.BlockSourceRevision(queue.ActionPoison)
			}
			if resetPoison {
				if _, err := tx.Exec(ctx, `UPDATE actions SET poison_count = 0, poison_notified = false
					WHERE queue_id = $1 AND poison_count > 0`, q); err != nil {
					return err
				}
			}
			if execution != nil {
				var err error
				stale, err = s.authorizeStart(ctx, tx, *execution, event)
				if err != nil || stale {
					return err
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
	if stale {
		return 0, ErrStale
	}
	return watermark, nil
}

type emission struct {
	claim ClaimedAction
	cfg   queue.Config
}

// Emit returns the executor callback bound to this claim and planner config.
// A nil error means the event has committed. ErrStale means a start was rejected
// and its action cancelled; the executor must return before any forge call.
// Use a worker context whose lifetime covers execution and result persistence.
func (s *Store) Emit(ctx context.Context, claim ClaimedAction, cfg queue.Config) func(queue.Event) error {
	return func(e queue.Event) error {
		_, err := s.append(ctx, claim.QueueID, []queue.Event{e}, "", &emission{claim, cfg}, nil)
		return err
	}
}

// The caller holds the queue lock through validation, cancellation and append.
func (s *Store) authorizeStart(ctx context.Context, tx pgx.Tx, execution emission, event queue.Event) (bool, error) {
	var at time.Time
	switch start := event.(type) {
	case queue.BatchStarted:
		at = start.At
	case queue.LandingStarted:
		at = start.At
	default:
		return false, nil
	}
	claim := execution.claim
	var record actionRecord
	err := tx.QueryRow(ctx, `SELECT id, queue_id, key, payload, watermark FROM actions
		WHERE queue_id = $1 AND key = $2 AND attempt_id = $3
		AND status = 'in_flight' AND lease_until > $4 FOR UPDATE`, claim.QueueID, claim.Key, claim.AttemptID, at).
		Scan(&record.id, &record.queue, &record.key, &record.payload, &record.watermark)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrClaimLost
	}
	if err != nil {
		return false, err
	}
	stored, err := restoreAction(record)
	if err != nil {
		return false, err
	}
	// Malformed events or modified caller payloads must not cancel valid work.
	if !reflect.DeepEqual(claim.Action, stored.Action) {
		return false, ErrStale
	}
	switch start := event.(type) {
	case queue.BatchStarted:
		build, ok := stored.Action.(queue.BuildTestRef)
		if !ok || build.BatchID != start.BatchID || build.BaseSHA != start.BaseSHA || !slices.Equal(build.PRs, start.PRs) {
			return false, ErrStale
		}
	case queue.LandingStarted:
		land, ok := stored.Action.(queue.Land)
		if !ok || land.BatchID != start.BatchID || land.FenceToken != start.FenceToken {
			return false, ErrStale
		}
	}
	state, _, err := load(ctx, tx, claim.QueueID)
	if err != nil {
		return false, err
	}
	if stored.Key == stored.Action.Key() && queue.CanStart(execution.cfg, state, stored.Action, event) {
		return false, nil
	}
	_, err = tx.Exec(ctx, `UPDATE actions SET status = 'cancelled', attempt_id = NULL,
		lease_until = NULL, updated_at = $2 WHERE id = $1`, record.id, at)
	return true, err
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
// Cancelled and poisoned keys are reactivated with current inputs. Three poison
// incidents pause the queue until an action-poisoned clear, except ResolveLanding always
// repairs without pausing. Done keys remain terminal.
// It returns unclaimed work, quarantining invalid rows for operators.
func (s *Store) Plan(ctx context.Context, q QueueID, cfg queue.Config, now time.Time) ([]PlannedAction, error) {
	var result []PlannedAction
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.lockQueue(ctx, tx, q); err != nil {
			return err
		}
		if _, err := publishPoisonPauses(ctx, tx, q); err != nil {
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
				WHERE actions.status = 'cancelled' OR (actions.status = 'poisoned'
					AND (actions.poison_count < 3 OR EXCLUDED.type IN ('resolve_landing', 'fast_forward_resolve_landing')))`, q, action.Key(), name, string(payload), watermark); err != nil {
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
		paused, err := publishPoisonPauses(ctx, tx, q)
		if paused {
			result = slices.DeleteFunc(result, func(action PlannedAction) bool {
				return !queue.IsLandingResolution(action.Action)
			})
		}
		return err
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
		poison_count = poison_count + 1, attempt_id = NULL, lease_until = NULL,
		updated_at = now() WHERE id = $1 AND status <> 'poisoned'`, id, cause.Error())
	return err
}

// The caller holds the queue lock. Notification and its event commit together;
// Plan also drains pending notifications if a claimer stopped after quarantine.
// ResolveLanding never blocks. Other poisoned work shares the action-poisoned source.
func publishPoisonPauses(ctx context.Context, tx pgx.Tx, q QueueID) (bool, error) {
	rows, err := tx.Query(ctx, `UPDATE actions SET poison_notified = true
		WHERE queue_id = $1 AND status = 'poisoned' AND poison_count >= 3 AND NOT poison_notified
			AND type NOT IN ('resolve_landing', 'fast_forward_resolve_landing')
		RETURNING key, error`, q)
	if err != nil {
		return false, err
	}
	events, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (queue.ActionPoisoned, error) {
		var event queue.ActionPoisoned
		err := row.Scan(&event.Key, &event.Reason)
		return event, err
	})
	if err != nil {
		return false, err
	}
	if len(events) == 0 {
		return false, nil
	}
	state, _, err := load(ctx, tx, q)
	if err != nil {
		return false, err
	}
	for i, poison := range events {
		name, payload, err := queue.EncodeEvent(queue.BlockSourceBlocked{
			Source: queue.ActionPoison, Subject: poison.Key,
			Detail: fmt.Sprintf("Queue paused after repeated invalid action %s: %s; refresh the queue to retry", poison.Key, poison.Reason),
			Basis:  state.BlockSourceRevision(queue.ActionPoison) + uint64(i),
		})
		if err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO events(queue_id, type, payload) VALUES ($1, $2, $3::jsonb)`, q, name, string(payload)); err != nil {
			return false, err
		}
	}
	return true, nil
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
	Incidents int
}

// Poisoned lists quarantined actions in insertion order, across all queues.
func (s *Store) Poisoned(ctx context.Context) ([]PoisonedAction, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, queue_id, key, watermark, payload, error, poison_count
		FROM actions WHERE status = 'poisoned' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (PoisonedAction, error) {
		var a PoisonedAction
		err := row.Scan(&a.ID, &a.QueueID, &a.Key, &a.Watermark, &a.Payload, &a.Error, &a.Incidents)
		return a, err
	})
}
