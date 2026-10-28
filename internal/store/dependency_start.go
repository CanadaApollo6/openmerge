package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/journalreader"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// ADR 0011 slice 4a (Hold). Activation is test-seam only: the public loader
// still rejects dependency configuration, and only the private serve seam may
// install this context value (journalreader writer guard).
type dependencyActivationKey struct{}
type dependencySkipFormationClockKey struct{}
type dependencyClockPreparationKey struct{}
type dependencyClockPreparation struct {
	queue    QueueID
	through  uint64
	entries  []queue.Entry
	clocks   map[int]time.Time
	computed bool
}

// dependencyLocks maps each locked direct-source key to its stored queue.
type dependencyLocks = map[dependency.QueueKey]QueueID

// dependencyLockSet is what lock() acquired, plus each direct source's
// settlement facts verified once, before any lock was taken.
type dependencyLockSet struct {
	queues  dependencyLocks
	settled map[settlementKey]settledBatch
}

type settlementKey struct {
	queue QueueID
	event uint64
}

// settledBatch is an accepted Landed outcome and the members its batch bound
// immediately before it. raw is the settlement row, re-read under the lock.
type settledBatch struct {
	raw     []byte
	outcome queue.Landed
	members []settledMember
}

type settledMember struct {
	change   queue.ChangeID
	revision uint64
	head     string
}

type dependencyActivation struct {
	queue   QueueID
	binding dependency.JournalInitialized
}

// WithDependencyActivation installs the planner gate for one queue's loads in
// ctx. The binding must be independently verified identity, never a snapshot's.
// Without it every load, plan and start is byte-identical legacy behavior.
func WithDependencyActivation(ctx context.Context, q QueueID, binding dependency.JournalInitialized) context.Context {
	return context.WithValue(ctx, dependencyActivationKey{}, dependencyActivation{q, binding})
}

// withDependencyWaits derives the planner overlay from recorded facts in the
// same replay, plus accumulated eligible intervals from recorded history.
// A wrong or absent binding fails the load closed; nothing is inferred.
func withDependencyWaits(ctx context.Context, db journalQuery, q QueueID, r journalreader.Reader) (queue.State, error) {
	a, ok := ctx.Value(dependencyActivationKey{}).(dependencyActivation)
	if !ok || a.queue != q {
		return r.Queue, nil
	}
	deps, err := r.BoundDependencies(a.binding)
	if err != nil {
		return queue.State{}, fmt.Errorf("dependency activation: %w", err)
	}
	key, err := storedKey(ctx, db, q)
	if err != nil {
		return queue.State{}, err
	}
	waits := map[int]queue.DependencyWait{}
	admitted := false
	for _, entry := range r.Queue.Entries {
		change, revision, known := changeRevision(key, r.Queue, entry.PR, entry.HeadSHA)
		waiting := !known || deps.Admission(string(change), revision).Code != ""
		waits[entry.PR] = queue.DependencyWait{Head: entry.HeadSHA, Waiting: waiting}
		admitted = admitted || !waiting && r.Queue.BatchForPR(entry.PR) == nil
	}
	if admitted && ctx.Value(dependencySkipFormationClockKey{}) == nil {
		var clocks map[int]time.Time
		if prepared, ok := ctx.Value(dependencyClockPreparationKey{}).(dependencyClockPreparation); ok {
			if !prepared.computed || prepared.queue != q || prepared.through != deps.Through() || !sameDependencyClockEntries(prepared.entries, r.Queue.Entries) {
				return queue.State{}, ErrStale
			}
			clocks = prepared.clocks
		} else {
			clocks, err = dependencyEligibleClocks(ctx, db, q, r.Queue, key)
			if err != nil {
				return queue.State{}, err
			}
		}
		for pr, w := range waits {
			if !w.Waiting {
				w.EligibleAt = clocks[pr]
				waits[pr] = w
			}
		}
	}

	return r.Queue.WithDependencyWaits(waits), nil
}

