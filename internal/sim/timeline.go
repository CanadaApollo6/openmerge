package sim

import (
	"container/heap"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

type eventKind uint8

const (
	arrival eventKind = iota
	enqueue
	ciComplete
)

type scheduledEvent struct {
	at       time.Time
	sequence uint64
	kind     eventKind
	pr       int
	sha      string
}

// Equal-time deliveries retain insertion order; neither maps nor scheduler
// timing can decide whether an arrival, enqueue, or check is observed first.
type timeline []scheduledEvent

func (q timeline) Len() int { return len(q) }
func (q timeline) Less(i, j int) bool {
	if q[i].at.Equal(q[j].at) {
		return q[i].sequence < q[j].sequence
	}
	return q[i].at.Before(q[j].at)
}
func (q timeline) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *timeline) Push(x any)   { *q = append(*q, x.(scheduledEvent)) }
func (q *timeline) Pop() any {
	old := *q
	x := old[len(old)-1]
	*q = old[:len(old)-1]
	return x
}

func (s *simulation) schedule(at time.Time, kind eventKind, pr int, sha string) {
	s.sequence++
	heap.Push(&s.scheduled, scheduledEvent{at: at, sequence: s.sequence, kind: kind, pr: pr, sha: sha})
}

func (s *simulation) deliver(event scheduledEvent) error {
	switch event.kind {
	case arrival:
		p := &s.prs[event.pr]
		// Independent file changes on the initial tree avoid accidental conflicts
		// or dependencies on previously landed PRs.
		pr, err := s.forge.OpenPR(s.repo, event.pr, s.rootSHA, !p.untrusted)
		if err != nil {
			return err
		}
		p.head = pr.HeadSHA
		s.record(queue.PRObserved{PR: event.pr, HeadSHA: pr.HeadSHA, Eligible: true, Trusted: !p.untrusted})
		s.schedule(s.now.Add(time.Second), enqueue, event.pr, "")
	case enqueue:
		s.prs[event.pr].enqueued = s.now
		s.record(queue.EnqueueRequested{PR: event.pr, At: s.now})
	case ciComplete:
		run := s.tests[event.sha]
		run.done = true
		state := forge.Failure
		if run.success {
			state = forge.Success
		}
		s.forge.SetCheckRuns(s.repo, event.sha, []forge.CheckRun{{ID: 1, Name: "ci", State: state}})
		if s.webhook.Float64() < s.cfg.DropWebhookRate {
			s.result.DroppedWebhooks++
		} else {
			s.record(queue.ChecksCompleted{TestedSHA: event.sha, Success: run.success})
		}
	}
	return nil
}
