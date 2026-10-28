package store

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// addDepPR enqueues another PR in d's queue with an empty (dependency-free)
// declaration, so only explicit edges make it wait.
func addDepPR(t *testing.T, s *Store, d depQueue, pr int, head byte) depQueue {
	t.Helper()
	change, err := queue.PRChangeID("7", strings.TrimPrefix(d.key.Repository, "https://github.com/"), "main", "", pr)
	must(t, err)
	m := d
	m.pr, m.head, m.change, m.alias.Number = pr, sha(head), change, uint64(pr)
	appendEvents(t, s, d.id, queue.PRObserved{PR: pr, HeadSHA: m.head, Eligible: true, Trusted: true}, queue.ChangeRevisionObserved{PR: pr, ChangeID: change, Revision: 1, HeadSHA: m.head, Basis: queue.RevisionObservationBasis{BaseSHA: d.base, At: time.Now()}}, queue.EnqueueRequested{PR: pr, At: time.Now()})
	return m
}

// A failed four-member batch leaves pending groups [7,8],[9,10]. Members that
// then wait on an unobserved dependency are withheld by a journaled record in
// the start transaction, so the activated planner's start folds both live and
// on replay without the overlay, at depths 1 and 4, including a fully gated
// first group. Deleting the record reproduces the review livelock.
func TestDependencyWaitsWithheldStartFoldsThroughStore(t *testing.T) {
	for _, depth := range []int{1, 4} {
		for name, tc := range map[string]struct {
			waiting []int
			want    []int
		}{"partial first group": {[]int{7}, []int{8}}, "fully gated first group": {[]int{7, 8}, []int{9, 10}}} {
			t.Run(fmt.Sprint(depth, "/", name), func(t *testing.T) {
				// The fixture's PR 7 declares an unobserved edge on A: it waits.
				f := newDepFixture(t)
				members := map[int]depQueue{7: f.c}
				for i, pr := range []int{8, 9, 10} {
					m := addDepPR(t, f.s, f.c, pr, byte('p'+i))
					if slices.Contains(tc.waiting, pr) {
						m.declare(t, f.s, f.b, dependency.Basis{}, 1, false, edge(f.a, false))
					} else {
						m.declare(t, f.s, f.b, dependency.Basis{}, 1, false)
					}
					members[pr] = m
				}
				now := time.Now()
				var prs []queue.BatchPR
				for _, pr := range []int{7, 8, 9, 10} {
					prs = append(prs, queue.BatchPR{PR: pr, HeadSHA: members[pr].head})
				}
				appendEvents(t, f.s, f.c.id, queue.BatchStarted{BatchID: "b1", BaseSHA: f.c.base, PRs: prs, At: now}, queue.TestRefBuilt{BatchID: "b1", TestedSHA: sha('f'), Method: queue.Merge, At: now}, queue.ChecksCompleted{TestedSHA: sha('f'), Success: false})
				cfg := depCfg
				cfg.BatchMaxSize, cfg.SpeculationMaxDepth = 4, depth
				ctx := f.ctx(t.Context())
				planned, err := f.s.Plan(ctx, f.c.id, cfg, now)
				must(t, err)
				var build queue.BuildTestRef
				var chained *queue.BuildChainedTestRef
				for _, a := range planned {
					switch a := a.Action.(type) {
					case queue.BuildTestRef:
						build = a
					case queue.BuildChainedTestRef:
						build, chained = a.BuildTestRef, &a
					}
				}
				if got := batchPRNumbers(build.PRs); !slices.Equal(got, tc.want) {
					t.Fatalf("gated plan %v want %v (%v)", got, tc.want, planned)
				}
				claims, err := f.s.ClaimQueue(ctx, f.c.id, 10, time.Minute, now)
				must(t, err)
				var claim ClaimedAction
				for _, c := range claims {
					switch c.Action.(type) {
					case queue.BuildTestRef, queue.BuildChainedTestRef:
						claim = c
					}
				}
				var start queue.Event = queue.BatchStarted{BatchID: build.BatchID, BaseSHA: build.BaseSHA, PRs: build.PRs, At: now}
				if chained != nil {
					start = queue.ChainedBatchStarted{BatchID: build.BatchID, BaseSHA: build.BaseSHA, PRs: build.PRs, ParentBatchID: chained.ParentBatchID, Position: chained.Position, At: now}
				}
				if claim.Action == nil {
					t.Fatalf("no build claim in %+v", claims)
				}
				ok, err := f.s.BeginConsequential(ctx, claim, cfg, now)
				must(t, err)
				if !ok {
					t.Fatal("build declined")
				}
				must(t, f.s.Emit(ctx, claim, cfg)(start))
				for _, activated := range []bool{false, true} {
					loadCtx := t.Context()
					if activated {
						loadCtx = ctx
					}
					state, _, err := f.s.Load(loadCtx, f.c.id)
					must(t, err)
					if state.Active == nil || state.Active.ID != build.BatchID || !slices.Equal(batchPRNumbers(state.Active.PRs), tc.want) || len(state.Entries) != 4 {
						t.Fatal("start did not fold", activated, state.Active)
					}
				}
				var kinds []string
				rows, err := f.s.pool.Query(t.Context(), `SELECT type FROM events WHERE queue_id=$1 AND type IN ('dependency_waits_withheld','batch_started','chained_batch_started') ORDER BY id`, f.c.id)
				must(t, err)
				for rows.Next() {
					var kind string
					must(t, rows.Scan(&kind))
					kinds = append(kinds, kind)
				}
				rows.Close()
				if len(kinds) != 3 || kinds[1] != "dependency_waits_withheld" {
					t.Fatal("withheld record not bound to its start", kinds)
				}
				// Full offline replay from the seed folds the same start.
				reader, _, err := replayJournal(t.Context(), f.s.pool, f.c.id, queue.State{BaseBranch: "main", Fence: "fence-acme/client"}, 0, true)
				must(t, err)
				if reader.Queue.Active == nil || reader.Queue.Active.ID != build.BatchID {
					t.Fatal("replay refused the start")
				}
			})
		}
	}
}

