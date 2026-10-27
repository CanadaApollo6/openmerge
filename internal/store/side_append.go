package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/journalreader"
	pj "github.com/CanadaApollo6/openmerge/internal/provenance/journal"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// SideAppend describes one atomic bundle of dependency/provenance evidence.
// Build receives reserved journal IDs so chunks and companions can bind exact
// positions before any INSERT. It must have no external effects. Binding must be
// independently operator-verified; a snapshot or the candidate init is not trust.
// This API is deliberately unwired: future writers still need authority/start
// fencing and reader-first rollout approval, beyond this persistence foundation.
type SideAppend struct {
	Count     int
	DedupeKey string
	Binding   dependency.JournalInitialized
	Build     func(ids []uint64) ([]journalreader.Event, error)
}

// AppendSide validates the entire bundle through the exact replay fold under the
// queue lock, before inserting any record. Failed validation rolls back cursor
// changes too. Sequence reservations may leave gaps, as ordinary rollbacks do.
func (s *Store) AppendSide(ctx context.Context, q QueueID, request SideAppend) (int64, error) {
	var watermark int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.lockQueue(ctx, tx, q); err != nil {
			return err
		}
		if err := checkApprovalRead(ctx, tx, q); err != nil {
			return err
		}
		var err error
		watermark, err = s.appendSideTx(ctx, tx, q, request)
		return err
	})
	return watermark, err
}

