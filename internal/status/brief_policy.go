package status

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/classification"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// BriefPolicyRequest pins configuration to the journal at classification time.
// All supplemental inputs are recorded evidence, never fresh membership.
type BriefPolicyRequest struct {
	Config    queue.ConfigApplied
	Evidence  classification.Evidence
	Facts     queue.ApprovalFacts
	Governing *forge.GoverningFiles
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

type briefPolicyPin struct {
	evidence classification.Evidence
	config   queue.ConfigApplied
}

func briefPolicyProjection(ctx context.Context, source Source, j store.Journal, projection *Queue) func(queue.State, queue.ApprovalBasis) (brief.Fact, string) {
	current := currentBriefConfig(j, projection)
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
		pin, pinKnown := recordedBriefPolicyPin(j, projection, stored.Evidence)
		if !pinKnown || pin.Commit == "" || pin.Digest == "" {
			return unavailable()
		}
		policy, err := provider.BriefPolicy(ctx, j.Key, BriefPolicyRequest{Config: pin, Evidence: stored.Evidence, Facts: facts})
		var mismatch queue.BriefPolicyMismatchError
		if errors.As(err, &mismatch) {
			return brief.Fact{}, "policy_digest_mismatch"
		}
		if briefPolicyBusy(err) {
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
		return guardedBriefProgress(f, policy, s, facts, projection.eligibilityPins[b.ChangeID], pin, current, func() (cp.Policy, error) {
			return provider.BriefPolicy(ctx, j.Key, BriefPolicyRequest{Config: current, Evidence: stored.Evidence, Facts: facts})
		})
	}
}

func currentBriefConfig(j store.Journal, projection *Queue) queue.ConfigApplied {
	current := queue.ConfigApplied{Commit: j.Seed.ConfigCommit, Digest: j.Seed.ConfigDigest}
	for _, row := range j.Events {
		if applied, ok := row.Event.(queue.ConfigApplied); ok && applied.Commit == projection.state.ConfigCommit && applied.Digest == projection.state.ConfigDigest {
			current = applied
		}
	}
	return current
}

func recordedBriefPolicyPin(j store.Journal, projection *Queue, evidence classification.Evidence) (queue.ConfigApplied, bool) {
	for _, recorded := range projection.policyPins {
		if recorded.evidence.InputFingerprint == evidence.InputFingerprint && reflect.DeepEqual(recorded.evidence.Classification.Inputs, evidence.Classification.Inputs) {
			return recorded.config, true
		}
	}
	pin := queue.ConfigApplied{Commit: j.Seed.ConfigCommit, Digest: j.Seed.ConfigDigest}
	return pin, pin.Commit == evidence.Classification.Inputs.GoverningCommit
}

func briefPolicyBusy(err error) bool {
	var transient interface{ Temporary() bool }
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &transient) && transient.Temporary())
}

// guardedBriefProgress keeps eligibility recorded after a config change from
// blessing an old classification without proof that its policy component matches.
// Without a local policy provider, only the same governing commit and recorded
// identity pin can prove that a config change preserves the policy component.
func guardedBriefProgress(f brief.Fact, policy cp.Policy, s queue.State, facts queue.ApprovalFacts, eligibility briefEligibilityPin, historical, current queue.ConfigApplied, currentPolicy func() (cp.Policy, error)) (brief.Fact, string) {
	if policy.Approvals == nil {
		return f, ""
	}
	if !eligibility.stale && facts.Eligibility != nil && current.Digest != historical.Digest {
		eligibility.stale = historical.Commit == "" || historical.Commit != current.Commit || historical.Policy == nil || !reflect.DeepEqual(historical.Policy, current.Policy)
		if currentPolicy != nil {
			live, err := currentPolicy()
			if briefPolicyBusy(err) {
				return brief.Fact{}, "policy_busy"
			}
			eligibility.stale = err != nil || live.Digest != policy.Digest
		}
	}
	return recordedBriefProgress(f, policy, s, facts, eligibility), ""
}

// ReconstructBriefPolicy asserts the effective digest before exposing requirements.
// Approval queues require the recorded static identity pin and local policy tree.
func ReconstructBriefPolicy(data []byte, key store.QueueKey, defaultBranch string, req BriefPolicyRequest) (cp.Policy, error) {
	cfg, _, err := config.Load(data)
	if err != nil {
		return cp.Policy{}, fmt.Errorf("pinned config unavailable: %w", err)
	}
	if req.Config.Policy != nil {
		defaultBranch = req.Config.Policy.DefaultBranch
	}
	if defaultBranch == "" {
		return cp.Policy{}, fmt.Errorf("repository default branch unavailable")
	}
	selected, diagnostics := cfg.ResolveBranch(key.BaseBranch, defaultBranch)
	if len(diagnostics) != 0 {
		return cp.Policy{}, fmt.Errorf("pinned branch policy unavailable: %v", diagnostics)
	}
	component := ""
	if selected.Approvals != nil {
		if req.Config.Policy == nil || req.Governing == nil {
			return cp.Policy{}, fmt.Errorf("recorded identity pin or local governing objects unavailable")
		}
		g := req.Governing
		if g.CommitSHA != req.Config.Commit || g.Coverage.Completeness != forge.DiffComplete {
			return cp.Policy{}, fmt.Errorf("local governing tree unavailable")
		}
		owners, attrs := policyContents(*g)
		var candidate cp.CandidateQueue
		for _, q := range cfg.Policy.Queues {
			if q.Name == selected.Name {
				candidate = q
			}
		}
		component, err = cp.PolicyDigest(cp.DigestInputs{Repository: cfg.Policy, Queue: candidate, Resolved: req.Config.Policy.Resolved, Owners: owners, Attributes: attrs, ClassifierVersion: req.Config.Policy.ClassifierVersion})
		if err != nil {
			return cp.Policy{}, err
		}
		if component != req.Evidence.Classification.Inputs.PolicyDigest {
			return cp.Policy{}, queue.BriefPolicyMismatchError{Recorded: req.Evidence.Classification.Inputs.PolicyDigest}
		}
	}
	matches := false
	for _, deleteHead := range []bool{true, false} {
		if cfg.DeleteHeadBranch != nil && deleteHead != *cfg.DeleteHeadBranch {
			continue
		}
		effective, err := cp.WithPolicyDigest(selected.Digest(deleteHead), component)
		if err == nil && effective == req.Config.Digest {
			matches = true
		}
	}
	if !matches {
		return cp.Policy{}, queue.BriefPolicyMismatchError{Recorded: req.Config.Digest}
	}
	if component != "" {
		return cp.Policy{Digest: component, Approvals: selected.Approvals}, nil
	}
	return cp.Policy{Digest: req.Config.Digest}, nil
}

func policyContents(g forge.GoverningFiles) (cp.GoverningContent, []cp.GoverningContent) {
	content := func(f forge.GoverningFile) cp.GoverningContent {
		return cp.GoverningContent{Path: f.Path, Known: f.Coverage.Completeness == forge.DiffComplete, Present: f.Present, Content: f.Content}
	}
	owners := cp.GoverningContent{Path: ".github/CODEOWNERS", Known: true}
	for _, f := range g.Codeowners {
		if f.Path == g.SelectedCodeowners {
			owners = content(f)
		}
	}
	attrs := []cp.GoverningContent{}
	for _, f := range g.Attributes {
		if f.Present || f.Path == ".gitattributes" {
			attrs = append(attrs, content(f))
		}
	}
	return owners, attrs
}
