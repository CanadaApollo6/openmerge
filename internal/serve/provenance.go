package serve

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/gitops"
	p "github.com/CanadaApollo6/openmerge/internal/provenance"
	"github.com/CanadaApollo6/openmerge/internal/provenanceeval"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// provenancePass is the private 4a/4b activation seam. Public loaders and
// constructors cannot install it. The worker calls its activated landing gate
// only with stable local objects and trusted composer observations.
type provenancePass struct {
	policy    cp.Policy
	added     *p.Policy
	mergeWhen condition.Expression
	inputs    condition.Expression
	// Installed only by the internal/test seam. Local holds already-acquired
	// immutable objects stable; Compositions supplies trusted composer output,
	// never mappings inferred from contributor trailers.
	local        func(context.Context, func(gitDir, scratch string) error) error
	compositions func(context.Context, queue.Land, string) ([]provenanceComposition, error)
}

func newProvenancePass(cfg config.Queue, baseline cp.Policy, rules []p.Rule, identities []p.IdentityRule, parser p.ParserPolicy, limits p.InventoryLimits) (*provenancePass, error) {
	// Bind the full queue policy as well as its approval/classification component.
	// This is internal-only; public queues retain their existing digest path.
	digest, err := cp.WithPolicyDigest(cfg.Digest(false), baseline.Digest)
	if err != nil {
		return nil, err
	}
	baseline.Digest = digest
	added, err := p.NewPolicy(baseline, rules, identities, parser, limits, cfg.EnterWhen, cfg.MergeWhen)
	if err != nil {
		return nil, err
	}
	baseline.Digest = added.Digest()
	expressions := []condition.Expression{cfg.MergeWhen}
	for _, r := range added.Rules() {
		expressions = append(expressions, r.When)
		if r.Conditions != nil {
			expressions = append(expressions, *r.Conditions)
		}
	}
	return &provenancePass{policy: baseline, added: added, mergeWhen: cfg.MergeWhen, inputs: condition.All(expressions...)}, nil
}

type provenanceMember struct {
	Pin   queue.BatchPR
	Fresh queue.ApprovalEligibilityObserved
	PR    forge.PullRequest
	// Excluded is specific to this member's trusted source/build mapping.
	Excluded []string
}

type provenanceBatch struct {
	// Included is the per-change pre-CI filter, before any composition or test
	// publication. Review/CI waits remain speculative and cannot pool members.
	Included []queue.BatchPR
	Members  []provenanceeval.Evaluation
}

// evaluate reads already-acquired, self-contained local objects only. The
// caller holds them stable throughout this pass. Exclusions must be trusted
// synthetic markers/other changes' originals, never contributor trailers.
func (pass *provenancePass) evaluate(ctx context.Context, state queue.State, repository, lane, buildBase, gitDir, scratch string, members []provenanceMember, checks []forge.CheckRun) (provenanceBatch, error) {
	out := provenanceBatch{Included: []queue.BatchPR{}, Members: []provenanceeval.Evaluation{}}
	classifier, _, limits := pass.added.ReaderInputs()
	seen := map[queue.ChangeID]bool{}
	for _, member := range members {
		b := member.Fresh.Basis
		r, ok := state.PRRevision(member.Pin.PR)
		if !ok || seen[r.ChangeID] || r.ChangeID != b.ChangeID || r.Revision != b.Revision || r.HeadSHA != member.Pin.HeadSHA || r.HeadSHA != b.Head || member.PR.Number != member.Pin.PR || member.PR.HeadSHA != b.Head {
			return provenanceBatch{}, fmt.Errorf("provenance member basis mismatch")
		}
		seen[r.ChangeID] = true
		req := p.InventoryRequest{Repository: repository, Queue: lane, ChangeID: string(r.ChangeID), Revision: strconv.FormatUint(uint64(r.Revision), 10), Generation: b.ReadID, Head: r.HeadSHA, PinnedBase: r.Basis.BaseSHA, BuildBase: buildBase, Excluded: member.Excluded}
		inventory := gitops.ReadProvenanceInventory(ctx, gitDir, scratch, req, classifier, limits)
		values, err := policyValues(pass.inputs, member.PR, checks)
		if err != nil {
			return provenanceBatch{}, err
		}
		result, err := provenanceeval.Evaluate(state, pass.policy, member.Fresh, pass.mergeWhen, values, pass.added, inventory)
		if err != nil {
			return provenanceBatch{}, err
		}
		out.Members = append(out.Members, result)
		if result.PreCI == cp.True {
			out.Included = append(out.Included, member.Pin)
		}
	}
	return out, nil
}

// provenanceComposition is transient trusted build input, not a journal field.
// Tested IDs are attempt-bound; Landed IDs are Tested.ID + "/landed".
type provenanceComposition struct {
	Tested, Landed p.BuildObservation
	Excluded       []string
}

