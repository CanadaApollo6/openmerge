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
	"strings"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/journalreader"
	p "github.com/CanadaApollo6/openmerge/internal/provenance"
	pj "github.com/CanadaApollo6/openmerge/internal/provenance/journal"
	"github.com/CanadaApollo6/openmerge/internal/provenanceeval"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// SideAppend describes one atomic bundle of dependency/provenance evidence.
// Build receives reserved journal IDs so chunks and companions can bind exact
// positions before any INSERT. It must have no external effects. Binding must be
// independently operator-verified; a snapshot or the candidate init is not trust.
// The activated provenance start and recovery below share this adapter. Public
// policy still cannot activate them; dependency writers remain unwired.
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
		watermark, err = s.appendSideTx(ctx, tx, q, request, "")
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
// Only RecoverLanded supplies recoveryAttempt, after validating the immutable
// outcome. That mode must settle the attempt and strictly reduce retained bytes.
func (s *Store) appendSideTx(ctx context.Context, tx pgx.Tx, q QueueID, request SideAppend, recoveryAttempt string) (int64, error) {
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
		var recoveryBefore pj.CapacityGauge
		if recoveryAttempt != "" {
			recoveryBefore = reader.Provenance.Capacity(s.sideHighWater)
		}
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
		if recoveryAttempt != "" {
			if _, settled := reader.Provenance.SettledAttempt(recoveryAttempt); !settled {
				return errors.New("landed recovery did not settle attempt")
			}
			if err := reader.Provenance.PreflightRecovery(recoveryBefore); err != nil {
				return err
			}
		} else {
			if err := reader.Preflight(s.sideHighWater); err != nil {
				return err
			}
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
// The activated RecoverProvenance coordinator is its only runtime caller.
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
		validateCompanion := func(companion pj.Landing, candidates map[uint64]journalreader.Event) error {
			if companion.DecisionID >= uint64(landedID) || companion.Stage != "landed" || companion.BatchID != outcome.BatchID || companion.AttemptID != batch.LandingAttempt.ID {
				return errors.New("landed recovery companion binding mismatch")
			}
			if err := validateBatchCompanion(before.Queue, batch, companion, outcome.NewBaseSHA); err != nil {
				return err
			}
			// Every certifying decision must equal accepted historical evidence,
			// including secondary members and the already-complete path. Identical
			// duplicates are valid; matching member identities and SHAs alone is not.
			refs := map[uint64]bool{companion.DecisionID: true}
			for _, member := range companion.Members {
				for _, layer := range member.Layers {
					for _, ref := range layer.References {
						refs[ref] = true
					}
				}
			}
			for ref := range refs {
				event, candidate := candidates[ref]
				if !candidate {
					var raw []byte
					if err := tx.QueryRow(ctx, `SELECT payload FROM events WHERE queue_id=$1 AND id=$2`, q, ref).Scan(&raw); err != nil {
						return err
					}
					stored, err := journalreader.DecodeEvent(raw)
					if err != nil {
						return err
					}
					event = stored
				}
				decision, ok := event.Queue.(queue.LandingDecisionRecorded)
				if !ok && ref != companion.DecisionID {
					continue
				}
				// Decisions certify the final batch result; member mappings may
				// legitimately describe intermediate commits.
				if !ok || ref >= uint64(landedID) || decision.TestedSHA != batch.TestedSHA || decision.LandedSHA != outcome.NewBaseSHA || decision.BatchID != companion.BatchID || decision.AttemptID != companion.AttemptID || !slices.ContainsFunc(before.Queue.LandingDecisions(), func(d queue.LandingDecisionRecorded) bool { return reflect.DeepEqual(d, decision) }) {
					return errors.New("landed recovery decision binding mismatch")
				}
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
					if err := validateCompanion(companion, nil); err != nil {
						return err
					}
					return LandedAlreadyCompleteError{CompanionID: id}
				}
			}
		}
		var historical pj.Landing
		for _, record := range before.Provenance.Records() {
			event, err := pj.DecodeEvent(record.Payload)
			if err != nil {
				return err
			}
			if l, ok := event.(pj.Landing); ok && l.Stage == "tested" && l.AttemptID == batch.LandingAttempt.ID && l.BatchID == batch.ID {
				historical = l
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
			if len(events) != len(ids) {
				return nil, errors.New("side append bundle count mismatch")
			}
			companion, ok := events[len(events)-1].Provenance.(pj.Landing)
			if !ok {
				return nil, errors.New("missing landed recovery companion")
			}
			if err := validateRecoveryInventories(events, ids, companion, historical, before.Provenance); err != nil {
				return nil, err
			}
			candidates := make(map[uint64]journalreader.Event, len(events))
			for i, event := range events {
				candidates[ids[i]] = event
			}
			if err := validateCompanion(companion, candidates); err != nil {
				return nil, err
			}
			return events, nil
		}
		request.DedupeKey = key
		watermark, err = s.appendSideTx(ctx, tx, q, request, batch.LandingAttempt.ID)
		return err
	})
	if err != nil {
		return 0, err
	}
	return watermark, nil
}

