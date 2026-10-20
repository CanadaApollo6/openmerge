package authority

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Visibility records what a read established. Absence requires a complete,
// authoritative inventory; permission errors cannot establish absence.
type Visibility string

// Visibility states distinguish positive reads from absence and uncertainty.
const (
	Visible     Visibility = "visible"
	Absent      Visibility = "absent"
	Hidden      Visibility = "hidden"
	ReadFailure Visibility = "read_failure"
)

// Validate rejects unknown visibility states.
func (v Visibility) Validate() error {
	switch v {
	case Visible, Absent, Hidden, ReadFailure:
		return nil
	}
	return fmt.Errorf("invalid authority visibility %q", v)
}

// PolicyBinding is the identity shared by draft verification, activation and
// runtime observations. Membership and governing commit are deliberately absent:
// membership is mutable, and the landed commit is bound by the activation pin.
type PolicyBinding struct {
	InstallationID  int64         `json:"installation_id"`
	RepositoryID    int64         `json:"repository_id"`
	Repository      string        `json:"repository"`
	QueueKey        string        `json:"queue_key"`
	EffectiveDigest string        `json:"effective_digest"`
	CoverageDigest  string        `json:"coverage_digest"`
	DefaultBranch   string        `json:"default_branch"`
	MergeMethod     string        `json:"merge_method"`
	DeclaredActor   Actor         `json:"declared_actor"`
	ResolvedActor   ResolvedActor `json:"resolved_actor"`
}

// Validate checks immutable policy identities and actor-kind agreement.
func (b PolicyBinding) Validate() error {
	if b.InstallationID <= 0 || b.RepositoryID <= 0 || strings.Count(b.Repository, "/") != 1 || b.QueueKey == "" || b.DefaultBranch == "" || !IsDigest(b.EffectiveDigest) || !IsDigest(b.CoverageDigest) {
		return fmt.Errorf("invalid authority policy binding")
	}
	switch b.MergeMethod {
	case "squash", "merge", "rebase", "fast-forward":
	default:
		return fmt.Errorf("invalid authority merge method")
	}
	if err := b.DeclaredActor.Validate(); err != nil {
		return err
	}
	return (ActorBinding{Declared: b.DeclaredActor, Resolved: b.ResolvedActor}).Validate()
}

// Parameter is a normalized rule/condition parameter, never a raw API body.
// Name is a relative JSON pointer into parameters/conditions (escaped segments,
// numeric array indices); scalar arrays use Values in source order. Each scalar
// uses JSON spelling: strings are quoted, numbers/booleans/null are bare. A null
// leaf has Values ["null"], an empty array has Values [], and an absent leaf has
// no Parameter. An empty object has Values ["{}"], distinct from the quoted
// string "{}". Nested objects retain their associations through Name.
type Parameter struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

// Rule contains a normalized rule kind and its explicit parameters.
type Rule struct {
	Kind       string      `json:"kind"`
	Parameters []Parameter `json:"parameters"`
}

// BypassActor records observed inventory, independently of the declared Actor
// union. ID is required for app/team/user/repository_role/enterprise_role and
// absent for class-wide organization_admin/deploy_key/enterprise_owner. Unknown forge
// types use Kind "unknown" and retain their exact type in RawKind; they must be
// interpreted as unauthorized, never discarded. Role IDs come from the adapter.
type BypassActor struct {
	Kind    string `json:"kind"`
	ID      string `json:"id,omitempty"`
	Mode    string `json:"mode"`
	RawKind string `json:"raw_kind,omitempty"`
}

// Validate checks the principal kind, stable identity and bypass mode.
func (a BypassActor) Validate() error {
	switch a.Kind {
	case "app", "team", "repository_role", "user", "enterprise_role":
		if a.ID == "" {
			return fmt.Errorf("missing bypass actor identity")
		}
	case "organization_admin", "deploy_key", "enterprise_owner":
	case "unknown":
		if a.RawKind == "" {
			return fmt.Errorf("missing unknown bypass actor kind")
		}
	default:
		return fmt.Errorf("invalid bypass actor kind")
	}
	if a.Kind != "unknown" && a.RawKind != "" {
		return fmt.Errorf("raw kind on known bypass actor")
	}
	if a.Mode != "always" && a.Mode != "pull_request" && a.Mode != "exempt" {
		return fmt.Errorf("invalid bypass mode")
	}
	return nil
}

