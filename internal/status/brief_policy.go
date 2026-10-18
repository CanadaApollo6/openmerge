package status

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

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

// ReconstructBriefPolicy accepts the internal candidate format or an existing
// public legacy config. It does not extend the public loader with approval keys.
func ReconstructBriefPolicy(data []byte, key store.QueueKey, req BriefPolicyRequest) (cp.Policy, error) {
	r, _, err := cp.ParseCandidate(data)
	if err != nil {
		legacy, _, loadErr := config.Load(data)
		if loadErr != nil {
			return cp.Policy{}, fmt.Errorf("pinned policy unavailable")
		}
		for _, q := range legacy.Queues {
			if key.Lane != "" && q.Name != key.Lane {
				continue
			}
			cleanup := legacy.DeleteHeadBranch == nil || *legacy.DeleteHeadBranch
			if legacy.DeleteHeadBranch == nil && q.Digest(false) == req.Config.Digest {
				cleanup = false
			}
			digest := q.Digest(cleanup)
			if key.Lane == "" && digest != req.Config.Digest {
				continue
			}
			if digest != req.Config.Digest || digest != req.Evidence.Classification.Inputs.PolicyDigest {
				return cp.Policy{}, queue.BriefPolicyMismatchError{Supplied: digest, Recorded: req.Evidence.Classification.Inputs.PolicyDigest}
			}
			return cp.Policy{Digest: digest}, nil
		}
		return cp.Policy{}, fmt.Errorf("pinned queue unavailable")
	}
	var selected *cp.CandidateQueue
	for i := range r.Queues {
		if r.Queues[i].Name == key.Lane || key.Lane == "" && (len(r.Queues) == 1 || r.Queues[i].Name == key.BaseBranch) {
			selected = &r.Queues[i]
			break
		}
	}
	if selected == nil {
		return cp.Policy{}, fmt.Errorf("pinned queue unavailable")
	}
	// Names can only be resolved from recorded identities. Team IDs and other
	// unrecorded static resolutions remain unavailable until 11a records them.
	resolved := []cp.ResolvedIdentity{}
	names := map[string]bool{}
	add := func(i cp.Identity) {
		n := strings.ToLower(i.Login)
		if n != "" && !names[n] {
			names[n] = true
			resolved = append(resolved, cp.ResolvedIdentity{Name: n, ID: i.ID, Kind: i.Kind})
		}
	}
	if a := req.Facts.Accountability; a != nil {
		add(a.Actor)
		add(a.Author)
		for _, h := range a.Humans {
			add(h)
		}
	}
	// Hash only configured names, as unrelated authors do not enter policy identity.
	needed := map[string]bool{}
	for _, members := range r.Groups {
		for _, n := range members {
			needed[n] = true
		}
	}
	for _, a := range r.Agents {
		needed[a.Identity] = true
		for _, n := range a.Accountable {
			needed[n] = true
		}
	}
	filtered := []cp.ResolvedIdentity{}
	for _, i := range resolved {
		if needed[i.Name] {
			filtered = append(filtered, i)
		}
	}
	governing := req.Evidence.Governing
	owners := cp.GoverningContent{Path: ".github/CODEOWNERS", Known: true}
	for _, file := range governing.Codeowners {
		if file.Coverage.Completeness != forge.DiffComplete {
			return cp.Policy{}, fmt.Errorf("governing owners unavailable")
		}
		if file.Present {
			owners = cp.GoverningContent{Path: file.Path, Known: true, Present: true, Content: file.Content}
			break
		}
	}
	if len(governing.Codeowners) == 0 {
		return cp.Policy{}, fmt.Errorf("governing owners unavailable")
	}
	attributes := []cp.GoverningContent{}
	for _, file := range governing.Attributes {
		attributes = append(attributes, cp.GoverningContent{Path: file.Path, Known: file.Coverage.Completeness == forge.DiffComplete, Present: file.Present, Content: file.Content})
	}
	digest, err := cp.PolicyDigest(cp.DigestInputs{Repository: r, Queue: *selected, Resolved: filtered, Owners: owners, Attributes: attributes, ClassifierVersion: req.Evidence.Classification.Inputs.ClassifierVersion})
	if err != nil {
		return cp.Policy{}, err
	}
	if selected.Approvals == nil {
		return cp.Policy{}, fmt.Errorf("candidate omits effective legacy queue settings")
	}
	if digest != req.Evidence.Classification.Inputs.PolicyDigest {
		return cp.Policy{}, queue.BriefPolicyMismatchError{Supplied: digest, Recorded: req.Evidence.Classification.Inputs.PolicyDigest}
	}
	return cp.Policy{Digest: digest, Approvals: selected.Approvals}, nil
}