// dependencyWithheld returns the record that folds the activated planner's
// prune of gated pending members before this start, if any. ok is false when
// the fold would still refuse the start, which then cancels as stale. Without
// the overlay nothing is withheld and legacy starts are unchanged.
func dependencyWithheld(state queue.State, event queue.Event) (*queue.DependencyWaitsWithheld, bool) {
	var id string
	switch start := event.(type) {
	case queue.BatchStarted:
		id = start.BatchID
	case queue.ChainedBatchStarted:
		id = start.BatchID
	default:
		return nil, true
	}
	members := state.DependencyWithheld()
	if len(members) == 0 {
		return nil, true
	}
	withheld := queue.DependencyWaitsWithheld{PRs: members}
	return &withheld, queue.Apply(queue.Apply(state, withheld), event).Batch(id) != nil
}

func insertWithheld(ctx context.Context, tx pgx.Tx, q QueueID, withheld *queue.DependencyWaitsWithheld) error {
	if withheld == nil {
		return nil
	}
	name, payload, err := queue.EncodeEvent(*withheld)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO events(queue_id, type, payload) VALUES ($1, $2, $3::jsonb)`, q, name, string(payload))
	return err
}

// dependencyEligibleClocks accumulates eligible intervals, rather than restarting
// max_wait after a source rewind. This activated-only bounded history read is
// independent of disposable cursors, so restarts preserve the remaining budget.
// Queue-only loads take none of these reads. Hitting the explicit acquisition
// bound fails closed; it never returns a partial, permissive clock.
func dependencyEligibleClocks(ctx context.Context, db journalQuery, q QueueID, state queue.State, key QueueKey) (map[int]time.Time, error) {
	const historyLimit = 32768
	rows, err := db.Query(ctx, `SELECT id,payload,recorded_at FROM events WHERE queue_id=$1 AND type=ANY($2) ORDER BY id LIMIT $3`, q, dependency.ClockEventKinds(), historyLimit+1)
	if err != nil {
		return nil, err
	}
	type record struct {
		id  uint64
		raw []byte
		at  time.Time
	}
	defer rows.Close()
	type clock struct {
		start    time.Time
		elapsed  time.Duration
		change   queue.ChangeID
		revision uint64
	}
	clocks := map[int]clock{}
	for _, e := range state.Entries {
		change, revision, ok := changeRevision(key, state, e.PR, e.HeadSHA)
		if ok {
			clocks[e.PR] = clock{change: change, revision: revision}
		}
	}
	var deps dependency.State
	count, bytes := 0, 0
	var lastAt time.Time
	for rows.Next() {
		var r record
		if err := rows.Scan(&r.id, &r.raw, &r.at); err != nil {
			return nil, err
		}
		count++
		bytes += len(r.raw)
		if count > historyLimit || bytes > 32<<20 {
			return nil, dependency.ErrLimit
		}
		if r.at.Before(lastAt) {
			r.at = lastAt
		}
		lastAt = r.at
		event, err := dependency.DecodeEvent(r.raw)
		if err != nil {
			return nil, err
		}
		deps, err = dependency.Apply(deps, r.id, event)
		if err != nil {
			return nil, err
		}
		for _, entry := range state.Entries {
			c, ok := clocks[entry.PR]
			if !ok {
				continue
			}
			at := r.at
			if at.Before(entry.EnqueuedAt) {
				at = entry.EnqueuedAt
			}
			eligible := deps.Admission(string(c.change), c.revision).Code == ""
			if eligible && c.start.IsZero() {
				c.start = at
			}
			if !eligible && !c.start.IsZero() {
				c.elapsed += max(time.Duration(0), at.Sub(c.start))
				c.start = time.Time{}
			}
			clocks[entry.PR] = c
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := map[int]time.Time{}
	for pr, c := range clocks {
		if !c.start.IsZero() {
			out[pr] = c.start.Add(-c.elapsed)
		}
	}
	return out, nil
}

func storedKey(ctx context.Context, db journalQuery, q QueueID) (QueueKey, error) {
	var key QueueKey
	err := db.QueryRow(ctx, `SELECT installation, repository, base, lane FROM queues WHERE id=$1`, q).Scan(&key.Installation, &key.Repository, &key.BaseBranch, &key.Lane)
	return key, err
}

// changeRevision binds a PR's current head to its ADR 0007 identity. Legacy
// observations carry no ChangeID; derive it from the stored canonical key, never
// from a discovery alias, exactly as ChangeRevisionObserved validation does.
func changeRevision(key QueueKey, state queue.State, pr int, head string) (queue.ChangeID, uint64, bool) {
	rev, ok := state.PRRevision(pr)
	if !ok || rev.HeadSHA != head || rev.Revision == 0 {
		return "", 0, false
	}
	derived, err := key.ChangeID(pr)
	if err != nil || rev.ChangeID != "" && rev.ChangeID != derived {
		return "", 0, false
	}
	return derived, uint64(rev.Revision), true
}

// DependencyStart is supplied only by the private serve seam after all forge
// reads. Members follow the batch order; each member's Observations and Sources
// follow its declaration's canonical edge order. The transaction re-derives the
// direct-source set from the current declarations and refuses any difference.
type DependencyStart struct {
	Binding dependency.JournalInitialized
	Members []DependencyMemberStart
}

// DependencyMemberStart carries one member's fresh landing-start reads.
// PolicyCommit/PolicyDigest are the fresh default-branch governing read; a
// mismatch with the recorded companion (including a revoked single-maintainer
// opt-in) refuses the start until the new policy companion is recorded.
type DependencyMemberStart struct {
	Observations []dependency.Observed
	Sources      []dependency.SourceSettlement
	PolicyCommit string
	PolicyDigest string
}

// StartDependencyLanding is the sanctioned activated dependency writer. It
// commits fresh eligibility, decisions, unchanged LandingStarted, landing-start
// observations and one decision companion per member in ONE transaction.
func (s *Store) StartDependencyLanding(ctx context.Context, claim ClaimedAction, cfg queue.Config, policy cp.Policy, fresh []queue.ApprovalEligibilityObserved, decisions []queue.LandingDecisionRecorded, start queue.LandingStarted, evidence DependencyStart, checks ...queue.LandingChecksRecorded) error {
	// Formation clocks cannot affect authorization of an already-built Land.
	// Do not scan clock history under B plus its direct-source locks.
	ctx = context.WithValue(ctx, dependencySkipFormationClockKey{}, true)
	return retryLandingDecision(s.startLandingTransaction(ctx, claim, cfg, policy, fresh, decisions, start, s.authorizeStart, nil, nil, &evidence, checks...))
}

func (v DependencyStart) queueKey(repository, base, lane string) (dependency.QueueKey, error) {
	_, origin, ok := strings.Cut(v.Binding.ForgeInstance, ":")
	if !ok {
		return dependency.QueueKey{}, dependency.ErrOutOfScope
	}
	return dependency.QueueKey{Repository: origin + "/" + repository, Base: base, Lane: lane}, nil
}

// sourceQueue resolves an exact stored canonical key in this installation.
func (v DependencyStart) sourceQueue(ctx context.Context, tx pgx.Tx, key dependency.QueueKey) (QueueID, error) {
	path, err := dependency.RepositoryPath(v.Binding, key.Repository)
	if err != nil {
		return 0, err
	}
	var id QueueID
	err = tx.QueryRow(ctx, `SELECT id FROM queues WHERE installation=$1 AND repository=$2 AND base=$3 AND lane=$4`, v.Binding.Installation, path, key.Base, key.Lane).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, dependency.ErrOutOfScope
	}
	return id, err
}

// lock acquires B plus every supplied direct source in ascending queue-ID
// order, the canonical order of the existing multi-queue lock paths. It runs
// before any other lock in this transaction and performs no forge I/O.
//
// Before taking any lock it verifies each distinct source settlement exactly
// once (verifySettlement). That journal prefix is immutable once the settlement
// row is committed: every insert into a queue's journal holds that queue's
// advisory lock through commit, so no lower ID can appear later. Under the
// locks, settlement() only re-reads the row and checks the current source fold.
func (v DependencyStart) lock(ctx context.Context, s *Store, tx pgx.Tx, q QueueID) (dependencyLockSet, error) {
	set := dependencyLockSet{queues: dependencyLocks{}, settled: map[settlementKey]settledBatch{}}
	ids := []QueueID{q}
	for _, m := range v.Members {
		for _, source := range m.Sources {
			id, ok := set.queues[source.Queue]
			if !ok {
				var err error
				if id, err = v.sourceQueue(ctx, tx, source.Queue); err != nil {
					return set, fmt.Errorf("%w: %w", ErrStale, err)
				}
				if id == q {
					return set, fmt.Errorf("%w: %w", ErrStale, dependency.ErrOutOfScope)
				}
				set.queues[source.Queue] = id
				ids = append(ids, id)
			}
			key := settlementKey{id, source.SettlementEventID}
			if _, ok := set.settled[key]; ok {
				continue
			}
			settled, err := verifySettlement(ctx, tx, id, source.SettlementEventID)
			if err != nil {
				return set, err
			}
			set.settled[key] = settled
		}
	}
	slices.Sort(ids)
	for _, id := range slices.Compact(ids) {
		if err := s.lockQueue(ctx, tx, id); err != nil {
			return set, err
		}
	}
	return set, nil
}

// verifySettlement proves, without any lock, that event is an accepted Landed
// outcome in source queue id: replaying the immutable prefix before it, the
// batch was landing and the fold accepts the outcome. It records the batch's
// member bindings at that point. This is the only prefix replay per settlement.
func verifySettlement(ctx context.Context, tx pgx.Tx, id QueueID, event uint64) (settledBatch, error) {
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT payload FROM events WHERE queue_id=$1 AND id=$2`, id, int64(event)).Scan(&raw); err != nil {
		return settledBatch{}, fmt.Errorf("%w: dependency-unverified: %v", ErrStale, err)
	}
	decoded, err := journalreader.DecodeEvent(raw)
	if err != nil {
		return settledBatch{}, err
	}
	outcome, ok := decoded.Queue.(queue.Landed)
	if !ok {
		return settledBatch{}, fmt.Errorf("%w: dependency-unverified", ErrStale)
	}
	var seed queue.State
	if err := tx.QueryRow(ctx, `SELECT base,COALESCE(initial_fence,'') FROM queues WHERE id=$1`, id).Scan(&seed.BaseBranch, &seed.Fence); err != nil {
		return settledBatch{}, err
	}
	before, _, err := replayJournal(ctx, tx, id, seed, int64(event)-1, true)
	if err != nil {
		return settledBatch{}, err
	}
	b := before.Queue.Batch(outcome.BatchID)
	if b == nil || b.Phase != queue.Landing {
		return settledBatch{}, fmt.Errorf("%w: dependency-unverified", ErrStale)
	}
	if after := queue.Apply(before.Queue, outcome); after.Batch(outcome.BatchID) != nil || after.BaseSHA != outcome.NewBaseSHA {
		return settledBatch{}, fmt.Errorf("%w: dependency-unverified", ErrStale)
	}
	key, err := storedKey(ctx, tx, id)
	if err != nil {
		return settledBatch{}, err
	}
	settled := settledBatch{raw: raw, outcome: outcome}
	bindings := before.Queue.BatchRevisions(b.ID)
	for i, pr := range b.PRs {
		change, _, ok := changeRevision(key, before.Queue, pr.PR, pr.HeadSHA)
		if ok && i < len(bindings) && bindings[i].HeadSHA == pr.HeadSHA {
			settled.members = append(settled.members, settledMember{change, uint64(bindings[i].Revision), pr.HeadSHA})
		}
	}
	return settled, nil
}

