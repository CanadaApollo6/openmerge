// Package brief defines factual Stage A briefs. It performs no reads or rendering.
package brief

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
)

// Version pins the canonical serialization domain and schema.
const Version = "openmerge/brief/v2"

// Intent is explicitly the submitter's claim. Recorded=false means not recorded,
// distinct from a recorded empty title/body.
type Intent struct {
	Recorded            bool
	Title, Body, Digest string
}

// Obligation contains the independently expanded policy requirement and its
// current progress. Paths/sides are explicit; consumers never parse Origin.
type Obligation struct {
	Origin, Path                      string
	Cause                             cp.ObligationCause
	RuleIndex                         *int
	Side                              cp.OwnerSide
	Requirement                       cp.Requirement
	PoolIDs, EligibleIDs, ApprovedIDs []string
	Unresolved, Met                   bool
	Code, Explanation                 string
}

// Fact is the single JSON fact model for 7b and 9. Classification retains the
// original bounded file/hunk records verbatim; a brief never truncates them.
// Nil Classification/Intent means historical evidence was not recorded.
type Fact struct {
	Classification    *cp.Classification
	Intent            *Intent
	ApprovalsRequired bool
	Obligations       []Obligation
}

// Build expands recorded policy evidence and returns a brief with current
// progress. It derives its local receipt binding only from the recorded
// classification and claimed intent; callers own State-level identity assembly
// and must not pass fresh overlay identity observations through this function.
func Build(policy cp.Policy, in cp.Input, title, body string) (Fact, error) {
	f, err := BuildRecorded(policy, in, title, body)
	if err != nil {
		return Fact{}, err
	}
	identity, err := Identity(f)
	if err != nil {
		return Fact{}, err
	}
	in.Current = identity
	f.Obligations = obligations(cp.Evaluate(policy, in).Requirements)
	return f, nil
}

// BuildRecorded expands only recorded evidence. Fresh progress belongs in
// OverlayProgress; receipt preparation can hash this fact once.
func BuildRecorded(policy cp.Policy, in cp.Input, title, body string) (Fact, error) {
	if err := validateBuildInputs(policy, in); err != nil {
		return Fact{}, err
	}
	c := in.Classification
	f := Fact{Classification: &c, Intent: &Intent{true, cp.NormalizeIntent(title), cp.NormalizeIntent(body), cp.IntentDigest(title, body)}, ApprovalsRequired: policy.Approvals != nil}
	in.Current = receiptBasis(f)
	f.Obligations = obligations(cp.Evaluate(policy, in).Requirements)
	return f, nil
}

func validateBuildInputs(policy cp.Policy, in cp.Input) error {
	if !in.Classification.Inputs.Recorded() {
		return fmt.Errorf("brief classification evidence not recorded")
	}
	if policy.Approvals == nil {
		return nil
	}
	a := policy.Approvals
	if err := a.Default.Validate(); err != nil || a.Default.Selector == cp.None || a.Default.Selector == cp.CodeOwners {
		return fmt.Errorf("invalid approvals.default")
	}
	for i, rule := range a.Rules {
		if err := rule.Require.Validate(); err != nil {
			return fmt.Errorf("invalid rule requirement at %d", i)
		}
		if _, err := cp.MatchRule(rule.When, in.Classification, in.AuthorKind); err != nil {
			return fmt.Errorf("invalid rule match at %d: %w", i, err)
		}
	}
	c, current := in.Classification.Inputs, in.Current
	if current.ChangeID != c.ChangeID || current.Revision != c.Revision || current.Generation != c.Generation ||
		current.Base != c.Base || current.Head != c.Head || current.MergeBase != c.MergeBase || current.PolicyDigest != c.PolicyDigest {
		return fmt.Errorf("brief current basis is unresolved or stale")
	}
	return nil
}

func receiptBasis(f Fact) cp.ReceiptIdentity {
	i := f.Classification.Inputs
	return cp.ReceiptIdentity{ChangeID: i.ChangeID, Revision: i.Revision, Generation: i.Generation, Base: i.Base, Head: i.Head, MergeBase: i.MergeBase, IntentDigest: f.Intent.Digest, PolicyDigest: i.PolicyDigest}
}

