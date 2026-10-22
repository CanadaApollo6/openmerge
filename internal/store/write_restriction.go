package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// AuthorityQueueKey is the canonical authority partition identity. Authority
// v1 deliberately supports only the default lane.
func AuthorityQueueKey(k QueueKey) string {
	return k.Installation + "/" + k.Repository + "/" + k.BaseBranch
}

func validateAuthorityQueue(k QueueKey, b authority.PolicyBinding) error {
	if k.Lane != "" || k.Installation != strconv.FormatInt(b.InstallationID, 10) || k.Repository != b.Repository || AuthorityQueueKey(k) != b.QueueKey {
		return errors.New("authority queue identity mismatch")
	}
	return b.Validate()
}

// VerificationRecord binds one pre-read queue basis to an invocation record.
type VerificationRecord struct {
	QueueID QueueID
	Event   queue.WriteRestrictionVerified
}

// VerificationReceipt identifies the committed source event for one queue.
type VerificationReceipt struct {
	QueueID QueueID
	EventID int64
	Event   queue.WriteRestrictionVerified
}

// CommitWriteRestrictionVerification takes all queue locks in canonical queue-ID
// order and commits every record in one transaction. Network reads and nonce
// generation have already finished. The existing queue/dedupe index owns retries.
func (s *Store) CommitWriteRestrictionVerification(ctx context.Context, records []VerificationRecord, emission ...AuthorityEmissionGate) ([]VerificationReceipt, error) {
	if len(records) == 0 || len(records) > 1000 {
		return nil, errors.New("invalid verification target count")
	}
	records = slices.Clone(records)
	slices.SortFunc(records, func(a, b VerificationRecord) int {
		if a.QueueID < b.QueueID {
			return -1
		}
		if a.QueueID > b.QueueID {
			return 1
		}
		return 0
	})
	for i, r := range records {
		if err := r.Event.Validate(); err != nil {
			return nil, err
		}
		if i > 0 && records[i-1].QueueID == r.QueueID || r.Event.Nonce != records[0].Event.Nonce {
			return nil, errors.New("duplicate queue or mixed invocation nonces")
		}
	}
	var receipts []VerificationReceipt
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if len(emission) > 0 {
			if err := s.lockTenancy(ctx, tx, false); err != nil {
				return err
			}
		}
		for _, r := range records {
			if err := s.lockQueue(ctx, tx, r.QueueID); err != nil {
				return err
			}
		}
		for _, gate := range emission {
			if err := checkAuthorityEmission(ctx, tx, gate); err != nil {
				return err
			}
		}
		for _, r := range records {
			var k QueueKey
			if err := tx.QueryRow(ctx, `SELECT installation,repository,base,lane FROM queues WHERE id=$1`, r.QueueID).Scan(&k.Installation, &k.Repository, &k.BaseBranch, &k.Lane); err != nil {
				return err
			}
			if err := validateAuthorityQueue(k, r.Event.Binding); err != nil {
				return err
			}
			key := fmt.Sprintf("write-restriction/verify/%x", sha256.Sum256([]byte(r.Event.Scope+"\x00"+AuthorityQueueKey(k)+"\x00"+r.Event.Nonce)))
			name, raw, err := queue.EncodeEvent(r.Event)
			if err != nil {
				return err
			}
			var id int64
			var previous []byte
			err = tx.QueryRow(ctx, `SELECT id,payload FROM events WHERE queue_id=$1 AND dedupe_key=$2`, r.QueueID, key).Scan(&id, &previous)
			if err == nil {
				e, err := queue.DecodeEvent(previous)
				if err != nil {
					return err
				}
				_, canonicalPrevious, err := queue.EncodeEvent(e)
				if err != nil {
					return err
				}
				if !bytes.Equal(canonicalPrevious, raw) {
					return errors.New("verification nonce reused for a different read")
				}
			} else {
				if !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				state, _, err := load(ctx, tx, r.QueueID)
				if err != nil {
					return err
				}
				pin := state.AuthorityPin()
				if r.Event.Scope == "applied" {
					if pin == nil || pin.OpenMerge == nil || !reflect.DeepEqual(pin.OpenMerge.Binding, r.Event.Binding) || state.ConfigDigest != r.Event.Binding.EffectiveDigest {
						return errors.New("applied verification differs from pinned policy")
					}
				} else if pin != nil && pin.Authority == authority.OpenMerge {
					return errors.New("proposed verification requires a github queue")
				}
				if err := tx.QueryRow(ctx, `INSERT INTO events(queue_id,type,payload,dedupe_key) VALUES($1,$2,$3::jsonb,$4) RETURNING id`, r.QueueID, name, string(raw), key).Scan(&id); err != nil {
					return err
				}
			}
			receipts = append(receipts, VerificationReceipt{r.QueueID, id, r.Event})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return receipts, nil
}

// ProposedVerification reads a queue's ordered journal in bounded pages using
// events_queue_watermark. A caller-supplied payload cannot authenticate EventID.
func (s *Store) ProposedVerification(ctx context.Context, q QueueID, eventID int64) (queue.WriteRestrictionVerified, error) {
	return proposedVerification(ctx, s.pool, q, eventID)
}

type authorityJournalReader interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func proposedVerification(ctx context.Context, reader authorityJournalReader, q QueueID, eventID int64) (queue.WriteRestrictionVerified, error) {
	var result queue.WriteRestrictionVerified
	if eventID <= 0 {
		return result, errors.New("invalid proposed event ID")
	}
	var after int64
	for {
		rows, err := reader.Query(ctx, `SELECT id,payload FROM events WHERE queue_id=$1 AND id>$2 AND id<=$3 ORDER BY id LIMIT 256`, q, after, eventID)
		if err != nil {
			return result, err
		}
		page, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (JournalEvent, error) {
			var r JournalEvent
			err := row.Scan(&r.ID, &r.Payload)
			return r, err
		})
		if err != nil {
			return result, err
		}
		for _, r := range page {
			after = r.ID
			if r.ID != eventID {
				continue
			}
			e, err := queue.DecodeEvent(r.Payload)
			if err != nil {
				return result, err
			}
			v, ok := e.(queue.WriteRestrictionVerified)
			if !ok || v.Scope != "proposed" {
				return result, errors.New("event is not a proposed verification")
			}
			return v, nil
		}
		if len(page) < 256 {
			return result, errors.New("proposed verification not found in queue")
		}
	}
}

