package queue

import (
	"fmt"
	"maps"
	"slices"
	"time"
)

// DependencyWait is a planner-only ADR 0011 Stage A overlay derived from
// recorded dependency facts by an activated caller. It is never journaled or
// snapshotted, and Apply never consults it: the fold stays byte-identical.
// Head pins the admitted entry head. EligibleAt is the equivalent eligible-clock origin, after subtracting prior
// eligible intervals from the latest resumption: it is stable while admission stays continuously
// satisfied (unchanged evidence may be re-recorded) and preserves elapsed eligible time across
// waiting-to-eligible transitions. A waiting change's batch max_wait clock is
// suspended until then.
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
	if w, ok := s.dependencyWaits[entry.PR]; ok && w.Head == entry.HeadSHA && !w.EligibleAt.IsZero() {
		return w.EligibleAt
	}
	return entry.EnqueuedAt
}

// pruneDependencyWaits removes waiting members from pending bisection/retry
// groups on this planning copy. A wait consumes no slot and is never bisected.
// The fold starts only its exact first pending group, so an activated start
// journals the same prune first as DependencyWaitsWithheld.
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

// DependencyWaitsWithheld journals the planner's ADR 0011 prune immediately
// before the start that relies on it: these pending bisection/retry members were
// dependency-waiting at these heads. They leave their groups and keep their
// entry order, so unrelated eligible changes pass them; a later eligible head
// re-enters a fresh composition. Nothing is bisected, retried or dequeued, and
// queues without the activated overlay never emit it.
type DependencyWaitsWithheld struct {
	PRs []BatchPR `json:"prs"`
}

func (DependencyWaitsWithheld) queueEvent() {}

// Validate requires distinct members within the snapshot bound.
func (e DependencyWaitsWithheld) Validate() error {
	if err := validateBatchPRs(e.PRs); err != nil || len(e.PRs) > maxSnapshotEntries {
		return fmt.Errorf("invalid dependency-withheld members")
	}
	return nil
}

// DependencyWithheld returns the pending members the overlay gates, in group
// order: exactly the members pruneDependencyWaits drops from this view.
func (s State) DependencyWithheld() []BatchPR {
	var out []BatchPR
	for _, g := range s.pending {
		out = append(out, slices.DeleteFunc(slices.Clone(g.prs), func(pr BatchPR) bool { return !s.DependencyGated(pr) })...)
	}
	return out
}

// withholdDependencyWaits applies only when every member is still pending at
// its exact head; otherwise the record is a no-op and the fold is unchanged.
func (s *State) withholdDependencyWaits(e DependencyWaitsWithheld) {
	if e.Validate() != nil {
		return
	}
	for _, pr := range e.PRs {
		if !slices.ContainsFunc(s.pending, func(g pendingGroup) bool { return slices.Contains(g.prs, pr) }) {
			return
		}
	}
	for _, pr := range e.PRs {
		s.removePendingPR(pr.PR)
	}
}
