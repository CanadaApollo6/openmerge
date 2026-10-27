package serve

import (
	"context"
	"fmt"
	"strconv"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/gitops"
	p "github.com/CanadaApollo6/openmerge/internal/provenance"
	"github.com/CanadaApollo6/openmerge/internal/provenanceeval"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// provenancePass is the package-private 4a activation seam. It has no Store,
// Driver, acquisition or journal API. Public loaders/constructors cannot install
// it. 4b must bind its evidence atomically before connecting it to the worker.
type provenancePass struct {
	policy    cp.Policy
	added     *p.Policy
	mergeWhen condition.Expression
	inputs    condition.Expression
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
	return &provenancePass{baseline, added, cfg.MergeWhen, condition.All(expressions...)}, nil
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