// appendSideTx validates and inserts using the caller's transaction. The caller
// MUST acquire this queue's advisory lock before any action/observation row locks
// or events INSERTs (whose FK takes queues KEY SHARE). Never enter via another
// transaction while holding that lock. Check checkApprovalRead BEFORE the first
// write, not after inserting the transaction's own eligibility records.
// Errors require rolling back the whole tx,
// including preceding decisions; this helper neither commits nor rolls back.
func (s *Store) appendSideTx(ctx context.Context, tx pgx.Tx, q QueueID, request SideAppend) (int64, error) {
	if request.Count < 1 || request.Count >= pj.MaxActiveRecords || request.Build == nil || request.DedupeKey == "" {
		return 0, errors.New("invalid side append bundle")
	}
	var watermark int64
	err := func() error {
		err := tx.QueryRow(ctx, `SELECT id FROM events WHERE queue_id=$1 AND dedupe_key=$2`, q, request.DedupeKey).Scan(&watermark)
		if err == nil {
			return compareSideBundle(ctx, tx, q, watermark, request)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var seed queue.State
		if err := tx.QueryRow(ctx, `SELECT base, COALESCE(initial_fence, '') FROM queues WHERE id=$1`, q).Scan(&seed.BaseBranch, &seed.Fence); err != nil {
			return err
		}
		reader, _, err := replayJournal(ctx, tx, q, seed, 0, true)
		if err != nil {
			return err
		}
		packingBefore := reader.Provenance.PackingWork()
		ids := make([]uint64, request.Count)
		for i := range ids {
			// Reserve real global sequence positions, never max(id)+1. Other queues may
			// append concurrently, and these exact IDs also serve the in-bundle lookup.
			if err := tx.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('events','id'))`).Scan(&ids[i]); err != nil {
				return err
			}
		}
		events, err := request.Build(slices.Clone(ids))
		if err != nil {
			return err
		}
		if len(events) != len(ids) {
			return errors.New("side append bundle count mismatch")
		}
		type encoded struct {
			kind string
			raw  []byte
		}
		records := make([]encoded, len(events))
		candidates := map[uint64][]byte{}
		side := false
		var bundleBytes int
		for i, event := range events {
			count := 0
			if event.Queue != nil {
				count++
			}
			if event.Dependency != nil {
				count++
			}
			if event.Provenance != nil {
				count++
			}
			if count != 1 {
				return errors.New("side append requires exactly one event variant")
			}
			var name string
			var raw []byte
			switch {
			case event.Queue != nil:
				// Decisions remain on StartLanding's policy/phase-authorized path.
				// Future integration may co-transact evidence there, never bypass it here.
				return errors.New("side append does not admit queue events")
			case event.Dependency != nil:
				switch event.Dependency.(type) {
				case dependency.JournalInitialized, dependency.DependenciesDeclared, dependency.PolicyApplied, dependency.Acknowledged, dependency.SelfAcknowledged, dependency.Observed:
				default:
					return errors.New("side append dependency kind is not admitted")
				}
				side = true
				if err := request.Binding.Validate(); err != nil {
					return err
				}
				name, raw, err = dependency.EncodeEvent(event.Dependency)
			case event.Provenance != nil:
				switch event.Provenance.(type) {
				case pj.Observation, pj.Landing:
				default:
					return errors.New("side append provenance kind is not admitted")
				}
				side = true
				name, raw, err = pj.EncodeEvent(event.Provenance)
			}
			if err != nil {
				return err
			}
			bundleBytes += len(raw)
			if bundleBytes >= 32<<20 {
				return pj.RetentionLimitError{Records: i + 1, Bytes: bundleBytes}
			}
			records[i] = encoded{name, raw}
			candidates[ids[i]] = raw
		}
		if !side {
			return errors.New("side append bundle has no side evidence")
		}
		// The overlay exposes only earlier candidate IDs (Reader.target enforces
		// ordering); existing targets resolve in this queue, under the same lock.
		reader.WithLookup(func(id uint64) ([]byte, error) {
			if raw, ok := candidates[id]; ok {
				return raw, nil
			}
			var raw []byte
			err := tx.QueryRow(ctx, `SELECT payload FROM events WHERE queue_id=$1 AND id=$2`, q, id).Scan(&raw)
			return raw, err
		})
		reader.UpgradeCursor()
		for i, record := range records {
			if _, err := reader.Apply(ids[i], record.raw); err != nil {
				return err
			}
			if events[i].Dependency != nil {
				if err := reader.VerifyDependencyBinding(request.Binding); err != nil {
					return err
				}
			}
		}
		if err := reader.Preflight(s.sideHighWater); err != nil {
			return err
		}
		if s.afterSidePreflight != nil {
			s.afterSidePreflight(reader.Provenance.PackingWork()-packingBefore, reader.Provenance.Capacity(s.sideHighWater).FenceCount)
		}
		// Upgrade the existing cursor at ITS watermark, preserving outside-move
		// bookkeeping. Advancing it to the bundle would skip unaudited base moves.
		var progress outsidePushProgress
		var current []byte
		err = tx.QueryRow(ctx, `SELECT value FROM observations WHERE queue_id=$1 AND key=$2`, q, outsidePushCursorKey).Scan(&current)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		cursor := sharedReader(ctx, tx, q, seed)
		if len(current) > 0 {
			if err := json.Unmarshal(current, &progress); err != nil {
				return fmt.Errorf("side append cursor: %w", err)
			}
			if progress.ScannedThrough < 0 {
				return journalreader.ErrReference
			}
			if err := cursor.LoadSnapshot(progress.State, uint64(progress.ScannedThrough)); err != nil {
				return err
			}
		}
		cursor.UpgradeCursor()
		progress.State, err = cursor.CursorSnapshot()
		if err != nil {
			return err
		}
		raw, err := json.Marshal(progress)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO observations(queue_id,key,value) VALUES($1,$2,$3)
	   ON CONFLICT(queue_id,key) DO UPDATE SET value=EXCLUDED.value`, q, outsidePushCursorKey, raw); err != nil {
			return err
		}
		// No candidate row reaches events until EVERY candidate and cursor is valid.
		for i, record := range records {
			var key *string
			if i == len(records)-1 {
				key = &request.DedupeKey
			}
			if _, err := tx.Exec(ctx, `INSERT INTO events(id,queue_id,type,payload,dedupe_key) VALUES($1,$2,$3,$4::jsonb,$5)`, ids[i], q, record.kind, string(record.raw), key); err != nil {
				return err
			}
		}
		manifest, err := json.Marshal(sideBundleManifest{IDs: ids, Binding: request.Binding})
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO observations(queue_id,key,value) VALUES($1,$2,$3)`, q, "side-append/bundle/"+request.DedupeKey, manifest); err != nil {
			return err
		}
		watermark = int64(ids[len(ids)-1])
		checkpoint, err := reader.CursorSnapshot()
		if err != nil {
			return err
		}
		checkpointRaw, err := json.Marshal(outsidePushProgress{ScannedThrough: watermark, State: checkpoint})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO observations(queue_id,key,value) VALUES($1,$2,$3) ON CONFLICT(queue_id,key) DO UPDATE SET value=EXCLUDED.value`, q, sideCheckpointKey, checkpointRaw)
		return err
	}()
	if err != nil {
		return 0, err
	}
	return watermark, nil
}

