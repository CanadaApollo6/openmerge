package sim

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// IsolationCase is one failed multi-change batch as first observed on its base:
// members in batch order, their true brokenness, and strategy-visible scores.
type IsolationCase struct {
	Members []int
	Broken  []bool
	Scores  []float64
}

// IsolationPolicy selects how a failing group is split. The zero value is the
// production planner's rule (queue.State.failBatch): test the first half by
// position, then the second half, recursing depth-first, left first.
//
// CulpritFirst is the Bayesian alternative. With independent prior scores s_i,
// the posterior that change i is a culprit given that the group failed is
// s_i / (1 - Π(1-s_j)). When the top posterior reaches Threshold (0.5 if
// zero), that change is tested alone first and the remainder second; otherwise
// the group is split where cumulative score mass is closest to half.
//
// Dedupe reuses the result of an identical tree: the tested tree is the base
// plus every change landed so far in the isolation plus the group.
type IsolationPolicy struct {
	CulpritFirst bool
	Threshold    float64
	Dedupe       bool
}

// IsolationCost counts the runs needed to isolate every culprit and land every
// innocent change of one failed batch. It excludes the batch's own failing run.
// Wall time is serial, as at depth one. InnocentDelay sums, over innocent
// changes, the time from the batch failure to the completion of the run that
// let them land.
type IsolationCost struct {
	Runs, DedupeHits      int
	Minutes, WallMinutes  float64
	InnocentDelayMinutes  float64
	Innocents, Culprits   int
	FalseCulpritDequeues  int
	MissedCulpritLandings int
}

// Isolate replays one failed batch against a deterministic CI with no flakes:
// a group fails if and only if it contains a broken change. pass and fail are
// the run durations of passing and failing runs (fail-fast shortens the latter).
// The model reproduces the planner's bisection run count exactly at depth one
// without flakes, interactions or base churn (see TestIsolationModelCalibration).
func Isolate(c IsolationCase, p IsolationPolicy, pass, fail time.Duration) IsolationCost {
	if p.Threshold == 0 {
		p.Threshold = 0.5
	}
	m := &isolation{c: c, p: p, pass: pass.Minutes(), fail: fail.Minutes(), landed: make([]bool, len(c.Members)), trees: make(map[string]bool)}
	all := make([]int, len(c.Members))
	for i := range all {
		all[i] = i
	}
	m.trees[m.key(all)] = true
	m.resolve(all, true)
	return m.cost
}

type isolation struct {
	c          IsolationCase
	p          IsolationPolicy
	pass, fail float64
	clock      float64
	landed     []bool
	trees      map[string]bool
	cost       IsolationCost
}

func (m *isolation) key(group []int) string {
	var b strings.Builder
	for i := range m.c.Members {
		if m.landed[i] || slices.Contains(group, i) {
			fmt.Fprintf(&b, "%d,", i)
		}
	}
	return b.String()
}

// test returns whether the group fails, charging a run unless deduplicated.
func (m *isolation) test(group []int) bool {
	fails := slices.ContainsFunc(group, func(i int) bool { return m.c.Broken[i] })
	key := m.key(group)
	if m.p.Dedupe {
		if cached, ok := m.trees[key]; ok {
			m.cost.DedupeHits++
			return cached
		}
	}
	d := m.pass
	if fails {
		d = m.fail
	}
	m.cost.Runs++
	m.cost.Minutes += d
	m.clock += d
	m.cost.WallMinutes = m.clock
	m.trees[key] = fails
	return fails
}

func (m *isolation) resolve(group []int, knownFailing bool) {
	if !knownFailing && !m.test(group) {
		for _, i := range group {
			m.landed[i] = true
			if m.c.Broken[i] {
				m.cost.MissedCulpritLandings++
			} else {
				m.cost.Innocents++
				m.cost.InnocentDelayMinutes += m.clock
			}
		}
		return
	}
	if len(group) == 1 {
		if m.c.Broken[group[0]] {
			m.cost.Culprits++
		} else {
			m.cost.FalseCulpritDequeues++
		}
		return
	}
	first, rest := m.split(group)
	m.resolve(first, false)
	m.resolve(rest, false)
}

func (m *isolation) split(group []int) (first, rest []int) {
	if !m.p.CulpritFirst {
		middle := len(group) / 2
		return slices.Clone(group[:middle]), slices.Clone(group[middle:])
	}
	clean, mass := 1.0, 0.0
	top := group[0]
	for _, i := range group {
		s := m.c.Scores[i]
		clean *= 1 - s
		mass += s
		if s > m.c.Scores[top] {
			top = i
		}
	}
	if failing := 1 - clean; failing > 0 && m.c.Scores[top]/failing >= m.p.Threshold {
		for _, i := range group {
			if i != top {
				rest = append(rest, i)
			}
		}
		return []int{top}, rest
	}
	// Mass-balanced split by position: the prefix whose score mass is closest to
	// half, with at least one change on each side.
	best, bestGap, running := 1, 2.0, 0.0
	for k := 1; k < len(group); k++ {
		running += m.c.Scores[group[k-1]]
		gap := running - mass/2
		if gap < 0 {
			gap = -gap
		}
		if gap < bestGap {
			best, bestGap = k, gap
		}
	}
	return slices.Clone(group[:best]), slices.Clone(group[best:])
}
