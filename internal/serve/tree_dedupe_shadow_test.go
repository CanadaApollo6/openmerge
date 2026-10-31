package serve

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"github.com/CanadaApollo6/openmerge/internal/treededupe"
)

const shadowConfig = "version: 1\nqueues:\n  - name: default\n    batch:\n      max_size: 2\n    merge_when:\n      - check-success = ci\n"

// fakeTreeInputs keys fake commits by their full file content, standing in
// for gitops.TreeDedupeInputs (covered by real-git tests in gitops).
func fakeTreeInputs(f *fake.Forge) func(context.Context, store.QueueKey, string) (treededupe.GitInputs, error) {
	return func(_ context.Context, _ store.QueueKey, sha string) (treededupe.GitInputs, error) {
		c, err := f.Commit(repo, sha)
		if err != nil {
			return treededupe.GitInputs{}, err
		}
		h := sha1.New()
		for _, path := range slices.Sorted(maps.Keys(c.Files)) {
			_, _ = fmt.Fprintf(h, "%s\x00%s\x00", path, c.Files[path])
		}
		return treededupe.GitInputs{Tree: fmt.Sprintf("%x", h.Sum(nil)), Workflows: []treededupe.Workflow{
			{Mode: "100644", Type: "blob", OID: strings.Repeat("b", 40), Path: ".github/workflows/ci.yml"},
		}}, nil
	}
}

// shadowRun drives one queue through a failed pair, bisection, a landed left
// half, a dequeued culprit, and then PR 3 carrying the culprit's identical
// change on the same base (a requeue under another number). It
// returns the queue-visible trace: journal event kinds with their batch
// composition, forge statuses, and the final base content.
func shadowRun(t *testing.T, shadow func(*fake.Forge) *TreeDedupeShadow) ([]string, *store.Store, store.QueueID) {
	t.Helper()
	s, w, f, _ := fixture(t, shadowConfig)
	s.options.TreeDedupeShadow = shadow(f)
	base, err := f.ReadRef(t.Context(), repo, "main")
	must(t, err)
	_, err = f.OpenPR(repo, 2, base, true)
	must(t, err)
	checkID := uint64(0)
	f.OnTestRef(func(sha string, prs []int) {
		checkID++
		state := forge.Success
		if slices.Contains(prs, 2) || slices.Contains(prs, 3) {
			state = forge.Failure
		}
		f.SetCheckRuns(repo, sha, []forge.CheckRun{{ID: checkID, Name: "ci", State: state}})
	})
	drain := func() {
		for range 40 {
			time.Sleep(2 * time.Millisecond)
			must(t, s.cycle(t.Context(), w))
			state, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			if len(state.Entries) == 0 && state.Active == nil {
				return
			}
		}
		t.Fatal("queue did not drain")
	}
	culprit, err := f.PullRequest(t.Context(), repo, 2)
	must(t, err)
	head, err := f.Commit(repo, culprit.HeadSHA)
	must(t, err)
	_, err = f.OpenPRWithFiles(repo, 3, base, true, map[string]string{"pr-2.txt": head.Files["pr-2.txt"]})
	must(t, err)
	// With max_size 2, PR 3 waits until the {1,2} bisection has finished.
	for _, pr := range []int{1, 2, 3} {
		f.AddComment(repo, pr, "writer", "@openmerge queue")
	}
	drain()

	journals, err := s.store.Journals(t.Context(), []store.QueueKey{w.Key})
	must(t, err)
	var trace []string
	for _, e := range journals[0].Events {
		line := fmt.Sprintf("%T", e.Event)
		switch v := e.Event.(type) {
		case queue.BatchStarted:
			line += fmt.Sprint(" prs=", prNumbers(v.PRs), " batch=", v.BatchID, " base=", v.BaseSHA)
		case queue.TestRefBuilt:
			line += fmt.Sprint(" batch=", v.BatchID, " tested=", v.TestedSHA)
		case queue.Landed:
			line += fmt.Sprint(" batch=", v.BatchID, " base=", v.NewBaseSHA)
		case queue.Dequeued:
			line += fmt.Sprint(" pr=", v.PR, " reason=", v.Reason)
		case queue.ChecksCompleted:
			line += fmt.Sprint(" success=", v.Success)
		}
		trace = append(trace, line)
	}
	for _, pr := range []int{1, 2, 3} {
		for _, st := range f.Statuses(repo, pr) {
			trace = append(trace, fmt.Sprintf("status #%d %s %s", pr, st.State, st.Reason))
		}
	}
	tip, err := f.ReadRef(t.Context(), repo, "main")
	must(t, err)
	c, err := f.Commit(repo, tip)
	must(t, err)
	trace = append(trace, fmt.Sprint("base files ", slices.Sorted(maps.Keys(c.Files))))
	return trace, s.store, w.id
}

func prNumbers(prs []queue.BatchPR) []int {
	out := make([]int, len(prs))
	for i, pr := range prs {
		out[i] = pr.PR
	}
	return out
}

func shadowLedger(t *testing.T, db *store.Store, key store.QueueKey) (treededupe.Ledger, bool) {
	t.Helper()
	journals, err := db.Journals(t.Context(), []store.QueueKey{key})
	must(t, err)
	if journals[0].TreeDedupeShadow == nil {
		return treededupe.Ledger{}, false
	}
	l, err := treededupe.Decode(journals[0].TreeDedupeShadow)
	must(t, err)
	return l, true
}

