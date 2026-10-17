// Package capacity is a deterministic feasibility model of ADR 0009. It does
// not call the planner or claim to prove landing safety. Time is virtual; all
// ordering is explicit, including equal-time events and FIFO runner admission.
package capacity

import (
	"container/heap"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"time"
)

// Config describes one seed, drained to completion after uniform arrivals stop.
// All CI durations are service time after acquiring one runner. CheckTimeout
// starts at request, including queueing; reruns get a new deadline.
type Config struct {
	Seed                                       uint64
	Changes                                    int
	ArrivalWindow, CI, MaxWait, CheckTimeout   time.Duration
	BrokenRate, FlakeRate, InteractionRate     float64
	MaxSize, MinDepth, MaxDepth, Runners       int
	Cancellation, Retries, Adaptive, Isolation bool
	Target                                     float64
}

// Sample preserves arrival identity for pooling and first-day slices.
type Sample struct {
	Change       int
	Arrival, TTM time.Duration
}

// Result includes all CI started, even cancelled, invalidated, or isolation
// runs. RunnerMinutes measures consumed service; cancelled waiting jobs cost 0.
type Result struct {
	Config                                                                              Config
	Merges                                                                              []Sample
	Broken, Dequeued, Runs, Reruns, CancelRequested, CancelConfirmed, CancelUnsupported int
	Timeouts, OriginalFailures, BisectionFailures, Invalidated, Landings                int
	FreshBatches, FreshAboveAdaptiveCap, MaxRunning, MaxOpen                            int
	FinalWindow                                                                         int
	MeanWindow                                                                          float64 // time-weighted over the entire drained run
	// First-day cost allocates each run proportionally to its batch members
	// arriving in the first 24 hours, including later reruns and cancellations.
	FirstDayRuns, FirstDayRunnerMinutes float64
	RunnerMinutes                       float64
	Span                                time.Duration
}

// Stats uses midpoint median and nearest-rank p95, matching internal/sim.
type Stats struct {
	Merged, Runs                                      int
	Median, P95                                       time.Duration
	RunsPerMerge, RunnerMinutesPerMerge, TailFraction float64
}

// Summarize pools merge samples without averaging per-seed percentiles.
func Summarize(samples []Sample, runs int, runnerMinutes float64, threshold time.Duration) Stats {
	s := Stats{Merged: len(samples), Runs: runs}
	if len(samples) == 0 {
		return s
	}
	times := make([]time.Duration, len(samples))
	beyond := 0
	for i, p := range samples {
		times[i] = p.TTM
		if p.TTM >= threshold {
			beyond++
		}
	}
	slices.Sort(times)
	s.Median = times[len(times)/2]
	if len(times)%2 == 0 {
		a, b := times[len(times)/2-1], s.Median
		s.Median = a + (b-a)/2
	}
	s.P95 = times[len(times)-len(times)/20-1]
	s.RunsPerMerge = float64(runs) / float64(len(times))
	s.RunnerMinutesPerMerge = runnerMinutes / float64(len(times))
	s.TailFraction = float64(beyond) / float64(len(times))
	return s
}

// AdaptiveCap applies the last-200 resolved-change estimator, with a cold start
// below 20. true means a confirmed checks-failure/timeout dequeue, false a land.
func AdaptiveCap(outcomes []bool, maxSize int, target float64) int {
	if len(outcomes) > 200 {
		outcomes = outcomes[len(outcomes)-200:]
	}
	if len(outcomes) < 20 {
		return maxSize
	}
	failures := 0
	for _, failed := range outcomes {
		if failed {
			failures++
		}
	}
	p := float64(failures) / float64(len(outcomes))
	ceiling := 1
	for k := 1; k <= maxSize; k++ {
		if math.Pow(1-p, float64(k)) >= target {
			ceiling = k
		}
	}
	return ceiling
}

func (c Config) validate() error {
	if c.Changes < 0 || c.ArrivalWindow < 0 || c.CI <= 0 || c.MaxWait < 0 || c.CheckTimeout < 0 || c.MaxSize < 1 || c.MaxSize > 128 || c.MinDepth < 1 || c.MaxDepth < c.MinDepth || c.MaxDepth > 32 || c.Runners < 1 {
		return fmt.Errorf("invalid capacity dimensions: %+v", c)
	}
	for _, p := range []float64{c.BrokenRate, c.FlakeRate, c.InteractionRate} {
		if math.IsNaN(p) || p < 0 || p > 1 {
			return fmt.Errorf("invalid failure rate")
		}
	}
	if math.IsNaN(c.Target) || c.Target <= 0 || c.Target >= 1 {
		return fmt.Errorf("target must be strictly between 0 and 1")
	}
	return nil
}

