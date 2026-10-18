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
const Version = "openmerge/brief/v1"

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
	Classification          *cp.Classification
	Intent                  *Intent
	Obligations             []Obligation
	TestLinks               []string
	QueuePosition           int
	ApprovalProgress        string
	Timestamp, RenderedText string
}

// Build includes the same expanded obligations as the policy evaluator.
func Build(policy cp.Policy, in cp.Input, title, body string) Fact {
	c := in.Classification
	f := Fact{Classification: &c, Intent: &Intent{true, cp.NormalizeIntent(title), cp.NormalizeIntent(body), cp.IntentDigest(title, body)}}
	for _, r := range cp.Evaluate(policy, in).Requirements {
		code := "approval_required"
		switch {
		case !c.InventoryComplete:
			code = "unknown_inventory"
		case r.Unresolved && (r.Path != "" || r.Requirement.Selector == cp.CodeOwners || r.Origin == "policy owners"):
			code = "unknown_owners"
		case r.Unresolved:
			code = "unknown_eligibility"
		case r.Path != "" && len(r.EligibleIDs) == 0 && len(r.PoolIDs) > 0 && !slices.ContainsFunc(r.PoolIDs, func(id string) bool { return !slices.Contains(in.ExcludedIDs, id) }):
			code = "excluded_owners"
		case r.Met:
			code = "met"
		}
		f.Obligations = append(f.Obligations, Obligation{r.Origin, r.Path, r.Side, r.Requirement, r.PoolIDs, r.EligibleIDs, r.ApprovedIDs, r.Unresolved, r.Met, code, r.Reason})
	}
	return f
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
	// Deep-copy before normalizing: callers retain exact source evidence.
	raw, err := json.Marshal(f.Classification)
	if err != nil {
		return nil, err
	}
	var c cp.Classification
	if err = json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
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
		Origin, Path string
		Side         cp.OwnerSide
		Requirement  cp.Requirement
	}
	obligations := make([]requirement, 0, len(f.Obligations))
	for _, r := range f.Obligations {
		obligations = append(obligations, requirement{r.Origin, r.Path, r.Side, r.Requirement})
	}
	slices.SortFunc(obligations, func(a, b requirement) int {
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		return slices.Compare(x, y)
	})
	return json.Marshal(struct {
		Version        string
		Classification cp.Classification
		IntentDigest   string
		Requirements   []requirement
	}{Version, c, f.Intent.Digest, obligations})
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