var shadowKey = store.QueueKey{Installation: "7", Repository: repo.Path, BaseBranch: "main"}

// TestTreeDedupeShadowChangesNothing is the byte-identity proof at the serve
// boundary: the same scenario with the shadow off, on, and failing yields the
// same journal kinds, batch compositions, dequeues, forge statuses and base.
func TestTreeDedupeShadowChangesNothing(t *testing.T) {
	t.Parallel()
	off, offDB, _ := shadowRun(t, func(*fake.Forge) *TreeDedupeShadow { return nil })
	if _, ok := shadowLedger(t, offDB, shadowKey); ok {
		t.Fatal("disabled shadow wrote a ledger")
	}
	on, onDB, _ := shadowRun(t, func(f *fake.Forge) *TreeDedupeShadow {
		return &TreeDedupeShadow{Inputs: fakeTreeInputs(f), RunnerImage: func(store.QueueKey) string { return "test-image/1" }}
	})
	broken, brokenDB, _ := shadowRun(t, func(*fake.Forge) *TreeDedupeShadow {
		return &TreeDedupeShadow{Inputs: func(context.Context, store.QueueKey, string) (treededupe.GitInputs, error) {
			panic("injected shadow fault")
		}}
	})
	if !slices.Equal(off, on) || !slices.Equal(off, broken) {
		t.Fatalf("shadow changed queue behaviour:\noff:    %q\non:     %q\nbroken: %q", off, on, broken)
	}
	// The scenario must actually exercise bisection, a landing and a requeue.
	joined := strings.Join(off, "\n")
	for _, want := range []string{"BatchStarted prs=[1 2]", "BatchStarted prs=[1] ", "BatchStarted prs=[2] ", "queue.Landed", "Dequeued pr=2 reason=checks-failed", "BatchStarted prs=[3] ", "Dequeued pr=3 reason=checks-failed"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("scenario lacks %q:\n%s", want, joined)
		}
	}

	l, ok := shadowLedger(t, onDB, shadowKey)
	if !ok {
		t.Fatal("enabled shadow wrote no ledger")
	}
	kinds := make([]string, len(l.Builds))
	for i, b := range l.Builds {
		kinds[i] = b.Kind + ":" + b.Hit
	}
	// {1,2} fails unconfirmed; the bisection halves {1} and {2} follow, and {2}
	// on the landed {1} has the failed pair's tree but no hit because that
	// failure was never confirmed. {2} failing alone with no retry is
	// confirmed, so PR 3's identical change on the same base would reuse it.
	want := []string{"batch:", "bisection:", "bisection:", "batch:" + treededupe.WouldReuseFailure}
	if !slices.Equal(kinds, want) {
		t.Fatalf("shadow builds = %q, want %q", kinds, want)
	}
	if l.Builds[2].Key != l.Builds[0].Key {
		t.Fatal("bisection right half did not reproduce the failed pair's key")
	}
	if s := l.Summarize(time.Time{}, time.Time{}); s.WouldReuseFailure != 1 || s.WouldReuseSuccess != 0 || s.Agreed != 1 || s.Disagreed != 0 || s.Keyed != 4 {
		t.Fatalf("summary = %+v", s)
	}
	// A panicking input reader drops the observation; nothing is recorded.
	if bl, ok := shadowLedger(t, brokenDB, shadowKey); ok && len(bl.Builds) != 0 && bl.Builds[0].Key != "" {
		t.Fatalf("faulty shadow recorded keys: %+v", bl.Builds)
	}
}

// TestTreeDedupeShadowUnknownImageAndStoreFailure checks the fail-closed key
// and that a store failure is only logged.
func TestTreeDedupeShadowUnknownImageAndStoreFailure(t *testing.T) {
	t.Parallel()
	_, db, id := shadowRun(t, func(f *fake.Forge) *TreeDedupeShadow {
		return &TreeDedupeShadow{Inputs: fakeTreeInputs(f)} // no runner image
	})
	l, ok := shadowLedger(t, db, shadowKey)
	if !ok || len(l.Builds) != 4 {
		t.Fatalf("ledger = %+v", l)
	}
	if s := l.Summarize(time.Time{}, time.Time{}); s.Keyed != 0 || s.WouldReuseFailure+s.WouldReuseSuccess != 0 || s.KeyUnknown["runner_image"] != 4 {
		t.Fatalf("unknown image summary = %+v", s)
	}
	// A ledger this binary cannot read is never overwritten.
	must(t, db.UpdateTreeDedupeShadow(t.Context(), id, func([]byte) ([]byte, error) { return []byte(`{"version":99}`), nil }))
	s := &Server{store: db, options: Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), TreeDedupeShadow: &TreeDedupeShadow{}}}
	w := &worker{id: id, Target: Target{Key: shadowKey}}
	s.recordTreeDedupeShadow(w, &treeDedupeCapture{events: []queue.Event{queue.ChecksCompleted{TestedSHA: strings.Repeat("a", 40), Success: true}}})
	journals, err := db.Journals(t.Context(), []store.QueueKey{shadowKey})
	must(t, err)
	if string(journals[0].TreeDedupeShadow) != `{"version":99}` {
		t.Fatalf("future ledger overwritten: %s", journals[0].TreeDedupeShadow)
	}
	if _, err := treededupe.Decode(journals[0].TreeDedupeShadow); !errors.Is(err, treededupe.ErrLedgerVersion) {
		t.Fatal(err)
	}
}