func obligations(results []cp.RequirementResult) []Obligation {
	out := make([]Obligation, 0, len(results))
	for _, r := range results {
		code := "approval_required"
		switch {
		case r.Met:
			code = "met"
		case r.UnresolvedCause != "":
			code = string(r.UnresolvedCause)
		}
		out = append(out, Obligation{Origin: r.Origin, Cause: r.Cause, RuleIndex: r.RuleIndex, Path: r.Path, Side: r.Side, Requirement: r.Requirement, PoolIDs: r.PoolIDs, EligibleIDs: r.EligibleIDs, ApprovedIDs: r.ApprovedIDs, Unresolved: r.Unresolved, Met: r.Met, Code: code, Explanation: r.Reason})
	}
	return out
}

// OverlayProgress joins fresh evaluation progress onto recorded requirements.
// It never replaces classification, intent or requirement identity. Unmatched
// rows become unresolved, so missing fresh evidence cannot preserve old success.
func OverlayProgress(recorded Fact, fresh cp.Evaluation) Fact {
	out := recorded
	out.Obligations = slices.Clone(recorded.Obligations)
	progressRows := obligations(fresh.Requirements)
	for i, row := range out.Obligations {
		progress := Obligation{Unresolved: true, Code: "unknown_eligibility", Explanation: "fresh progress unavailable"}
		for _, candidate := range progressRows {
			if sameRequirement(row, candidate) {
				progress = candidate
				break
			}
		}
		row.PoolIDs = slices.Clone(progress.PoolIDs)
		row.EligibleIDs = slices.Clone(progress.EligibleIDs)
		row.ApprovedIDs = slices.Clone(progress.ApprovedIDs)
		row.Unresolved, row.Met = progress.Unresolved, progress.Met
		row.Code, row.Explanation = progress.Code, progress.Explanation
		out.Obligations[i] = row
	}
	return out
}

func sameRequirement(a, b Obligation) bool {
	return a.Cause == b.Cause && a.Path == b.Path && a.Side == b.Side && a.Requirement == b.Requirement &&
		(a.RuleIndex == nil && b.RuleIndex == nil || a.RuleIndex != nil && b.RuleIndex != nil && *a.RuleIndex == *b.RuleIndex)
}

// sortedSet gives nil and empty the same canonical [] spelling.
func sortedSet[T ~string](v []T) []T {
	out := append([]T{}, v...)
	slices.Sort(out)
	return slices.Compact(out)
}
func reasons(v []cp.LabelReasons) []cp.LabelReasons {
	out := make([]cp.LabelReasons, len(v))
	for i, r := range v {
		out[i] = cp.LabelReasons{Label: r.Label, Reasons: sortedSet(r.Reasons)}
	}
	slices.SortFunc(out, func(a, b cp.LabelReasons) int {
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		return slices.Compare(x, y)
	})
	return out
}
func canonicalPool(p cp.IdentitySet) cp.IdentitySet {
	p.IDs = sortedSet(p.IDs)
	p.Fresh = false
	p.Reason = ""
	return p
}

