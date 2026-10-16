package queue

// PendingDequeue reports a recorded request that has not yet been settled. It
// does not claim that a PR was removed; an in-flight landing may still succeed.
func (s State) PendingDequeue(pr int) DequeueReason {
	return s.dequeues[pr].Reason
}

// ChecksPassed reports the fold's current success, including observations
// retained before TestRefBuilt. It is for explanations, never authorization.
func (s State) ChecksPassed(sha string) bool {
	if s.Active != nil && s.Active.TestedSHA == sha {
		return s.Active.checksPassed
	}
	check, ok := s.checks[sha]
	return ok && check.terminal && check.success
}
