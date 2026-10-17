package store

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestCanonicalRegistrationReusesLiveQueue(t *testing.T) {
	db := openTestStore(t, databaseURL(t))
	key := QueueKey{Installation: "172166049", Repository: "CanadaApollo6/openmerge", BaseBranch: "main"}
	id, err := db.EnsureQueue(t.Context(), key)
	must(t, err)
	_, err = db.Append(t.Context(), id, []queue.Event{queue.FenceInitialized{Token: "existing-fence"}}, "")
	must(t, err)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Go(func() {
			incoming := key
			incoming.Repository = strings.ToUpper(key.Repository)
			got, canonical, err := db.CanonicalQueue(t.Context(), incoming)
			if err != nil || got != id || canonical != key {
				t.Errorf("registration = %d %+v %v", got, canonical, err)
			}
		})
	}
	wg.Wait()
	keys, err := db.QueueKeys(t.Context())
	must(t, err)
	state, watermark, err := db.Load(t.Context(), id)
	must(t, err)
	if len(keys) != 1 || state.Fence != "existing-fence" || watermark != 1 {
		t.Fatalf("queue history changed: keys=%+v state=%+v watermark=%d", keys, state, watermark)
	}
}

func TestCanonicalNewQueueRegistrationRace(t *testing.T) {
	db := openTestStore(t, databaseURL(t))
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Go(func() {
			name := "owner/repo"
			if i%2 == 0 {
				name = "Owner/Repo"
			}
			_, err := db.EnsureQueue(t.Context(), QueueKey{Installation: "7", Repository: name, BaseBranch: "main"})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	keys, err := db.QueueKeys(t.Context())
	must(t, err)
	if len(keys) != 1 {
		t.Fatalf("racing registrations created %d queues", len(keys))
	}
}

func TestCanonicalSelectionSurvivesDrainAndReopen(t *testing.T) {
	url := databaseURL(t)
	db := openTestStore(t, url)
	key := QueueKey{Installation: "7", Repository: "Owner/Repo", BaseBranch: "main"}
	older, err := db.EnsureQueue(t.Context(), key)
	must(t, err)
	_, err = db.Append(t.Context(), older, []queue.Event{queue.FenceInitialized{Token: "stale"}}, "")
	must(t, err)
	var newer QueueID
	err = db.pool.QueryRow(t.Context(), `INSERT INTO queues(installation,repository,base,lane) VALUES ('7','owner/repo','main','') RETURNING id`).Scan(&newer)
	must(t, err)
	_, err = db.Append(t.Context(), newer, []queue.Event{queue.FenceInitialized{Token: "current"}, queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: time.Now()}}, "")
	must(t, err)
	must(t, db.Record(t.Context(), newer, "comment/123", []byte(`true`)))
	must(t, db.Record(t.Context(), newer, "cursor/1", []byte(`"2026-10-16T00:00:00Z"`)))
	got, canonical, err := db.CanonicalQueue(t.Context(), key)
	must(t, err)
	if got != newer || canonical.Repository != "owner/repo" {
		t.Fatalf("wrong live selection: %d %+v", got, canonical)
	}
	state, _, err := db.Load(t.Context(), newer)
	must(t, err)
	_, err = db.Append(t.Context(), newer, []queue.Event{queue.Dequeued{PR: 1, AdmissionID: state.Entries[0].AdmissionID, Reason: queue.Requested}}, "")
	must(t, err)
	reopened := openTestStore(t, url)
	got, _, err = reopened.CanonicalQueue(t.Context(), key)
	must(t, err)
	if got != newer {
		t.Fatal("canonical selection reverted after drain and reopen")
	}
	state, _, err = reopened.Load(t.Context(), got)
	must(t, err)
	receipt, err := reopened.Observation(t.Context(), got, "comment/123")
	must(t, err)
	cursor, err := reopened.Observation(t.Context(), got, "cursor/1")
	must(t, err)
	if len(state.Entries) != 0 || state.Fence != "current" || string(receipt) != "true" || len(cursor) == 0 {
		t.Fatalf("canonical history lost: %+v receipt=%s cursor=%s", state, receipt, cursor)
	}
}