// Protection retains each independently visible part of an applicable ruleset
// or classic protection. A hidden list is never a visible empty list.
// Includes/Excludes are ref_name conditions; Conditions keeps every other
// condition leaf, including repository_name, repository_id and
// repository_property selectors. Target is empty only when targets are unseen;
// classic branch protection uses "branch".
type Protection struct {
	Source            string        `json:"source"`
	ID                string        `json:"id"`
	Revision          string        `json:"revision"`
	Enforcement       string        `json:"enforcement"`
	Target            string        `json:"target"`
	TargetsVisibility Visibility    `json:"targets_visibility"`
	Includes          []string      `json:"includes"`
	Excludes          []string      `json:"excludes"`
	Conditions        []Parameter   `json:"conditions"`
	RulesVisibility   Visibility    `json:"rules_visibility"`
	Rules             []Rule        `json:"rules"`
	BypassVisibility  Visibility    `json:"bypass_visibility"`
	BypassActors      []BypassActor `json:"bypass_actors"`
}

// Coverage identifies which observed protections cover a required ref.
type Coverage struct {
	Ref           string     `json:"ref"`
	Visibility    Visibility `json:"visibility"`
	ProtectionIDs []string   `json:"protection_ids"`
}

// Finding identifies one violated element, allowing PR 4 to clear structural
// findings independently of a previously visible unauthorized bypass actor.
type Finding struct {
	Element      string       `json:"element"`
	Kind         string       `json:"kind"`
	ProtectionID string       `json:"protection_id"`
	Ref          string       `json:"ref"`
	Actor        *BypassActor `json:"actor,omitempty"`
}

// Observation is a read result plus typed interpretation. The reader and pure
// interpreter remain separate; neither credentials nor arbitrary response JSON
// have a place in this contract. Failed partial reads retain successful elements.
type Observation struct {
	Inventory   Visibility   `json:"inventory"`
	Protections []Protection `json:"protections"`
	Coverage    []Coverage   `json:"coverage"`
	Findings    []Finding    `json:"findings"`
}

const maxEvidenceItems = 1000

