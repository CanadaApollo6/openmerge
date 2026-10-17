package store

import (
	"strings"
	"sync"
	"testing"

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
