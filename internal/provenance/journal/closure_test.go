package journal

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

type countedClosureStore struct {
	values     map[string][]byte
	gets, puts int
	fail       bool
}

func (s *countedClosureStore) Get(key string) ([]byte, error) {
	s.gets++
	if s.fail {
		return nil, errors.New("scratch unavailable")
	}
	return s.values[key], nil
}
func (s *countedClosureStore) Put(key string, raw []byte) error {
	s.puts++
	if s.fail {
		return errors.New("scratch unavailable")
	}
	s.values[key] = raw
	return nil
}

func TestClosureFoldLinearWork(t *testing.T) {
	sizes := []int{100, 300}
	if !testing.Short() {
		sizes = append(sizes, 1000, 5000)
	}
	for _, n := range sizes {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			h, commits := fixture(t)
			store := &countedClosureStore{values: map[string][]byte{}}
			fold := ClosureFold{Store: store}
			start := time.Now()
			for i := 1; i <= n; i++ {
				h.Request.ChangeID = fmt.Sprintf("change-%d", i)
				h.Digest = InventoryDigest(h, commits)
				if err := fold.Apply(uint64(i), h); err != nil {
					t.Fatal(err)
				}
			}
			if store.gets != 2*n || store.puts != 2*n {
				t.Fatalf("nonlinear fold work: gets=%d puts=%d records=%d", store.gets, store.puts, n)
			}
			t.Logf("folded=%d gets=%d puts=%d duration=%s", n, store.gets, store.puts, time.Since(start))
		})
	}
}

func TestClosureFoldMatchesRetainedFold(t *testing.T) {
	for _, bad := range []string{"none", "previous", "chunk-order", "inventory-digest", "member-inventory", "duplicate-stage", "landed-without-tested", "stale-generation"} {
		t.Run(bad, func(t *testing.T) {
			h, commits := fixture(t)
			pieces := chunks(h, commits)
			l := landing(h, []uint64{11, 12})
			events := []Event{pieces[0], pieces[1], l}
			switch bad {
			case "previous":
				pieces[0].PreviousObservationID = 9
				pieces[0].Digest = InventoryDigest(pieces[0], commits)
				events[0] = pieces[0]
			case "chunk-order":
				events[0], events[1] = events[1], events[0]
			case "inventory-digest":
				pieces[1].Digest = oid("a") + "000000000000000000000000"
				events[1] = pieces[1]
			case "member-inventory":
				l.Members[0].ObservationIDs = []uint64{11}
				l.Members[0].RecordDigest = MemberDigest(l.Members[0])
				events[2] = l
			case "duplicate-stage":
				events = append(events, l)
			case "landed-without-tested":
				l.Stage = "landed"
				l.Members[0].Mapping.Build.Stage = "landed"
				l.Members[0].RecordDigest = MemberDigest(l.Members[0])
				events[2] = l
			case "stale-generation":
				next := h
				next.Request.Generation = "generation-2"
				next.PreviousObservationID = 11
				next.Digest = InventoryDigest(next, commits)
				events = []Event{pieces[0], pieces[1], next, l}
			}
			store := &countedClosureStore{values: map[string][]byte{}}
			fold := ClosureFold{Store: store}
			var state State
			rejected := false
			for i, e := range events {
				id := uint64(i + 11)
				next, want := Apply(state, id, e)
				got := fold.Apply(id, e)
				if (got == nil) != (want == nil) {
					t.Fatalf("step=%d closure=%v retained=%v", i, got, want)
				}
				if got != nil {
					rejected = true
					break
				}
				state = next
			}
			if rejected != (bad != "none") {
				t.Fatalf("wrong rejection for %s", bad)
			}
		})
	}
}

func TestClosureFoldFailsClosed(t *testing.T) {
	h, _ := fixture(t)
	store := &countedClosureStore{values: map[string][]byte{}, fail: true}
	fold := ClosureFold{Store: store}
	if fold.Apply(1, h) == nil {
		t.Fatal("storage failure accepted")
	}
	store.fail = false
	if fold.Apply(1, h) == nil {
		t.Fatal("failed fold reused")
	}
}
