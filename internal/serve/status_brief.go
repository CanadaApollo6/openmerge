package serve

import (
	"context"
	"fmt"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// BriefPolicy reconstructs historical display requirements from local immutable
// objects only. The worker's current cfg and forge Driver are never consulted.
func (s *Server) BriefPolicy(ctx context.Context, key store.QueueKey, req status.BriefPolicyRequest) (cp.Policy, error) {
	for _, w := range s.workers() {
		w.mu.Lock()
		target := w.Target
		w.mu.Unlock()
		if target.Key != key {
			continue
		}
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