// AuthorityAdoptionOutcome distinguishes a durable draft adoption from a pin
// whose draft remains historical and requires a fresh applied verification.
type AuthorityAdoptionOutcome string

// Adoption outcomes describe the committed pin without implying a clean finding set.
const (
	AuthorityDraftAdopted     AuthorityAdoptionOutcome = "adopted"
	AuthorityPinnedUnverified AuthorityAdoptionOutcome = "pinned-unverified"
)

// AuthorityAdoptionReceipt identifies the committed pin and its adopted source.
// ProposedEventID is zero when no proposed verification was adopted.
type AuthorityAdoptionReceipt struct {
	QueueID         QueueID
	EventID         int64
	ProposedEventID int64
	Outcome         AuthorityAdoptionOutcome
}

// String is the operator-facing outcome, including the durable receipt and
// source IDs. Callers can print this after commit without implying activation.
func (r AuthorityAdoptionReceipt) String() string {
	return fmt.Sprintf("authority %s: queue_id=%d pin_event_id=%d proposed_event_id=%d", r.Outcome, r.QueueID, r.EventID, r.ProposedEventID)
}

func authorityAdoptionReceipt(q QueueID, id int64, pin queue.PolicyAuthorityApplied) AuthorityAdoptionReceipt {
	r := AuthorityAdoptionReceipt{QueueID: q, EventID: id, Outcome: AuthorityPinnedUnverified}
	if queue.AuthorityDraftAdopted(pin) {
		r.Outcome = AuthorityDraftAdopted
		r.ProposedEventID = pin.OpenMerge.Activation.Proposed.EventID
	}
	return r
}

// authorityConfigPair validates the atomic lifecycle boundary before it takes a
// queue lock. ConfigApplied is intentionally unchanged on the wire, so the
// equal governing commit and digest are the only durable association between
// its event and the authority pin that follows it.
func authorityConfigPair(config queue.ConfigApplied, pin queue.PolicyAuthorityApplied) error {
	if err := config.Validate(); err != nil {
		return err
	}
	if err := pin.Validate(); err != nil {
		return err
	}
	if config.Commit != pin.GoverningCommit || config.Digest != pin.EffectiveDigest {
		return errors.New("authority pin differs from applied config")
	}
	switch pin.Authority {
	case authority.OpenMerge:
		if pin.OpenMerge == nil || pin.OpenMerge.Activation != nil || pin.OpenMerge.VerificationInput != nil {
			return errors.New("expected an unadopted openmerge pin")
		}
	case authority.GitHub:
		// PolicyAuthorityApplied.Validate already rejects an OpenMerge actor on
		// this variant. A github pin is emitted only by the rollback rule below.
	default:
		return errors.New("invalid authority mode")
	}
	return nil
}

