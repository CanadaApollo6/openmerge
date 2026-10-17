package sim

import (
	"container/heap"
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
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
	// TailThreshold zero disables tail measurement.
	TailThreshold time.Duration
}

func (c CIConfig) validate() error {
	if c.Runners < 0 || c.TailThreshold < 0 || math.IsNaN(c.InteractionRate) || c.InteractionRate < 0 || c.InteractionRate > 1 {
		return fmt.Errorf("invalid CI workload configuration")
	}
	return nil
}

// CIMetrics counts service actually consumed, including cancelled and orphaned
// runs. WastedRuns counts started attempts other than the final successful
// attempt of a landed batch, including failed diagnosis, superseded retries and
// invalidated work. A queued cancellation is not a started or wasted run.
type CIMetrics struct {
	RunsStarted, RerunsStarted, WastedRuns                                   int
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
	attempt                          uint64
	sha                              string
	duration                         time.Duration
	started, finished                time.Time
	cancelled, cancellationRequested bool
	rerun, cancellationUnsupported   bool
	success                          bool
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
	attempt := uint64(1)
	if previous := s.tests[sha].job; previous != nil {
		id = previous.id + 1
		attempt = previous.attempt + 1
	}
	job := &ciJob{id: id, attempt: attempt, sha: sha, duration: duration, rerun: s.tests[sha].done || s.tests[sha].rerun, success: s.tests[sha].success}
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
		job.cancellationUnsupported = true
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
	s.forge.SetCheckRuns(s.repo, sha, []forge.CheckRun{{ID: s.tests[sha].job.id, Attempt: s.tests[sha].job.attempt, Name: "ci", State: forge.Pending}})
	return true
}

// retryChecksEnabled controls only the additive retry-aware event path. The
// legacy path remains untouched when the planner setting is zero.
func (s *simulation) retryChecksEnabled() bool {
	return s.cfg.Planner.ChecksRetryFailed == 1
}

func (s *simulation) retryObservation(sha string, run *testRun, terminal, success bool) (queue.ChecksObserved, error) {
	checks, err := s.forge.CheckRuns(context.Background(), s.repo, sha)
	if err != nil {
		return queue.ChecksObserved{}, err
	}
	started := run.startedTestingAt
	if active := s.state.Batch(run.batchID); active != nil && active.TestedSHA == sha && !active.StartedTestingAt.IsZero() {
		started = active.StartedTestingAt
	}
	timeout := s.cfg.Planner.CheckTimeout
	deadline := time.Time{}
	if timeout > 0 {
		deadline = started.Add(timeout)
	}
	runs := make([]queue.CheckRun, 0, len(checks))
	for _, check := range checks {
		attempt := check.Attempt
		if attempt == 0 {
			attempt = 1
		}
		runs = append(runs, queue.CheckRun{ID: check.ID, Attempt: attempt, Name: check.Name, Source: check.Source, State: string(check.State)})
	}
	return queue.ChecksObserved{
		BatchID: run.batchID, TestedSHA: sha, Runs: runs, Terminal: terminal, Success: success,
		OriginalDeadline: deadline, Timeout: timeout,
	}, nil
}

func (s *simulation) ciMetrics(merged int) *CIMetrics {
	m := &CIMetrics{}
	for _, job := range s.ciJobs {
		if job.cancellationRequested {
			m.CancellationsRequested++
			if job.cancellationUnsupported {
				m.CancellationsUnsupported++
			} else if job.cancelled || !job.finished.IsZero() {
				m.CancellationsConfirmed++
			}
		}
		if job.started.IsZero() {
			continue
		}
		m.RunsStarted++
		if run := s.tests[job.sha]; run == nil || !run.landed || run.job != job || job.cancelled {
			m.WastedRuns++
		}
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
		if s.cfg.CI.TailThreshold > 0 && elapsed >= s.cfg.CI.TailThreshold {
			beyond++
		}
	}
	if merged > 0 {
		m.RunnerMinutesPerMergedChange = m.RunnerMinutes / float64(merged)
		m.TailFraction = float64(beyond) / float64(merged)
	}
	return m
}