// Canonical returns compact UTF-8 Go encoding/json with fixed struct order and
// sorted map keys. Domain, null/empty rules and ordering are specified in README.
// No mutable approval progress or provenance-only field enters these bytes.
func Canonical(f Fact) ([]byte, error) {
	if f.Classification == nil || !f.Classification.Inputs.Recorded() || f.Intent == nil || !f.Intent.Recorded || f.Intent.Digest != cp.IntentDigest(f.Intent.Title, f.Intent.Body) {
		return nil, fmt.Errorf("brief evidence not recorded or intent inconsistent")
	}
	// Copy only the slices whose elements are normalized below. Maps remain
	// read-only, so callers retain their exact source evidence without a
	// marshal/unmarshal round trip.
	c := *f.Classification
	c.Files = slices.Clone(c.Files)
	c.CodeOwners = slices.Clone(c.CodeOwners)
	c.PolicyOwners = slices.Clone(c.PolicyOwners)
	c.Inputs.ConfigBlob = ""
	c.Inputs.GoverningCommit = ""
	c.Paths = sortedSet(c.Paths)
	c.Reasons = reasons(c.Reasons)
	if c.Inputs.AttributeBlobs == nil {
		c.Inputs.AttributeBlobs = map[string]string{}
	}
	if c.Facts == nil {
		c.Facts = map[string]cp.Truth{}
	}
	for i := range c.Files {
		file := &c.Files[i]
		file.Reasons = reasons(file.Reasons)
		if file.Facts == nil {
			file.Facts = map[string]cp.Truth{}
		}
		if file.Hunks.Hunks == nil {
			file.Hunks.Hunks = []cp.Hunk{}
		}
		if file.Hunks.Omitted == nil {
			file.Hunks.Omitted = []cp.HunkRange{}
		}
	}
	if c.Files == nil {
		c.Files = []cp.FileEvidence{}
	}
	slices.SortFunc(c.Files, func(a, b cp.FileEvidence) int {
		if n := compare(a.NewPath, b.NewPath); n != 0 {
			return n
		}
		return compare(a.OldPath, b.OldPath)
	})
	for i := range c.CodeOwners {
		c.CodeOwners[i].Eligible = canonicalPool(c.CodeOwners[i].Eligible)
	}
	for i := range c.PolicyOwners {
		c.PolicyOwners[i].Eligible = canonicalPool(c.PolicyOwners[i].Eligible)
	}
	if c.CodeOwners == nil {
		c.CodeOwners = []cp.CodeOwner{}
	}
	if c.PolicyOwners == nil {
		c.PolicyOwners = []cp.PolicyOwner{}
	}
	slices.SortFunc(c.CodeOwners, func(a, b cp.CodeOwner) int {
		if n := compare(a.Path, b.Path); n != 0 {
			return n
		}
		return compare(string(a.Side), string(b.Side))
	})
	slices.SortFunc(c.PolicyOwners, func(a, b cp.PolicyOwner) int { return compare(a.Path, b.Path) })
	type requirement struct {
		Cause       cp.ObligationCause
		RuleIndex   *int
		Path        string
		Side        cp.OwnerSide
		Requirement cp.Requirement
	}
	obligations := make([]requirement, 0, len(f.Obligations))
	for _, r := range f.Obligations {
		obligations = append(obligations, requirement{r.Cause, r.RuleIndex, r.Path, r.Side, r.Requirement})
	}
	slices.SortFunc(obligations, func(a, b requirement) int {
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		return slices.Compare(x, y)
	})
	obligations = slices.CompactFunc(obligations, func(a, b requirement) bool {
		return a.Cause == b.Cause && a.Path == b.Path && a.Side == b.Side && a.Requirement == b.Requirement &&
			(a.RuleIndex == nil && b.RuleIndex == nil || a.RuleIndex != nil && b.RuleIndex != nil && *a.RuleIndex == *b.RuleIndex)
	})
	return json.Marshal(struct {
		Version           string
		Classification    cp.Classification
		IntentDigest      string
		ApprovalsRequired bool
		Requirements      []requirement
	}{Version, c, f.Intent.Digest, f.ApprovalsRequired, obligations})
}
func compare(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// Digest is SHA-256 of Canonical, in lowercase hexadecimal.
func Digest(f Fact) (string, error) {
	raw, err := Canonical(f)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Identity supplies the complete binding for acceptance and landing checks.
func Identity(f Fact) (cp.ReceiptIdentity, error) {
	digest, err := Digest(f)
	if err != nil {
		return cp.ReceiptIdentity{}, err
	}
	i := f.Classification.Inputs
	return cp.ReceiptIdentity{ChangeID: i.ChangeID, Revision: i.Revision, Generation: i.Generation, Base: i.Base, Head: i.Head, MergeBase: i.MergeBase, IntentDigest: f.Intent.Digest, PolicyDigest: i.PolicyDigest, BriefDigest: digest}, nil
}

// ValidateReceipt compares supplied evidence exactly, never substituting a digest.
// Call at acceptance and again with current evidence immediately before landing.
func ValidateReceipt(supplied cp.ReceiptIdentity, current Fact) error {
	identity, err := Identity(current)
	if err != nil {
		return err
	}
	if !supplied.Recorded() || supplied != identity {
		return fmt.Errorf("receipt_binding_mismatch")
	}
	return nil
}
