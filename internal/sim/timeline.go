package sim

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

type eventKind uint8

const (
	arrival eventKind = iota
	enqueue
	ciComplete
	repush
	delayedPush
	duplicateLand
)

type scheduledEvent struct {
	at       time.Time
	sequence uint64
	kind     eventKind
	pr       int
	sha      string
	request  forge.LandRequest
	action   queue.Land
	scope    string
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
		changes := map[string]string{fmt.Sprintf("pr-%d.txt", event.pr): fmt.Sprintf("PR %d", event.pr)}
		if p.shared {
			changes[fmt.Sprintf("shared-%d.txt", (event.pr-1)/2)] = fmt.Sprintf("PR %d", event.pr)
		}
		pr, err := s.forge.OpenPRWithFiles(s.repo, event.pr, s.rootSHA, !p.untrusted, changes)
		if err != nil {
			return err
		}
		p.head = pr.HeadSHA
		s.record(queue.PRObserved{PR: event.pr, HeadSHA: pr.HeadSHA, Eligible: true, Trusted: !p.untrusted})
		s.schedule(s.now.Add(time.Second), enqueue, event.pr, "")
	case enqueue:
		s.prs[event.pr].enqueued = s.now
		s.record(queue.EnqueueRequested{PR: event.pr, At: s.now})
		p := &s.prs[event.pr]
		if p.repush && !p.repushed {
			p.repushed = true
			duration := s.cfg.CIDuration
			if s.cfg.CIMin > 0 {
				duration = s.cfg.CIMin
			}
			s.schedule(s.now.Add(max(time.Nanosecond, min(time.Second, duration/2))), repush, event.pr, "")
		}
	case repush:
		if !slices.ContainsFunc(s.state.Entries, func(e queue.Entry) bool { return e.PR == event.pr }) {
			return nil
		}
		pr, err := s.forge.PushPR(s.repo, event.pr, "updated queued head")
		if err != nil {
			return err
		}
		s.prs[event.pr].head = pr.HeadSHA
		s.record(queue.PRObserved{PR: event.pr, HeadSHA: pr.HeadSHA, Eligible: true, Trusted: !s.prs[event.pr].untrusted})
	case delayedPush:
		_, err := s.forge.Land(context.Background(), s.repo, event.request)
		if errors.Is(err, forge.ErrLeaseRejected) {
			s.result.RejectedReplays++
			return nil
		}
		if err != nil {
			return fmt.Errorf("replay landing: %w", err)
		}
		if err := s.checkInvariants(); err != nil {
			return err
		}
	case duplicateLand:
		s.boundary.scope = event.scope
		err := s.executor.Execute(context.Background(), event.action, func(e queue.Event) error { s.record(e); return s.violation })
		if inv := s.checkInvariants(); inv != nil {
			return inv
		}
		var tampered *executor.ErrFenceTampered
		if err != nil && !errors.As(err, &tampered) && !errors.Is(err, forge.ErrLeaseRejected) && !errors.Is(err, errForge) && !errors.Is(err, errDelayed) {
			return err
		}
		s.recoverFaults(event.scope)
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
