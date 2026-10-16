package sim

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

var (
	errForge   = errors.New("simulated forge failure")
	errDelayed = &pushTimeout{}
	errCrash   = errors.New("simulated crash before Landed emit")
)

type pullRequest struct {
	broken, untrusted        bool
	head                     string
	shared, repush, repushed bool
	enqueued, landed         time.Time
	dequeued                 queue.DequeueReason
}

type testRun struct {
	heads   []queue.BatchPR
	success bool
	done    bool
	base    string
	covered []string
}

type loggedEvent struct {
	At    time.Time
	Event queue.Event
}

type simulation struct {
	cfg                 Config
	now, start          time.Time
	forge               *fake.Forge
	repo                forge.RepoRef
	state               queue.State
	executor            executor.Executor
	ci, webhook, faults *rand.Rand
	prs                 []pullRequest
	tests               map[string]*testRun
	events              []loggedEvent
	scheduled           timeline
	sequence, attempt   uint64
	completed           map[string]bool
	retryAt             map[string]time.Time
	pendingFaults       map[string]int
	mainSHA             string
	rootSHA             string
	landed              map[int]string
	violation           error
	result              Result
	boundary            *faultForge
	initialized         bool
	attempts            map[string]string
	abandoned           map[string]string
	observed            map[string]*forgeLanding
	lastFence           string
}

// Run drains a workload, outstanding CI, and durable side effects. It returns a
// partial Result with an error on an invariant violation, pause, or step-limit
// wedge. There is no sleep, goroutine, wall clock, or external forge dependency.
func Run(cfg Config) (Result, error) {
	s, err := newSimulation(cfg)
	if err != nil {
		return Result{}, err
	}
	return s.run()
}

func newSimulation(cfg Config) (*simulation, error) {
	cfg, err := cfg.normalized()
	if err != nil {
		return nil, err
	}
	start := time.Unix(0, 0).UTC()
	s := &simulation{
		cfg: cfg, start: start, now: start, forge: fake.New(), repo: forge.RepoRef{Path: "sim/project"},
		ci: rand.New(rand.NewPCG(cfg.Seed, 2)), webhook: rand.New(rand.NewPCG(cfg.Seed, 3)),
		faults: rand.New(rand.NewPCG(cfg.Seed, 4)), tests: make(map[string]*testRun),
		completed: make(map[string]bool), retryAt: make(map[string]time.Time),
		pendingFaults: make(map[string]int), landed: make(map[int]string),
		attempts: make(map[string]string), abandoned: make(map[string]string), observed: make(map[string]*forgeLanding),
		result: Result{Config: cfg, FaultsByMethod: make(map[string]int), PRs: cfg.PRs, Dequeued: make(map[queue.DequeueReason]int), Blocked: make(map[queue.DequeueReason]int)},
	}
	base, fence := s.forge.Init(s.repo, "main", "openmerge/fence/main")
	s.lastFence = fence
	s.boundary = &faultForge{Forge: s.forge, sim: s, scope: "setup"}
	s.mainSHA = base
	s.rootSHA = base
	s.state = queue.State{BaseBranch: "main", BaseSHA: base, Fence: fence}
	s.executor = executor.Executor{
		Driver: s.boundary, Git: s.boundary, Repo: s.repo, Base: "main", RequiredChecks: []string{"ci"},
		Now: func() time.Time { return s.now },
		NewID: func() (string, error) {
			s.attempt++
			return fmt.Sprintf("%032x", s.attempt), nil
		},
	}
	s.forge.OnTestRef(s.startCI)
	// Keep workload draws separate from operational draws so fault and webhook
	// variants always submit exactly the same PRs at exactly the same times.
	rng := rand.New(rand.NewPCG(cfg.Seed, 1))
	churn := rand.New(rand.NewPCG(cfg.Seed, 5))
	shared := false
	probes := cfg.PRs / 50
	if cfg.PRs > 0 && probes == 0 {
		probes = 1
	}
	s.result.Untrusted = probes
	s.prs = make([]pullRequest, cfg.PRs+probes+1) // PR numbers start at 1.
	for pr := 1; pr < len(s.prs); pr++ {
		p := &s.prs[pr]
		p.untrusted = pr > cfg.PRs
		if pr%2 == 1 {
			shared = churn.Float64() < cfg.ConflictRate
		}
		p.shared = shared && !p.untrusted
		p.repush = churn.Float64() < cfg.RepushRate && !p.untrusted
		p.broken = !p.untrusted && rng.Float64() < cfg.BrokenRate
		if p.broken {
			s.result.Broken++
		}
		var delay time.Duration
		if cfg.ArrivalWindow > 0 {
			delay = time.Duration(rng.Int64N(int64(cfg.ArrivalWindow)))
		}
		s.schedule(s.start.Add(delay), arrival, pr, "")
	}
	return s, nil
}