type change struct {
	at     time.Duration
	broken bool
}
type batch struct {
	members []int
	half    bool
	state   status
	bad     bool
	run     *run
}
type status uint8

const (
	runningChecks status = iota
	provisional
	passed
	confirmed
	retired
)

type run struct {
	batch                                             *batch
	started, finished                                 time.Duration
	running, done, cancelled, requested, rerun, fails bool
}
type isolation struct{ batches []*batch }
type event struct {
	at     time.Duration
	seq    int
	kind   uint8
	change int
	run    *run
}

const (
	arrive uint8 = iota
	complete
	timeout
	wake
)

type events []event

func (q events) Len() int { return len(q) }
func (q events) Less(i, j int) bool {
	if q[i].at == q[j].at {
		if q[i].kind != q[j].kind {
			return q[i].kind < q[j].kind
		}
		return q[i].seq < q[j].seq
	}
	return q[i].at < q[j].at
}
func (q events) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *events) Push(x any)   { *q = append(*q, x.(event)) }
func (q *events) Pop() any     { old := *q; x := old[len(old)-1]; *q = old[:len(old)-1]; return x }

type model struct {
	cfg                       Config
	result                    Result
	changes                   []change
	interactions              [][2]int
	landed                    []int
	waiting                   []int
	halves                    [][]int
	chain                     []*batch
	isolations                []*isolation
	runners                   []*run
	runs                      []*run
	running, window, sequence int
	now, wakeAt               time.Duration
	events                    events
	ci                        *rand.Rand
	outcomes                  []bool
	windowTime                float64
}

// Run executes only in memory, using PCG streams compatible with the simulator
// for arrival/broken draws (stream 1) and flakes (stream 2). The model omits the
// simulator's one-second observation delay, Git, refresh and fence overhead.
func Run(c Config) (Result, error) {
	if err := c.validate(); err != nil {
		return Result{}, err
	}
	m := &model{cfg: c, result: Result{Config: c}, window: c.MaxDepth, ci: rand.New(rand.NewPCG(c.Seed, 2)), changes: make([]change, c.Changes), wakeAt: -1}
	rng := rand.New(rand.NewPCG(c.Seed, 1))
	interactions := rand.New(rand.NewPCG(c.Seed, 7))
	for i := range m.changes {
		ch := change{broken: rng.Float64() < c.BrokenRate}
		if c.ArrivalWindow > 0 {
			ch.at = time.Duration(rng.Int64N(int64(c.ArrivalWindow)))
		}
		m.changes[i] = ch
		if ch.broken {
			m.result.Broken++
		}
		m.push(event{at: ch.at, kind: arrive, change: i})
		if i%2 == 1 && interactions.Float64() < c.InteractionRate {
			m.interactions = append(m.interactions, [2]int{i - 1, i})
		}
	}
	for steps := 0; len(m.events) > 0; steps++ {
		if steps > 10_000_000 {
			return m.result, fmt.Errorf("capacity model exceeded event limit (possible interacting isolation cycle)")
		}
		e := heap.Pop(&m.events).(event)
		m.windowTime += float64(e.at-m.now) * float64(m.window)
		m.now = e.at
		switch e.kind {
		case arrive:
			m.waiting = append(m.waiting, e.change)
		case complete:
			r := e.run
			if !r.cancelled && !r.done {
				m.finish(r)
				if r.batch.state == runningChecks && r.batch.run == r {
					if r.fails {
						if r.rerun || !c.Retries {
							r.batch.state = confirmed
						} else {
							r.batch.state = provisional
						}
					} else {
						r.batch.state = passed
					}
				}
			}
		case timeout:
			r := e.run
			if !r.done && !r.cancelled && r.batch.state == runningChecks && r.batch.run == r {
				r.batch.state = confirmed
				m.result.Timeouts++
				m.cancel(r)
			}
		case wake:
			if e.at == m.wakeAt {
				m.wakeAt = -1
			}
		}
		if len(m.events) > 0 && m.events[0].at == m.now {
			continue
		}
		m.resolve()
		m.admit()
		m.startRunners()
	}
	if len(m.waiting) > 0 || len(m.chain) > 0 || len(m.halves) > 0 || len(m.isolations) > 0 {
		return m.result, fmt.Errorf("capacity model stalled")
	}
	if m.now > m.result.Span {
		m.windowTime -= float64(m.now-m.result.Span) * float64(m.window)
	}
	m.result.FinalWindow = m.window
	if m.result.Span > 0 {
		m.result.MeanWindow = m.windowTime / float64(m.result.Span)
	}
	for _, r := range m.runs {
		if r.running || r.done {
			minutes := (r.finished - r.started).Minutes()
			m.result.RunnerMinutes += minutes
			firstDay := 0
			for _, id := range r.batch.members {
				if m.changes[id].at < 24*time.Hour {
					firstDay++
				}
			}
			share := float64(firstDay) / float64(len(r.batch.members))
			m.result.FirstDayRuns += share
			m.result.FirstDayRunnerMinutes += minutes * share
		}
	}
	slices.SortFunc(m.result.Merges, func(a, b Sample) int { return a.Change - b.Change })
	return m.result, nil
}