// The dedupe marker belongs to the final row. Global IDs can have gaps, so read
// the preceding queue rows rather than subtracting Count from the marker.
func compareSideBundle(ctx context.Context, tx pgx.Tx, q QueueID, watermark int64, request SideAppend) error {
	var manifest []byte
	if err := tx.QueryRow(ctx, `SELECT value FROM observations WHERE queue_id=$1 AND key=$2`, q, "side-append/bundle/"+request.DedupeKey).Scan(&manifest); err != nil {
		return err
	}
	var manifestValue sideBundleManifest
	if err := json.Unmarshal(manifest, &manifestValue); err != nil {
		return err
	}
	if len(manifestValue.IDs) != request.Count || manifestValue.Binding != request.Binding {
		return errors.New("side append dedupe bundle mismatch")
	}
	rows, err := tx.Query(ctx, `SELECT id,payload FROM events WHERE queue_id=$1 AND id<=$2 ORDER BY id DESC LIMIT $3`, q, watermark, request.Count)
	if err != nil {
		return err
	}
	type stored struct {
		id  uint64
		raw []byte
	}
	records, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (stored, error) {
		var v stored
		err := row.Scan(&v.id, &v.raw)
		return v, err
	})
	if err != nil {
		return err
	}
	if len(records) != request.Count {
		return errors.New("side append dedupe bundle mismatch")
	}
	slices.Reverse(records)
	ids := make([]uint64, len(records))
	for i, v := range records {
		ids[i] = v.id
		if ids[i] != manifestValue.IDs[i] {
			return errors.New("side append dedupe bundle mismatch")
		}
	}
	events, err := request.Build(ids)
	if err != nil {
		return err
	}
	if len(events) != len(records) {
		return errors.New("side append dedupe bundle mismatch")
	}
	for i, event := range events {
		var raw []byte
		switch {
		case event.Queue == nil && event.Dependency == nil && event.Provenance != nil:
			_, raw, err = pj.EncodeEvent(event.Provenance)
		case event.Queue == nil && event.Dependency != nil && event.Provenance == nil:
			if err := request.Binding.Validate(); err != nil {
				return err
			}
			if init, ok := event.Dependency.(dependency.JournalInitialized); ok {
				if err := init.VerifyBinding(request.Binding); err != nil {
					return err
				}
			}
			_, raw, err = dependency.EncodeEvent(event.Dependency)
		default:
			return errors.New("side append dedupe bundle mismatch")
		}
		if err != nil {
			return err
		}
		// JSONB changes whitespace/key order; canonicalize the stored event through
		// its strict owner codec before comparing the complete payload bytes.
		old, err := journalreader.DecodeEvent(records[i].raw)
		if err != nil {
			return err
		}
		var canonical []byte
		if old.Provenance != nil {
			_, canonical, err = pj.EncodeEvent(old.Provenance)
		} else if old.Dependency != nil {
			_, canonical, err = dependency.EncodeEvent(old.Dependency)
		}
		if err != nil {
			return err
		}
		if !bytes.Equal(raw, canonical) {
			return errors.New("side append dedupe bundle mismatch")
		}
	}
	return nil
}

type sideBundleManifest struct {
	IDs     []uint64
	Binding dependency.JournalInitialized
}

