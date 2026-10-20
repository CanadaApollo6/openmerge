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
	var result queue.WriteRestrictionVerified
	if eventID <= 0 {
		return result, errors.New("invalid proposed event ID")
	}
	var after int64
	for {
		rows, err := s.pool.Query(ctx, `SELECT id,payload FROM events WHERE queue_id=$1 AND id>$2 AND id<=$3 ORDER BY id LIMIT 256`, q, after, eventID)
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

// AdoptAuthorityPin is deliberately unwired until PR 9. The caller captures the
// source basis before a fresh seeing read begun after landing. Draft mismatch or
// incomplete evidence leaves activation unverified. Adoption and the authority
// pin are one event/transaction. Repeated per-operation calls append nothing.
func (s *Store) AdoptAuthorityPin(ctx context.Context, q QueueID, pin queue.PolicyAuthorityApplied, draftID int64, basis uint64, readStarted, readAt time.Time, fresh authority.Observation) (int64, error) {
	if pin.OpenMerge == nil || pin.OpenMerge.Activation != nil {
		return 0, errors.New("expected an unadopted openmerge pin")
	}
	if err := pin.Validate(); err != nil {
		return 0, err
	}
	var draft queue.WriteRestrictionVerified
	if draftID > 0 {
		var err error
		draft, err = s.ProposedVerification(ctx, q, draftID)
		if err != nil {
			return 0, err
		}
	}
	var id int64
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
		state, _, err := load(ctx, tx, q)
		if err != nil {
			return err
		}
		if previous := state.AuthorityPin(); previous != nil && previous.GoverningCommit == pin.GoverningCommit && previous.EffectiveDigest == pin.EffectiveDigest && previous.OpenMerge != nil && reflect.DeepEqual(previous.OpenMerge.Binding, pin.OpenMerge.Binding) {
			return tx.QueryRow(ctx, `SELECT id FROM events WHERE queue_id=$1 AND type='policy_authority_applied' ORDER BY id DESC LIMIT 1`, q).Scan(&id)
		}
		if state.ConfigCommit != pin.GoverningCommit || state.ConfigDigest != pin.EffectiveDigest {
			return errors.New("authority pin differs from governing config")
		}
		var governingRecordedAt time.Time
		if err := tx.QueryRow(ctx, `SELECT recorded_at FROM events WHERE queue_id=$1 AND type='config_applied' ORDER BY id DESC LIMIT 1`, q).Scan(&governingRecordedAt); err != nil {
			return err
		}
		if !readStarted.Before(governingRecordedAt) && basis == state.RulesSourceRevision(queue.WriteRestriction) && !readStarted.IsZero() && !readAt.Before(readStarted) && time.Since(readAt) >= 0 && time.Since(readStarted) <= 2*time.Minute {
			pin = queue.BindAuthorityDraft(pin, draftID, draft, readStarted, readAt, fresh)
		}
		name, raw, err := queue.EncodeEvent(pin)
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO events(queue_id,type,payload) VALUES($1,$2,$3::jsonb) RETURNING id`, q, name, string(raw)).Scan(&id)
	})
	return id, err
}