// prepareStart runs after the final fresh eligibility/check reads and before
// opening the store transaction. It maps T and the actual expected L separately.
func (pass *provenancePass) prepareStart(ctx context.Context, s *store.Store, w *worker, state queue.State, land queue.Land, attempt string, fresh []queue.ApprovalEligibilityObserved, checks []forge.CheckRun) (store.ProvenanceStart, error) {
	if pass == nil || pass.local == nil || pass.compositions == nil || w.stageA == nil || w.stageA.policy.Digest != pass.policy.Digest {
		return store.ProvenanceStart{}, errors.New("incomplete provenance activation")
	}
	builds, err := pass.compositions(ctx, land, attempt)
	if err != nil {
		return store.ProvenanceStart{}, err
	}
	if len(builds) != len(land.PRs) || len(fresh) != len(land.PRs) {
		return store.ProvenanceStart{}, errors.New("incomplete provenance compositions")
	}
	v := store.ProvenanceStart{Policy: pass.added, Expression: pass.mergeWhen, DefaultBranch: w.stageA.defaultBranch, Members: make([]store.ProvenanceMemberInput, len(fresh))}
	requests := make([]p.InventoryRequest, len(fresh))
	for i, f := range fresh {
		r, ok := state.PRRevision(land.PRs[i].PR)
		if !ok || r.ChangeID != f.Basis.ChangeID || r.HeadSHA != land.PRs[i].HeadSHA || r.HeadSHA != f.Basis.Head {
			return v, store.ErrLandingDecisionStale
		}
		requests[i] = p.InventoryRequest{Repository: w.Key.Repository, Queue: w.Key.BaseBranch + "/" + w.Key.Lane, ChangeID: string(r.ChangeID), Revision: strconv.FormatUint(uint64(r.Revision), 10), Generation: f.Basis.ReadID, Head: r.HeadSHA, PinnedBase: r.Basis.BaseSHA, BuildBase: builds[i].Tested.Base, Excluded: append([]string{}, builds[i].Excluded...)}
	}
	previous, err := s.ProvenancePredecessors(ctx, w.id, requests)
	if err != nil {
		return v, err
	}
	classifier, _, limits := pass.added.ReaderInputs()
	err = pass.local(ctx, func(dir, scratch string) error {
		for i, req := range requests {
			inventory := gitops.ReadProvenanceInventory(ctx, dir, scratch, req, classifier, limits)
			if !inventory.Complete {
				return fmt.Errorf("provenance inventory %s:%s", inventory.Diagnostic.Outcome, inventory.Diagnostic.Resource)
			}
			t := gitops.MapProvenanceBuild(ctx, dir, scratch, inventory, builds[i].Tested, limits)
			l := gitops.MapProvenanceBuild(ctx, dir, scratch, inventory, builds[i].Landed, limits)
			if !t.Complete || !l.Complete {
				return errors.New("provenance tested/landed mapping incomplete")
			}
			values, err := policyValues(pass.inputs, w.prs[land.PRs[i].PR], checks)
			if err != nil {
				return err
			}
			v.Members[i] = store.ProvenanceMemberInput{Inventory: inventory, Tested: t, Landed: l, Values: values, PreviousObservationID: previous[i]}
		}
		return nil
	})
	return v, err
}

// reviewEvidence uses the same pinned inventory and declaration identity as
// final admission without requiring pending CI or approval conditions to pass.
func (pass *provenancePass) reviewEvidence(ctx context.Context, s *store.Store, w *worker, state queue.State, land queue.Land, fresh []queue.ApprovalEligibilityObserved, checks []forge.CheckRun) ([]*ApprovalEvidence, error) {
	prepared, err := pass.prepareStart(ctx, s, w, state, land, "preflight", fresh, checks)
	if err != nil {
		return nil, err
	}
	reviews := make([]*ApprovalEvidence, 0, len(prepared.Members))
	for i, member := range prepared.Members {
		evaluation, err := provenanceeval.Evaluate(state, pass.policy, fresh[i], prepared.Expression, member.Values, prepared.Policy, member.Inventory)
		if err != nil {
			return nil, err
		}
		reviews = append(reviews, &ApprovalEvidence{Known: evaluation.BaselineApprovals.ReviewsKnown, Reviewers: evaluation.BaselineApprovals.ReviewLogins})
	}
	return reviews, nil
}

// provenanceGate preserves the existing fresh-read/metadata protocol. The activated
// writer is reached only here; ordinary queues keep their original Commit.
func provenanceGate(g *executor.LandingDecisionGate, evidence *store.ProvenanceStart) *executor.LandingDecisionGate {
	g.Prepare = func(state queue.State, land queue.Land, start queue.LandingStarted, fresh []queue.ApprovalEligibilityObserved) ([]queue.LandingDecisionRecorded, error) {
		d, _, err := evidence.Decisions(state, g.Policy, land, start, fresh)
		return d, err
	}
	g.ReadInputs = nil
	return g
}