// Only inventories actually consumed by this companion may share recovery's
// high-water exemption. The reader still validates chunks, digests and the
// generation CAS; the historical tested request fixes the immutable inputs.
func validateRecoveryInventories(events []journalreader.Event, ids []uint64, companion, historical pj.Landing, evidence pj.State) error {
	for i, event := range events[:len(events)-1] {
		h, ok := event.Provenance.(pj.Observation)
		if !ok || event.Queue != nil || event.Dependency != nil {
			return errors.New("landed recovery admits only required member inventories")
		}
		bound := false
		for _, member := range companion.Members {
			if !slices.Contains(member.ObservationIDs, ids[i]) || !reflect.DeepEqual(h.Request, member.Request) || h.Digest != member.InventoryDigest || h.DeclarationDigest != member.DeclarationDigest {
				continue
			}
			for _, prior := range historical.Members {
				request := h.Request
				request.Generation = prior.Request.Generation
				if !reflect.DeepEqual(request, prior.Request) {
					continue
				}
				inventory, ok := evidence.Inventory(prior.Request)
				if !ok {
					continue
				}
				inventory.Header.Request = h.Request
				inventory.Header.PreviousObservationID = h.PreviousObservationID
				if pj.InventoryDigest(inventory.Header, inventory.Commits) == h.Digest {
					bound = true
				}
			}
		}
		if !bound {
			return errors.New("landed recovery inventory is not bound to historical member")
		}
	}
	return nil
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

// ProvenanceStart is supplied only by the private activated serve seam. Objects
// and mappings are read before entering SQL; validate re-evaluates approvals and
// requirements against the queue-locked state. No callback performs acquisition.
type ProvenanceStart struct {
	Policy        *p.Policy
	Expression    condition.Expression
	DefaultBranch string
	Members       []ProvenanceMemberInput
}

// ProvenanceMemberInput pins one change's sources and independently mapped T/L.
type ProvenanceMemberInput struct {
	Inventory             p.Inventory
	Tested, Landed        p.BuildMapping
	Values                condition.Snapshot
	PreviousObservationID uint64
}

// Decisions prepares declaration-bound baseline decisions without publishing any
// evidence. StartProvenanceLanding independently repeats this under the lock.
func (v ProvenanceStart) Decisions(state queue.State, policy cp.Policy, land queue.Land, start queue.LandingStarted, fresh []queue.ApprovalEligibilityObserved) ([]queue.LandingDecisionRecorded, []provenanceeval.Evaluation, error) {
	if v.Policy == nil || v.Policy.Digest() != policy.Digest || !policy.Enabled() || len(v.Members) != len(land.PRs) || len(fresh) != len(land.PRs) || len(fresh) == 0 {
		return nil, nil, errors.New("incomplete activated provenance start")
	}
	decisions := make([]queue.LandingDecisionRecorded, len(fresh))
	evaluations := make([]provenanceeval.Evaluation, len(fresh))
	buildBase := land.ExpectedBaseSHA
	for i, member := range v.Members {
		if member.Tested.Build.Method != string(land.Method) || member.Landed.Build.Method != string(land.Method) || member.Tested.Build.Base != buildBase {
			return nil, nil, errors.New("provenance batch composition mismatch")
		}
		buildBase = member.Tested.Build.Result
		if i == len(v.Members)-1 && buildBase != land.TestedSHA {
			return nil, nil, errors.New("provenance tested terminal mismatch")
		}
		if member.Inventory.Request.Head != land.PRs[i].HeadSHA || !member.Tested.Complete || !member.Landed.Complete || member.Tested.Build.Stage != "tested" || member.Landed.Build.Stage != "landed" || member.Tested.Build.ID == member.Landed.Build.ID || !reflect.DeepEqual(member.Tested.Request, member.Inventory.Request) || !reflect.DeepEqual(member.Landed.Request, member.Inventory.Request) || member.Tested.PolicyID != member.Inventory.PolicyID || member.Landed.PolicyID != member.Inventory.PolicyID {
			return nil, nil, errors.New("provenance composition binding mismatch")
		}
		e, err := provenanceeval.Evaluate(state, policy, fresh[i], v.Expression, member.Values, v.Policy, member.Inventory)
		if err != nil {
			return nil, nil, err
		}
		if e.PreCI != cp.True || e.Layers.Result != cp.True {
			return nil, nil, errors.New("provenance landing requirements unmet")
		}
		declarations, err := p.DeclarationDigest(member.Inventory.Commits, true, v.Policy.Rules())
		if err != nil {
			return nil, nil, err
		}
		decisions[i], err = state.PrepareProvenanceLandingDecision(policy, fresh[i], start, land, declarations)
		if err != nil {
			return nil, nil, err
		}
		evaluations[i] = e
	}
	return decisions, evaluations, nil
}

// StartProvenanceLanding is the sanctioned activated writer. The ordinary
// StartLanding path retains its literal nil companion builder and wire bytes.
func (s *Store) StartProvenanceLanding(ctx context.Context, claim ClaimedAction, cfg queue.Config, policy cp.Policy, fresh []queue.ApprovalEligibilityObserved, decisions []queue.LandingDecisionRecorded, start queue.LandingStarted, evidence ProvenanceStart, checks ...queue.LandingChecksRecorded) error {
	return retryLandingDecision(s.startLandingTransaction(ctx, claim, cfg, policy, fresh, decisions, start, s.authorizeStart, nil, &evidence, checks...))
}

func (v ProvenanceStart) validate(ctx context.Context, tx pgx.Tx, q QueueID, state queue.State, policy cp.Policy, land queue.Land, start queue.LandingStarted, fresh []queue.ApprovalEligibilityObserved, decisions []queue.LandingDecisionRecorded) (func([]uint64) (SideAppend, error), error) {
	expected, evaluations, err := v.Decisions(state, policy, land, start, fresh)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(expected, decisions) {
		return nil, errors.New("provenance decisions changed")
	}
	declarations := make([]string, len(evaluations))
	for i, e := range evaluations {
		declarations[i] = e.Brief.DeclarationDigest
	}
	if err := state.ValidateProvenanceLandingDecisions(policy, land, start, fresh, decisions, declarations); err != nil {
		return nil, err
	}
	// This read shares the queue lock and sees any generation appended since the
	// off-transaction object reads. appendSideTx applies the same CAS again.
	var seed queue.State
	var repository, lane string
	if err := tx.QueryRow(ctx, `SELECT base,COALESCE(initial_fence,''),repository,lane FROM queues WHERE id=$1`, q).Scan(&seed.BaseBranch, &seed.Fence, &repository, &lane); err != nil {
		return nil, err
	}
	for _, member := range v.Members {
		if member.Inventory.Request.Repository != repository || member.Inventory.Request.Queue != seed.BaseBranch+"/"+lane {
			return nil, errors.New("provenance queue namespace mismatch")
		}
	}
	reader, _, err := replayJournal(ctx, tx, q, seed, 0, true)
	if err != nil {
		return nil, err
	}
	if len(reader.Provenance.AwaitingLandings()) != 0 {
		return nil, errors.New("provenance landed recovery required before admission")
	}
	headers := make([][]pj.Observation, len(v.Members))
	layers := make([][]pj.Layer, len(v.Members))
	count := 1
	for i, member := range v.Members {
		if reader.Provenance.PreviousObservationID(member.Inventory.Request) != member.PreviousObservationID {
			return nil, pj.ErrBinding
		}
		headers[i], err = provenanceChunks(member.Inventory, pj.PolicySource{DefaultBranch: v.DefaultBranch, ReadRef: "refs/heads/" + v.DefaultBranch, Commit: state.ConfigCommit, EffectiveDigest: policy.Digest}, member.PreviousObservationID)
		if err != nil {
			return nil, err
		}
		layers[i], err = provenanceLayers(ctx, tx, q, state, fresh[i], evaluations[i], v.Expression, v.Policy.Rules())
		if err != nil {
			return nil, err
		}
		count += len(headers[i])
	}
	return func(decisionIDs []uint64) (SideAppend, error) {
		if len(decisionIDs) != len(v.Members) {
			return SideAppend{}, errors.New("missing provenance decision IDs")
		}
		return SideAppend{Count: count, DedupeKey: "provenance/tested/" + start.AttemptID, Build: func(ids []uint64) ([]journalreader.Event, error) {
			out := make([]journalreader.Event, 0, count)
			companion := pj.Landing{AttemptID: start.AttemptID, BatchID: start.BatchID, DecisionID: decisionIDs[0], Stage: "tested", Members: []pj.Member{}}
			for i, member := range v.Members {
				positions := []uint64{}
				for _, h := range headers[i] {
					positions = append(positions, ids[len(out)])
					out = append(out, journalreader.Event{Provenance: h})
				}
				memberLayers := slices.Clone(layers[i])
				for j := range memberLayers {
					memberLayers[j].References = sortedPositions(append(slices.Clone(memberLayers[j].References), decisionIDs[i]))
				}
				m := pj.Member{Request: member.Inventory.Request, ObservationIDs: positions, InventoryDigest: headers[i][0].Digest, DeclarationDigest: headers[i][0].DeclarationDigest, Mapping: member.Tested, Layers: memberLayers, Result: "true"}
				m.RecordDigest = pj.MemberDigest(m)
				companion.Members = append(companion.Members, m)
			}
			// L was assessed separately against the actual expected landing before
			// authorizing effects. Only the deterministic supported compositions may
			// be reconstructed from T on restart.
			landed := companion
			landed.Stage = "landed"
			landed.Members = slices.Clone(companion.Members)
			for i := range landed.Members {
				landed.Members[i].Mapping = v.Members[i].Landed
			}
			if err := validateBatchCompanion(state, state.Batch(start.BatchID), landed, decisions[0].LandedSHA); err != nil {
				return nil, err
			}
			for i, m := range companion.Members {
				mapping, err := landedMapping(m.Mapping, decisions[0].LandedSHA, i == len(companion.Members)-1)
				if err != nil || !reflect.DeepEqual(mapping, v.Members[i].Landed) {
					return nil, errors.New("landed composition cannot be recovered from immutable tested mapping")
				}
			}
			return append(out, journalreader.Event{Provenance: companion}), nil
		}}, nil
	}, nil
}

func provenanceChunks(inv p.Inventory, policy pj.PolicySource, previous uint64) ([]pj.Observation, error) {
	d, err := p.DeclarationDigest(inv.Commits, inv.Complete, nil)
	if err != nil {
		return nil, err
	}
	commits := append([]p.Commit{}, inv.Commits...)
	for i := range commits {
		commits[i].Declarations = append([]p.Declaration{}, commits[i].Declarations...)
		key := func(d p.Declaration) string {
			return fmt.Sprintf("%q", []string{d.Source, d.Version, d.Kind, d.Value, d.ValueDigest, string(d.Outcome), string(d.Class), d.RuleID})
		}
		slices.SortFunc(commits[i].Declarations, func(a, b p.Declaration) int { return strings.Compare(key(a), key(b)) })
		commits[i].Declarations = slices.Compact(commits[i].Declarations)
	}
	slices.SortFunc(commits, func(a, b p.Commit) int { return strings.Compare(a.ObjectID, b.ObjectID) })
	h := pj.Observation{PreviousObservationID: previous, Request: inv.Request, Parser: inv.Policy, ParserID: inv.PolicyID, Policy: policy, Limits: inv.Limits, Stable: append([]string{}, inv.Stable...), Additional: append([]string{}, inv.Additional...), MergeBases: append([]string{}, inv.MergeBases...), BuildMergeBases: append([]string{}, inv.BuildMergeBases...), Complete: inv.Complete, Diagnostic: inv.Diagnostic, DeclarationDigest: d}
	h.Digest = pj.InventoryDigest(h, commits)
	var out []pj.Observation
	for size := 128; size >= 1; size /= 2 {
		h.Chunks = max(1, (len(commits)+size-1)/size)
		out = make([]pj.Observation, h.Chunks)
		var failure error
		for i := range out {
			out[i] = h
			out[i].Index = i
			out[i].Commits = append([]p.Commit{}, commits[min(i*size, len(commits)):min((i+1)*size, len(commits))]...)
			if _, _, err := pj.EncodeEvent(out[i]); err != nil {
				failure = err
				break
			}
		}
		if failure == nil {
			return out, nil
		}
		if size == 1 {
			return nil, failure
		}
	}
	return out, nil
}

func provenanceTruth(t cp.Truth) string {
	switch t {
	case cp.True:
		return "true"
	case cp.False:
		return "false"
	default:
		return "pending"
	}
}

func sortedPositions(ids []uint64) []uint64 { slices.Sort(ids); return slices.Compact(ids) }

func provenanceLayer(kind, rule string, expression condition.Expression, e condition.Evidence, truth cp.Truth, approvals cp.Evaluation) pj.Layer {
	l := pj.Layer{Kind: kind, Rule: rule, Expression: e.Expression, Inputs: []pj.Input{}, Eligibility: []string{}, References: []uint64{}, Truth: provenanceTruth(truth)}
	for _, in := range e.Inputs {
		typ := in.Kind
		if typ == "unknown" {
			typ = expression.InputType(in.Attribute)
		}
		l.Inputs = append(l.Inputs, pj.Input{Name: in.Attribute, Type: typ, Known: in.Kind != "unknown", String: in.Text, Bool: in.Flag, List: sortedStrings(in.List)})
		if in.Checks {
			l.Inputs = append(l.Inputs, pj.Input{Name: in.Attribute + ".observed", Type: "list", Known: true, List: sortedStrings(in.Observed)}, pj.Input{Name: in.Attribute + ".unsettled", Type: "list", Known: true, List: sortedStrings(in.Unsettled)})
		}
	}
	slices.SortFunc(l.Inputs, func(a, b pj.Input) int { return strings.Compare(a.Name, b.Name) })
	for _, req := range approvals.Requirements {
		l.Eligibility = append(l.Eligibility, req.EligibleIDs...)
	}
	l.Eligibility = sortedStrings(l.Eligibility)
	return l
}

func sortedStrings(s []string) []string {
	out := append([]string{}, s...)
	slices.Sort(out)
	return slices.Compact(out)
}

func provenanceLayers(ctx context.Context, tx pgx.Tx, q QueueID, state queue.State, fresh queue.ApprovalEligibilityObserved, e provenanceeval.Evaluation, expression condition.Expression, rules []p.Rule) ([]pj.Layer, error) {
	out := []pj.Layer{provenanceLayer("baseline", "", expression, e.BaselineInputs, e.Layers.Baseline, e.BaselineApprovals)}
	for i, r := range rules {
		key := strconv.Itoa(i)
		out = append(out, provenanceLayer("trigger", key, r.When, e.Layers.Rules[i].WhenInputs, e.Layers.Rules[i].Trigger, e.BaselineApprovals))
		evidence := e.Layers.Rules[i].ConditionInputs
		if r.Conditions == nil {
			evidence = condition.Evidence{Expression: "approvals", Inputs: []condition.ObservedInput{}}
		}
		obligation := condition.All()
		if r.Conditions != nil {
			obligation = *r.Conditions
		}
		layer := provenanceLayer("requirement", key, obligation, evidence, e.Layers.Rules[i].Obligation, e.LocalApprovals[i])
		facts, _ := state.ApprovalFacts(fresh.Basis.ChangeID)
		for _, requirement := range e.LocalApprovals[i].Requirements {
			for _, stance := range requirement.ReliedOn {
				source, ok := facts.Sources[string(stance.Source.Channel)+"/"+stance.Source.ID]
				if !ok || !reflect.DeepEqual(source.Event.Stance, stance) {
					return nil, errors.New("provenance approval source missing")
				}
				_, raw, err := queue.EncodeEvent(source.Event)
				if err != nil {
					return nil, err
				}
				var id uint64
				if err := tx.QueryRow(ctx, `SELECT id FROM events WHERE queue_id=$1 AND payload=$2::jsonb ORDER BY id LIMIT 1`, q, string(raw)).Scan(&id); err != nil {
					return nil, err
				}
				layer.References = append(layer.References, id)
				if stance.Receipt != nil {
					receipt, ok := state.ReceiptRecords(fresh.Basis.ChangeID)[stance.Source.ID]
					if !ok || !receipt.Accepted {
						return nil, errors.New("provenance receipt missing")
					}
					_, raw, err = queue.EncodeEvent(receipt)
					if err != nil {
						return nil, err
					}
					if err := tx.QueryRow(ctx, `SELECT id FROM events WHERE queue_id=$1 AND payload=$2::jsonb ORDER BY id LIMIT 1`, q, string(raw)).Scan(&id); err != nil {
						return nil, err
					}
					layer.References = append(layer.References, id)
				}
			}
		}
		layer.References = sortedPositions(layer.References)
		out = append(out, layer)
	}
	return out, nil
}

// ProvenancePredecessors captures generation anchors before object reads; the
// transaction compares them again rather than silently rebasing stale success.
func (s *Store) ProvenancePredecessors(ctx context.Context, q QueueID, requests []p.InventoryRequest) ([]uint64, error) {
	var out []uint64
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var seed queue.State
		if err := tx.QueryRow(ctx, `SELECT base,COALESCE(initial_fence,'') FROM queues WHERE id=$1`, q).Scan(&seed.BaseBranch, &seed.Fence); err != nil {
			return err
		}
		r, _, err := replayJournal(ctx, tx, q, seed, 0, true)
		if err != nil {
			return err
		}
		out = make([]uint64, len(requests))
		for i, req := range requests {
			out[i] = r.Provenance.PreviousObservationID(req)
		}
		return nil
	})
	return out, err
}

