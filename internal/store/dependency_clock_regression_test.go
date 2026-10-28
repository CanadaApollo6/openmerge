package store

import (
	"errors"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// A queue-only discard can expose an eligible entry without changing the
// dependency position or entry identities. Preparation must refuse that race.
func TestDependencyPreparedClockBatchMembershipGap(t *testing.T) {
	f := newDepFixture(t)
	state, _, err := f.s.Load(t.Context(), f.c.id)
	must(t, err)
	origin := state.Entries[0].EnqueuedAt.Truncate(time.Microsecond)
	at := func(minutes int) {
		_, err := f.s.pool.Exec(t.Context(), `UPDATE events SET recorded_at=$2 WHERE id=$1`, f.observedID, origin.Add(time.Duration(minutes)*time.Minute))
		must(t, err)
	}
	f.observe(t, "landed", f.landedA)
	at(1)
	id := admitDependencyBuild(t, f, depCfg) // E is now inside batch id
	f.observe(t, "waiting", f.a.base)
	at(4)
	f.observe(t, "landed", f.landedA)
	at(20)
	ctx := f.ctx(t.Context())
	cfg := depCfg
	cfg.BatchMaxSize = 2
	cfg.BatchMaxWait = 10 * time.Minute
	prep, err := f.s.prepareDependencyClocks(ctx, f.c.id, cfg)
	must(t, err)
	p := prep.Value(dependencyClockPreparationKey{}).(dependencyClockPreparation)
	t.Logf("prepared clocks while entry batched: %v (len %d)", p.clocks, len(p.clocks))
	// queue-only event: discards the latched batch; no dependency event, same entries.
	_, err = f.s.Append(ctx, f.c.id, []queue.Event{queue.BatchesInvalidated{BatchIDs: []string{id}, Reason: "dependency-changed"}}, "")
	must(t, err)
	reader, _, err := replayJournal(t.Context(), f.s.pool, f.c.id, queue.State{BaseBranch: "main", Fence: "fence-acme/client"}, 0, true)
	must(t, err)
	_, err = withDependencyWaits(prep, f.s.pool, f.c.id, reader)
	if !errors.Is(err, ErrStale) {
		t.Fatalf("missing batch-membership fence: %v", err)
	}
}

func TestDependencyClockClampsRegressingTimestamps(t *testing.T) {
	f := newDepFixture(t)
	state, _, err := f.s.Load(t.Context(), f.c.id)
	must(t, err)
	origin := state.Entries[0].EnqueuedAt.Truncate(time.Microsecond)
	for _, observation := range []struct {
		result string
		minute int
	}{{"landed", 1}, {"waiting", 4}, {"landed", 2}} {
		f.observe(t, observation.result, f.landedA)
		_, err = f.s.pool.Exec(t.Context(), `UPDATE events SET recorded_at=$2 WHERE id=$1`, f.observedID, origin.Add(time.Duration(observation.minute)*time.Minute))
		must(t, err)
	}
	key, err := storedKey(t.Context(), f.s.pool, f.c.id)
	must(t, err)
	clocks, err := dependencyEligibleClocks(t.Context(), f.s.pool, f.c.id, state, key)
	must(t, err)
	if got := clocks[state.Entries[0].PR]; !got.Equal(origin.Add(time.Minute)) {
		t.Fatalf("regressing clock: got %v want %v", got, origin.Add(time.Minute))
	}
}
