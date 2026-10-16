package store

import (
	"reflect"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestJournalsAreScopedAndReproduceLoad(t *testing.T) {
	s := newStore(t)
	key := QueueKey{Installation: "7", Repository: "owner/repo", BaseBranch: "release/v1"}
	id, err := s.EnsureQueue(t.Context(), key)
	must(t, err)
	must(t, s.InitializeFence(t.Context(), id, "initial"))
	_, err = s.Append(t.Context(), id, seedEvents(), "")
	must(t, err)
	other := key
	other.Installation = "8"
	_, err = s.EnsureQueue(t.Context(), other)
	must(t, err)
	missing := key
	missing.Repository = "not/served"
	journals, err := s.Journals(t.Context(), []QueueKey{key, missing})
	must(t, err)
	if len(journals) != 1 || journals[0].Key != key || len(journals[0].Events) != 3 {
		t.Fatalf("journals=%+v", journals)
	}
	j := journals[0]
	state := j.Seed
	for _, record := range j.Events {
		event, err := queue.DecodeEvent(record.Payload)
		must(t, err)
		if record.RecordedAt.IsZero() || !reflect.DeepEqual(event, record.Event) {
			t.Fatalf("journal lost metadata: %+v", record)
		}
		state = queue.Apply(state, record.Event)
	}
	want, watermark, err := s.Load(t.Context(), id)
	must(t, err)
	if !reflect.DeepEqual(want, state) || watermark != j.Events[len(j.Events)-1].ID {
		t.Fatal("journal and load disagree")
	}
	var count int
	must(t, s.pool.QueryRow(t.Context(), "SELECT count(*) FROM queues").Scan(&count))
	if count != 2 {
		t.Fatal("read created a missing queue")
	}
}