func landedMapping(t p.BuildMapping, result string, terminal bool) (p.BuildMapping, error) {
	l := t
	l.Build.Stage, l.Build.ID = "landed", t.Build.ID+"/landed"
	if t.Build.Method == "fast-forward" {
		l.Build.Result, l.Build.Marker = t.Request.Head, ""
	} else if terminal && t.Build.Result != result {
		return p.BuildMapping{}, errors.New("landed differs from assessed composition")
	}
	if terminal && l.Build.Result != result {
		return p.BuildMapping{}, errors.New("landed terminal mismatch")
	}
	return l, nil
}

// RecoverProvenance runs before admission (including after a restart). The
// executor lands the exact tested composition, or the unmodified final head for
// fast-forward. T retains originals and replay targets; Landed fixes the actual
// final commit. No current refs, Git acquisition or forge reads are consulted.
func (s *Store) RecoverProvenance(ctx context.Context, q QueueID) error {
	var seed queue.State
	if err := s.pool.QueryRow(ctx, `SELECT base, COALESCE(initial_fence,'') FROM queues WHERE id=$1`, q).Scan(&seed.BaseBranch, &seed.Fence); err != nil {
		return err
	}
	r, _, err := replayJournal(ctx, s.pool, q, seed, 0, true)
	if err != nil {
		return err
	}
	for _, outcomeID := range r.Provenance.AwaitingLandings() {
		var raw []byte
		if err := s.pool.QueryRow(ctx, `SELECT payload FROM events WHERE queue_id=$1 AND id=$2`, q, outcomeID).Scan(&raw); err != nil {
			return err
		}
		event, err := journalreader.DecodeEvent(raw)
		if err != nil {
			return err
		}
		outcome, ok := event.Queue.(queue.Landed)
		if !ok {
			return errors.New("pending provenance outcome is not Landed")
		}
		var tested *pj.Landing
		for _, record := range r.Provenance.Records() {
			e, err := pj.DecodeEvent(record.Payload)
			if err != nil {
				return err
			}
			if t, ok := e.(pj.Landing); ok && t.Stage == "tested" && t.BatchID == outcome.BatchID {
				tested = &t
			}
		}
		if tested == nil {
			return errors.New("pending provenance tested evidence missing")
		}
		landed := *tested
		landed.Stage = "landed"
		var rebuilt []struct {
			member int
			chunks []pj.Observation
		}
		count := 1
		for i := range landed.Members {
			m := &landed.Members[i]
			m.Mapping, err = landedMapping(m.Mapping, outcome.NewBaseSHA, i == len(landed.Members)-1)
			if err != nil {
				return err
			}
			prior := r.Provenance.PreviousObservationID(m.Request)
			if prior != m.ObservationIDs[0] {
				inventory, ok := r.Provenance.Inventory(m.Request)
				if !ok {
					return errors.New("historical tested inventory missing")
				}
				h := inventory.Header
				h.Request.Generation = fmt.Sprintf("recover/%d/%d", outcomeID, prior)
				h.PreviousObservationID = prior
				h.Digest = pj.InventoryDigest(h, inventory.Commits)
				chunks := make([]pj.Observation, h.Chunks)
				// Historical canonical chunk framing is immutable. Reuse its exact
				// original partition while rebinding only generation/predecessor.
				for _, record := range r.Provenance.Records() {
					e, err := pj.DecodeEvent(record.Payload)
					if err != nil {
						return err
					}
					if old, ok := e.(pj.Observation); ok && reflect.DeepEqual(old.Request, m.Request) {
						chunk := h
						chunk.Index, chunk.Commits = old.Index, old.Commits
						chunks[old.Index] = chunk
					}
				}
				m.Request, m.InventoryDigest, m.Mapping.Request = h.Request, h.Digest, h.Request
				rebuilt = append(rebuilt, struct {
					member int
					chunks []pj.Observation
				}{i, chunks})
				count += len(chunks)
			}
			m.RecordDigest = pj.MemberDigest(*m)
		}
		_, err = s.RecoverLanded(ctx, q, int64(outcomeID), SideAppend{Count: count, Build: func(ids []uint64) ([]journalreader.Event, error) {
			companion := landed
			companion.Members = slices.Clone(landed.Members)
			events := make([]journalreader.Event, 0, count)
			for _, group := range rebuilt {
				m := &companion.Members[group.member]
				m.ObservationIDs = []uint64{}
				for _, h := range group.chunks {
					m.ObservationIDs = append(m.ObservationIDs, ids[len(events)])
					events = append(events, journalreader.Event{Provenance: h})
				}
				m.RecordDigest = pj.MemberDigest(*m)
			}
			return append(events, journalreader.Event{Provenance: companion}), nil
		}})
		var complete LandedAlreadyCompleteError
		if err != nil && !errors.As(err, &complete) {
			return err
		}
		r, _, err = replayJournal(ctx, s.pool, q, seed, 0, true)
		if err != nil {
			return err
		}
	}
	return nil
}
