package store

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// The dependency overlay must use the same retirement protocol as ADR 0009,
// including immutable cancellation targets and the depth-one exclusion.
func TestDependencyOrderingRestartLateResultsAndCancellation(t *testing.T) {
	for _, mode := range []string{"supported", "absent", "depth-one"} {
		t.Run(mode, func(t *testing.T) {
			f := newDepFixture(t)
			f.observe(t, "landed", f.landedA)
			cfg := depCfg
			cfg.SpeculationMaxDepth = 4
			count, firstRetired := 4, 1
			if mode == "absent" {
				count = 2
			}
			if mode == "depth-one" {
				cfg.SpeculationMaxDepth = 1
				count, firstRetired = 1, 0
			}
			members := []depQueue{f.c}
			declarations := []dependency.DependenciesDeclared{f.decl}
			declarationIDs := []uint64{f.declID}
			for i := 1; i < count; i++ {
				m := addDepPR(t, f.s, f.c, 7+i, byte('1'+i))
				d, id := m.declare(t, f.s, f.b, dependency.Basis{}, 1, false)
				members = append(members, m)
				declarations = append(declarations, d)
				declarationIDs = append(declarationIDs, id)
			}
			ctx := f.ctx(t.Context())
			now := time.Now()
			var ids, tested []string
			for i := 0; i < count; i++ {
				id := admitDependencyBuild(t, f, cfg)
				state, _, err := f.s.Load(ctx, f.c.id)
				must(t, err)
				b := state.Batch(id)
				sha := fmt.Sprintf("%040x", 500+i)
				var built queue.Event = queue.TestRefBuilt{BatchID: id, TestedSHA: sha, Method: queue.Merge, At: now}
				if mode != "depth-one" {
					built = queue.ChainedTestRefBuilt{BatchID: id, TestedSHA: sha, LandingSHA: sha, ParentBatchID: b.ParentBatchID, Position: b.ChainPosition, Method: queue.Merge, At: now}
				}
				_, err = f.s.Append(ctx, f.c.id, []queue.Event{built, queue.ChecksObserved{BatchID: id, TestedSHA: sha, Runs: []queue.CheckRun{{ID: uint64(i + 1), Attempt: 1, Name: "ci", Source: "app:1", State: "pending"}}, OriginalDeadline: now.Add(time.Hour), Timeout: time.Hour}}, "")
				must(t, err)
				ids = append(ids, id)
				tested = append(tested, sha)
			}
			before, _, err := f.s.Load(ctx, f.c.id)
			must(t, err)
			var replayFrom uint64
			must(t, f.s.pool.QueryRow(t.Context(), `SELECT max(id) FROM events WHERE queue_id=$1`, f.c.id).Scan(&replayFrom))
			// A declaration edit in the middle retires exactly that suffix. The
			// unaffected prefix must still settle before rebuilt work may land.
			n := firstRetired
			if mode == "depth-one" {
				f.observe(t, "waiting", f.a.base)
			} else {
				declarations[n], declarationIDs[n] = members[n].declare(t, f.s, f.b, dependency.Basis{EventID: declarationIDs[n], Digest: declarations[n].Digest}, 2, false, edge(f.a, false))
			}
			state, _, err := f.s.Load(ctx, f.c.id)
			must(t, err)
			invalid := state.DependencyInvalidationTargets()
			if !slices.Equal(invalid, ids[n:]) {
				t.Fatalf("suffix %v want %v", invalid, ids[n:])
			}
			actions, err := f.s.Plan(ctx, f.c.id, cfg, now)
			must(t, err)
			found := false
			for _, a := range actions {
				if v, ok := a.Action.(queue.InvalidateBatches); ok {
					found = slices.Equal(v.BatchIDs, invalid)
				}
			}
			if !found {
				t.Fatal("store planner lost exact suffix", actions)
			}
			_, err = f.s.Append(ctx, f.c.id, []queue.Event{queue.BatchesInvalidated{BatchIDs: invalid, Reason: "dependency-changed"}}, "")
			must(t, err)
			// Drop disposable checkpoints to model a cold restart; journal facts own
			// retirement, queue order and cancellation, never an in-memory reverse map.
			_, err = f.s.pool.Exec(t.Context(), `DELETE FROM observations WHERE queue_id=$1`, f.c.id)
			must(t, err)
			state, _, err = f.s.Load(ctx, f.c.id)
			must(t, err)
			if !reflect.DeepEqual(state.Entries, before.Entries) || len(state.Batches()) != n {
				t.Fatal("retirement reordered queue or lost prefix")
			}
			for i := n; i < count; i++ {
				late := []queue.Event{
					queue.TestRefBuilt{BatchID: ids[i], TestedSHA: tested[i], Method: queue.Merge, At: now},
					queue.ChecksObserved{BatchID: ids[i], TestedSHA: tested[i], Terminal: true, Success: true},
					queue.ChecksCompleted{TestedSHA: tested[i], Success: true},
					queue.LandingStarted{BatchID: ids[i], AttemptID: "late", FenceToken: state.Fence, At: now},
				}
				for _, e := range late {
					if got := queue.Apply(state, e); got.Batch(ids[i]) != nil || got.BaseSHA != state.BaseSHA {
						t.Fatal("raw fold revived retired work", e)
					}
				}
				// External late results are journaled, but cannot change the retired ID.
				_, err = f.s.Append(ctx, f.c.id, late[:3], "")
				must(t, err)
			}
			state, _, err = f.s.Load(ctx, f.c.id)
			must(t, err)
			requests := []queue.RecordCheckCancellation{}
			for _, a := range queue.Plan(cfg, state, now) {
				if r, ok := a.(queue.RecordCheckCancellation); ok {
					requests = append(requests, r)
				}
			}
			if mode == "depth-one" {
				if len(requests) != 0 {
					t.Fatal("added depth-one cancellation")
				}
			} else {
				if len(requests) != count-n {
					t.Fatal("missing suffix cancellations", requests)
				}
				for _, r := range requests {
					i := slices.Index(ids, r.BatchID)
					if i < n || r.TestedSHA != tested[i] || r.Runs[0].ID != uint64(i+1) || r.Reason != "dependency-changed" {
						t.Fatal("wrong cancellation targets", r)
					}
					_, err = f.s.Append(ctx, f.c.id, []queue.Event{queue.CheckCancellationRequested(r)}, "")
					must(t, err)
					_, err = f.s.Plan(ctx, f.c.id, cfg, now)
					must(t, err)
					claim := round2ClaimKey(t, f.s, f.c.id, now, queue.CancelChecks(r).Key())
					var driver forge.Driver = round2RecoveryDriver{}
					if mode == "absent" {
						driver = struct{ forge.Driver }{}
					}
					x := executor.Executor{Driver: driver, AllowNoRequiredChecks: true}
					must(t, x.Execute(ctx, claim.Action, f.s.Emit(ctx, claim, cfg)))
					must(t, f.s.Complete(ctx, claim, Done))
				}
			}
			state, _, err = f.s.Load(ctx, f.c.id)
			must(t, err)
			observed := 0
			for _, a := range queue.Plan(cfg, state, now) {
				switch a.(type) {
				case queue.CancelChecks, queue.RecordCheckCancellation:
					t.Fatal("settled cancellation still runnable", a)
				case queue.ObserveRetiredChecks:
					observed++
				}
			}
			if mode == "absent" && observed != count-n {
				t.Fatal("unsupported capability lost read-only followup", observed)
			}
			for _, id := range ids[n:] {
				if state.Batch(id) != nil {
					t.Fatal("late result revived batch", id)
				}
			}
			switch mode {
			case "supported":
				assertCompositionReplayAfter(t, f, cfg, replayFrom)
			case "depth-one":
				assertCompositionReplay(t, f, cfg)
			}
			if mode == "supported" {
				// Fresh eligibility permits new work, never reuse of the retired CI. The
				// unaffected prefix settles first, then the oldest retired entry rebuilds.
				o := f.observed
				o.Alias = members[n].alias
				o.ChangeID = string(members[n].change)
				o.Declaration = dependency.Basis{EventID: declarationIDs[n], Digest: declarations[n].Digest}
				o.ExpectedPrior = 0
				o.ReadSequence = 1
				depSide(t, f.s, f.c.id, f.b, "second-now-landed", o)
				_, err = f.s.Append(ctx, f.c.id, []queue.Event{queue.ChecksCompleted{TestedSHA: tested[0], Success: true}}, "")
				must(t, err)
				plans, err := f.s.Plan(ctx, f.c.id, cfg, now)
				must(t, err)
				var key string
				for _, p := range plans {
					if land, ok := p.Action.(queue.Land); ok {
						if land.BatchID != ids[0] {
							t.Fatal("out-of-order landing", land)
						}
						key = land.Key()
					}
				}
				if key == "" {
					t.Fatal("prefix no longer landable")
				}
				var skip []string
				for _, p := range plans {
					if p.Key != key {
						skip = append(skip, p.Key)
					}
				}
				claims, err := f.s.ClaimQueue(ctx, f.c.id, 1, time.Minute, now, skip...)
				must(t, err)
				if len(claims) != 1 || claims[0].Key != key {
					t.Fatal("missing prefix claim", claims)
				}
				claim := claims[0]
				ok, err := f.s.BeginConsequential(ctx, claim, cfg, now)
				must(t, err)
				if !ok {
					t.Fatal("prefix declined")
				}
				land := claim.Action.(queue.Land)
				must(t, f.s.StartDependencyLanding(ctx, claim, cfg, cp.Policy{}, nil, nil, queue.LandingStarted{BatchID: land.BatchID, AttemptID: "prefix", FenceToken: land.FenceToken, At: now}, f.evidence()))
				_, err = f.s.Append(ctx, f.c.id, []queue.Event{queue.Landed{BatchID: land.BatchID, NewBaseSHA: land.TestedSHA, NewFence: "prefix-settled"}}, "")
				must(t, err)
				must(t, f.s.Complete(ctx, claim, Done))
				rebuilt := admitDependencyBuild(t, f, cfg)
				state, _, err = f.s.Load(ctx, f.c.id)
				must(t, err)
				b := state.Batch(rebuilt)
				if slices.Contains(ids, rebuilt) || b == nil || len(b.PRs) != 1 || b.PRs[0].PR != 8 || b.TestedSHA != "" {
					t.Fatal("suffix did not rebuild in original order", b)
				}
			}
		})
	}
}