func (m *model) push(e event) { m.sequence++; e.seq = m.sequence; heap.Push(&m.events, e) }
func (m *model) history(failed bool) {
	if len(m.outcomes) == 200 {
		copy(m.outcomes, m.outcomes[1:])
		m.outcomes[199] = failed
	} else {
		m.outcomes = append(m.outcomes, failed)
	}
}
func (m *model) bad(members []int) bool {
	for _, i := range members {
		if m.changes[i].broken {
			return true
		}
	}
	for _, pair := range m.interactions {
		if slices.Contains(members, pair[0]) && slices.Contains(members, pair[1]) {
			return true
		}
	}
	return false
}
func (m *model) submit(b *batch, retry bool) {
	b.state = runningChecks
	r := &run{batch: b, rerun: retry, fails: m.ci.Float64() < m.cfg.FlakeRate || b.bad}
	b.run = r
	m.runs = append(m.runs, r)
	m.runners = append(m.runners, r)
	// Queue the completion before an equal-time timeout so service finishing
	// exactly at its deadline wins, just as the planner's refresh gate does.
	m.startRunners()
	if m.cfg.CheckTimeout > 0 {
		m.push(event{at: m.now + m.cfg.CheckTimeout, kind: timeout, run: r})
	}
}
func (m *model) startRunners() {
	for len(m.runners) > 0 && m.running < m.cfg.Runners {
		r := m.runners[0]
		m.runners = m.runners[1:]
		if r.cancelled {
			continue
		}
		r.started = m.now
		r.running = true
		m.running++
		m.result.Runs++
		if r.rerun {
			m.result.Reruns++
		}
		m.result.MaxRunning = max(m.result.MaxRunning, m.running)
		m.push(event{at: m.now + m.cfg.CI, kind: complete, run: r})
	}
}

