package sim

import (
	"container/heap"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
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
	errStale   = errors.New("action is no longer authorized to start")
)

type pullRequest struct {
	broken, untrusted        bool
	head                     string
	shared, repush, repushed bool
	enqueued, landed         time.Time
	firstEnqueued            time.Time
	externalBeforeLand       bool
	risk, score              float64
	labInvalidation          *labInvalidation
	dequeued                 queue.DequeueReason
}

type testRun struct {
	job              *ciJob
	heads            []queue.BatchPR
	success          bool
	done             bool
	landed           bool
	rerun            bool
	batchID          string
	startedTestingAt time.Time
	base             string
	covered          []string
	method           queue.MergeMethod
	ref              string
}

type loggedEvent struct {
	At    time.Time
	Event queue.Event
}

type simulation struct {
	expectedWindow             int
	windowFailures             map[string]bool
	expectedOutcomes           []bool
	windowArea, coldWindowArea float64
	// Fresh batch starts, those at window 1 and those the backlog guard raised
	// above the adaptive size (lab metrics only).
	freshBatches, freshAtWindowOne, guardRaised int
	ciWaiting                  []*ciJob
	ciRunning                  int
	ciJobs                     []*ciJob
	interactions               [][2]int
	cfg                        Config
	now, start                 time.Time
	forge                      *fake.Forge
	repo                       forge.RepoRef
	state                      queue.State
	executor                   executor.Executor
	ci, webhook, faults        *rand.Rand
	prs                        []pullRequest
	tests                      map[string]*testRun
	events                     []loggedEvent
	scheduled                  timeline
	sequence, attempt          uint64
	completed                  map[string]bool
	planned                    map[string]queue.Action
	retryAt                    map[string]time.Time
	pendingFaults              map[string]int
	mainSHA                    string
	mainTips                   []string
	rootSHA                    string
	landed                     map[int]string
	violation                  error
	result                     Result
	boundary                   *faultForge
	initialized                bool
	attempts                   map[string]string
	disturbance                map[string]int // per landing attempt: disturbances seen when it started
	abandoned                  map[string]string
	abandonedAttempts          map[string]bool
	observed                   map[string]*forgeLanding
	lastFence                  string
	lab                        *labState
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

// The optional storage override is used to prove compact/full fake equivalence.
func newSimulation(cfg Config, compactTrees ...bool) (*simulation, error) {
	cfg, err := cfg.normalized()
	if err != nil {
		return nil, err
	}
	if cfg.Planner.MaxDepth() > 1 || cfg.Planner.BatchTargetPassRate > 0 {
		policy, _ := json.Marshal(cfg.Planner)
		cfg.Planner.ConfigDigest = fmt.Sprintf("%x", sha256.Sum256(policy))
	}
	start := time.Unix(0, 0).UTC()
	s := &simulation{
		expectedWindow: cfg.Planner.MaxDepth(), windowFailures: make(map[string]bool),
		cfg: cfg, start: start, now: start, forge: fake.NewDeterministic(), repo: forge.RepoRef{Path: "sim/project"},
		ci: rand.New(rand.NewPCG(cfg.Seed, 2)), webhook: rand.New(rand.NewPCG(cfg.Seed, 3)),
		faults: rand.New(rand.NewPCG(cfg.Seed, 4)), tests: make(map[string]*testRun),
		completed: make(map[string]bool), planned: make(map[string]queue.Action), retryAt: make(map[string]time.Time),
		pendingFaults: make(map[string]int), landed: make(map[int]string),
		attempts: make(map[string]string), disturbance: make(map[string]int), abandoned: make(map[string]string), abandonedAttempts: make(map[string]bool), observed: make(map[string]*forgeLanding),
		result: Result{Config: cfg, FaultsByMethod: make(map[string]int), PRs: cfg.PRs, Dequeued: make(map[queue.DequeueReason]int), Blocked: make(map[queue.DequeueReason]int)},
	}
	compact := cfg.PRs > 2000
	if len(compactTrees) > 0 {
		compact = compactTrees[0]
	}
	if compact {
		s.forge.CompactTrees(s.repo)
	}
	if cfg.Lab != nil {
		s.lab = newLabState(cfg)
	}
	base, fence := s.forge.Init(s.repo, "main", "openmerge/fence/main")
	s.lastFence = fence
	s.boundary = &faultForge{Forge: s.forge, sim: s, scope: "setup"}
	s.mainSHA = base
	s.mainTips = []string{base}
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
	external := rand.New(rand.NewPCG(cfg.Seed, 6))
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
		p.externalBeforeLand = external.Float64() < cfg.ExternalPushRate && !p.untrusted
		p.broken = !p.untrusted && rng.Float64() < cfg.BrokenRate
		var delay time.Duration
		if cfg.ArrivalWindow > 0 {
			delay = time.Duration(rng.Int64N(int64(cfg.ArrivalWindow)))
		}
		if s.lab != nil && !p.untrusted {
			delay = s.drawChange(p, p.broken, delay)
		}
		if p.broken {
			s.result.Broken++
		}
		s.schedule(s.start.Add(delay), arrival, pr, "")
		if p.externalBeforeLand {
			duration := cfg.CIDuration
			if cfg.CIMin > 0 {
				duration = cfg.CIMin
			}
			s.schedule(s.start.Add(delay+time.Second+duration/2), externalPush, 0, "")
		}
	}
	if cfg.CI != nil {
		rng := rand.New(rand.NewPCG(cfg.Seed, 7))
		for pr := 1; pr < cfg.PRs; pr += 2 {
			if rng.Float64() < cfg.CI.InteractionRate {
				s.interactions = append(s.interactions, [2]int{pr, pr + 1})
			}
		}
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
			s.advance(retry)
		}
		_, err := s.boundary.EnsureFence(context.Background(), s.repo, "openmerge/fence/main", forge.PushOptions{})
		if errors.Is(err, errForge) {
			s.retryAt["setup"] = s.now.Add(s.cfg.RetryDelay)
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if s.cfg.Planner.ConfigDigest != "" {
			s.record(s.cfg.Planner.Applied("sim-policy", s.cfg.Planner.ConfigDigest))
		}
		s.initialized = true
		s.recoverFaults("setup")
		return false, nil
	}

	if len(s.scheduled) > 0 && !s.scheduled[0].at.After(s.now) {
		return false, s.deliver(heap.Pop(&s.scheduled).(scheduledEvent))
	}
	actions, err := s.plan()
	if err != nil {
		return false, err
	}
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
	if at, ok := queue.NextWake(s.plannerConfig(), s.state, s.now); ok {
		consider(at)
	}
	if !next.IsZero() {
		s.advance(next)
		return false, nil
	}
	if len(s.state.Entries) > 0 || len(actions) > 0 {
		return false, fmt.Errorf("wedge: %d queued with no executable action or pending timer (pause=%q, active=%v)",
			len(s.state.Entries), s.state.PauseReason(s.cfg.Planner), s.state.Active)
	}
	return true, nil
}

