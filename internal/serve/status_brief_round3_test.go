package serve

import (
	"context"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"testing"
	"time"
)

func TestR3BriefPolicyBusyUnrelatedWorker(t *testing.T) {
	other := &worker{Target: Target{Key: store.QueueKey{Repository: "other/repo"}}}
	other.mu.Lock()
	s := &Server{queues: []*worker{other}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := s.BriefPolicy(ctx, store.QueueKey{Repository: "wanted/repo"}, status.BriefPolicyRequest{})
		done <- err
	}()
	select {
	case <-done:
		other.mu.Unlock()
	case <-ctx.Done():
		// Allow scheduling after cancellation, then verify the request is still blocked.
		select {
		case <-done:
			other.mu.Unlock()
		case <-time.After(50 * time.Millisecond):
			t.Error("BriefPolicy remains blocked past context deadline on an unrelated worker mutex")
			other.mu.Unlock()
			<-done
		}
	}
}

func TestR3BriefPolicyBusyTargetAndRegistry(t *testing.T) {
	key := store.QueueKey{Repository: "wanted/repo"}
	w := &worker{Target: Target{Key: key}}
	s := &Server{queues: []*worker{w}}
	for _, registry := range []bool{false, true} {
		if registry {
			s.mu.Lock()
		} else {
			w.mu.Lock()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		done := make(chan error, 1)
		go func() { _, err := s.BriefPolicy(ctx, key, status.BriefPolicyRequest{}); done <- err }()
		select {
		case err := <-done:
			temporary, ok := err.(interface{ Temporary() bool })
			if !ok || !temporary.Temporary() {
				t.Errorf("missing transient error: %v", err)
			}
		case <-ctx.Done():
			t.Error("policy lock waited past deadline")
		}
		if registry {
			s.mu.Unlock()
		} else {
			w.mu.Unlock()
		}
		cancel()
	}
}