func batchPRNumbers(prs []queue.BatchPR) []int {
	var out []int
	for _, pr := range prs {
		out = append(out, pr.PR)
	}
	return out
}

// Review repro (round 1): re-recording unchanged landed evidence, or an
// unchanged policy, must not postpone batch max_wait; only a waiting-to-
// eligible transition restarts the eligibility clock.
func TestDependencyEligibilityStableAcrossUnchangedRefresh(t *testing.T) {
	f := newDepFixture(t)
	f.observe(t, "landed", f.landedA)
	_, err := f.s.pool.Exec(t.Context(), `UPDATE events SET recorded_at=recorded_at-interval '5 minutes' WHERE queue_id=$1`, f.c.id)
	must(t, err)
	cfg := depCfg
	cfg.BatchMaxSize, cfg.BatchMaxWait = 2, 10*time.Minute
	now := time.Now().Add(6 * time.Minute)
	wake := func() time.Time {
		t.Helper()
		state, _, err := f.s.Load(f.ctx(t.Context()), f.c.id)
		must(t, err)
		at, ok := queue.NextWake(cfg, state, now)
		if !ok {
			t.Fatal("missing max_wait wake")
		}
		return at
	}
	before := wake()
	f.observe(t, "landed", f.landedA)
	f.c.applyPolicy(t, f.s, f.b, true, []string{f.a.key.Repository}, []string{})
	if after := wake(); !after.Equal(before) {
		t.Fatalf("unchanged refresh postponed max_wait: %v -> %v", before, after)
	}
	// waiting -> landed is a real transition: the clock restarts at the read.
	f.observe(t, "waiting", f.a.base)
	f.observe(t, "landed", f.landedA)
	var restarted time.Time
	must(t, f.s.pool.QueryRow(t.Context(), `SELECT recorded_at FROM events WHERE id=$1`, f.observedID).Scan(&restarted))
	if after := wake(); !after.Equal(restarted.Add(cfg.BatchMaxWait)) || !after.After(before) {
		t.Fatalf("transition did not restart the clock: %v (before %v, read %v)", after, before, restarted)
	}
}

// lock() refuses evidence naming B's own queue as a direct source before any
// settlement read or lock. Declarations can never name their own repository,
// so validate would refuse later too; this keeps B's lock out of the set.
func TestDependencyLockRefusesSelfSource(t *testing.T) {
	f := newDepFixture(t)
	f.observe(t, "landed", f.landedA)
	locks := 0
	f.s.beforeQueueLock = func() { locks++ }
	defer func() { f.s.beforeQueueLock = nil }()
	tx, err := f.s.pool.Begin(t.Context())
	must(t, err)
	defer func() { _ = tx.Rollback(t.Context()) }()
	e := f.evidence()
	e.Members[0].Sources[0].Queue = f.c.key
	if _, err := e.lock(t.Context(), f.s, tx, f.c.id); !errors.Is(err, ErrStale) || !errors.Is(err, dependency.ErrOutOfScope) || locks != 0 {
		t.Fatal("self source admitted to the lock set:", err, locks)
	}
}
