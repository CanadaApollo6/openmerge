package sim

import (
	"container/heap"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// CIConfig describes capabilities of the simulated CI provider, independently
// of what the planner requests. Zero runners means unlimited, as in v1.
type CIConfig struct {
	Runners               int
	CancellationSupported bool
	RerunsSupported       bool
	// InteractionRate selects disjoint adjacent PR-number pairs. Neither member
	// is broken alone; a tested commit containing both fails, including on base.
	InteractionRate float64
	TailThreshold   time.Duration
}

func (c CIConfig) validate() error {
	if c.Runners < 0 || c.TailThreshold < 0 || math.IsNaN(c.InteractionRate) || c.InteractionRate < 0 || c.InteractionRate > 1 {
		return fmt.Errorf("invalid CI workload configuration")
	}
	return nil
}

// CIMetrics counts service actually consumed, including cancelled and orphaned
// runs. A queued run cancelled before acquiring a runner is not a started run.
type CIMetrics struct {
	RunsStarted, RerunsStarted                                               int
	CancellationsRequested, CancellationsConfirmed, CancellationsUnsupported int
	RunnerMinutes, RunnerMinutesPerMergedChange                              float64
	Merges                                                                   []MergeSample
	TailFraction                                                             float64
}

// MergeSample records a merged change for pooled and first-day latency slices.
type MergeSample struct {
	PR                    int
	Enqueued, TimeToMerge time.Duration
}

type ciJob struct {
	id                               uint64
	sha                              string
	duration                         time.Duration
	started, finished                time.Time
	cancelled, cancellationRequested bool
	rerun                            bool
}

func (s *simulation) interactionFails(prs []int) bool {
	for _, pair := range s.interactions {
		if slices.Contains(prs, pair[0]) && slices.Contains(prs, pair[1]) {
			return true
		}
	}
	return false
}

func (s *simulation) submitCI(sha string, duration time.Duration) {
	id := uint64(1)
	if previous := s.tests[sha].job; previous != nil {
		id = previous.id + 1
	}
	job := &ciJob{id: id, sha: sha, duration: duration, rerun: s.tests[sha].done || s.tests[sha].rerun}
	s.tests[sha].job = job
	s.ciJobs = append(s.ciJobs, job)
	s.ciWaiting = append(s.ciWaiting, job)
	s.admitCI()
}

func (s *simulation) admitCI() {
	for len(s.ciWaiting) > 0 && (s.cfg.CI.Runners == 0 || s.ciRunning < s.cfg.CI.Runners) {
		job := s.ciWaiting[0]
		s.ciWaiting = s.ciWaiting[1:]
		if job.cancelled {
			continue
		}
		job.started = s.now
		s.ciRunning++
		s.result.CIRuns++
		s.sequence++
		heap.Push(&s.scheduled, scheduledEvent{at: s.now.Add(job.duration), sequence: s.sequence, kind: ciComplete, sha: job.sha, job: job})
	}
}

// cancelCI is idempotent and best effort. Unsupported cancellations leave both
// queued and running work in place; late completions retain their old SHA.
func (s *simulation) cancelCI(sha string) {
	run := s.tests[sha]
	if run == nil || run.job == nil {
		return
	}
	job := run.job
	if job.cancelled || !job.finished.IsZero() || job.cancellationRequested {
		return
	}
	job.cancellationRequested = true
	if !s.cfg.CI.CancellationSupported {
		return
	}
	job.cancelled = true
	if !job.started.IsZero() {
		job.finished = s.now
		s.ciRunning--
	}
	s.admitCI()
}

// rerunFailedCI is the CI capability PR 2 can connect to its retry action. It
// deliberately emits no planner event or retry decision in PR 1.
func (s *simulation) rerunFailedCI(sha string) bool {
	run := s.tests[sha]
	if s.cfg.CI == nil || !s.cfg.CI.RerunsSupported || run == nil || !run.done || run.success {
		return false
	}
	commit, err := s.forge.Commit(s.repo, sha)
	if err != nil {
		s.violation = err
		return false
	}
	run.success = s.ci.Float64() >= s.cfg.FlakeRate
	for _, pr := range commit.PRs {
		if s.prs[pr].broken {
			run.success = false
		}
	}
	if s.interactionFails(commit.PRs) {
		run.success = false
	}
	// Keep done through submit so the ledger identifies a rerun.
	s.submitCI(sha, s.ciDuration())
	run.done = false
	s.result.CIReruns++
	s.forge.SetCheckRuns(s.repo, sha, []forge.CheckRun{{ID: s.tests[sha].job.id, Name: "ci", State: forge.Pending}})
	return true
}

func (s *simulation) ciMetrics(merged int) *CIMetrics {
	m := &CIMetrics{}
	for _, job := range s.ciJobs {
		if job.cancellationRequested {
			m.CancellationsRequested++
			if job.cancelled {
				m.CancellationsConfirmed++
			} else {
				m.CancellationsUnsupported++
			}
		}
		if job.started.IsZero() {
			continue
		}
		m.RunsStarted++
		if job.rerun {
			m.RerunsStarted++
		}
		end := job.finished
		if end.IsZero() {
			end = s.now
		}
		m.RunnerMinutes += end.Sub(job.started).Minutes()
	}
	beyond := 0
	for pr := 1; pr < len(s.prs); pr++ {
		p := s.prs[pr]
		if p.landed.IsZero() {
			continue
		}
		elapsed := p.landed.Sub(p.enqueued)
		m.Merges = append(m.Merges, MergeSample{PR: pr, Enqueued: p.enqueued.Sub(s.start), TimeToMerge: elapsed})
		if elapsed >= s.cfg.CI.TailThreshold {
			beyond++
		}
	}
	if merged > 0 {
		m.RunnerMinutesPerMergedChange = m.RunnerMinutes / float64(merged)
		m.TailFraction = float64(beyond) / float64(merged)
	}
	return m
}