// Validate rejects ambiguous visibility and disconnected evidence.
func (o Observation) Validate() error {
	if err := o.Inventory.Validate(); err != nil {
		return err
	}
	if o.Inventory == Absent || o.Inventory == Hidden {
		return fmt.Errorf("inventory must be visible or read_failure")
	}
	if o.Protections == nil || o.Coverage == nil || o.Findings == nil || len(o.Protections) > maxEvidenceItems || len(o.Coverage) > maxEvidenceItems || len(o.Findings) > maxEvidenceItems {
		return fmt.Errorf("invalid authority evidence bounds")
	}
	ids := map[string]bool{}
	protections := map[string]Protection{}
	for _, p := range o.Protections {
		key := p.Source + "/" + p.ID
		if p.ID == "" || ids[key] {
			return fmt.Errorf("invalid protection identity")
		}
		ids[key] = true
		protections[key] = p
		switch p.Source {
		case "repository_ruleset", "inherited_ruleset", "classic_protection":
		default:
			return fmt.Errorf("invalid protection source")
		}
		switch p.Enforcement {
		case "active", "evaluate", "disabled":
		default:
			return fmt.Errorf("invalid enforcement")
		}
		switch p.Target {
		case "branch", "tag", "push", "repository":
		case "":
			if p.TargetsVisibility == Visible {
				return fmt.Errorf("missing visible protection target")
			}
		default:
			return fmt.Errorf("invalid protection target")
		}
		for _, v := range []Visibility{p.TargetsVisibility, p.RulesVisibility, p.BypassVisibility} {
			if err := v.Validate(); err != nil {
				return err
			}
			if v == Absent {
				return fmt.Errorf("present protection element cannot be absent")
			}
		}
		if p.Includes == nil || p.Excludes == nil || p.Conditions == nil || p.Rules == nil || p.BypassActors == nil {
			return fmt.Errorf("missing protection collections")
		}
		if len(p.Includes) > maxEvidenceItems || len(p.Excludes) > maxEvidenceItems || len(p.Rules) > maxEvidenceItems || len(p.BypassActors) > maxEvidenceItems {
			return fmt.Errorf("invalid protection bounds")
		}
		if p.TargetsVisibility != Visible && (len(p.Includes)+len(p.Excludes)+len(p.Conditions) > 0) || p.RulesVisibility != Visible && len(p.Rules) > 0 || p.BypassVisibility != Visible && len(p.BypassActors) > 0 {
			return fmt.Errorf("unseen protection data")
		}
		if err := parametersValid(p.Conditions); err != nil {
			return err
		}
		for _, r := range p.Rules {
			if r.Kind == "" {
				return fmt.Errorf("missing rule kind")
			}
			if err := parametersValid(r.Parameters); err != nil {
				return err
			}
		}
		seen := map[BypassActor]bool{}
		for _, a := range p.BypassActors {
			if err := a.Validate(); err != nil {
				return err
			}
			if seen[a] {
				return fmt.Errorf("duplicate bypass actor")
			}
			seen[a] = true
		}
	}
	refs := map[string]bool{}
	for _, c := range o.Coverage {
		if c.Ref == "" || refs[c.Ref] || c.ProtectionIDs == nil || len(c.ProtectionIDs) > maxEvidenceItems {
			return fmt.Errorf("invalid coverage")
		}
		refs[c.Ref] = true
		if err := c.Visibility.Validate(); err != nil {
			return err
		}
		covered := map[string]bool{}
		for _, id := range c.ProtectionIDs {
			if !ids[id] || covered[id] {
				return fmt.Errorf("unknown or duplicate coverage protection")
			}
			covered[id] = true
		}
		if c.Visibility == Visible && len(c.ProtectionIDs) == 0 {
			return fmt.Errorf("visible coverage requires a protection")
		}
		if c.Visibility != Visible && len(c.ProtectionIDs) > 0 {
			return fmt.Errorf("unseen coverage data")
		}
	}
	elements := map[string]bool{}
	for _, f := range o.Findings {
		if f.Element == "" || elements[f.Element] {
			return fmt.Errorf("invalid finding element")
		}
		elements[f.Element] = true
		switch f.Kind {
		case "missing_protection", "target_mismatch", "missing_rule", "missing_bypass", "unexpected_bypass", "unsupported_rule":
		default:
			return fmt.Errorf("invalid finding kind")
		}
		if f.Actor != nil {
			if err := f.Actor.Validate(); err != nil {
				return err
			}
		}
		if (f.Kind == "missing_bypass" || f.Kind == "unexpected_bypass") != (f.Actor != nil) {
			return fmt.Errorf("finding actor mismatch")
		}
		if f.Kind == "missing_protection" {
			proven := false
			for _, c := range o.Coverage {
				if c.Ref == f.Ref && c.Visibility == Absent {
					proven = true
				}
			}
			if !proven || f.ProtectionID != "" {
				return fmt.Errorf("missing protection finding lacks absence evidence")
			}
			continue
		}
		p, ok := protections[f.ProtectionID]
		if !ok {
			return fmt.Errorf("finding references unknown protection")
		}
		switch f.Kind {
		case "target_mismatch":
			if p.TargetsVisibility != Visible {
				return fmt.Errorf("unseen target finding")
			}
		case "missing_rule", "unsupported_rule":
			if p.RulesVisibility != Visible {
				return fmt.Errorf("unseen rule finding")
			}
		case "missing_bypass", "unexpected_bypass":
			if p.BypassVisibility != Visible {
				return fmt.Errorf("unseen bypass finding")
			}
			present := slices.Contains(p.BypassActors, *f.Actor)
			if present != (f.Kind == "unexpected_bypass") {
				return fmt.Errorf("bypass finding contradicts visible actors")
			}
		}
	}
	return nil
}

func parametersValid(ps []Parameter) error {
	if ps == nil || len(ps) > maxEvidenceItems {
		return fmt.Errorf("invalid parameters")
	}
	seen := map[string]bool{}
	for _, p := range ps {
		if p.Name == "" || seen[p.Name] || p.Values == nil || len(p.Values) > maxEvidenceItems {
			return fmt.Errorf("invalid parameter")
		}
		seen[p.Name] = true
	}
	return nil
}

