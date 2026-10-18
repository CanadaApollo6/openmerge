package serve

import (
	"context"
	"fmt"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// BriefPolicy reconstructs historical display requirements from local immutable
// objects only. The worker's current cfg and forge Driver are never consulted.
func (s *Server) BriefPolicy(ctx context.Context, key store.QueueKey, req status.BriefPolicyRequest) (cp.Policy, error) {
	if err := ctx.Err(); err != nil {
		return cp.Policy{}, err
	}
	if !s.mu.TryRLock() {
		return cp.Policy{}, diagnostic.BusyError("worker registry busy")
	}
	workers := append([]*worker(nil), s.queues...)
	s.mu.RUnlock()
	for _, w := range workers {
		if w.Key != key {
			continue
		} // Key is immutable after worker construction.
		if err := ctx.Err(); err != nil {
			return cp.Policy{}, err
		}
		if !w.mu.TryLock() {
			return cp.Policy{}, diagnostic.BusyError("policy worker busy")
		}
		target := w.Target
		w.mu.Unlock()
		reader, ok := target.Git.(interface {
			LocalConfig(context.Context, forge.RepoRef, string) ([]byte, error)
		})
		if !ok {
			break
		}
		data, err := reader.LocalConfig(ctx, forge.RepoRef{Path: key.Repository}, req.Config.Commit)
		if err != nil {
			return cp.Policy{}, err
		}
		return status.ReconstructBriefPolicy(data, key, req)
	}
	return cp.Policy{}, fmt.Errorf("local policy object reader unavailable")
}