func latestAuthorityPin(ctx context.Context, tx pgx.Tx, q QueueID) (int64, queue.PolicyAuthorityApplied, bool, error) {
	var id int64
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT id,payload FROM events WHERE queue_id=$1 AND type='policy_authority_applied' ORDER BY id DESC LIMIT 1`, q).Scan(&id, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, queue.PolicyAuthorityApplied{}, false, nil
	}
	if err != nil {
		return 0, queue.PolicyAuthorityApplied{}, false, err
	}
	event, err := queue.DecodeEvent(raw)
	if err != nil {
		return 0, queue.PolicyAuthorityApplied{}, false, err
	}
	pin, ok := event.(queue.PolicyAuthorityApplied)
	if !ok {
		return 0, queue.PolicyAuthorityApplied{}, false, errors.New("invalid persisted authority pin")
	}
	return id, pin, true, nil
}

func latestConfigApplied(ctx context.Context, tx pgx.Tx, q QueueID) (queue.ConfigApplied, bool, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT payload FROM events WHERE queue_id=$1 AND type='config_applied' ORDER BY id DESC LIMIT 1`, q).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return queue.ConfigApplied{}, false, nil
	}
	if err != nil {
		return queue.ConfigApplied{}, false, err
	}
	event, err := queue.DecodeEvent(raw)
	if err != nil {
		return queue.ConfigApplied{}, false, err
	}
	config, ok := event.(queue.ConfigApplied)
	if !ok {
		return queue.ConfigApplied{}, false, errors.New("invalid persisted config")
	}
	return config, true, nil
}

// sameAuthorityPin ignores an existing interpreted activation. A successful
// retry must return that verified pin, rather than replacing it with the
// caller's necessarily unadopted input pin.
func sameAuthorityPin(previous, candidate queue.PolicyAuthorityApplied) bool {
	if previous.GoverningCommit != candidate.GoverningCommit || previous.EffectiveDigest != candidate.EffectiveDigest || previous.Authority != candidate.Authority || previous.DefaultBranch != candidate.DefaultBranch || previous.CoverageDigest != candidate.CoverageDigest {
		return false
	}
	switch candidate.Authority {
	case authority.GitHub:
		return previous.OpenMerge == nil
	case authority.OpenMerge:
		return previous.OpenMerge != nil && candidate.OpenMerge != nil && reflect.DeepEqual(previous.OpenMerge.Binding, candidate.OpenMerge.Binding)
	default:
		return false
	}
}

// authorityInputsMatch keeps retries and governing-commit rebinds bound to
// the interpretation inputs of the durable adoption, not just its policy digest.
func authorityInputsMatch(pin queue.PolicyAuthorityApplied, in forge.WriteRestrictionInput) bool {
	return pin.OpenMerge == nil || pin.OpenMerge.VerificationInput == nil || queue.AuthorityInputsMatch(pin.OpenMerge.VerificationInput, in)
}

// authorityDraftFloor returns the newest durable boundary after which a
// proposed verification must have been recorded before it can be adopted. A
// github pin is a rollback boundary. An OpenMerge pin that did not fold an
// authenticated activation is likewise unverified; accepting a prior proposal
// after either boundary would silently revive stale verification evidence.
func authorityDraftFloor(ctx context.Context, tx pgx.Tx, q QueueID) (int64, error) {
	rows, err := tx.Query(ctx, `SELECT id,payload FROM events WHERE queue_id=$1 AND type='policy_authority_applied' ORDER BY id DESC`, q)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return 0, err
		}
		event, err := queue.DecodeEvent(raw)
		if err != nil {
			return 0, err
		}
		pin, ok := event.(queue.PolicyAuthorityApplied)
		if !ok {
			return 0, errors.New("invalid persisted authority pin")
		}
		if pin.Authority == authority.GitHub || pin.Authority == authority.OpenMerge && !queue.AuthorityDraftAdopted(pin) {
			return id, nil
		}
	}
	return 0, rows.Err()
}

