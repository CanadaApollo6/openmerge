package queue

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// LandingDecisionRecorded is immutable evidence for one member of one attempt.
// Classification and source references resolve to journal records, not mutable
// forge objects. ADR 0008 can add check evidence in a separate event.
type LandingDecisionRecorded struct {
	BatchID   string                   `json:"batch_id"`
	AttemptID string                   `json:"attempt_id"`
	TestedSHA string                   `json:"tested_sha"`
	LandedSHA string                   `json:"landed_sha"`
	Variant   string                   `json:"variant"`
	Approval  *LandingApprovalDecision `json:"approval"`
	Remedy    *GeneratedRemedyDecision `json:"remedy"`
}

// GeneratedRemedyDecision records ADR 0005's exemption without generating work.
type GeneratedRemedyDecision struct {
	ChangeID ChangeID       `json:"change_id"`
	Revision ChangeRevision `json:"revision"`
	Head     string         `json:"head"`
	Suspect  string         `json:"suspect"`
	Evidence []string       `json:"evidence"`
	OnRed    string         `json:"on_red"`
}

// LandingApprovalDecision references recorded classification inputs and the
// exact fresh eligibility observation journaled in the start transaction.
type LandingApprovalDecision struct {
	Sources                []LandingSourceRecord `json:"sources"`
	Basis                  ApprovalBasis         `json:"basis"`
	Identity               wireReceiptIdentity   `json:"identity"`
	ClassificationDigest   string                `json:"classification_digest"`
	Labels                 []string              `json:"labels"`
	InputFingerprint       string                `json:"input_fingerprint"`
	EligibilityReadID      string                `json:"eligibility_read_id"`
	DirectProtection       string                `json:"direct_protection"`
	Actor                  wireReceiptActor      `json:"actor"`
	Author                 wireReceiptActor      `json:"author"`
	SubmitterID            string                `json:"submitter_id"`
	AccountableHumans      []wireReceiptActor    `json:"accountable_humans"`
	AccountabilityRecorded bool                  `json:"accountability_recorded"`
	Requirements           []LandingRequirement  `json:"requirements"`
}

// LandingRequirement is an expanded obligation, including both rename sides.
// A shared approval is listed in every obligation it satisfies.
type LandingRequirement struct {
	Cause       cp.ObligationCause      `json:"cause"`
	RuleIndex   *int                    `json:"rule_index"`
	Path        string                  `json:"path"`
	Side        cp.OwnerSide            `json:"side"`
	Selector    cp.Selector             `json:"selector"`
	Group       string                  `json:"group"`
	Human       bool                    `json:"human"`
	Receipt     bool                    `json:"receipt"`
	Count       int                     `json:"count"`
	PoolIDs     []string                `json:"pool_ids"`
	EligibleIDs []string                `json:"eligible_ids"`
	ReliedOn    []LandingApprovalSource `json:"relied_on"`
}

// LandingApprovalSource names the original attributed stance and, for receipt
// commands, the acceptance record. Empty native bindings mean not recorded.
type LandingApprovalSource struct {
	UserID       string           `json:"user_id"`
	Channel      cp.SourceChannel `json:"channel"`
	SourceID     string           `json:"source_id"`
	ReadID       string           `json:"read_id"`
	ReceiptID    string           `json:"receipt_id"`
	IntentDigest string           `json:"intent_digest"`
	BriefDigest  string           `json:"brief_digest"`
}

func (LandingDecisionRecorded) queueEvent() {}

func canonicalDecisionIDs(ids []string) bool {
	if ids == nil {
		return false
	}
	for i, id := range ids {
		if !validActorID(id) || i > 0 && ids[i-1] >= id {
			return false
		}
	}
	return true
}