func (s *simulation) execute(action queue.Action) error {
	// The executor is reused across virtual actions. Bind every retry-sensitive
	// input to the currently projected batch before each execution.
	s.executor.ChecksRetryFailed = s.cfg.Planner.ChecksRetryFailed
	s.executor.CheckTimeout = s.cfg.Planner.CheckTimeout
	s.executor.StartedTestingAt = time.Time{}
	s.executor.CheckRetry = nil
	s.executor.Speculative = s.cfg.Planner.MaxDepth() > 1
	s.executor.PreviousObservedRuns = nil
	if active := s.state.ActionBatch(action); active != nil {
		s.executor.StartedTestingAt = active.StartedTestingAt
		s.executor.CheckRetry = active.CheckRetry
		s.executor.PreviousObservedRuns = active.LastObservedRuns
	}
	s.boundary.scope = faultScope(action)
	_, s.boundary.retiredObservation = action.(queue.ObserveRetiredChecks)
	if retry, ok := action.(queue.RetryCheckCancellation); ok {
		s.boundary.retiredObservation = retry.ReadOnly
	}
	defer func() { s.boundary.retiredObservation = false }()
	if a, ok := action.(queue.Land); ok && s.faults.Float64() < s.cfg.Faults.DuplicateExecutionRate {
		s.sequence++
		scope := fmt.Sprintf("duplicate/%d", s.sequence)
		s.injectFault(scope)
		s.result.DuplicateExecutions++
		heap.Push(&s.scheduled, scheduledEvent{at: s.now.Add(s.cfg.Faults.ReplayDelay), sequence: s.sequence, kind: duplicateLand, action: a, scope: scope})
	}
	err := s.executor.Execute(context.Background(), action, func(event queue.Event) error {
		if err := s.authorizeStart(action, event); err != nil {
			return err
		}
		if _, landed := event.(queue.Landed); landed {
			if _, pushed := action.(queue.Land); pushed && s.faults.Float64() < s.cfg.Faults.LandingCrashRate {
				s.injectFault(faultScope(action))
				return errCrash
			}
		}
		s.record(event)
		if _, started := event.(queue.LandingStarted); started && s.violation == nil {
			// Model another actor in the durable-start/pre-push window. Each
			// selected PR triggers this once, so rebuilding can eventually land.
			for _, pr := range s.state.Active.PRs {
				if s.prs[pr.PR].externalBeforeLand {
					s.prs[pr.PR].externalBeforeLand = false
					if err := s.pushExternalBase(); err != nil {
						return err
					}
				}
			}
		}
		return s.violation
	})
	if errors.Is(err, errStale) {
		delete(s.retryAt, action.Key())
		return nil
	}
	if err != nil {
		if !errors.Is(err, errForge) && !errors.Is(err, errCrash) && !errors.Is(err, errDelayed) && !errors.Is(err, executor.ErrChecksNotSuccessful) && !errors.Is(err, executor.ErrCancellationPending) {
			return fmt.Errorf("execute %s: %w", action.Key(), err)
		}
		if deferred, ok := queue.DeferCheckCancellation(action, s.now, err.Error()); ok {
			s.record(deferred)
			s.completed[action.Key()] = true
			delete(s.retryAt, action.Key())
			if deferred.Abandoned {
				s.recoverFaults(faultScope(action))
			}
			return nil
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
	case queue.BatchStarted:
		s.labBatchStarted(event.PRs)
	case queue.ChainedBatchStarted:
		s.labBatchStarted(event.PRs)
	case queue.ChainedTestRefBuilt:
		if run := s.tests[event.TestedSHA]; run != nil {
			run.startedTestingAt = event.At
		}
	case queue.TestRefBuilt:
		if run := s.tests[event.TestedSHA]; run != nil {
			run.startedTestingAt = event.At
		}
	case queue.LandingStarted:
		if s.cfg.Planner.MaxDepth() > 1 && (batch == nil || batch.ID != event.BatchID) {
			s.violation = fmt.Errorf("landing order differs from chain order: started %s ahead of oldest", event.BatchID)
			return
		}
		if batch != nil && batch.ID == event.BatchID {
			s.attempts[event.AttemptID] = batch.TestedSHA
			s.disturbance[event.AttemptID] = s.disturbances()
		}
	case queue.Landed:
		if batch != nil && batch.ID == event.BatchID && batch.Phase == queue.Landing {
			if err := s.verifyLanded(batch, event); err != nil {
				s.violation = err
				return
			}
			if run := s.tests[batch.TestedSHA]; run != nil {
				run.landed = true
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
		if event.Reason == queue.ChecksFailed || event.Reason == queue.ChecksTimeout || event.Reason == queue.Conflict {
			if batch == nil || len(batch.PRs) != 1 || batch.PRs[0].PR != event.PR {
				s.violation = fmt.Errorf("failure dequeued PR %d without isolating it in a singleton batch", event.PR)
				return
			}
		}
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
	before := s.state
	s.state = queue.Apply(s.state, event)
	s.observeAdaptation(before, event)
	if s.cfg.Planner.MaxDepth() > 1 {
		for _, b := range s.state.Following {
			for _, pr := range b.PRs {
				reason := s.state.PendingDequeue(pr.PR)
				if reason == queue.ChecksFailed || reason == queue.ChecksTimeout || reason == queue.Conflict {
					s.violation = fmt.Errorf("provisional failure blamed PR %d in batch %s", pr.PR, b.ID)
				}
			}
		}
	}

	if s.cfg.CI != nil && s.cfg.Planner.MaxDepth() == 1 && batch != nil && (s.state.Active == nil || s.state.Active.ID != batch.ID) {
		if _, landed := event.(queue.Landed); !landed {
			s.cancelCI(batch.TestedSHA)
		}
	}
	if reopened, ok := event.(queue.ChecksReopened); ok && batch != nil && batch.Phase == queue.Testing && batch.TestedSHA == reopened.TestedSHA {
		for _, action := range queue.Plan(s.plannerConfig(), s.state, s.now) {
			if _, landing := action.(queue.Land); landing {
				s.violation = fmt.Errorf("reopened checks for %s still authorize landing", reopened.TestedSHA)
			}
		}
	}
	if failed, ok := event.(queue.BatchFailed); ok && batch != nil && failed.BatchID != batch.ID && !reflect.DeepEqual(batch, s.state.Active) {
		s.violation = fmt.Errorf("stale failure for batch %s changed current batch %s", failed.BatchID, batch.ID)
	}
}

func (s *simulation) startCI(sha string, prs []int) {
	var batch *queue.Batch
	if s.cfg.Planner.MaxDepth() == 1 {
		batch = s.state.Active
	}
	for _, candidate := range s.state.Batches() {
		if candidate.Phase == queue.Building && len(prs) == len(candidate.PRs) && slices.EqualFunc(prs, candidate.PRs, func(pr int, pin queue.BatchPR) bool { return pr == pin.PR }) {
			batch = candidate
			break
		}
	}
	if batch == nil || len(prs) != len(batch.PRs) {
		s.violation = fmt.Errorf("CI membership %v differs from intended batch %v", prs, batch)
		return
	}
	for i, pr := range batch.PRs {
		if prs[i] != pr.PR {
			s.violation = fmt.Errorf("CI membership %v differs from intended batch PRs %v", prs, batch.PRs)
			return
		}
	}
	run := &testRun{batchID: batch.ID, startedTestingAt: batch.StartedTestingAt, base: batch.BaseSHA, heads: slices.Clone(batch.PRs), method: s.cfg.MergeMethod, ref: batch.TestRef, success: s.ci.Float64() >= s.cfg.FlakeRate}
	commit, err := s.forge.CommitMetadata(s.repo, sha)
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
	if s.interactionFails(commit.PRs) {
		run.success = false
	}
	coveredTip := sha
	if run.method == queue.FastForward {
		coveredTip = run.heads[len(run.heads)-1].HeadSHA
		if err := s.forge.VerifyFastForward(context.Background(), s.repo, run.base, sha, coveredTip); err != nil {
			s.violation = err
			return
		}
	}
	run.covered, err = s.firstParentRange(run.base, coveredTip)
	if err != nil {
		s.violation = err
		return
	}
	s.tests[sha] = run
	if s.cfg.CI == nil {
		s.result.CIRuns++
	}
	if batch.FromBisection {
		s.result.BisectionCIRuns++
	}
	s.forge.SetCheckRuns(s.repo, sha, []forge.CheckRun{{ID: 1, Attempt: 1, Name: "ci", State: forge.Pending}})
	switch {
	case s.cfg.CI == nil:
		s.schedule(s.now.Add(s.ciDuration()), ciComplete, 0, sha)
	case s.lab != nil:
		s.labSubmit(sha, run, batch)
	default:
		s.submitCI(sha, s.ciDuration())
	}
}

func (s *simulation) ciDuration() time.Duration {
	duration := s.cfg.CIDuration
	if s.cfg.CIMin > 0 {
		duration = s.cfg.CIMin
		if s.cfg.CIMax > s.cfg.CIMin {
			duration += time.Duration(s.ci.Int64N(int64(s.cfg.CIMax - s.cfg.CIMin)))
		}
	}
	return duration
}

func (s *simulation) metrics() Result {
	r := s.result
	r.Blocked = make(map[queue.DequeueReason]int)
	r.StillQueued = len(s.state.Entries)
	r.SimulatedSpan = s.now.Sub(s.start)
	var times, firstTimes []time.Duration
	for pr := 1; pr < len(s.prs); pr++ {
		p := s.prs[pr]
		if !p.landed.IsZero() {
			r.Merged++
			times = append(times, p.landed.Sub(p.enqueued))
			firstTimes = append(firstTimes, p.landed.Sub(p.firstEnqueued))
		}

		if status := s.forge.Statuses(s.repo, pr)[p.head]; status.State == forge.Blocked {
			r.Blocked[queue.DequeueReason(status.Reason)]++
		}
	}
	if len(times) > 0 {
		r.MedianTimeToMerge, r.P95TimeToMerge = percentiles(times)
		r.MedianTimeFromFirstEnqueue, r.P95TimeFromFirstEnqueue = percentiles(firstTimes)
		// Sum as floats to avoid overflowing Duration on large workloads.
		var total float64
		for _, elapsed := range times {
			total += float64(elapsed)
		}
		r.MeanTimeToMerge = time.Duration(total / float64(len(times)))
		r.CIRunsPerMergedPR = float64(r.CIRuns) / float64(r.Merged)
	}
	if s.cfg.CI != nil {
		r.CI = s.ciMetrics(r.Merged)
	}
	if s.lab != nil {
		r.Lab = s.labMetrics(r.Merged)
	}
	return r
}

func percentiles(times []time.Duration) (median, p95 time.Duration) {
	slices.Sort(times)
	median = times[len(times)/2]
	if len(times)%2 == 0 {
		a, b := times[len(times)/2-1], times[len(times)/2]
		median = a + (b-a)/2
	}
	return median, times[len(times)-len(times)/20-1] // nearest rank ceil(.95*n).
}

// Retain planned payloads independently of execution so a reused key cannot
// silently replace a previously published composition.
func (s *simulation) plan() ([]queue.Action, error) {
	actions := queue.Plan(s.plannerConfig(), s.state, s.now)
	for _, action := range actions {
		if previous, exists := s.planned[action.Key()]; exists && !reflect.DeepEqual(previous, action) {
			return nil, fmt.Errorf("action key %s reused for a changed payload", action.Key())
		}
		s.planned[action.Key()] = action
	}
	return actions, nil
}

func (s *simulation) authorizeStart(action queue.Action, event queue.Event) error {
	switch event.(type) {
	case queue.ChainedBatchStarted, queue.BatchStarted, queue.CheckRerunRequested, queue.LandingStarted:
		if !queue.CanStart(s.plannerConfig(), s.state, action, event) {
			return errStale
		}
	}
	return nil
}

func (s *simulation) emit(action queue.Action, event queue.Event) error {
	if err := s.authorizeStart(action, event); err != nil {
		return err
	}
	s.record(event)
	return s.violation
}

func (s *simulation) advance(next time.Time) {
	window := float64(s.state.Window(s.cfg.Planner))
	s.windowArea += next.Sub(s.now).Seconds() * window
	end := minTime(next, s.start.Add(24*time.Hour))
	if end.After(s.now) {
		s.coldWindowArea += end.Sub(s.now).Seconds() * window
	}
	s.now = next
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
