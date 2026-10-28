package store

import (
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

// dependencyLocks maps each locked direct-source key to its stored queue.
type dependencyLocks = map[dependency.QueueKey]QueueID

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
// same replay, plus the journal recorded_at of each admission's consumed rows.
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
	admitted := map[int][]uint64{}
	heads := map[int]string{}
	var ids []int64
	for _, entry := range r.Queue.Entries {
		change, revision, ok := changeRevision(key, r.Queue, entry.PR, entry.HeadSHA)
		if !ok {
			continue // absent from the overlay: gated
		}
		admission := deps.Admission(string(change), revision)
		if admission.Code != "" {
			waits[entry.PR] = queue.DependencyWait{Head: entry.HeadSHA, Waiting: true}
			continue
		}
		admitted[entry.PR], heads[entry.PR] = admission.Events, entry.HeadSHA
		for _, id := range admission.Events {
			ids = append(ids, int64(id))
		}
	}
	recorded := map[uint64]time.Time{}
	if len(ids) > 0 {
		rows, err := db.Query(ctx, `SELECT id, recorded_at FROM events WHERE queue_id=$1 AND id = ANY($2)`, q, ids)
		if err != nil {
			return queue.State{}, err
		}
		for rows.Next() {
			var id int64
			var at time.Time
			if err := rows.Scan(&id, &at); err != nil {
				rows.Close()
				return queue.State{}, err
			}
			recorded[uint64(id)] = at
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return queue.State{}, err
		}
	}
	for pr, events := range admitted {
		var eligible time.Time
		for _, id := range events {
			at, ok := recorded[id]
			if !ok {
				return queue.State{}, journalreader.ErrReference
			}
			if at.After(eligible) {
				eligible = at
			}
		}
		waits[pr] = queue.DependencyWait{Head: heads[pr], EligibleAt: eligible}
	}
	return r.Queue.WithDependencyWaits(waits), nil
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
func (v DependencyStart) lock(ctx context.Context, s *Store, tx pgx.Tx, q QueueID) (dependencyLocks, error) {
	sources := dependencyLocks{}
	ids := []QueueID{q}
	for _, m := range v.Members {
		for _, source := range m.Sources {
			if _, ok := sources[source.Queue]; ok {
				continue
			}
			id, err := v.sourceQueue(ctx, tx, source.Queue)
			if err != nil {
				return nil, fmt.Errorf("%w: %w", ErrStale, err)
			}
			if id == q {
				return nil, fmt.Errorf("%w: %w", ErrStale, dependency.ErrOutOfScope)
			}
			sources[source.Queue] = id
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range slices.Compact(ids) {
		if err := s.lockQueue(ctx, tx, id); err != nil {
			return nil, err
		}
	}
	return sources, nil
}

// validate rechecks every member under the full lock set. The direct-source set
// is re-derived from the current declarations: any change restarts preparation.
func (v DependencyStart) validate(ctx context.Context, tx pgx.Tx, q QueueID, locked dependencyLocks, state queue.State, land queue.Land, start queue.LandingStarted, decisions bool) (func([]uint64, uint64) (SideAppend, error), error) {
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
	used := map[dependency.QueueKey]bool{}
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
			id, ok := locked[source.Queue]
			if !ok {
				return nil, fmt.Errorf("%w: unlocked direct source", ErrStale)
			}
			used[source.Queue] = true
			if err := v.settlement(ctx, tx, id, edge, source, self); err != nil {
				return nil, err
			}
		}
	}
	if len(used) != len(locked) {
		return nil, fmt.Errorf("%w: direct-source set changed", ErrStale)
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
				companion := dependency.DecisionRecorded{Namespace: v.Binding.Namespace, BatchID: land.BatchID, AttemptID: start.AttemptID, TestedSHA: land.TestedSHA, LandedSHA: batch.LandingCommit(), StartEventID: startID, Member: uint64(i), Members: uint64(len(v.Members)), Alias: admission.Alias, ChangeID: string(changes[i]), Revision: uint64(bindings[i].Revision), Head: land.PRs[i].HeadSHA, Admission: admission.Basis, Sources: slices.Clone(m.Sources)}
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

// settlement checks one direct source under its queue lock: bilateral consent
// in the source's own policy companion, an unchanged journal base since the
// fresh ancestry read, and an accepted Landed outcome for the exact source
// change revision/head. No forge I/O occurs while these locks are held.
func (v DependencyStart) settlement(ctx context.Context, tx pgx.Tx, id QueueID, edge dependency.DeclaredEdge, source dependency.SourceSettlement, self dependency.QueueKey) error {
	var seed queue.State
	if err := tx.QueryRow(ctx, `SELECT base,COALESCE(initial_fence,'') FROM queues WHERE id=$1`, id).Scan(&seed.BaseBranch, &seed.Fence); err != nil {
		return err
	}
	current, watermark, err := replayJournal(ctx, tx, id, seed, 0, true)
	if err != nil {
		return err
	}
	if uint64(watermark) < source.Watermark || current.Queue.BaseSHA != source.BaseSHA {
		return fmt.Errorf("%w: direct source base drifted since the landing-start read", ErrStale)
	}
	deps, err := current.BoundDependencies(v.Binding)
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
	if err := tx.QueryRow(ctx, `SELECT payload FROM events WHERE queue_id=$1 AND id=$2`, id, int64(source.SettlementEventID)).Scan(&raw); err != nil {
		return fmt.Errorf("%w: dependency-unverified: %v", ErrStale, err)
	}
	event, err := journalreader.DecodeEvent(raw)
	if err != nil {
		return err
	}
	outcome, ok := event.Queue.(queue.Landed)
	if !ok || outcome.NewBaseSHA != source.Commit || source.SettlementEventID > uint64(watermark) {
		return fmt.Errorf("%w: dependency-unverified", ErrStale)
	}
	before, _, err := replayJournal(ctx, tx, id, seed, int64(source.SettlementEventID)-1, true)
	if err != nil {
		return err
	}
	b := before.Queue.Batch(outcome.BatchID)
	if b == nil || b.Phase != queue.Landing {
		return fmt.Errorf("%w: dependency-unverified", ErrStale)
	}
	if after := queue.Apply(before.Queue, outcome); after.Batch(outcome.BatchID) != nil || after.BaseSHA != outcome.NewBaseSHA {
		return fmt.Errorf("%w: dependency-unverified", ErrStale)
	}
	key, err := storedKey(ctx, tx, id)
	if err != nil {
		return err
	}
	bindings := before.Queue.BatchRevisions(b.ID)
	for i, pr := range b.PRs {
		change, _, ok := changeRevision(key, before.Queue, pr.PR, pr.HeadSHA)
		if ok && i < len(bindings) && string(change) == ref.ChangeID && uint64(bindings[i].Revision) == source.Revision.Revision && pr.HeadSHA == source.Revision.Head && bindings[i].HeadSHA == pr.HeadSHA {
			return nil
		}
	}
	return fmt.Errorf("%w: dependency-unverified", ErrStale)
}