// Validate checks nested values as well as the exclusive variant shape.
func (e LandingDecisionRecorded) Validate() error {
	if requireActionText("batch_id", e.BatchID, "attempt_id", e.AttemptID) != nil || !forge.ValidDiffObjectID(e.TestedSHA) || !forge.ValidDiffObjectID(e.LandedSHA) {
		return fmt.Errorf("invalid landing decision identity")
	}
	switch e.Variant {
	case "generated_remedy":
		r := e.Remedy
		if r == nil || e.Approval != nil || r.ChangeID.Validate() != nil || r.Revision == 0 || !forge.ValidDiffObjectID(r.Head) || !forge.ValidDiffObjectID(r.Suspect) || len(r.Evidence) == 0 || len(r.Evidence) > 128 || r.OnRed != "revert" {
			return fmt.Errorf("invalid generated remedy decision")
		}
		for _, v := range r.Evidence {
			if strings.TrimSpace(v) == "" || len(v) > 1024 {
				return fmt.Errorf("invalid remedy evidence reference")
			}
		}
	case "approvals":
		a := e.Approval
		if a == nil || e.Remedy != nil || a.Basis.Validate() != nil || !cp.ReceiptIdentity(a.Identity).Recorded() || !forge.ValidDiffObjectID(a.Identity.Base) || !forge.ValidDiffObjectID(a.Identity.Head) || !forge.ValidDiffObjectID(a.Identity.MergeBase) || !digestHex(a.Identity.PolicyDigest) || !digestHex(a.Identity.BriefDigest) || !digestHex(a.Identity.IntentDigest) || !digestHex(a.ClassificationDigest) || !digestHex(a.InputFingerprint) || a.Identity.ChangeID != string(a.Basis.ChangeID) || a.Identity.Revision != uint64(a.Basis.Revision) || a.Identity.Head != a.Basis.Head || a.EligibilityReadID != a.Basis.ReadID || a.DirectProtection != "not_applicable_pr" || !validIdentity(cp.Identity(a.Actor)) || !validIdentity(cp.Identity(a.Author)) || a.SubmitterID != "" && !validActorID(a.SubmitterID) || a.AccountableHumans == nil || a.AccountabilityRecorded != (len(a.AccountableHumans) > 0) || a.Labels == nil || a.Requirements == nil || len(a.Requirements) > maxSnapshotEntries {
			return fmt.Errorf("invalid approval decision")
		}
		if err := validateLandingSources(a); err != nil {
			return err
		}
		for i, l := range a.Labels {
			if !cp.ValidClassificationLabel(l) || i > 0 && a.Labels[i-1] >= l {
				return fmt.Errorf("invalid decision labels")
			}
		}
		for _, h := range a.AccountableHumans {
			if !validIdentity(cp.Identity(h)) || h.Kind != cp.HumanAccount {
				return fmt.Errorf("invalid decision accountable human")
			}
		}
		for _, r := range a.Requirements {
			requirement := cp.Requirement{Selector: r.Selector, Group: r.Group, Human: r.Human, Receipt: r.Receipt, Count: r.Count}
			if r.Selector == cp.CodeOwners {
				requirement.Count = 0
			}
			if requirement.Validate() != nil || r.Selector == cp.None || r.Count < 1 || !canonicalDecisionIDs(r.PoolIDs) || !canonicalDecisionIDs(r.EligibleIDs) || len(r.ReliedOn) < r.Count || len(r.ReliedOn) > len(r.EligibleIDs) {
				return fmt.Errorf("invalid decision requirement")
			}
			switch r.Cause {
			case cp.CauseDefault:
				if r.RuleIndex != nil || r.Path != "" || r.Side != "" {
					return fmt.Errorf("invalid default obligation")
				}
			case cp.CauseRule, cp.CauseRuleCodeOwners:
				if r.RuleIndex == nil || *r.RuleIndex < 0 {
					return fmt.Errorf("invalid rule obligation")
				}
			case cp.CausePolicyOwner:
				if r.RuleIndex != nil || r.Path == "" {
					return fmt.Errorf("invalid policy obligation")
				}
			default:
				return fmt.Errorf("unmet obligation in landing decision")
			}
			if r.Side != "" && r.Side != cp.OldSide && r.Side != cp.NewSide || r.Cause == cp.CauseRuleCodeOwners && (r.Path == "" || r.Side == "") {
				return fmt.Errorf("invalid owner side")
			}
			for i, src := range r.ReliedOn {
				if !slices.Contains(r.EligibleIDs, src.UserID) || i > 0 && r.ReliedOn[i-1].UserID >= src.UserID || !validSourceID(src.SourceID) || src.ReadID == "" {
					return fmt.Errorf("invalid relied-on source")
				}
				switch src.Channel {
				case cp.NativeReview:
					if r.Receipt || src.ReceiptID != "" || src.IntentDigest != "" || src.BriefDigest != "" {
						return fmt.Errorf("native binding not recorded")
					}
				case cp.CommentCommand:
					if src.ReceiptID != src.SourceID || src.IntentDigest != a.Identity.IntentDigest || src.BriefDigest != a.Identity.BriefDigest {
						return fmt.Errorf("invalid relied-on receipt")
					}
				default:
					return fmt.Errorf("invalid approval channel")
				}
			}
		}
	default:
		return fmt.Errorf("invalid landing decision variant")
	}
	return nil
}

