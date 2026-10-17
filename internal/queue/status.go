package queue

// PendingDequeue reports a recorded request that has not yet been settled. It
// does not claim that a PR was removed; an in-flight landing may still succeed.
func (s State) PendingDequeue(pr int) DequeueReason {
	return s.dequeues[pr].Reason
}

// ChecksPassed reports the fold's current success, including observations
// retained before TestRefBuilt. It is for explanations, never authorization.
func (s State) ChecksPassed(sha string) bool {
	if b := s.batchForSHA(sha); b != nil && b.InvalidationReason == "" {
		return b.checksPassed && b.ProvisionalFailure == ""
	}
	check, ok := s.checks[sha]
	return ok && check.terminal && check.success
}

// PendingBisection reports whether diagnosed halves await admission.
func (s State) PendingBisection() bool {
	for _, group := range s.pending {
		if group.bisection {
			return true
		}
	}
	return false
}
