package queue

import (
	"maps"
	"slices"
	"time"
)

// DependencyWait is a planner-only ADR 0011 Stage A overlay derived from
// recorded dependency facts by an activated caller. It is never journaled or
// snapshotted, and Apply never consults it: the fold stays byte-identical.
// Head pins the admitted entry head. EligibleAt is when the recorded admission
// last became satisfied; a waiting change's batch max_wait clock is suspended
// until then.
type DependencyWait struct {
	Head       string
	Waiting    bool
	EligibleAt time.Time
}

// WithDependencyWaits returns a planning view keyed by PR. A nil map keeps
// legacy planning exactly. A non-nil map fails closed: a PR without a value for
// its exact head is treated as waiting, never as dependency-free.
func (s State) WithDependencyWaits(waits map[int]DependencyWait) State {
	if waits == nil {
		s.dependencyWaits = nil
		return s
	}
	s.dependencyWaits = maps.Clone(waits)
	return s
}

// DependencyGated reports whether the overlay withholds pr from new compositions.
func (s State) DependencyGated(pr BatchPR) bool {
	if s.dependencyWaits == nil {
		return false
	}
	w, ok := s.dependencyWaits[pr.PR]
	return !ok || w.Waiting || w.Head != pr.HeadSHA
}

// dependencyClock resumes a change's batch max_wait clock from its eligibility,
// so waiting time is not charged against the remaining formation window.
func (s State) dependencyClock(entry Entry) time.Time {
	if w, ok := s.dependencyWaits[entry.PR]; ok && w.Head == entry.HeadSHA && w.EligibleAt.After(entry.EnqueuedAt) {
		return w.EligibleAt
	}
	return entry.EnqueuedAt
}

// pruneDependencyWaits removes waiting members from pending bisection/retry
// groups on this planning copy. A wait consumes no slot and is never bisected.
func (s *State) pruneDependencyWaits() {
	if s.dependencyWaits == nil {
		return
	}
	var groups []pendingGroup
	for _, g := range s.pending {
		prs := slices.DeleteFunc(slices.Clone(g.prs), s.DependencyGated)
		if len(prs) > 0 {
			groups = append(groups, pendingGroup{prs: prs, bisection: g.bisection})
		}
	}
	s.pending = groups
}