func (s *simulation) run() (Result, error) {
	for s.result.Steps < s.cfg.MaxSteps {
		done, err := s.step()
		if done {
			if err := s.checkSettled(); err != nil {
				return s.metrics(), err
			}
			return s.metrics(), nil
		}
		s.result.Steps++
		if err == nil {
			err = s.checkInvariants()
		}
		if err != nil {
			return s.metrics(), fmt.Errorf("step %d at %s: %w", s.result.Steps, s.now.Sub(s.start), err)
		}
	}
	return s.metrics(), fmt.Errorf("wedge: exceeded %d steps at %s (%d queued, active %v)",
		s.cfg.MaxSteps, s.now.Sub(s.start), len(s.state.Entries), s.state.Active)
}

func (s *simulation) step() (bool, error) {
	if !s.initialized {
		if retry := s.retryAt["setup"]; retry.After(s.now) {
			s.now = retry
		}
		_, err := s.boundary.EnsureFence(context.Background(), s.repo, "openmerge/fence/main")
		if errors.Is(err, errForge) {
			s.retryAt["setup"] = s.now.Add(s.cfg.RetryDelay)
			return false, nil
		}
		if err != nil {
			return false, err
		}
		s.initialized = true
		s.recoverFaults("setup")
		return false, nil
	}

	if len(s.scheduled) > 0 && !s.scheduled[0].at.After(s.now) {
		return false, s.deliver(heap.Pop(&s.scheduled).(scheduledEvent))
	}
	actions := queue.Plan(s.cfg.Planner, s.state, s.now)
	var next time.Time
	consider := func(at time.Time) {
		if at.After(s.now) && (next.IsZero() || at.Before(next)) {
			next = at
		}
	}
	for _, action := range actions {
		if s.completed[action.Key()] {
			continue
		}
		if retry := s.retryAt[action.Key()]; retry.After(s.now) {
			consider(retry)
			continue
		}
		// Every emit advances the watermark. Replan after each action so no later
		// action from the old plan can outlive its authorization (ADR 0002).
		return false, s.execute(action)
	}
	if len(s.scheduled) > 0 {
		consider(s.scheduled[0].at)
	}
	if at, ok := queue.NextWake(s.cfg.Planner, s.state, s.now); ok {
		consider(at)
	}
	if !next.IsZero() {
		s.now = next
		return false, nil
	}
	if len(s.state.Entries) > 0 || len(actions) > 0 {
		return false, fmt.Errorf("wedge: %d queued with no executable action or pending timer (pause=%q, active=%v)",
			len(s.state.Entries), s.state.PauseReason(s.cfg.Planner), s.state.Active)
	}
	return true, nil
}

func (s *simulation) execute(action queue.Action) error {
	s.boundary.scope = faultScope(action)
	if a, ok := action.(queue.Land); ok && s.faults.Float64() < s.cfg.Faults.DuplicateExecutionRate {
		s.sequence++
		scope := fmt.Sprintf("duplicate/%d", s.sequence)
		s.injectFault(scope)
		s.result.DuplicateExecutions++
		heap.Push(&s.scheduled, scheduledEvent{at: s.now.Add(s.cfg.Faults.ReplayDelay), sequence: s.sequence, kind: duplicateLand, action: a, scope: scope})
	}
	err := s.executor.Execute(context.Background(), action, func(event queue.Event) error {
		if _, landed := event.(queue.Landed); landed {
			if _, pushed := action.(queue.Land); pushed && s.faults.Float64() < s.cfg.Faults.LandingCrashRate {
				s.injectFault(faultScope(action))
				return errCrash
			}
		}
		s.record(event)
		return s.violation
	})
	if err != nil {
		if !errors.Is(err, errForge) && !errors.Is(err, errCrash) && !errors.Is(err, errDelayed) {
			return fmt.Errorf("execute %s: %w", action.Key(), err)
		}
		s.retryAt[action.Key()] = s.now.Add(s.cfg.RetryDelay)
		return nil
	}
	s.completed[action.Key()] = true
	delete(s.retryAt, action.Key())
	s.recoverFaults(faultScope(action))
	return nil
}

