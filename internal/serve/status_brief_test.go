package serve

import (
	"context"
	"testing"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/classification"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

type localPolicyGit struct {
	forge.Git // Every ordinary Git operation is deliberately unavailable.
	data      []byte
	commit    string
	calls     int
}

func (g *localPolicyGit) LocalConfig(_ context.Context, _ forge.RepoRef, commit string) ([]byte, error) {
	g.calls++
	g.commit = commit
	return g.data, nil
}

func TestBriefPolicyUsesOnlyPinnedLocalReader(t *testing.T) {
	data := []byte("version: 1\nqueues:\n  - name: default\n    merge_when: [check-success = ci]\n")
	cfg, _, err := config.Load(data)
	if err != nil {
		t.Fatal(err)
	}
	digest := cfg.Queues[0].Digest(true)
	key := store.QueueKey{Installation: "7", Repository: "owner/repo", BaseBranch: "main"}
	g := &localPolicyGit{data: data}
	s := &Server{queues: []*worker{{Target: Target{Key: key, Git: g}}}}
	req := status.BriefPolicyRequest{Config: queue.ConfigApplied{Commit: "recorded-commit", Digest: digest}, Evidence: classification.Evidence{Classification: cp.Classification{Inputs: cp.ClassificationInputs{PolicyDigest: digest}}}}
	policy, err := s.BriefPolicy(context.Background(), key, req)
	if err == nil || policy.Approvals != nil || policy.Digest != "" || g.calls != 1 || g.commit != "recorded-commit" {
		t.Fatalf("local provider: %+v %v %+v", policy, err, g)
	}
}
