package store

import (
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestNewPoisonInvalidatesPreparedSourceClear(t *testing.T) {
	s := newStore(t)
	q, claim := readyLand(t, s)
	appendEvents(t, s, q,
		queue.LandingStarted{BatchID: "b1", AttemptID: "landed", FenceToken: "fence", At: epoch},
		queue.Landed{BatchID: "b1", NewBaseSHA: "tested", NewFence: "next"},
		queue.PRObserved{PR: 2, HeadSHA: "head2", Eligible: true, Trusted: true},
		queue.EnqueueRequested{PR: 2, At: epoch})
	must(t, s.Complete(t.Context(), claim, Done))
	planned, err := s.Plan(t.Context(), q, config, epoch)
	must(t, err)
	var buildKey, cleanupKey string
	for _, p := range planned {
		switch p.Action.(type) {
		case queue.BuildTestRef:
			buildKey = p.Key
		case queue.DeleteRef:
			cleanupKey = p.Key
		}
	}
	if buildKey == "" || cleanupKey == "" {
		t.Fatal("missing real build/cleanup actions")
	}
	corruptAndDetect := func(key, field string) {
		t.Helper()
		_, err := s.pool.Exec(t.Context(), `UPDATE actions SET payload=payload #- ARRAY['data',$3::text] WHERE queue_id=$1 AND key=$2`, q, key, field)
		must(t, err)
		_, err = s.Plan(t.Context(), q, config, epoch)
		must(t, err)
	}
	// Both keys come from the real planner, and every incident is recorded by
	// its ordinary decoder/repair path. Keep old cleanup pending during builds.
	for range 2 {
		_, err = s.Plan(t.Context(), q, config, epoch)
		must(t, err)
		corruptAndDetect(cleanupKey, "ref")
	}
	for range 3 {
		_, err = s.Plan(t.Context(), q, config, epoch)
		must(t, err)
		corruptAndDetect(buildKey, "base_sha")
	}
	before, _, err := s.Load(t.Context(), q)
	must(t, err)
	if !before.SourceBlocked(queue.ActionPoison) {
		t.Fatal("initial action did not poison")
	}
	preparedClear := queue.BlockSourceCleared{Source: queue.ActionPoison, Basis: before.BlockSourceRevision(queue.ActionPoison)}
	// Another previously scheduled side effect reaches its third decode failure
	// after refresh read its basis, but before refresh commits its clear.
	corruptAndDetect(cleanupKey, "ref")
	afterFailure, _, err := s.Load(t.Context(), q)
	must(t, err)
	incidents := count(t, s, `SELECT poison_count FROM actions WHERE queue_id=$1 AND key=$2`, q, cleanupKey)
	if afterFailure.BlockSourceRevision(queue.ActionPoison) != before.BlockSourceRevision(queue.ActionPoison)+1 || incidents != 3 {
		t.Fatalf("new poison did not advance the source: revision %d -> %d, incidents %d", before.BlockSourceRevision(queue.ActionPoison), afterFailure.BlockSourceRevision(queue.ActionPoison), incidents)
	}
	appendEvents(t, s, q, preparedClear)
	afterClear, _, err := s.Load(t.Context(), q)
	must(t, err)
	afterIncidents := count(t, s, `SELECT poison_count FROM actions WHERE queue_id=$1 AND key=$2`, q, cleanupKey)
	_, err = s.Plan(t.Context(), q, config, epoch)
	must(t, err)
	if !afterClear.SourceBlocked(queue.ActionPoison) || afterIncidents != 3 {
		t.Fatal("prepared clear erased a newer poison failure and its incident count")
	}
	if r2Status(t, s, q, buildKey) != "poisoned" || r2Status(t, s, q, cleanupKey) != "poisoned" {
		t.Fatal("stale clear made quarantined work runnable")
	}
}

func TestEachPendingPoisonNotificationAdvancesSource(t *testing.T) {
	s := newStore(t)
	q, claim := readyLand(t, s)
	appendEvents(t, s, q,
		queue.LandingStarted{BatchID: "b1", AttemptID: "landed", FenceToken: "fence", At: epoch},
		queue.Landed{BatchID: "b1", NewBaseSHA: "tested", NewFence: "next"},
		queue.PRObserved{PR: 2, HeadSHA: "head2", Eligible: true, Trusted: true},
		queue.EnqueueRequested{PR: 2, At: epoch})
	must(t, s.Complete(t.Context(), claim, Done))
	planned, err := s.Plan(t.Context(), q, config, epoch)
	must(t, err)
	notifications := 0
	for _, action := range planned {
		switch action.Action.(type) {
		case queue.BuildTestRef, queue.DeleteRef:
			// Both real planner actions were quarantined before notification.
			_, err := s.pool.Exec(t.Context(), `UPDATE actions SET status='poisoned', error='invalid payload', poison_count=3 WHERE queue_id=$1 AND key=$2`, q, action.Key)
			must(t, err)
			notifications++
		}
	}
	if notifications != 2 {
		t.Fatalf("expected build and cleanup actions, got %d", notifications)
	}
	for range 2 {
		_, err = s.Plan(t.Context(), q, config, epoch)
		must(t, err)
		state, _, err := s.Load(t.Context(), q)
		must(t, err)
		if state.BlockSourceRevision(queue.ActionPoison) != 2 || count(t, s, `SELECT count(*) FROM events WHERE queue_id=$1 AND type='block_source_blocked' AND payload->'data'->>'source'='action-poisoned'`, q) != 2 {
			t.Fatal("poison notifications were coalesced or published more than once")
		}
	}
}
