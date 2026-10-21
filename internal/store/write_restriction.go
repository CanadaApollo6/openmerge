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
func (s *Store) CommitWriteRestrictionVerification(ctx context.Context, records []VerificationRecord) ([]VerificationReceipt, error) {
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
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, r := range records {
			if err := s.lockQueue(ctx, tx, r.QueueID); err != nil {
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

// AdoptAuthorityPin remains publicly unwired. The caller captures the source
// basis before a fresh seeing read begun after landing. Draft mismatch or
// incomplete evidence leaves activation unverified. Adoption and the authority
// pin are one event/transaction. Repeated per-operation calls append nothing and
// return before replaying the journal or looking up a proposed verification.
func (s *Store) AdoptAuthorityPin(ctx context.Context, q QueueID, pin queue.PolicyAuthorityApplied, draftID int64, basis uint64, readStarted, readAt time.Time, in forge.WriteRestrictionInput, fresh authority.Observation) (AuthorityAdoptionReceipt, error) {
	var receipt AuthorityAdoptionReceipt
	if pin.OpenMerge == nil || pin.OpenMerge.Activation != nil {
		return receipt, errors.New("expected an unadopted openmerge pin")
	}
	if err := pin.Validate(); err != nil {
		return receipt, err
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.lockQueue(ctx, tx, q); err != nil {
			return err
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
			if previous.GoverningCommit == pin.GoverningCommit && previous.EffectiveDigest == pin.EffectiveDigest && previous.OpenMerge != nil && reflect.DeepEqual(previous.OpenMerge.Binding, pin.OpenMerge.Binding) {
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
		if draftID > 0 {
			draft, err = proposedVerification(ctx, tx, q, draftID)
			if err != nil {
				return err
			}
		}
		var governingRecordedAt time.Time
		if err := tx.QueryRow(ctx, `SELECT recorded_at FROM events WHERE queue_id=$1 AND type='config_applied' ORDER BY id DESC LIMIT 1`, q).Scan(&governingRecordedAt); err != nil {
			return err
		}
		if !readStarted.Before(governingRecordedAt) && basis == state.RulesSourceRevision(queue.WriteRestriction) && !readStarted.IsZero() && !readAt.Before(readStarted) && time.Since(readAt) >= 0 && time.Since(readStarted) <= 2*time.Minute {
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