func classificationDecisionDigest(e ChangeClassified) string {
	// Exclude free-text diagnostics; the reference names the recorded generation
	// and its recorded input identity, excluding mutable diagnostic prose.
	c := detached(e.Evidence.Classification)
	c.Reasons = nil
	for i := range c.Files {
		c.Files[i].Reasons = nil
	}
	for i := range c.CodeOwners {
		c.CodeOwners[i].Eligible.Reason = ""
	}
	for i := range c.PolicyOwners {
		c.PolicyOwners[i].Eligible.Reason = ""
	}
	raw, _ := json.Marshal(c)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// PrepareLandingDecision consumes PR 6's bundle. It does not read the forge.
// The caller checks tested commit trailers before entering the store operation.
func (s State) PrepareLandingDecision(policy cp.Policy, fresh ApprovalEligibilityObserved, start LandingStarted, land Land) (LandingDecisionRecorded, error) {
	fact, err := s.RecordedBrief(policy, fresh.Basis)
	if err != nil {
		return LandingDecisionRecorded{}, err
	}
	identity, err := brief.Identity(fact)
	if err != nil {
		return LandingDecisionRecorded{}, err
	}
	bundle, err := s.PrepareApprovalDecision(policy, identity, fresh)
	if err != nil {
		return LandingDecisionRecorded{}, err
	}
	if !bundle.Evaluation.OptedIn || !bundle.Evaluation.Allowed || bundle.Accountability == nil || bundle.Intent == nil {
		return LandingDecisionRecorded{}, fmt.Errorf("landing approval unmet for %s", fresh.Basis.ChangeID)
	}
	recorded, ok := s.Classification(fresh.Basis.ChangeID, fresh.Basis.Revision)
	if !ok {
		return LandingDecisionRecorded{}, fmt.Errorf("classification not recorded")
	}
	acc := bundle.Accountability
	a := &LandingApprovalDecision{Sources: []LandingSourceRecord{}, Basis: fresh.Basis, Identity: wireReceiptIdentity(identity), ClassificationDigest: classificationDecisionDigest(recorded), Labels: cp.ClassificationLabels(recorded.Evidence.Classification), InputFingerprint: s.ApprovalFingerprint(fresh.Basis.ChangeID), EligibilityReadID: fresh.Basis.ReadID, DirectProtection: "not_applicable_pr", Actor: wireReceiptActor(acc.Actor), Author: wireReceiptActor(acc.Author), SubmitterID: acc.SubmitterID, AccountableHumans: []wireReceiptActor{}, AccountabilityRecorded: acc.Recorded, Requirements: []LandingRequirement{}}
	if a.Labels == nil {
		a.Labels = []string{}
	}
	for _, h := range acc.Humans {
		a.AccountableHumans = append(a.AccountableHumans, wireReceiptActor(h))
	}
	facts, _ := bundle.Snapshot.ApprovalFacts(fresh.Basis.ChangeID)
	receipts := bundle.Snapshot.ReceiptRecords(fresh.Basis.ChangeID)
	for _, result := range bundle.Evaluation.Requirements {
		r := result.Requirement
		out := LandingRequirement{Cause: result.Cause, RuleIndex: result.RuleIndex, Path: result.Path, Side: result.Side, Selector: r.Selector, Group: r.Group, Human: r.Human, Receipt: r.Receipt, Count: r.Count, PoolIDs: append([]string{}, result.PoolIDs...), EligibleIDs: append([]string{}, result.EligibleIDs...), ReliedOn: []LandingApprovalSource{}}
		for _, stance := range result.ReliedOn {
			key := string(stance.Source.Channel) + "/" + stance.Source.ID
			source, exists := facts.Sources[key]
			if !exists || source.Revision != fresh.Basis.Revision || !reflect.DeepEqual(source.Event.Stance, stance) {
				return LandingDecisionRecorded{}, fmt.Errorf("approval source not recorded")
			}
			ref := LandingApprovalSource{UserID: stance.UserID, Channel: stance.Source.Channel, SourceID: stance.Source.ID, ReadID: source.Event.Basis.ReadID}
			var accepted *ApprovalReceiptRecorded
			if stance.Receipt != nil {
				record, exists := receipts[stance.Source.ID]
				if !exists || !record.Accepted || !receiptSourceMatches(record, source) {
					return LandingDecisionRecorded{}, fmt.Errorf("receipt source not recorded")
				}
				if err := brief.ValidateReceipt(*stance.Receipt, fact); err != nil {
					return LandingDecisionRecorded{}, err
				}
				accepted = &record
				ref.ReceiptID, ref.IntentDigest, ref.BriefDigest = record.CommentID, stance.Receipt.IntentDigest, stance.Receipt.BriefDigest
			}
			if err := addLandingSource(a, source.Event, accepted); err != nil {
				return LandingDecisionRecorded{}, err
			}
			out.ReliedOn = append(out.ReliedOn, ref)
		}
		a.Requirements = append(a.Requirements, out)
	}
	landed := land.TestedSHA
	if land.Method == FastForward && len(land.PRs) > 0 {
		landed = land.PRs[len(land.PRs)-1].HeadSHA
	}
	e := LandingDecisionRecorded{BatchID: start.BatchID, AttemptID: start.AttemptID, TestedSHA: land.TestedSHA, LandedSHA: landed, Variant: "approvals", Approval: a}
	return e, e.Validate()
}

// ValidateLandingDecisions rederives every member under the caller's queue lock.
// It validates all evidence and admission budgets before any journal insert.
func (s State) ValidateLandingDecisions(policy cp.Policy, land Land, start LandingStarted, fresh []ApprovalEligibilityObserved, decisions []LandingDecisionRecorded) error {
	if policy.Approvals == nil || len(land.PRs) == 0 || len(fresh) != len(land.PRs) || len(decisions) != len(land.PRs) || land.BatchID != start.BatchID {
		return fmt.Errorf("incomplete landing decisions")
	}
	bindings := s.BatchRevisions(land.BatchID)
	if len(bindings) != len(land.PRs) {
		return fmt.Errorf("missing landing revision bindings")
	}
	next := s
	seen := map[ChangeID]bool{}
	for i, pr := range land.PRs {
		f, d := fresh[i], decisions[i]
		b := f.Basis
		current, ok := s.PRRevision(pr.PR)
		if !ok || current.ChangeID != b.ChangeID || current.Revision != b.Revision || current.HeadSHA != b.Head || pr.HeadSHA != b.Head || bindings[i].PR != pr.PR || bindings[i].Revision != b.Revision || bindings[i].HeadSHA != b.Head || seen[b.ChangeID] || b.At.After(start.At) || !s.currentApprovalInputBasis(b) {
			return fmt.Errorf("stale landing member %s", b.ChangeID)
		}
		seen[b.ChangeID] = true
		expected, err := s.PrepareLandingDecision(policy, f, start, land)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(expected, d) {
			return fmt.Errorf("mismatched landing decision for %s", b.ChangeID)
		}
		if err := next.CheckApprovalAdmission(f); err != nil {
			return remapAdmissionBudgetError(err, b.ChangeID)
		}
		next = Apply(next, f)
		if err := next.CheckLandingDecisionAdmission(d); err != nil {
			return err
		}
		next = Apply(next, d)
	}
	return nil
}

func (e LandingDecisionRecorded) memberID() ChangeID {
	if e.Approval != nil {
		return e.Approval.Basis.ChangeID
	}
	if e.Remedy != nil {
		return e.Remedy.ChangeID
	}
	return ""
}
func (e LandingDecisionRecorded) decisionKey() string {
	return e.BatchID + "/" + e.AttemptID + "/" + string(e.memberID())
}

func (s State) recordLandingDecision(e LandingDecisionRecorded) State {
	if e.Validate() != nil {
		return s
	}
	b := s.Batch(e.BatchID)
	if b == nil || b.Phase != Testing && b.Phase != Landing || b.Phase == Landing && b.LandingAttempt.ID != e.AttemptID {
		return s
	}
	key := e.decisionKey()
	if _, exists := s.landingDecisions[key]; exists {
		return s
	}
	next := make(map[string]LandingDecisionRecorded, len(s.landingDecisions)+1)
	for k, v := range s.landingDecisions {
		if v.BatchID == e.BatchID && v.AttemptID != e.AttemptID {
			continue
		}
		next[k] = v
	}
	next[key] = detached(e)
	s.landingDecisions = next
	return s
}

// retainLandingDecisions drops settled evidence without mutating prior states.
// Testing decisions are staged immediately before LandingStarted in one store
// transaction; settlement discards the batch, and a new start pins its attempt.
func (s *State) retainLandingDecisions() {
	if len(s.landingDecisions) == 0 {
		return
	}
	live := func(d LandingDecisionRecorded) bool {
		b := s.Batch(d.BatchID)
		return b != nil && (b.Phase == Testing && b.InvalidationReason == "" && b.ProvisionalFailure == "" || b.Phase == Landing && b.LandingAttempt.ID == d.AttemptID)
	}
	for _, d := range s.landingDecisions {
		if !live(d) {
			retained := make(map[string]LandingDecisionRecorded)
			for key, candidate := range s.landingDecisions {
				if live(candidate) {
					retained[key] = candidate
				}
			}
			if len(retained) == 0 {
				retained = nil
			}
			s.landingDecisions = retained
			return
		}
	}
}

// LandingDecisions returns detached records for unsettled attempts only.
// Historical evidence is available through store.Journal.Events.
func (s State) LandingDecisions() []LandingDecisionRecorded {
	keys := make([]string, 0, len(s.landingDecisions))
	for key := range s.landingDecisions {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	out := make([]LandingDecisionRecorded, 0, len(keys))
	for _, key := range keys {
		out = append(out, detached(s.landingDecisions[key]))
	}
	return out
}

func validateLandingDecisionSnapshot(records map[string]LandingDecisionRecorded) error {
	if len(records) > maxSnapshotEntries {
		return fmt.Errorf("landing decision count budget exceeded")
	}
	for key, e := range records {
		if key != e.decisionKey() || e.Validate() != nil {
			return fmt.Errorf("invalid landing decision snapshot")
		}
	}
	return nil
}

// CheckLandingDecisionAdmission includes all shared evidence and full snapshot
// bounds; budget errors identify the change whose admission was attempted.
func (s State) CheckLandingDecisionAdmission(e LandingDecisionRecorded) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if _, exists := s.landingDecisions[e.decisionKey()]; exists {
		return fmt.Errorf("duplicate landing decision for %s", e.memberID())
	}
	if err := checkSharedAdmissionBudget(Apply(s, e), e.memberID()); err != nil {
		return fmt.Errorf("landing decision admission rejected for %s: %w", e.memberID(), err)
	}
	return nil
}

// PrepareLandingRejection evaluates every member with the same recorded brief
// contract as successful decisions. Fresh is applied before Waiting's fingerprint
// and is journaled no later than Waiting.Basis.At by RejectLanding.
func (s State) PrepareLandingRejection(policy cp.Policy, land Land, fresh []ApprovalEligibilityObserved) (ApprovalGateRejected, error) {
	e := ApprovalGateRejected{BatchID: land.BatchID, TestedSHA: land.TestedSHA, PRs: slices.Clone(land.PRs), Waiting: []ApprovalWait{}}
	if policy.Approvals == nil || len(fresh) != len(land.PRs) {
		return e, fmt.Errorf("incomplete approval rejection inputs")
	}
	next := s
	for _, f := range fresh {
		if !s.currentApprovalInputBasis(f.Basis) {
			return e, fmt.Errorf("stale eligibility read")
		}
		if err := next.CheckApprovalAdmission(f); err != nil {
			return e, err
		}
		next = Apply(next, f)
	}
	for _, f := range fresh {
		fact, err := s.RecordedBrief(policy, f.Basis)
		if err != nil {
			return e, err
		}
		identity, err := brief.Identity(fact)
		if err != nil {
			return e, err
		}
		bundle, err := s.PrepareApprovalDecision(policy, identity, f)
		if err != nil {
			return e, err
		}
		if bundle.Evaluation.Allowed {
			continue
		}
		reasons := []string{"approval-required"}
		if !bundle.Evaluation.ReviewsKnown {
			reasons = append(reasons, "eligibility-unresolved")
		}
		if len(bundle.Evaluation.BlockingIDs) > 0 {
			reasons = append(reasons, "reviewer-requested-changes")
		}
		e.Waiting = append(e.Waiting, ApprovalWait{Basis: f.Basis, Fingerprint: next.ApprovalFingerprint(f.Basis.ChangeID), Reasons: reasons})
	}
	if len(e.Waiting) == 0 {
		return e, nil
	}
	if err := e.Validate(); err != nil {
		return e, err
	}
	rejected := Apply(next, e)
	if b := rejected.Batch(land.BatchID); b == nil || b.InvalidationReason != "approval-wait" {
		return e, fmt.Errorf("stale rejected composition")
	}
	if err := checkSharedAdmissionBudget(rejected, e.Waiting[0].Basis.ChangeID); err != nil {
		return e, err
	}
	return e, nil
}

var landingDecisionKey = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

func readLandingDecisionWire(raw json.RawMessage) (LandingDecisionRecorded, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return LandingDecisionRecorded{}, err
	}
	var check func(any) error
	check = func(v any) error {
		switch v := v.(type) {
		case map[string]any:
			for key, child := range v {
				if !landingDecisionKey.MatchString(key) {
					return fmt.Errorf("invalid landing decision key %q", key)
				}
				if err := check(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range v {
				if err := check(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := check(value); err != nil {
		return LandingDecisionRecorded{}, err
	}
	return readApprovalWire[LandingDecisionRecorded](raw)
}