func TestDependencyTrimOneSkipsClockPreparation(t *testing.T) {
	f := newDepFixture(t)
	cfg := depCfg
	cfg.BatchMaxSize, cfg.BatchMaxWait = 1, time.Minute
	ctx, err := f.s.prepareDependencyClocks(f.ctx(t.Context()), f.c.id, cfg)
	must(t, err)
	if ctx.Value(dependencySkipFormationClockKey{}) == nil || ctx.Value(dependencyClockPreparationKey{}) != nil {
		t.Fatal("Trim 1 scanned formation clocks")
	}
}

func TestDependencyLateBuilderReissuesCleanupAfterRestart(t *testing.T) {
	f := newDepFixture(t)
	f.observe(t, "landed", f.landedA)
	cfg := depCfg
	cfg.SpeculationMaxDepth = 4
	id := admitDependencyBuild(t, f, cfg)
	ctx, now := f.ctx(t.Context()), time.Now()
	before, _, err := f.s.Load(ctx, f.c.id)
	must(t, err)
	b := before.Batch(id)
	f.observe(t, "waiting", f.a.base)
	_, err = f.s.Append(ctx, f.c.id, []queue.Event{queue.BatchesInvalidated{BatchIDs: []string{id}, Reason: "dependency-changed"}, queue.ActionCompleted{Key: (queue.DeleteRef{Ref: b.TestRef}).Key()}}, "")
	must(t, err)
	_, err = f.s.pool.Exec(t.Context(), `DELETE FROM observations WHERE queue_id=$1`, f.c.id)
	must(t, err)
	late := queue.ChainedTestRefBuilt{BatchID: id, TestedSHA: sha('6'), LandingSHA: sha('6'), ParentBatchID: b.ParentBatchID, Position: b.ChainPosition, Method: queue.Merge, At: now}
	_, err = f.s.Append(ctx, f.c.id, []queue.Event{late, queue.ChecksCompleted{TestedSHA: late.TestedSHA, Success: true}}, "")
	must(t, err)
	state, _, err := f.s.Load(ctx, f.c.id)
	must(t, err)
	if state.Batch(id) != nil || !reflect.DeepEqual(state.Entries, before.Entries) {
		t.Fatal("late builder revived retired composition")
	}
	found := false
	for _, a := range queue.Plan(cfg, state, now) {
		if cleanup, ok := a.(queue.CleanupInvalidatedRef); ok && cleanup.Ref == b.TestRef {
			found = true
		}
		switch a.(type) {
		case queue.RecordCheckCancellation, queue.CancelChecks, queue.Land:
			t.Fatal("late unobserved build supplied CI or landing authority", a)
		}
	}
	if !found {
		t.Fatal("late publication did not reissue cleanup")
	}
	assertCompositionReplay(t, f, cfg)
}