// finish releases a runner even when this run has since been invalidated.
func (m *model) finish(r *run) {
	r.done = true
	r.finished = m.now
	m.running--
	m.result.Span = max(m.result.Span, m.now)
}
func (m *model) cancel(r *run) {
	if r == nil || r.done || r.cancelled || r.requested {
		return
	}
	r.requested = true
	m.result.CancelRequested++
	if !m.cfg.Cancellation {
		m.result.CancelUnsupported++
		return
	}
	r.cancelled = true
	m.result.CancelConfirmed++
	if r.running {
		r.finished = m.now
		r.done = true
		m.running--
	}
}
func (m *model) newBatch(members []int, half bool) {
	bad := m.bad(members)
	for _, b := range m.chain {
		bad = bad || b.bad
	}
	if len(m.interactions) > 0 {
		cumulative := slices.Clone(m.landed)
		for _, b := range m.chain {
			cumulative = append(cumulative, b.members...)
		}
		cumulative = append(cumulative, members...)
		bad = m.bad(cumulative)
	}
	b := &batch{members: slices.Clone(members), half: half, bad: bad}
	m.chain = append(m.chain, b)
	m.result.MaxOpen = max(m.result.MaxOpen, len(m.chain))
	m.submit(b, false)
	if !half {
		m.result.FreshBatches++
		if len(members) > AdaptiveCap(m.outcomes, m.cfg.MaxSize, m.cfg.Target) {
			m.result.FreshAboveAdaptiveCap++
		}
	}
}
func (m *model) resolve() {
	// Off-chain jobs may confirm independently; passers never land here. They
	// return in their original order and must be tested again on the live chain.
	for i := 0; i < len(m.isolations); {
		group := m.isolations[i]
		done := true
		for _, b := range group.batches {
			if b.state == provisional {
				m.submit(b, true)
			}
			if b.state != passed && b.state != confirmed {
				done = false
			}
		}
		if !done {
			i++
			continue
		}
		m.result.Span = max(m.result.Span, m.now)
		var returned []int
		for _, b := range group.batches {
			if b.state == passed {
				returned = append(returned, b.members...)
			} else {
				m.result.Dequeued++
				m.history(true)
			}
			b.state = retired
			m.cancel(b.run)
		}
		m.waiting = append(returned, m.waiting...)
		m.isolations = append(m.isolations[:i], m.isolations[i+1:]...)
	}
	for len(m.chain) > 0 {
		head := m.chain[0]
		switch head.state {
		case passed:
			m.result.Span = max(m.result.Span, m.now)
			for _, id := range head.members {
				m.result.Merges = append(m.result.Merges, Sample{Change: id, Arrival: m.changes[id].at, TTM: m.now - m.changes[id].at})
				m.landed = append(m.landed, id)
				m.history(false)
			}
			head.state = retired
			m.chain = m.chain[1:]
			m.window = min(m.cfg.MaxDepth, m.window+1)
			m.result.Landings++
		case provisional:
			m.submit(head, true)
			return
		case confirmed:
			m.result.Span = max(m.result.Span, m.now)
			if head.half {
				m.result.BisectionFailures++
			} else {
				m.result.OriginalFailures++
				m.window = max(m.cfg.MinDepth, m.window/2)
			}
			var returned []int
			var halves [][]int
			for _, b := range m.chain[1:] {
				m.result.Invalidated++
				b.state = retired
				m.cancel(b.run)
				if b.half {
					halves = append(halves, b.members)
				} else {
					returned = append(returned, b.members...)
				}
			}
			m.chain = nil
			m.waiting = append(returned, m.waiting...)
			head.state = retired
			m.cancel(head.run)
			if len(head.members) == 1 {
				m.result.Dequeued++
				m.history(true)
			} else if m.cfg.Isolation {
				group := &isolation{}
				for _, id := range head.members {
					members := []int{id}
					if len(m.interactions) > 0 {
						members = append(slices.Clone(m.landed), id)
					}
					b := &batch{members: []int{id}, bad: m.bad(members)}
					group.batches = append(group.batches, b)
					m.submit(b, false)
				}
				m.isolations = append(m.isolations, group)
			} else {
				mid := len(head.members) / 2
				halves = append([][]int{head.members[:mid], head.members[mid:]}, halves...)
			}
			m.halves = append(halves, m.halves...)
		default:
			return
		}
	}
}
func (m *model) admit() {
	for len(m.chain) < m.window {
		if len(m.halves) > 0 {
			members := m.halves[0]
			m.halves = m.halves[1:]
			m.newBatch(members, true)
			continue
		}
		if len(m.waiting) == 0 || slices.ContainsFunc(m.chain, func(b *batch) bool { return b.half }) {
			return
		}
		ceiling := m.cfg.MaxSize
		if m.cfg.Adaptive {
			ceiling = AdaptiveCap(m.outcomes, ceiling, m.cfg.Target)
		}
		n := min(ceiling, len(m.waiting))
		oldest := m.changes[m.waiting[0]].at
		for _, id := range m.waiting[:n] {
			oldest = min(oldest, m.changes[id].at)
		}
		deadline := oldest + m.cfg.MaxWait
		if n < ceiling && m.now < deadline {
			if m.wakeAt != deadline {
				m.wakeAt = deadline
				m.push(event{at: deadline, kind: wake})
			}
			return
		}
		members := m.waiting[:n]
		m.waiting = m.waiting[n:]
		m.newBatch(members, false)
	}
}
