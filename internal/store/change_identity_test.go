package store

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestChangeIdentityCanonicalAliasAndPartitions(t *testing.T) {
	db := newStore(t)
	stored := QueueKey{Installation: "7", Repository: "Owner/Repo", BaseBranch: "main"}
	q, canonical, err := db.CanonicalQueue(t.Context(), stored)
	must(t, err)
	want, err := canonical.ChangeID(12)
	must(t, err)
	alias := stored
	alias.Repository = strings.ToLower(stored.Repository)
	reused, key, err := db.CanonicalQueue(t.Context(), alias)
	must(t, err)
	got, err := key.ChangeID(12)
	must(t, err)
	if reused != q || got != want {
		t.Fatal("case alias split change identity")
	}
	for _, other := range []QueueKey{
		{Installation: "8", Repository: stored.Repository, BaseBranch: "main"},
		{Installation: "7", Repository: stored.Repository, BaseBranch: "release/1"},
		{Installation: "7", Repository: stored.Repository, BaseBranch: "main", Lane: "batch"},
	} {
		otherQ, otherKey, err := db.CanonicalQueue(t.Context(), other)
		must(t, err)
		otherID, err := otherKey.ChangeID(12)
		must(t, err)
		if otherQ == q || otherID == want {
			t.Fatal("partition collision")
		}
	}
	appendEvents(t, db, q, queue.PRObserved{PR: 12, HeadSHA: "H1", Eligible: true, Trusted: true})
	state, _, err := db.Load(t.Context(), q)
	must(t, err)
	current, known := state.PRRevision(12)
	if !known || current.Revision != 1 || current.ChangeID != "" {
		t.Fatal("legacy observation missing projection or backfilled identity")
	}
	if count(t, db, `SELECT count(*) FROM events WHERE type='change_revision_observed'`) != 0 {
		t.Fatal("legacy queue emitted Stage A event")
	}
}

func TestChangeRevisionAppendRollsBackStaleDelivery(t *testing.T) {
	db := newStore(t)
	key := QueueKey{Installation: "7", Repository: "Owner/Repo", BaseBranch: "main"}
	q, err := db.EnsureQueue(t.Context(), key)
	must(t, err)
	id, err := key.ChangeID(12)
	must(t, err)
	observed := queue.ChangeRevisionObserved{PR: 12, ChangeID: id, Revision: 1, HeadSHA: "H1", Basis: queue.RevisionObservationBasis{BaseSHA: "base", At: time.Now().UTC()}}
	appendEvents(t, db, q, observed)
	state, watermark, err := db.Load(t.Context(), q)
	must(t, err)
	_, err = db.Append(t.Context(), q, []queue.Event{queue.PRClosed{PR: 12}, observed}, "change-stale-delivery")
	if !errors.Is(err, ErrStale) {
		t.Fatalf("stale delivery error %v", err)
	}
	after, gotWatermark, err := db.Load(t.Context(), q)
	must(t, err)
	beforeRevision, _ := state.PRRevision(12)
	afterRevision, _ := after.PRRevision(12)
	if gotWatermark != watermark || beforeRevision != afterRevision || count(t, db, `SELECT count(*) FROM events WHERE queue_id=$1`, q) != 1 {
		t.Fatal("partial stale delivery committed")
	}
	wrong := observed
	wrong.Revision = 2
	wrong.Predecessor = 1
	wrong.HeadSHA = "H2"
	wrong.ChangeID, err = queue.PRChangeID("7", "Owner/Repo", "release/1", "", 12)
	must(t, err)
	_, err = db.Append(t.Context(), q, []queue.Event{wrong}, "")
	if !errors.Is(err, ErrStale) {
		t.Fatalf("accepted cross-queue identity: %v", err)
	}
	fresh := wrong
	fresh.ChangeID = id
	_, err = db.Append(t.Context(), q, []queue.Event{fresh}, "change-stale-delivery")
	must(t, err)
	state, _, err = db.Load(t.Context(), q)
	must(t, err)
	current, _ := state.PRRevision(12)
	if current.Revision != 2 || current.HeadSHA != "H2" {
		t.Fatal("failed retry after rollback")
	}
}

func TestChangeRevisionConcurrentUpdatesHaveOneWinner(t *testing.T) {
	db := newStore(t)
	key := QueueKey{Installation: "7", Repository: "Owner/Repo", BaseBranch: "main"}
	q, err := db.EnsureQueue(t.Context(), key)
	must(t, err)
	id, err := key.ChangeID(12)
	must(t, err)
	initial := queue.ChangeRevisionObserved{PR: 12, ChangeID: id, Revision: 1, HeadSHA: "H1", Basis: queue.RevisionObservationBasis{BaseSHA: "base", At: time.Now().UTC()}}
	appendEvents(t, db, q, initial)
	outcomes := make(chan error, 2)
	var wg sync.WaitGroup
	for _, head := range []string{"H2-a", "H2-b"} {
		wg.Go(func() {
			update := initial
			update.Revision = 2
			update.Predecessor = 1
			update.HeadSHA = head
			_, err := db.Append(t.Context(), q, []queue.Event{update}, "")
			outcomes <- err
		})
	}
	wg.Wait()
	close(outcomes)
	success, stale := 0, 0
	for err := range outcomes {
		if err == nil {
			success++
		} else if errors.Is(err, ErrStale) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || stale != 1 || count(t, db, `SELECT count(*) FROM events WHERE queue_id=$1`, q) != 2 {
		t.Fatalf("concurrent updates: success=%d stale=%d", success, stale)
	}
}