func (s *simulation) record(event queue.Event) {
	batch := s.state.Active
	switch event := event.(type) {
	case queue.LandingStarted:
		if batch != nil && batch.ID == event.BatchID {
			s.attempts[event.AttemptID] = batch.TestedSHA
		}
	case queue.Landed:
		if batch != nil && batch.ID == event.BatchID && batch.Phase == queue.Landing {
			if err := s.verifyLanded(batch, event); err != nil {
				s.violation = err
				return
			}
			for _, pr := range batch.PRs {
				s.prs[pr.PR].landed = s.now
			}
			s.recoverFaults("checks/" + batch.ID)
			s.recoverFaults("landing/" + batch.ID)
		}
	case queue.LandingAbandoned:
		if batch != nil && batch.ID == event.BatchID && batch.Phase == queue.Landing {
			if err := s.verifyAbandoned(batch, event); err != nil {
				s.violation = err
				return
			}
		}
		s.recoverFaults("landing/" + event.BatchID)
	case queue.Dequeued:
		for _, entry := range s.state.Entries {
			if entry.PR == event.PR && entry.AdmissionID == event.AdmissionID {
				s.prs[event.PR].dequeued = event.Reason
				s.result.Dequeued[event.Reason]++
				if event.Reason == queue.HeadChanged {
					s.schedule(s.now.Add(time.Second), enqueue, event.PR, "")
				}
			}
		}
		if batch != nil && slices.ContainsFunc(batch.PRs, func(pr queue.BatchPR) bool { return pr.PR == event.PR }) {
			s.recoverFaults("checks/" + batch.ID)
		}
	}
	s.events = append(s.events, loggedEvent{At: s.now, Event: event})
	s.state = queue.Apply(s.state, event)
}

func (s *simulation) startCI(sha string, prs []int) {
	run := &testRun{base: s.mainSHA, success: s.ci.Float64() >= s.cfg.FlakeRate}
	commit, err := s.forge.Commit(s.repo, sha)
	if err != nil {
		s.violation = err
		return
	}
	for _, pr := range commit.PRs {
		if s.prs[pr].broken {
			run.success = false
		}
		if s.prs[pr].untrusted {
			s.violation = fmt.Errorf("untrusted PR %d appears in test commit %s", pr, sha)
		}
	}
	for _, pr := range prs {
		actual, err := s.forge.PullRequest(context.Background(), s.repo, pr)
		if err != nil {
			s.violation = err
			return
		}
		run.heads = append(run.heads, queue.BatchPR{PR: pr, HeadSHA: actual.HeadSHA})
	}
	run.covered, err = s.firstParentRange(run.base, sha)
	if err != nil {
		s.violation = err
		return
	}
	s.tests[sha] = run
	s.result.CIRuns++
	s.forge.SetCheckRuns(s.repo, sha, []forge.CheckRun{{ID: 1, Name: "ci", State: forge.Pending}})
	duration := s.cfg.CIDuration
	if s.cfg.CIMin > 0 {
		duration = s.cfg.CIMin
		if s.cfg.CIMax > s.cfg.CIMin {
			duration += time.Duration(s.ci.Int64N(int64(s.cfg.CIMax - s.cfg.CIMin)))
		}
	}
	s.schedule(s.now.Add(duration), ciComplete, 0, sha)
}

func (s *simulation) metrics() Result {
	r := s.result
	r.Blocked = make(map[queue.DequeueReason]int)
	r.StillQueued = len(s.state.Entries)
	r.SimulatedSpan = s.now.Sub(s.start)
	var times []time.Duration
	for pr := 1; pr < len(s.prs); pr++ {
		p := s.prs[pr]
		if !p.landed.IsZero() {
			r.Merged++
			times = append(times, p.landed.Sub(p.enqueued))
		}

		if status := s.forge.Statuses(s.repo, pr)[p.head]; status.State == forge.Blocked {
			r.Blocked[queue.DequeueReason(status.Reason)]++
		}
	}
	if len(times) > 0 {
		slices.Sort(times)
		// Sum as floats to avoid overflowing Duration on large workloads.
		var total float64
		for _, elapsed := range times {
			total += float64(elapsed)
		}
		r.MeanTimeToMerge = time.Duration(total / float64(len(times)))
		r.MedianTimeToMerge = times[len(times)/2]
		if len(times)%2 == 0 {
			a, b := times[len(times)/2-1], times[len(times)/2]
			r.MedianTimeToMerge = a + (b-a)/2
		}
		r.P95TimeToMerge = times[len(times)-len(times)/20-1] // nearest rank ceil(.95*n).
		r.CIRunsPerMergedPR = float64(r.CIRuns) / float64(r.Merged)
	}
	return r
}