// RecoverLanded repairs a crash after an accepted journal Landed but before its
// companion. Build must reconstruct canonical landed evidence from immutable
// inputs, including the actual landed composition; Landed alone does not prove
// a source-to-build mapping. No forge reads or effects may run inside Build.
// The journal outcome supplies the batch/attempt binding and deterministic
// dedupe key. Retries compare the entire canonical bundle, never just that key.
// This helper is Hold: only tests may call it until writer integration review.
func (s *Store) RecoverLanded(ctx context.Context, q QueueID, landedID int64, request SideAppend) (int64, error) {
	if landedID <= 1 || request.Build == nil {
		return 0, errors.New("invalid landed recovery")
	}
	var watermark int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.lockQueue(ctx, tx, q); err != nil {
			return err
		}
		if err := checkApprovalRead(ctx, tx, q); err != nil {
			return err
		}
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT payload FROM events WHERE queue_id=$1 AND id=$2`, q, landedID).Scan(&raw); err != nil {
			return err
		}
		event, err := journalreader.DecodeEvent(raw)
		if err != nil {
			return err
		}
		outcome, ok := event.Queue.(queue.Landed)
		if !ok {
			return errors.New("landed recovery requires journal Landed")
		}
		var seed queue.State
		if err := tx.QueryRow(ctx, `SELECT base, COALESCE(initial_fence, '') FROM queues WHERE id=$1`, q).Scan(&seed.BaseBranch, &seed.Fence); err != nil {
			return err
		}
		before, _, err := replayJournal(ctx, tx, q, seed, landedID-1, true)
		if err != nil {
			return err
		}
		batch := before.Queue.Batch(outcome.BatchID)
		if batch == nil || batch.Phase != queue.Landing || batch.LandingAttempt.ID == "" {
			return errors.New("landed recovery requires an accepted landing outcome")
		}
		after := queue.Apply(before.Queue, outcome)
		if after.Batch(outcome.BatchID) != nil || after.BaseSHA != outcome.NewBaseSHA {
			return errors.New("landed recovery requires an accepted landing outcome")
		}
		validateCompanion := func(companion pj.Landing) error {
			if companion.DecisionID >= uint64(landedID) || companion.Stage != "landed" || companion.BatchID != outcome.BatchID || companion.AttemptID != batch.LandingAttempt.ID {
				return errors.New("landed recovery companion binding mismatch")
			}
			if err := validateBatchCompanion(before.Queue, batch, companion, outcome.NewBaseSHA); err != nil {
				return err
			}
			// The decision certifies the batch's final result. Member mappings may
			// describe intermediate commits, so do not equate each to NewBaseSHA.
			var decisionRaw []byte
			if err := tx.QueryRow(ctx, `SELECT payload FROM events WHERE queue_id=$1 AND id=$2`, q, companion.DecisionID).Scan(&decisionRaw); err != nil {
				return err
			}
			decisionEvent, err := journalreader.DecodeEvent(decisionRaw)
			if err != nil {
				return err
			}
			decision, ok := decisionEvent.Queue.(queue.LandingDecisionRecorded)
			if !ok || decision.TestedSHA != batch.TestedSHA || decision.LandedSHA != outcome.NewBaseSHA || decision.BatchID != companion.BatchID || decision.AttemptID != companion.AttemptID || !slices.ContainsFunc(before.Queue.LandingDecisions(), func(d queue.LandingDecisionRecorded) bool { return reflect.DeepEqual(d, decision) }) {
				return errors.New("landed recovery decision binding mismatch")
			}
			return nil
		}
		key := fmt.Sprintf("side-append/recover-landed/%d", landedID)
		var ownKey bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE queue_id=$1 AND dedupe_key=$2)`, q, key).Scan(&ownKey); err != nil {
			return err
		}
		if !ownKey {
			current, _, err := replayJournal(ctx, tx, q, seed, 0, true)
			if err != nil {
				return err
			}
			if id, complete := current.Provenance.SettledAttempt(batch.LandingAttempt.ID); complete {
				var raw []byte
				if err := tx.QueryRow(ctx, `SELECT payload FROM events WHERE queue_id=$1 AND id=$2`, q, id).Scan(&raw); err != nil {
					return err
				}
				event, err := journalreader.DecodeEvent(raw)
				if err != nil {
					return err
				}
				if companion, ok := event.Provenance.(pj.Landing); ok && companion.Stage == "landed" && companion.BatchID == batch.ID && companion.AttemptID == batch.LandingAttempt.ID {
					if err := validateCompanion(companion); err != nil {
						return err
					}
					return LandedAlreadyCompleteError{CompanionID: id}
				}
			}
		}
		build := request.Build
		request.Build = func(ids []uint64) ([]journalreader.Event, error) {
			events, err := build(ids)
			if err != nil {
				return nil, err
			}
			if len(events) == 0 {
				return nil, errors.New("missing landed recovery companion")
			}
			companion, ok := events[len(events)-1].Provenance.(pj.Landing)
			if !ok {
				return nil, errors.New("missing landed recovery companion")
			}
			if err := validateCompanion(companion); err != nil {
				return nil, err
			}
			return events, nil
		}
		request.DedupeKey = key
		watermark, err = s.appendSideTx(ctx, tx, q, request)
		return err
	})
	if err != nil {
		return 0, err
	}
	return watermark, nil
}

