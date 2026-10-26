package queue

import (
	"cmp"
	"slices"
	"time"
)

// HeldRequest retains a writer's request for one exact head, before admission.
// EnqueueRequested and PRObserved reconstruct it from the existing event journal.
type HeldRequest struct {
	PR         int
	HeadSHA    string
	At         time.Time
	Conditions string
}

// HeldRequest returns the request still waiting for this PR's entry conditions.
func (s State) HeldRequest(pr int) (HeldRequest, bool) {
	request, ok := s.heldRequests[pr]
	return request, ok
}

// HeldRequests returns pending requests in PR order, independently of admissions.
func (s State) HeldRequests() []HeldRequest {
	requests := make([]HeldRequest, 0, len(s.heldRequests))
	for _, request := range s.heldRequests {
		requests = append(requests, request)
	}
	slices.SortFunc(requests, func(a, b HeldRequest) int { return cmp.Compare(a.PR, b.PR) })
	return requests
}

func (s *State) admit(pr int, head string, at time.Time) {
	s.nextAdmission++
	s.Entries = append(s.Entries, Entry{PR: pr, AdmissionID: s.nextAdmission, HeadSHA: head, EnqueuedAt: at})
}

func (s *State) refreshHeldRequest(observation PRObserved) {
	request, held := s.heldRequests[observation.PR]
	if !held {
		return
	}
	switch {
	case s.lifecycleBlocked():
		// A fresh request would be dropped in this lifecycle. Leave the PR's
		// observed status intact rather than reporting a dequeue or stale wait.
		delete(s.heldRequests, observation.PR)
	case request.HeadSHA != observation.HeadSHA:
		s.cancelHeldRequest(observation.PR, HeadChanged)
	case !observation.Eligible && observation.PendingConditions != "":
		request.Conditions = observation.PendingConditions
		s.heldRequests[observation.PR] = request
	case !observation.Eligible:
		delete(s.heldRequests, observation.PR)
		s.report(observation.PR, "blocked", string(NotEligible), "PR does not satisfy queue entry conditions")
	case !observation.Trusted:
		delete(s.heldRequests, observation.PR)
		s.report(observation.PR, "blocked", string(Untrusted), "PR head has not passed the trust gate")
	default:
		delete(s.heldRequests, observation.PR)
		s.admit(request.PR, request.HeadSHA, request.At)
	}
}

func (s *State) cancelHeldRequest(pr int, reason DequeueReason) {
	if request, ok := s.heldRequests[pr]; ok {
		delete(s.heldRequests, pr)
		s.reportHead(pr, request.HeadSHA, "dequeued", string(reason), "Queue request cancelled: "+string(reason))
	}
}
