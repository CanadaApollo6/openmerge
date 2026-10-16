package queue

// PendingDequeue reports a recorded request that has not yet been settled. It
// does not claim that a PR was removed; an in-flight landing may still succeed.
func (s State) PendingDequeue(pr int) DequeueReason {
	return s.dequeues[pr].Reason
}