// LandedAlreadyCompleteError reports a companion committed through another key.
// The original companion position is available without accepting altered retries.
type LandedAlreadyCompleteError struct{ CompanionID uint64 }

func (e LandedAlreadyCompleteError) Error() string {
	return fmt.Sprintf("landed companion already complete at %d", e.CompanionID)
}

// validateBatchCompanion binds exact coverage and the terminal composition to
// the historical batch. Earlier members may have intermediate build results.
func validateBatchCompanion(state queue.State, batch *queue.Batch, l pj.Landing, result string) error {
	if batch == nil || l.BatchID != batch.ID || len(l.Members) != len(batch.PRs) {
		return errors.New("companion batch membership mismatch")
	}
	for i, pr := range batch.PRs {
		revision, ok := state.PRRevision(pr.PR)
		if !ok {
			return errors.New("companion batch revision missing")
		}
		expectedRevision := strconv.FormatUint(uint64(revision.Revision), 10)
		for _, d := range state.LandingDecisions() {
			if d.BatchID != batch.ID || d.AttemptID != l.AttemptID {
				continue
			}
			if d.Approval != nil && d.Approval.Basis.ChangeID == revision.ChangeID && d.Approval.Basis.Head == pr.HeadSHA {
				expectedRevision = strconv.FormatUint(uint64(d.Approval.Basis.Revision), 10)
			}
			if d.Remedy != nil && d.Remedy.ChangeID == revision.ChangeID && d.Remedy.Head == pr.HeadSHA {
				expectedRevision = strconv.FormatUint(uint64(d.Remedy.Revision), 10)
			}
		}
		count := 0
		for _, m := range l.Members {
			if m.Request.ChangeID == string(revision.ChangeID) && m.Request.Head == pr.HeadSHA && m.Request.Revision == expectedRevision {
				count++
				if i == len(batch.PRs)-1 && m.Mapping.Build.Result != result {
					return errors.New("companion terminal composition mismatch")
				}
			}
		}
		if count != 1 {
			return errors.New("companion batch membership mismatch")
		}
	}
	return nil
}

// ProvenanceCapacity is the read-only operator gauge exposed by queue status.
type ProvenanceCapacity = pj.CapacityGauge

func withTestedBatchValidation(request SideAppend, state queue.State, batchID, attempt, testedSHA string, decisionIDs []uint64) (SideAppend, error) {
	inner := request.Build
	if inner == nil {
		return SideAppend{}, errors.New("missing tested companion builder")
	}
	request.Build = func(ids []uint64) ([]journalreader.Event, error) {
		events, err := inner(ids)
		if err != nil {
			return nil, err
		}
		if len(events) == 0 {
			return nil, errors.New("missing tested companion")
		}
		l, ok := events[len(events)-1].Provenance.(pj.Landing)
		if !ok || l.Stage != "tested" || l.AttemptID != attempt || !slices.Contains(decisionIDs, l.DecisionID) {
			return nil, errors.New("tested companion binding mismatch")
		}
		if err := validateBatchCompanion(state, state.Batch(batchID), l, testedSHA); err != nil {
			return nil, err
		}
		return events, nil
	}
	return request, nil
}
