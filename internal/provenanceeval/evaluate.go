// Package provenanceeval composes immutable queue evidence with local declarations.
// It owns no journal, network or snapshot writer.
package provenanceeval

import (
	"fmt"
	"strconv"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	p "github.com/CanadaApollo6/openmerge/internal/provenance"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// Evaluation is ephemeral decision evidence. It is deliberately not
// a queue event or snapshot field: atomic recording belongs to ADR 0010 4b.
type Evaluation struct {
	BaselineApprovals cp.Evaluation
	LocalApprovals    []cp.Evaluation
	BaselineInputs    condition.Evidence
	Layers            p.Evaluation
	Inventory         p.Inventory
	Brief             brief.Fact
	Identity          cp.ReceiptIdentity
	PreCI             cp.Truth
}

// Brief binds only complete, pinned declarations and potential rule
// definitions. It reads no review or checkpoint observation to choose rules.
func Brief(s queue.State, policy cp.Policy, b queue.ApprovalBasis, opt *p.Policy, inventory p.Inventory) (brief.Fact, error) {
	if opt == nil {
		return s.RecordedBrief(policy, b)
	}
	if err := checkProvenanceInventory(s, policy, b, opt, inventory); err != nil {
		return brief.Fact{}, err
	}
	f, err := s.RecordedBrief(policy, b)
	if err != nil {
		return brief.Fact{}, err
	}
	f.DeclarationDigest, err = p.DeclarationDigest(inventory.Commits, inventory.Complete, opt.Rules())
	if err != nil {
		return brief.Fact{}, err
	}
	return f, nil
}

func checkProvenanceInventory(s queue.State, policy cp.Policy, b queue.ApprovalBasis, opt *p.Policy, inventory p.Inventory) error {
	var boundary queue.RevisionRecord
	for _, candidate := range s.ChangeRevisions() {
		if candidate.ChangeID == b.ChangeID && candidate.Revision == b.Revision && candidate.HeadSHA == b.Head {
			boundary = candidate
			break
		}
	}
	ok := s.CheckApprovalBoundary(b) == nil
	r := inventory.Request
	_, parser, limits := opt.ReaderInputs()
	if !ok || policy.Digest != opt.Digest() || r.ChangeID != string(b.ChangeID) || r.Revision != strconv.FormatUint(uint64(b.Revision), 10) ||
		r.Head != b.Head || r.PinnedBase != boundary.Basis.BaseSHA || r.BuildBase == "" || r.Generation != b.ReadID ||
		(inventory.Complete || inventory.PolicyID != "") && (inventory.Policy != parser || inventory.PolicyID != parser.Identity()) || inventory.Limits != limits {
		return fmt.Errorf("provenance inventory basis or policy mismatch")
	}
	return nil
}

// Evaluate freezes baseline, trigger and rule-local inputs separately.
// Both baseline and local approval pools consume the same attributed stance fold
// and fresh eligibility read. Added pools never modify the policy or snapshot.
func Evaluate(s queue.State, policy cp.Policy, fresh queue.ApprovalEligibilityObserved, expression condition.Expression, values condition.Snapshot, opt *p.Policy, inventory p.Inventory) (Evaluation, error) {
	out := Evaluation{Inventory: inventory, PreCI: cp.True}
	if err := p.ValidateOrdinary(expression); err != nil {
		return out, err
	}
	f, err := Brief(s, policy, fresh.Basis, opt, inventory)
	if opt == nil {
		if err != nil {
			return out, err
		}
	} else if basisErr := checkProvenanceInventory(s, policy, fresh.Basis, opt, inventory); basisErr != nil {
		return out, basisErr
	} else if !inventory.Complete {
		out.PreCI = cp.Unknown
		if inventory.Diagnostic.Outcome == p.LimitExceeded || inventory.Diagnostic.Outcome == p.InvalidInput {
			out.PreCI = cp.False
		}
	} else if err != nil {
		return out, err
	}
	out.Brief = f
	// Incomplete inventories cannot issue receipts. An empty brief digest also
	// prevents a previously bound receipt from satisfying an added obligation.
	if err == nil {
		out.Identity, err = brief.Identity(f)
		if err != nil {
			return out, err
		}
	} else {
		recorded, recordedErr := s.RecordedBrief(policy, fresh.Basis)
		if recordedErr != nil {
			return out, recordedErr
		}
		out.Identity, err = brief.Identity(recorded)
		if err != nil {
			return out, err
		}
		out.Identity.BriefDigest = ""
	}
	out.BaselineApprovals = s.EvaluateApprovals(policy, out.Identity, fresh)
	baseline := make(condition.Snapshot, len(values)+1)
	for k, v := range values {
		baseline[k] = v
	}
	if out.BaselineApprovals.OptedIn {
		baseline["approved-reviews-by"] = out.BaselineApprovals.ReviewAttribute()
	}
	out.BaselineInputs = expression.Record(baseline)
	var rules []p.Rule
	if opt != nil {
		rules = opt.Rules()
		for _, rule := range rules {
			local := cp.Evaluation{}
			if rule.Approvals != nil {
				local = s.EvaluateAdditionalApproval(*rule.Approvals, out.Identity, fresh)
			}
			out.LocalApprovals = append(out.LocalApprovals, local)
		}
	}
	attrs := p.DeclarationAttributes(inventory.Commits, inventory.Complete)
	out.Layers = p.EvaluateRequirements(expression, baseline, out.BaselineApprovals, attrs, rules, out.LocalApprovals)
	// Extraction itself gates CI even when every trigger is false or a pending
	// trigger already has a satisfied obligation. Reviews and CI remain
	// speculative; their mutable outcomes gate landing, not test publication.
	if out.PreCI == cp.False || out.PreCI == cp.Unknown && out.Layers.Result == cp.True {
		out.Layers.Result = out.PreCI
	}
	return out, nil
}