func freshAuthorityAdoption(state queue.State, basis uint64, readStarted, readAt time.Time) bool {
	return basis == state.RulesSourceRevision(queue.WriteRestriction) && !readStarted.IsZero() && !readAt.Before(readStarted) && time.Since(readAt) >= 0 && time.Since(readStarted) <= 2*time.Minute
}

// ApplyAuthorityConfig atomically records a configuration and its authority
// lifecycle pin. It is intentionally the only lifecycle writer that can create
// a github pin: a github pin is a rollback receipt for a folded OpenMerge pin,
// never an event for ordinary github configuration. For an OpenMerge pin, an
// authenticated proposed draft is adopted only after the usual fresh seeing
// read and source-basis checks; otherwise the committed pin is unverified.
//
// An optional historical ConfigApplied is the one-time fixed-window backfill
// for the currently folded legacy policy. It precedes the target config and
// pin in the same queue transaction. A matching retry returns the durable pin
// before looking up draft/history state. In particular it never erases a
// durable verified activation with the caller's unadopted pin. Config and pin
// are inserted in one transaction under the queue lock, so invalid source
// evidence and binding errors leave neither event behind.
func (s *Store) ApplyAuthorityConfig(ctx context.Context, q QueueID, config queue.ConfigApplied, pin queue.PolicyAuthorityApplied, draftID int64, basis uint64, readStarted, readAt time.Time, in forge.WriteRestrictionInput, fresh authority.Observation, historical ...queue.ConfigApplied) (AuthorityAdoptionReceipt, error) {
	return s.applyAuthorityConfig(ctx, q, config, pin, draftID, basis, readStarted, readAt, in, fresh, nil, historical...)
}

// ApplyAuthorityConfigWithEmissionGate protects an initial boundary in the same
// transaction as its config/pin pair. Rollback callers never supply this gate.
func (s *Store) ApplyAuthorityConfigWithEmissionGate(ctx context.Context, q QueueID, config queue.ConfigApplied, pin queue.PolicyAuthorityApplied, draftID int64, basis uint64, readStarted, readAt time.Time, in forge.WriteRestrictionInput, fresh authority.Observation, gate *AuthorityEmissionGate, historical ...queue.ConfigApplied) (AuthorityAdoptionReceipt, error) {
	return s.applyAuthorityConfig(ctx, q, config, pin, draftID, basis, readStarted, readAt, in, fresh, gate, historical...)
}

