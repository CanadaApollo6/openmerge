package queuemetrics

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

var epoch = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func at(seconds int) time.Time { return epoch.Add(time.Duration(seconds) * time.Second) }

// journal builds fixture journals through the production encode/decode and
// fold. Every helper asserts the fold accepted the event it emitted.
type journal struct {
	t    *testing.T
	j    store.Journal
	s    queue.State
	next int
}

func newJournal(t *testing.T) *journal {
	t.Helper()
	seed := queue.State{BaseBranch: "main", BaseSHA: "base", Fence: "fence"}
	return &journal{t: t, j: store.Journal{Key: store.QueueKey{Installation: "1", Repository: "fixture/app", BaseBranch: "main"}, Seed: seed}, s: seed}
}

func (f *journal) emit(seconds int, e queue.Event) {
	f.t.Helper()
	_, payload, err := queue.EncodeEvent(e)
	if err != nil {
		f.t.Fatal(err)
	}
	decoded, err := queue.DecodeEvent(payload)
	if err != nil {
		f.t.Fatal(err)
	}
	f.j.Events = append(f.j.Events, store.JournalEvent{ID: int64(len(f.j.Events) + 1), RecordedAt: at(seconds), Payload: payload, Event: decoded})
	f.s = queue.Apply(f.s, decoded)
}

func (f *journal) config(seconds, depth int) {
	f.t.Helper()
	cfg := queue.Config{SpeculationMaxDepth: depth}
	f.emit(seconds, cfg.Applied(fmt.Sprintf("policy-%d", depth), strings.Repeat(fmt.Sprint(depth), 64)))
}

func (f *journal) enqueue(seconds int, prs ...int) {
	f.t.Helper()
	for _, pr := range prs {
		f.emit(seconds, queue.PRObserved{PR: pr, HeadSHA: fmt.Sprintf("head-%d", pr), Eligible: true, Trusted: true})
		f.emit(seconds, queue.EnqueueRequested{PR: pr, At: at(seconds)})
		if Entry(f.s, pr) == nil {
			f.t.Fatalf("PR %d was not admitted", pr)
		}
	}
}

func (f *journal) batchPRs(prs []int) []queue.BatchPR {
	f.t.Helper()
	var out []queue.BatchPR
	for _, pr := range prs {
		en := Entry(f.s, pr)
		if en == nil {
			f.t.Fatalf("PR %d has no admission", pr)
		}
		out = append(out, queue.BatchPR{PR: pr, HeadSHA: en.HeadSHA})
	}
	return out
}

// build starts a legacy batch and pushes its test ref at the same instant.
func (f *journal) build(seconds int, prs ...int) string {
	f.t.Helper()
	f.next++
	id := fmt.Sprintf("b%d", f.next)
	f.emit(seconds, queue.BatchStarted{BatchID: id, BaseSHA: f.s.BaseSHA, PRs: f.batchPRs(prs), At: at(seconds)})
	f.emit(seconds, queue.TestRefBuilt{BatchID: id, TestedSHA: "tested-" + id, At: at(seconds), Method: queue.Merge})
	if b := f.s.Batch(id); b == nil || b.TestedSHA != "tested-"+id {
		f.t.Fatalf("batch %s not built: %+v", id, b)
	}
	return id
}

// chain starts a speculative batch behind the current chain and builds it.
func (f *journal) chain(seconds int, prs ...int) string {
	f.t.Helper()
	f.next++
	id := fmt.Sprintf("b%d", f.next)
	parent, base, pos := "", f.s.BaseSHA, uint64(1)
	if bs := f.s.Batches(); len(bs) > 0 {
		b := bs[len(bs)-1]
		parent, base, pos = b.ID, b.LandingCommit(), b.ChainPosition+1
	}
	f.emit(seconds, queue.ChainedBatchStarted{BatchID: id, BaseSHA: base, PRs: f.batchPRs(prs), At: at(seconds), ParentBatchID: parent, Position: pos})
	tested := "tested-" + id
	f.emit(seconds, queue.ChainedTestRefBuilt{BatchID: id, TestedSHA: tested, LandingSHA: tested, At: at(seconds), Method: queue.Merge, ParentBatchID: parent, Position: pos})
	if b := f.s.Batch(id); b == nil || b.TestedSHA != tested {
		f.t.Fatalf("chained batch %s not built: %+v", id, b)
	}
	return id
}

func (f *journal) checks(seconds int, id string, success bool) {
	f.t.Helper()
	f.emit(seconds, queue.ChecksCompleted{TestedSHA: "tested-" + id, Success: success})
}

func (f *journal) land(seconds int, id string) {
	f.t.Helper()
	f.emit(seconds, queue.LandingStarted{BatchID: id, AttemptID: "attempt-" + id, FenceToken: f.s.Fence, At: at(seconds)})
	b := f.s.Batch(id)
	if b == nil || b.Phase != queue.Landing {
		f.t.Fatalf("landing %s did not start: %+v", id, b)
	}
	f.emit(seconds, queue.Landed{BatchID: id, NewBaseSHA: b.LandingCommit(), NewFence: "fence-" + id})
	if f.s.Batch(id) != nil {
		f.t.Fatalf("batch %s did not land", id)
	}
}

func (f *journal) pass(seconds int, id string) {
	f.t.Helper()
	f.checks(seconds, id, true)
	f.land(seconds, id)
}

func (f *journal) dequeue(seconds, pr int, reason queue.DequeueReason) {
	f.t.Helper()
	en := Entry(f.s, pr)
	if en == nil {
		f.t.Fatalf("PR %d not queued", pr)
	}
	f.emit(seconds, queue.Dequeued{PR: pr, AdmissionID: en.AdmissionID, Reason: reason})
	if Entry(f.s, pr) != nil {
		f.t.Fatalf("PR %d was not dequeued", pr)
	}
}

// rerun records a failed observation, the single retry and the forge's
// accepted dispatch for the active batch.
func (f *journal) rerun(seconds int, id string) {
	f.t.Helper()
	b := f.s.Batch(id)
	tested := "tested-" + id
	timeout := time.Hour
	deadline := b.StartedTestingAt.Add(timeout)
	failed := []queue.CheckRun{{ID: 7, Attempt: 1, Name: "ci", Source: "app:1", State: "failure"}}
	f.emit(seconds, queue.ChecksObserved{BatchID: id, TestedSHA: tested, Runs: failed, Terminal: true, OriginalDeadline: deadline, Timeout: timeout})
	f.emit(seconds, queue.CheckRerunRequested{BatchID: id, TestedSHA: tested, Runs: failed, OriginalDeadline: deadline, At: at(seconds), Deadline: at(seconds).Add(timeout), Timeout: timeout})
	f.emit(seconds, queue.CheckRerunDispatched{BatchID: id, TestedSHA: tested, Detail: "accepted"})
	if r := f.s.Batch(id).CheckRetry; r == nil || !r.Dispatched || r.Unsupported {
		f.t.Fatalf("rerun not dispatched: %+v", r)
	}
	passed := []queue.CheckRun{{ID: 7, Attempt: 2, Name: "ci", Source: "app:1", State: "success"}}
	f.emit(seconds+30, queue.ChecksObserved{BatchID: id, TestedSHA: tested, Runs: passed, Terminal: true, Success: true, OriginalDeadline: deadline, Timeout: timeout})
	if !f.s.ChecksPassed(tested) {
		f.t.Fatal("rerun did not pass")
	}
}