// SeeingComplete establishes only visibility, not a passing interpretation.
func (o Observation) SeeingComplete() bool {
	if o.Validate() != nil || o.Inventory != Visible {
		return false
	}
	for _, p := range o.Protections {
		if p.TargetsVisibility != Visible || p.RulesVisibility != Visible || p.BypassVisibility != Visible {
			return false
		}
	}
	for _, c := range o.Coverage {
		if c.Visibility != Visible && c.Visibility != Absent {
			return false
		}
	}
	return len(o.Coverage) > 0
}

// Fingerprint freezes PR 4's complete protection inventory identity. Acquisition
// times and findings are excluded. Sort unordered inventories without mutating
// the reader's result; preserve parameter values whose order may matter.
func (o Observation) Fingerprint() (string, error) {
	if !o.SeeingComplete() {
		return "", fmt.Errorf("fingerprint requires complete seeing evidence")
	}
	o = CloneObservation(o)
	o.Findings = []Finding{}
	slices.SortFunc(o.Protections, func(a, b Protection) int { return strings.Compare(a.Source+"/"+a.ID, b.Source+"/"+b.ID) })
	for i := range o.Protections {
		p := &o.Protections[i]
		slices.Sort(p.Includes)
		slices.Sort(p.Excludes)
		slices.SortFunc(p.Conditions, func(a, b Parameter) int { return strings.Compare(a.Name, b.Name) })
		for j := range p.Rules {
			slices.SortFunc(p.Rules[j].Parameters, func(a, b Parameter) int { return strings.Compare(a.Name, b.Name) })
		}
		slices.SortFunc(p.Rules, func(a, b Rule) int {
			x, _ := json.Marshal(a)
			y, _ := json.Marshal(b)
			return strings.Compare(string(x), string(y))
		})
		slices.SortFunc(p.BypassActors, func(a, b BypassActor) int {
			return cmp.Or(strings.Compare(a.Kind, b.Kind), strings.Compare(a.ID, b.ID), strings.Compare(a.Mode, b.Mode), strings.Compare(a.RawKind, b.RawKind))
		})
	}
	slices.SortFunc(o.Coverage, func(a, b Coverage) int { return strings.Compare(a.Ref, b.Ref) })
	for i := range o.Coverage {
		slices.Sort(o.Coverage[i].ProtectionIDs)
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

// CloneObservation returns an independent copy of all nested evidence.
func CloneObservation(o Observation) Observation {
	// All fields are explicit JSON values, so this copy also preserves empty
	// versus absent collections. Callers validate at external boundaries.
	raw, _ := json.Marshal(o)
	var result Observation
	_ = json.Unmarshal(raw, &result)
	return result
}

// IsDigest recognizes a canonical lowercase SHA-256 digest.
func IsDigest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && strings.ToLower(s) == s
}

// ProposedVerification binds a proposed seeing read to its durable source event.
// PR 4 must load EventID and check this exact binding against the journal before
// adoption; a wire payload alone cannot authenticate another event's identity.
type ProposedVerification struct {
	EventID               int64         `json:"event_id"`
	Scope                 string        `json:"scope"`
	Binding               PolicyBinding `json:"binding"`
	ProtectionFingerprint string        `json:"protection_fingerprint"`
	SeeingReadAt          time.Time     `json:"seeing_read_at"`
}

// Activation records adoption after a fresh complete read matches the proposed
// policy and protection fingerprint. The enclosing pin supplies the landed commit.
type Activation struct {
	Proposed              ProposedVerification `json:"proposed"`
	ProtectionFingerprint string               `json:"protection_fingerprint"`
	SeeingReadAt          time.Time            `json:"seeing_read_at"`
	Evidence              Observation          `json:"evidence"`
}

// Validate checks scope, policy identity, time ordering and both fingerprints.
func (a Activation) Validate() error {
	if err := a.Proposed.Binding.Validate(); err != nil {
		return err
	}
	fingerprint, err := a.Evidence.Fingerprint()
	if err != nil {
		return err
	}
	if a.Proposed.EventID <= 0 || a.Proposed.Scope != "proposed" || a.Proposed.SeeingReadAt.IsZero() || a.SeeingReadAt.IsZero() || a.SeeingReadAt.Before(a.Proposed.SeeingReadAt) || fingerprint != a.ProtectionFingerprint || fingerprint != a.Proposed.ProtectionFingerprint {
		return fmt.Errorf("invalid authority activation binding")
	}
	return nil
}
