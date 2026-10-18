package status

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"go.yaml.in/yaml/v3"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/classification"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// BriefPolicyRequest pins configuration to the journal at classification time.
// All supplemental inputs are recorded evidence, never fresh membership.
type BriefPolicyRequest struct {
	Config   queue.ConfigApplied
	Evidence classification.Evidence
	Facts    queue.ApprovalFacts
}

type briefPolicySource interface {
	BriefPolicy(context.Context, store.QueueKey, BriefPolicyRequest) (cp.Policy, error)
}

type briefOptions struct {
	sanitizer *diagnostic.Sanitizer
	policy    func(queue.State, queue.ApprovalBasis) (brief.Fact, string)
}

func applyBriefPolicy(f *brief.Fact, s queue.State, b queue.ApprovalBasis, opt briefOptions) string {
	if opt.policy == nil {
		return "policy_unavailable"
	}
	recorded, code := opt.policy(s, b)
	if code != "" {
		return code
	}
	recorded.Presentation = f.Presentation
	recorded.Presentation.RequirementsMissing = false
	*f = recorded
	return ""
}

func briefPolicyProjection(ctx context.Context, source Source, j store.Journal) func(queue.State, queue.ApprovalBasis) (brief.Fact, string) {
	return func(s queue.State, b queue.ApprovalBasis) (brief.Fact, string) {
		unavailable := func() (brief.Fact, string) { return brief.Fact{}, "policy_unavailable" }
		provider, ok := source.(briefPolicySource)
		stored, found := s.Classification(b.ChangeID, b.Revision)
		facts, recorded := s.ApprovalFacts(b.ChangeID)
		if !ok || !found || !recorded || facts.Intent == nil {
			return unavailable()
		}
		// Reuse a genuine recorded read basis: synthesized wall-clock reads cannot
		// become receipt identity or pretend that missing ingestion has happened.
		b = facts.Intent.Basis
		pin := queue.ConfigApplied{Commit: j.Seed.ConfigCommit, Digest: j.Seed.ConfigDigest}
		pinKnown := pin.Commit == stored.Evidence.Classification.Inputs.GoverningCommit
		state := j.Seed
		for _, row := range j.Events {
			state = queue.Apply(state, row.Event)
			if e, ok := row.Event.(queue.ChangeClassified); ok {
				i := e.Evidence.Classification.Inputs
				wanted := stored.Evidence.Classification.Inputs
				if reflect.DeepEqual(i, wanted) && e.Evidence.InputFingerprint == stored.Evidence.InputFingerprint {
					pin = queue.ConfigApplied{Commit: state.ConfigCommit, Digest: state.ConfigDigest}
					pinKnown = true
					break
				}
			}
		}
		if !pinKnown || pin.Commit == "" || pin.Digest == "" {
			return unavailable()
		}
		policy, err := provider.BriefPolicy(ctx, j.Key, BriefPolicyRequest{pin, stored.Evidence, facts})
		var mismatch queue.BriefPolicyMismatchError
		if errors.As(err, &mismatch) {
			return brief.Fact{}, "policy_digest_mismatch"
		}
		if errors.Is(err, ErrCandidatePolicyUnavailable) {
			return brief.Fact{}, "policy_composition_11b"
		}
		var transient interface{ Temporary() bool }
		if errors.As(err, &transient) && transient.Temporary() {
			return brief.Fact{}, "policy_busy"
		}
		if err != nil {
			return unavailable()
		}
		f, err := s.RecordedBrief(policy, b)
		if errors.As(err, &mismatch) {
			return brief.Fact{}, "policy_digest_mismatch"
		}
		if err != nil {
			return unavailable()
		}
		return f, ""
	}
}

// ErrCandidatePolicyUnavailable identifies the 11b composition obligation.
var ErrCandidatePolicyUnavailable = errors.New("recorded-policy digest composition is defined in 11b")

// ReconstructBriefPolicy verifies legacy effective configuration. Candidate
// policy composition remains unavailable until 11b defines its recorded digest.
func ReconstructBriefPolicy(data []byte, key store.QueueKey, defaultBranch string, req BriefPolicyRequest) (cp.Policy, error) {
	cfg, _, err := config.Load(data)
	if err != nil {
		var candidate struct {
			Queues []struct {
				Approvals yaml.Node `yaml:"approvals"`
			} `yaml:"queues"`
		}
		if yaml.Unmarshal(data, &candidate) == nil {
			for _, q := range candidate.Queues {
				if q.Approvals.Kind != 0 {
					return cp.Policy{}, ErrCandidatePolicyUnavailable
				}
			}
		}
		if _, _, candidateErr := cp.ParseCandidate(data); candidateErr == nil {
			return cp.Policy{}, ErrCandidatePolicyUnavailable
		}
		return cp.Policy{}, fmt.Errorf("pinned config unavailable: %w", err)
	}
	if defaultBranch == "" {
		return cp.Policy{}, fmt.Errorf("repository default branch unavailable")
	}
	selected, diagnostics := cfg.ResolveBranch(key.BaseBranch, defaultBranch)
	if len(diagnostics) != 0 {
		return cp.Policy{}, fmt.Errorf("pinned branch policy unavailable: %v", diagnostics)
	}
	matches := false
	for _, deleteHead := range []bool{true, false} {
		if cfg.DeleteHeadBranch != nil && deleteHead != *cfg.DeleteHeadBranch {
			continue
		}
		if selected.Digest(deleteHead) == req.Config.Digest {
			matches = true
		}
	}
	if !matches {
		return cp.Policy{}, queue.BriefPolicyMismatchError{Recorded: req.Config.Digest}
	}
	return cp.Policy{Digest: req.Config.Digest}, nil
}