// validate rechecks every member under the full lock set. The direct-source set
// is re-derived from the current declarations: any change restarts preparation.
func (v DependencyStart) validate(ctx context.Context, tx pgx.Tx, q QueueID, locked dependencyLockSet, state queue.State, land queue.Land, start queue.LandingStarted, decisions bool) (func([]uint64, uint64) (SideAppend, error), error) {
	var seed queue.State
	var repository, lane string
	if err := tx.QueryRow(ctx, `SELECT base,COALESCE(initial_fence,''),repository,lane FROM queues WHERE id=$1`, q).Scan(&seed.BaseBranch, &seed.Fence, &repository, &lane); err != nil {
		return nil, err
	}
	self, err := v.queueKey(repository, seed.BaseBranch, lane)
	if err != nil {
		return nil, err
	}
	reader, watermark, err := replayJournal(ctx, tx, q, seed, 0, true)
	if err != nil {
		return nil, err
	}
	// RecoverLanded must drain accepted outcomes before any new admission.
	if len(reader.Provenance.AwaitingLandings()) != 0 {
		return nil, errors.New("landed recovery required before admission")
	}
	deps, err := reader.BoundDependencies(v.Binding)
	if err != nil {
		return nil, err
	}
	batch := state.Batch(land.BatchID)
	bindings := state.BatchRevisions(land.BatchID)
	composition, _, composed := deps.Composition(land.BatchID)
	if !composed {
		return nil, ErrStale
	}

	if batch == nil || batch.Phase != queue.Testing || len(v.Members) != len(land.PRs) || len(bindings) != len(land.PRs) {
		return nil, ErrStale
	}
	policy, _, ok := deps.QueuePolicy(self)
	if !ok || !policy.Enabled {
		return nil, dependency.ErrOutOfScope
	}
	stored, err := storedKey(ctx, tx, q)
	if err != nil {
		return nil, err
	}
	changes := make([]queue.ChangeID, len(land.PRs))
	sources := map[QueueID]sourceFold{}
	for i, member := range v.Members {
		change, revision, ok := changeRevision(stored, state, land.PRs[i].PR, land.PRs[i].HeadSHA)
		if !ok || bindings[i].HeadSHA != land.PRs[i].HeadSHA || uint64(bindings[i].Revision) != revision {
			return nil, ErrStale
		}
		changes[i] = change
		edges, _, ok := deps.Edges(string(change), revision)
		if !ok {
			return nil, fmt.Errorf("%w: dependency-unresolved", dependency.ErrNotAdmitted)
		}
		if member.PolicyCommit != policy.Commit || member.PolicyDigest != policy.Digest {
			return nil, fmt.Errorf("%w: governing dependency policy changed", ErrStale)
		}
		if len(member.Observations) != len(edges) || len(member.Sources) != len(edges) {
			return nil, fmt.Errorf("%w: direct-source set changed", ErrStale)
		}
		for j, edge := range edges {
			source := member.Sources[j]
			if member.Observations[j].Source != edge.Reference || member.Observations[j].Result != "landed" || source.Reference != edge.Reference || source.Queue != edge.Queue {
				return nil, fmt.Errorf("%w: direct-source set changed", ErrStale)
			}
			id, ok := locked.queues[source.Queue]
			settled, verified := locked.settled[settlementKey{id, source.SettlementEventID}]
			if !ok || !verified {
				return nil, fmt.Errorf("%w: unlocked direct source", ErrStale)
			}
			current, ok := sources[id]
			if !ok {
				if current, err = foldSource(ctx, tx, id); err != nil {
					return nil, err
				}
				sources[id] = current
			}
			if err := v.settlement(ctx, tx, id, edge, source, self, current, settled); err != nil {
				return nil, err
			}
		}
	}
	// Fold the supplied reads at placeholder positions so a hold, CAS race or
	// non-landed read refuses before any insert, with its typed code.
	simulated, position := deps, uint64(watermark)
	for i, member := range v.Members {
		for _, o := range member.Observations {
			position++
			if simulated, err = dependency.Apply(simulated, position, o); err != nil {
				return nil, err
			}
		}
		if a := simulated.Admission(string(changes[i]), uint64(bindings[i].Revision)); a.Code != "" {
			return nil, fmt.Errorf("%w: %s", dependency.ErrNotAdmitted, a.Code)
		}
	}
	if !simulated.CompositionValid(composition) || batch.InvalidationReason != "" {
		return nil, fmt.Errorf("%w: dependency-changed", ErrStale)
	}
	return func(decisionIDs []uint64, startID uint64) (SideAppend, error) {
		if startID == 0 || decisions && len(decisionIDs) != len(v.Members) || !decisions && len(decisionIDs) != 0 {
			return SideAppend{}, errors.New("dependency start binding mismatch")
		}
		count := len(v.Members)
		for _, m := range v.Members {
			count += len(m.Observations)
		}
		return SideAppend{Count: count, DedupeKey: "dependency/decision/" + land.BatchID + "/" + fmt.Sprint(startID), Binding: v.Binding, startID: startID, Build: func(ids []uint64) ([]journalreader.Event, error) {
			out := make([]journalreader.Event, 0, count)
			next := deps
			for i, m := range v.Members {
				for _, o := range m.Observations {
					var err error
					if next, err = dependency.Apply(next, ids[len(out)], o); err != nil {
						return nil, err
					}
					out = append(out, journalreader.Event{Dependency: o})
				}
				admission := next.Admission(string(changes[i]), uint64(bindings[i].Revision))
				if admission.Code != "" {
					return nil, fmt.Errorf("%w: %s", dependency.ErrNotAdmitted, admission.Code)
				}
				companion := dependency.DecisionRecorded{Namespace: v.Binding.Namespace, BatchID: land.BatchID, AttemptID: start.AttemptID, TestedSHA: land.TestedSHA, LandedSHA: batch.LandingCommit(), StartEventID: startID, Member: uint64(i), Members: uint64(len(v.Members)), Alias: admission.Alias, ChangeID: string(changes[i]), Revision: uint64(bindings[i].Revision), Head: land.PRs[i].HeadSHA, Admission: admission.Basis, Sources: append([]dependency.SourceSettlement{}, m.Sources...)}
				if decisions {
					companion.DecisionID = decisionIDs[i]
				}
				var err error
				if next, err = dependency.Apply(next, ids[len(out)], companion); err != nil {
					return nil, err
				}
				out = append(out, journalreader.Event{Dependency: companion})
			}
			return out, nil
		}}, nil
	}, nil
}

