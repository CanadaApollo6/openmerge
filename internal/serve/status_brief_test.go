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
	s := &Server{queues: []*worker{{Target: Target{Key: key, Git: g}, configBranch: "main"}}}
	req := status.BriefPolicyRequest{Config: queue.ConfigApplied{Commit: "recorded-commit", Digest: digest}, Evidence: classification.Evidence{Classification: cp.Classification{Inputs: cp.ClassificationInputs{PolicyDigest: digest}}}}
	policy, err := s.BriefPolicy(context.Background(), key, req)
	if err != nil || policy.Approvals != nil || policy.Digest != digest || g.calls != 1 || g.commit != "recorded-commit" {
		t.Fatalf("local provider: %+v %v %+v", policy, err, g)
	}
}

func TestR4BriefPolicyDefaultBranch(t *testing.T) {
	data := []byte("version: 1\nqueues:\n  - name: main\n    merge_when: [check-success = ci]\n  - name: rel\n    base: release/*\n    merge_when: [check-success = release-ci]\n")
	cfg, _, err := config.Load(data)
	if err != nil {
		t.Fatal(err)
	}
	selected, diagnostics := cfg.ResolveBranch("release/1", "main")
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	key := store.QueueKey{Installation: "7", Repository: "owner/repo", BaseBranch: "release/1"}
	for _, defaultBranch := range []string{"main", ""} {
		t.Run("default="+defaultBranch, func(t *testing.T) {
			for _, deletion := range []bool{true, false} {
				g := &localPolicyGit{data: data}
				s := &Server{queues: []*worker{{Target: Target{Key: key, Git: g}, configBranch: defaultBranch}}}
				digest := selected.Digest(deletion)
				p, err := s.BriefPolicy(context.Background(), key, status.BriefPolicyRequest{Config: queue.ConfigApplied{Commit: "pinned", Digest: digest}})
				if defaultBranch == "" {
					if err == nil {
						t.Fatal("unknown default accepted")
					}
					continue
				}
				if err != nil || p.Digest != digest || g.commit != "pinned" {
					t.Fatalf("policy=%+v err=%v git=%+v", p, err, g)
				}
			}
		})
	}
}
