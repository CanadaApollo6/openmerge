package dependency

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestReverseCascadeScopeRebuildBoundsAndCycles(t *testing.T) {
	f := journalTestFixture(t)
	source := f.ref(1, 0)
	// Scheduling has no permission to invalidate: a reverse cycle merely wakes
	// each consumer once, and a terminal landed node rechecks its own evidence.
	index := ReverseIndex{scope: source.Scope, consumers: map[string][]string{source.ChangeID: {"b"}, "b": {"c"}, "c": {"b"}}}
	got, err := index.Cascade(source)
	if err != nil || !reflect.DeepEqual(got, []string{"b", "c"}) {
		t.Fatal("cascade", got, err)
	}
	if _, err := (ReverseIndex{}).Cascade(source); !errors.Is(err, ErrStale) {
		t.Fatal("absent cache authorized graph", err)
	}
	for i := 0; i <= MaxDepth; i++ {
		from := fmt.Sprint(i)
		if i == 0 {
			from = source.ChangeID
		}
		index.consumers[from] = []string{fmt.Sprint(i + 1)}
	}
	if _, err := index.Cascade(source); !errors.Is(err, ErrLimit) {
		t.Fatal("unbounded reverse depth", err)
	}
	s := applyJournal(t, f.start(t), f.policy(false))
	s = applyJournal(t, s, f.declaration(t, s, Input{Reference: source}))
	for range 2 {
		rebuilt, err := RebuildReverseScope([]State{s}, f.init)
		if err != nil {
			t.Fatal(err)
		}
		got, err := rebuilt.Cascade(source)
		if err != nil || !reflect.DeepEqual(got, []string{f.ref(3, 0).ChangeID}) {
			t.Fatal("scope rebuild", got, err)
		}
	}
	if _, err := RebuildReverseScope([]State{{}}, f.init); !errors.Is(err, ErrOutOfScope) {
		t.Fatal("unknown queue silently omitted", err)
	}
}