// dependencyStale maps a fold refusal of the start bundle to a retryable stale
// admission, rolling back every insert in the transaction.
func dependencyStale(err error) error {
	if errors.Is(err, dependency.ErrStale) || errors.Is(err, dependency.ErrNotAdmitted) {
		return fmt.Errorf("%w: %w", ErrStale, err)
	}
	return err
}

// sourceFold is one locked direct source's current journal fold, replayed once
// per start from its checkpoint however many edges or members name it.
type sourceFold struct {
	reader    journalreader.Reader
	watermark uint64
}

func foldSource(ctx context.Context, tx pgx.Tx, id QueueID) (sourceFold, error) {
	var seed queue.State
	if err := tx.QueryRow(ctx, `SELECT base,COALESCE(initial_fence,'') FROM queues WHERE id=$1`, id).Scan(&seed.BaseBranch, &seed.Fence); err != nil {
		return sourceFold{}, err
	}
	reader, watermark, err := replayJournal(ctx, tx, id, seed, 0, true)
	return sourceFold{reader, uint64(watermark)}, err
}

// settlement checks one direct source under its queue lock: bilateral consent
// in the source's own policy companion, an unchanged journal base since the
// fresh ancestry read, and the pre-verified accepted Landed outcome for the
// exact source change revision/head, whose row must still be byte-identical.
// No forge I/O and no prefix replay occur while these locks are held.
func (v DependencyStart) settlement(ctx context.Context, tx pgx.Tx, id QueueID, edge dependency.DeclaredEdge, source dependency.SourceSettlement, self dependency.QueueKey, current sourceFold, settled settledBatch) error {
	if current.watermark < source.Watermark || current.reader.Queue.BaseSHA != source.BaseSHA {
		return fmt.Errorf("%w: direct source base drifted since the landing-start read", ErrStale)
	}
	deps, err := current.reader.BoundDependencies(v.Binding)
	if err != nil {
		return err
	}
	consent, _, ok := deps.QueuePolicy(source.Queue)
	if !ok || !consent.Enabled || !slices.Contains(consent.Policy.AllowTo, self.Repository) {
		return dependency.ErrOutOfScope
	}
	ref, err := dependency.ParseReference(edge.Reference, dependency.Scope{ForgeInstance: v.Binding.ForgeInstance, Installation: v.Binding.Installation, JournalNamespace: v.Binding.Namespace})
	if err != nil {
		return err
	}
	if ref.Revision > 0 && (ref.Revision != source.Revision.Revision || edge.PinnedHead != source.Revision.Head) {
		return fmt.Errorf("%w: dependency-stale", ErrStale)
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT payload FROM events WHERE queue_id=$1 AND id=$2`, id, int64(source.SettlementEventID)).Scan(&raw); err != nil || !bytes.Equal(raw, settled.raw) {
		return fmt.Errorf("%w: dependency-unverified: %v", ErrStale, err)
	}
	if settled.outcome.NewBaseSHA != source.Commit || source.SettlementEventID > current.watermark {
		return fmt.Errorf("%w: dependency-unverified", ErrStale)
	}
	for _, m := range settled.members {
		if string(m.change) == ref.ChangeID && m.revision == source.Revision.Revision && m.head == source.Revision.Head {
			return nil
		}
	}
	return fmt.Errorf("%w: dependency-unverified", ErrStale)
}

// dependencyComposition prepares a companion from this transaction's admitted
// batch and recorded dependency facts. It performs no source locks or I/O.
func dependencyComposition(ctx context.Context, tx pgx.Tx, q QueueID, event queue.Event, startID uint64) (*SideAppend, error) {
	activation, ok := ctx.Value(dependencyActivationKey{}).(dependencyActivation)
	if !ok || activation.queue != q {
		return nil, nil
	}
	var batchID string
	switch e := event.(type) {
	case queue.BatchStarted:
		batchID = e.BatchID
	case queue.ChainedBatchStarted:
		batchID = e.BatchID
	default:
		return nil, nil
	}
	current, err := foldSource(ctx, tx, q)
	if err != nil {
		return nil, err
	}
	deps, err := current.reader.BoundDependencies(activation.binding)
	if err != nil {
		return nil, err
	}
	b := current.reader.Queue.Batch(batchID)
	if b == nil {
		return nil, ErrStale
	}
	key, err := storedKey(ctx, tx, q)
	if err != nil {
		return nil, err
	}
	c := dependency.CompositionRecorded{Namespace: activation.binding.Namespace, BatchID: b.ID, BaseSHA: b.BaseSHA, StartEventID: startID, Members: []dependency.CompositionMember{}}
	for _, pr := range b.PRs {
		change, revision, known := changeRevision(key, current.reader.Queue, pr.PR, pr.HeadSHA)
		if !known {
			return nil, ErrStale
		}
		m, err := deps.CompositionMember(string(change), revision, pr.HeadSHA)
		if err != nil {
			return nil, err
		}
		c.Members = append(c.Members, m)
	}
	return &SideAppend{buildStartID: startID, Count: 1, DedupeKey: "dependency/composition/" + batchID, Binding: activation.binding, Build: func([]uint64) ([]journalreader.Event, error) { return []journalreader.Event{{Dependency: c}}, nil }}, nil
}

// dependencyInvalidation replaces only the activated dependency discard. The
// exact planned suffix is revalidated under B's lock before any insert.
func dependencyInvalidation(ctx context.Context, tx pgx.Tx, q QueueID, event queue.Event) (queue.Event, error) {
	if _, supplied := event.(queue.DependencyCompositionsInvalidated); supplied {
		return nil, ErrStale
	}
	e, ok := event.(queue.BatchesInvalidated)
	if !ok || e.Reason != "dependency-changed" {
		return event, nil
	}
	activation, ok := ctx.Value(dependencyActivationKey{}).(dependencyActivation)
	if !ok || activation.queue != q {
		return nil, ErrStale
	}
	current, err := foldSource(ctx, tx, q)
	if err != nil {
		return nil, err
	}
	deps, err := current.reader.BoundDependencies(activation.binding)
	if err != nil {
		return nil, err
	}
	expected := current.reader.Queue.DependencyInvalidationTargets()
	if !slices.Equal(e.BatchIDs, expected) {
		return nil, ErrStale
	}
	out := queue.DependencyCompositionsInvalidated{BatchIDs: slices.Clone(e.BatchIDs), CompositionIDs: []uint64{}, CauseEventIDs: []uint64{}, Reason: e.Reason}
	for _, batch := range e.BatchIDs {
		_, id, ok := deps.Composition(batch)
		if !ok {
			return nil, ErrStale
		}
		out.CompositionIDs = append(out.CompositionIDs, id)
		out.CauseEventIDs = append(out.CauseEventIDs, deps.CompositionCause(batch))
	}
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return out, nil
}

// DependencyCascade rebuilds the reverse scheduling aid from an explicit set of
// enrolled queues at one read boundary. No cache absence is used as evidence.
// It takes no queue locks, emits no events and never enqueues a source. The
// private activation caller may wake these consumers; ordinary polling remains
// the repair path. A wake only schedules a fresh direct-edge reconciliation.
func (s *Store) DependencyCascade(ctx context.Context, queues []QueueID, binding dependency.JournalInitialized, source dependency.Reference) ([]QueueID, error) {
	if len(queues) == 0 || len(queues) > dependency.MaxNodes {
		return nil, dependency.ErrLimit
	}
	var out []QueueID
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		states := []dependency.State{}
		consumers := map[string]QueueID{}
		for _, q := range queues {
			current, err := foldSource(ctx, tx, q)
			if err != nil {
				return err
			}
			deps, err := current.reader.BoundDependencies(binding)
			if err != nil {
				return err
			}
			states = append(states, deps)
			// Only command-admitted entries may schedule graph work.
			key, err := storedKey(ctx, tx, q)
			if err != nil {
				return err
			}
			for _, entry := range current.reader.Queue.Entries {
				change, _, known := changeRevision(key, current.reader.Queue, entry.PR, entry.HeadSHA)
				if known {
					consumers[string(change)] = q
				}
			}
		}
		reverse, err := dependency.RebuildReverseScope(states, binding)
		if err != nil {
			return err
		}
		changes, err := reverse.Cascade(source)
		if err != nil {
			return err
		}
		for _, change := range changes {
			if q, ok := consumers[change]; ok && !slices.Contains(out, q) {
				out = append(out, q)
			}
		}
		slices.Sort(out)
		return nil
	})
	return out, err
}

func sameDependencyClockEntries(a, b []queue.Entry) bool {
	if len(a) != len(b) {
		return false
	}
	for i, e := range a {
		other := b[i]
		if e.PR != other.PR || e.AdmissionID != other.AdmissionID || e.HeadSHA != other.HeadSHA || !e.EnqueuedAt.Equal(other.EnqueuedAt) {
			return false
		}
	}
	return true
}

// prepareDependencyClocks performs history verification before any advisory or
// action-row lock. The locked load compares the exact side position and entry
// identities and requires a computed clock for any eligible unbatched entry.
// Drift retries rather than rescanning history while holding locks.
func (s *Store) prepareDependencyClocks(ctx context.Context, q QueueID, cfg queue.Config) (context.Context, error) {
	if cfg.BatchMaxSize <= 1 || cfg.BatchMaxWait <= 0 {
		return withoutDependencyFormationClock(ctx, q), nil
	}
	activation, ok := ctx.Value(dependencyActivationKey{}).(dependencyActivation)
	if !ok || activation.queue != q || ctx.Value(dependencySkipFormationClockKey{}) != nil {
		return ctx, nil
	}
	var prepared dependencyClockPreparation
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		current, err := foldSource(ctx, tx, q)
		if err != nil {
			return err
		}
		deps, err := current.reader.BoundDependencies(activation.binding)
		if err != nil {
			return err
		}
		key, err := storedKey(ctx, tx, q)
		if err != nil {
			return err
		}
		prepared = dependencyClockPreparation{queue: q, through: deps.Through(), entries: slices.Clone(current.reader.Queue.Entries), clocks: map[int]time.Time{}}
		for _, e := range prepared.entries {
			change, revision, known := changeRevision(key, current.reader.Queue, e.PR, e.HeadSHA)
			if known && current.reader.Queue.BatchForPR(e.PR) == nil && deps.Admission(string(change), revision).Code == "" {
				prepared.computed = true
				prepared.clocks, err = dependencyEligibleClocks(ctx, tx, q, current.reader.Queue, key)
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, dependencyClockPreparationKey{}, prepared), nil
}

// withoutDependencyFormationClock keeps non-build actions on recorded gates
// without acquiring formation-clock history that cannot affect their authority.
func withoutDependencyFormationClock(ctx context.Context, q QueueID) context.Context {
	a, ok := ctx.Value(dependencyActivationKey{}).(dependencyActivation)
	if !ok || a.queue != q {
		return ctx
	}
	return context.WithValue(ctx, dependencySkipFormationClockKey{}, true)
}