func (s *Store) applyAuthorityConfig(ctx context.Context, q QueueID, config queue.ConfigApplied, pin queue.PolicyAuthorityApplied, draftID int64, basis uint64, readStarted, readAt time.Time, in forge.WriteRestrictionInput, fresh authority.Observation, gate *AuthorityEmissionGate, historical ...queue.ConfigApplied) (AuthorityAdoptionReceipt, error) {
	var receipt AuthorityAdoptionReceipt
	if err := authorityConfigPair(config, pin); err != nil {
		return receipt, err
	}
	if len(historical) > 1 {
		return receipt, errors.New("at most one historical config backfill is allowed")
	}
	if len(historical) == 1 {
		if err := historical[0].Validate(); err != nil {
			return receipt, err
		}
		if historical[0].Window == nil || historical[0].Policy != nil {
			return receipt, errors.New("historical config backfill must contain only window bounds")
		}
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if gate != nil {
			if err := s.lockTenancy(ctx, tx, false); err != nil {
				return err
			}
		}
		if err := s.lockQueue(ctx, tx, q); err != nil {
			return err
		}
		if gate != nil {
			if err := checkAuthorityEmission(ctx, tx, *gate); err != nil {
				return err
			}
		}
		var k QueueKey
		if err := tx.QueryRow(ctx, `SELECT installation,repository,base,lane FROM queues WHERE id=$1`, q).Scan(&k.Installation, &k.Repository, &k.BaseBranch, &k.Lane); err != nil {
			return err
		}
		if pin.Authority == authority.OpenMerge {
			if err := validateAuthorityQueue(k, pin.OpenMerge.Binding); err != nil {
				return err
			}
		}

		previousID, previous, havePrevious, err := latestAuthorityPin(ctx, tx, q)
		if err != nil {
			return err
		}
		previousConfig, haveConfig, err := latestConfigApplied(ctx, tx, q)
		if err != nil {
			return err
		}
		// This path deliberately precedes journal replay and draft lookup. It
		// mirrors AdoptAuthorityPin's no-op retry behavior and makes rollback
		// retries safe after their current folded pin is github.
		if havePrevious && haveConfig && sameAuthorityPin(previous, pin) && authorityInputsMatch(previous, in) && reflect.DeepEqual(previousConfig, config) {
			receipt = authorityAdoptionReceipt(q, previousID, previous)
			return nil
		}

		state, _, err := load(ctx, tx, q)
		if err != nil {
			return err
		}
		if len(historical) == 1 && (historical[0].Commit != state.ConfigCommit || historical[0].Digest != state.ConfigDigest) {
			return errors.New("historical config backfill differs from folded config")
		}
		switch pin.Authority {
		case authority.GitHub:
			// This is a mode rollback, not a generic github config event. The
			// folded state (rather than a caller-supplied event ID) proves the
			// immediate lifecycle being rolled back.
			current := state.AuthorityPin()
			if current == nil || current.Authority != authority.OpenMerge {
				return errors.New("github authority pin requires a folded openmerge pin")
			}
		case authority.OpenMerge:
			// A governing commit rebind does not change the authenticated policy.
			// Preserve both activation and interpretation context; emitting an
			// unadopted pin here would create a spurious draft floor.
			inputsDrifted := havePrevious && !authorityInputsMatch(previous, in)
			if draftID == 0 && havePrevious && !inputsDrifted && queue.AuthorityDraftAdopted(previous) && reflect.DeepEqual(previous.OpenMerge.Binding, pin.OpenMerge.Binding) {
				pin.OpenMerge = previous.OpenMerge
			}
			var draft queue.WriteRestrictionVerified
			if draftID > 0 && !inputsDrifted {
				draft, err = proposedVerification(ctx, tx, q, draftID)
				if err != nil {
					return err
				}
			}
			floor, err := authorityDraftFloor(ctx, tx, q)
			if err != nil {
				return err
			}
			if !inputsDrifted && draftID > 0 && (floor == 0 || draftID > floor) && freshAuthorityAdoption(state, basis, readStarted, readAt) {
				pin = queue.BindAuthorityDraft(pin, draftID, draft, readStarted, readAt, in, fresh)
			}
		}

		if len(historical) == 1 {
			name, raw, err := queue.EncodeEvent(historical[0])
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO events(queue_id,type,payload) VALUES($1,$2,$3::jsonb)`, q, name, string(raw)); err != nil {
				return err
			}
		}
		configName, configRaw, err := queue.EncodeEvent(config)
		if err != nil {
			return err
		}
		pinName, pinRaw, err := queue.EncodeEvent(pin)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO events(queue_id,type,payload) VALUES($1,$2,$3::jsonb)`, q, configName, string(configRaw)); err != nil {
			return err
		}
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO events(queue_id,type,payload) VALUES($1,$2,$3::jsonb) RETURNING id`, q, pinName, string(pinRaw)).Scan(&id); err != nil {
			return err
		}
		receipt = authorityAdoptionReceipt(q, id, pin)
		return nil
	})
	if err != nil {
		return AuthorityAdoptionReceipt{}, err
	}
	return receipt, nil
}

// AdoptAuthorityPin remains publicly unwired. The caller captures the source
// basis before a fresh seeing read begun after landing. Draft mismatch or
// incomplete evidence leaves activation unverified. Adoption and the authority
// pin are one event/transaction. Repeated per-operation calls append nothing and
// return before replaying the journal or looking up a proposed verification.
func (s *Store) AdoptAuthorityPin(ctx context.Context, q QueueID, pin queue.PolicyAuthorityApplied, draftID int64, basis uint64, readStarted, readAt time.Time, in forge.WriteRestrictionInput, fresh authority.Observation, emission ...AuthorityEmissionGate) (AuthorityAdoptionReceipt, error) {
	var receipt AuthorityAdoptionReceipt
	if pin.OpenMerge == nil || pin.OpenMerge.Activation != nil {
		return receipt, errors.New("expected an unadopted openmerge pin")
	}
	if err := pin.Validate(); err != nil {
		return receipt, err
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if len(emission) > 0 {
			if err := s.lockTenancy(ctx, tx, false); err != nil {
				return err
			}
		}
		if err := s.lockQueue(ctx, tx, q); err != nil {
			return err
		}
		for _, gate := range emission {
			if err := checkAuthorityEmission(ctx, tx, gate); err != nil {
				return err
			}
		}
		var k QueueKey
		if err := tx.QueryRow(ctx, `SELECT installation,repository,base,lane FROM queues WHERE id=$1`, q).Scan(&k.Installation, &k.Repository, &k.BaseBranch, &k.Lane); err != nil {
			return err
		}
		if err := validateAuthorityQueue(k, pin.OpenMerge.Binding); err != nil {
			return err
		}
		// Read the latest durable pin/config directly: an executor no-op must not
		// scan all proposed history, require a still-valid draft ID, or append.
		var previousID int64
		inputsDrifted := false
		var previousRaw, configRaw []byte
		err := tx.QueryRow(ctx, `SELECT id,payload FROM events WHERE queue_id=$1 AND type='policy_authority_applied' ORDER BY id DESC LIMIT 1`, q).Scan(&previousID, &previousRaw)
		if err == nil {
			previousEvent, err := queue.DecodeEvent(previousRaw)
			if err != nil {
				return err
			}
			previous, ok := previousEvent.(queue.PolicyAuthorityApplied)
			if !ok {
				return errors.New("invalid persisted authority pin")
			}
			inputsDrifted = !authorityInputsMatch(previous, in)
			if !inputsDrifted && previous.GoverningCommit == pin.GoverningCommit && previous.EffectiveDigest == pin.EffectiveDigest && previous.OpenMerge != nil && reflect.DeepEqual(previous.OpenMerge.Binding, pin.OpenMerge.Binding) {
				if err := tx.QueryRow(ctx, `SELECT payload FROM events WHERE queue_id=$1 AND type='config_applied' ORDER BY id DESC LIMIT 1`, q).Scan(&configRaw); err != nil {
					return err
				}
				configEvent, err := queue.DecodeEvent(configRaw)
				if err != nil {
					return err
				}
				config, ok := configEvent.(queue.ConfigApplied)
				if ok && config.Commit == pin.GoverningCommit && config.Digest == pin.EffectiveDigest {
					receipt = authorityAdoptionReceipt(q, previousID, previous)
					return nil
				}
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		state, _, err := load(ctx, tx, q)
		if err != nil {
			return err
		}
		if state.ConfigCommit != pin.GoverningCommit || state.ConfigDigest != pin.EffectiveDigest {
			return errors.New("authority pin differs from governing config")
		}
		var draft queue.WriteRestrictionVerified
		if draftID > 0 && !inputsDrifted {
			draft, err = proposedVerification(ctx, tx, q, draftID)
			if err != nil {
				return err
			}
		}
		// A draftless invalidation cannot adopt a proposal; avoid scanning its
		// historical floor, while retaining the exact same guarded write.
		var floor int64
		if draftID > 0 {
			floor, err = authorityDraftFloor(ctx, tx, q)
			if err != nil {
				return err
			}
		}
		var governingRecordedAt time.Time
		if err := tx.QueryRow(ctx, `SELECT recorded_at FROM events WHERE queue_id=$1 AND type='config_applied' ORDER BY id DESC LIMIT 1`, q).Scan(&governingRecordedAt); err != nil {
			return err
		}
		if !inputsDrifted && (floor == 0 || draftID > floor) && !readStarted.Before(governingRecordedAt) && basis == state.RulesSourceRevision(queue.WriteRestriction) && !readStarted.IsZero() && !readAt.Before(readStarted) && time.Since(readAt) >= 0 && time.Since(readStarted) <= 2*time.Minute {
			pin = queue.BindAuthorityDraft(pin, draftID, draft, readStarted, readAt, in, fresh)
		}
		name, raw, err := queue.EncodeEvent(pin)
		if err != nil {
			return err
		}
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO events(queue_id,type,payload) VALUES($1,$2,$3::jsonb) RETURNING id`, q, name, string(raw)).Scan(&id); err != nil {
			return err
		}
		receipt = authorityAdoptionReceipt(q, id, pin)
		return nil
	})
	if err != nil {
		return AuthorityAdoptionReceipt{}, err
	}
	return receipt, nil
}
