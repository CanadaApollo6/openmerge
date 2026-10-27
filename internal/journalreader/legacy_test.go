package journalreader_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/journalreader"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// The committed vectors were generated from origin/main 515ba818376f1815db3f33db642c034fe2ffbd6e
// with the original queue fold. They pin every prefix, including a live batch,
// #163 held request, landing settlement, planner action bytes and status output.
func TestLegacyMainReplayVectors(t *testing.T) {
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	events := []queue.Event{
		queue.BaseMoved{SHA: "base"},
		queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true},
		queue.EnqueueRequested{PR: 1, At: at},
		queue.BatchStarted{BatchID: "batch", BaseSHA: "base", PRs: []queue.BatchPR{{PR: 1, HeadSHA: "head"}}, At: at},
		queue.TestRefBuilt{BatchID: "batch", TestedSHA: "tested", Method: queue.Merge, At: at},
		queue.ChecksCompleted{TestedSHA: "tested", Success: true},
		queue.PRObserved{PR: 2, HeadSHA: "held-head", Trusted: true, PendingConditions: "sign-off"},
		queue.EnqueueRequested{PR: 2, At: at},
		queue.LandingStarted{BatchID: "batch", AttemptID: "attempt", FenceToken: "fence", At: at},
		queue.Landed{BatchID: "batch", NewBaseSHA: "tested", NewFence: "next"},
		queue.PRObserved{PR: 2, HeadSHA: "held-head", Eligible: true, Trusted: true},
	}
	type vector struct {
		Kind           string
		SnapshotSHA256 string
		Actions        []json.RawMessage
		StatusSHA256   string
	}
	var vectors []vector
	j := store.Journal{Key: store.QueueKey{Installation: "7", Repository: "owner/repo", BaseBranch: "main"}, Seed: queue.State{BaseBranch: "main", Fence: "fence"}}
	state := j.Seed
	reader := journalreader.New(state, dependency.JournalInitialized{}, nil)
	for i, e := range events {
		kind, raw, err := queue.EncodeEvent(e)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reader.Apply(uint64(i+1), raw); err != nil {
			t.Fatal(err)
		}
		state = reader.Queue
		j.Events = append(j.Events, store.JournalEvent{ID: int64(i + 1), Event: e, Payload: raw, RecordedAt: at})
		snap, err := queue.EncodeSnapshot(state)
		if err != nil {
			t.Fatal(err)
		}
		actions := []json.RawMessage{}
		for _, a := range queue.Plan(queue.Config{MergeMethod: queue.Merge, CheckTimeout: time.Minute, ResolveAfter: time.Minute}, state, at) {
			_, raw, err := queue.EncodeAction(a)
			if err != nil {
				t.Fatal(err)
			}
			actions = append(actions, raw)
		}
		q, _, _ := status.Project(j, 0, 0, nil)
		statusRaw, err := json.Marshal(q)
		if err != nil {
			t.Fatal(err)
		}
		vectors = append(vectors, vector{kind, hashBytes(snap), actions, hashBytes(statusRaw)})
	}
	raw, err := json.MarshalIndent(vectors, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	want, err := os.ReadFile("testdata/legacy-replay-main.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, raw) {
		t.Fatalf("main replay vectors changed: %s", raw)
	}
}
func hashBytes(raw []byte) string { return fmt.Sprintf("%x", sha256.Sum256(raw)) }
