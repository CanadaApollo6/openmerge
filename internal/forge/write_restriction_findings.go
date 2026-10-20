package forge

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/authority"
)

// UnknownBypassFindings retains the mandatory negative at the acquisition
// boundary. Unknown forge principal types can never become a passing read.
func UnknownBypassFindings(o authority.Observation) authority.Observation {
	o = authority.CloneObservation(o)
	for _, p := range o.Protections {
		for _, a := range p.BypassActors {
			if a.Kind != "unknown" {
				continue
			}
			id := p.Source + "/" + p.ID
			if !slices.ContainsFunc(o.Findings, func(f authority.Finding) bool {
				return f.ProtectionID == id && f.Kind == "unexpected_bypass" && f.Actor != nil && *f.Actor == a
			}) {
				o.Findings = append(o.Findings, bypassFinding(id, "unexpected_bypass", a))
			}
		}
	}
	return o
}

func bypassFinding(id, kind string, a authority.BypassActor) authority.Finding {
	raw, _ := json.Marshal(a)
	return authority.Finding{Element: fmt.Sprintf("%s/bypass/%s/%x", id, kind, sha256.Sum256(raw)), Kind: kind, ProtectionID: id, Actor: &a}
}

// InterpretWriteRestriction checks compiled future coverage as well as concrete
// refs. It consumes typed inventory only and performs no forge calls.
func InterpretWriteRestriction(in WriteRestrictionInput, o authority.Observation) (authority.Observation, error) {
	if err := ValidateWriteRestrictionInput(in); err != nil {
		return o, err
	}
	o = UnknownBypassFindings(o)
	if err := o.Validate(); err != nil {
		return o, err
	}
	compiled, err := authority.CompileRefCoverage(in.Binding.DefaultBranch, in.BaseSelectors)
	if err != nil {
		return o, err
	}
	required := slices.Clone(compiled.Includes)
	for _, name := range []string{"fence", "queue", "green", "verify"} {
		required = append(required, "refs/heads/openmerge/"+name+"/*", "refs/heads/openmerge/"+name+"/**/*")
	}
	required = append(required, in.ExpectedRefs...)
	slices.Sort(required)
	required = slices.Compact(required)
	o.Coverage = []authority.Coverage{}
	declared, err := in.Binding.ResolvedActor.BypassIdentity()
	if err != nil {
		return o, err
	}
	for _, ref := range required {
		internal := strings.HasPrefix(ref, "refs/heads/openmerge/")
		coverage := authority.Coverage{Ref: ref, Visibility: authority.Absent, ProtectionIDs: []string{}}
		uncertain := o.Inventory != authority.Visible
		for _, p := range o.Protections {
			if p.Source == "classic_protection" || p.Enforcement != "active" {
				continue
			}
			if p.TargetsVisibility != authority.Visible || p.RulesVisibility != authority.Visible {
				uncertain = true
				continue
			}
			if p.Target != "branch" {
				continue
			}
			matches := slices.Contains(p.Includes, ref) || slices.Contains(p.Includes, "~ALL") || ref == "refs/heads/"+in.Binding.DefaultBranch && slices.Contains(p.Includes, "~DEFAULT_BRANCH")
			if !strings.ContainsAny(ref, "*?") {
				matches, err = (authority.RefCoverage{Includes: p.Includes, Excludes: p.Excludes}).ContainsRef(ref, "refs/heads/"+in.Binding.DefaultBranch)
				if err != nil {
					uncertain = true
					continue
				}
			} else if len(p.Excludes) > 0 && (internal || !slices.Equal(p.Excludes, compiled.Excludes)) {
				matches = false
			}
			if !matches {
				continue
			}
			// A content-only ruleset cannot establish exclusive write authority.
			if !slices.ContainsFunc(p.Rules, func(r authority.Rule) bool { return r.Kind == "update" }) {
				continue
			}
			id := p.Source + "/" + p.ID
			coverage.ProtectionIDs = append(coverage.ProtectionIDs, id)
			for _, kind := range []string{"creation", "update", "deletion", "non_fast_forward"} {
				valid := slices.ContainsFunc(p.Rules, func(r authority.Rule) bool {
					if r.Kind != kind {
						return false
					}
					if kind != "update" {
						return len(r.Parameters) == 0
					}
					return len(r.Parameters) == 0 || len(r.Parameters) == 1 && r.Parameters[0].Name == "update_allows_fetch_and_merge" && slices.Equal(r.Parameters[0].Values, []string{"false"})
				})
				if !valid {
					o.Findings = appendUniqueFinding(o.Findings, authority.Finding{Element: id + "/rule/" + kind, Kind: "missing_rule", ProtectionID: id})
				}
			}
			if p.BypassVisibility != authority.Visible {
				continue
			}
			allowed := []authority.BypassActor{{Kind: "app", ID: in.AppID, Mode: "always"}}
			if !internal {
				allowed = append(allowed, declared)
			}
			for _, a := range allowed {
				if !slices.Contains(p.BypassActors, a) {
					o.Findings = appendUniqueFinding(o.Findings, bypassFinding(id, "missing_bypass", a))
				}
			}
			for _, a := range p.BypassActors {
				if !slices.Contains(allowed, a) {
					o.Findings = appendUniqueFinding(o.Findings, bypassFinding(id, "unexpected_bypass", a))
				}
			}
		}
		if len(coverage.ProtectionIDs) > 0 {
			coverage.Visibility = authority.Visible
		} else if uncertain {
			coverage.Visibility = authority.Hidden
		} else {
			o.Findings = appendUniqueFinding(o.Findings, authority.Finding{Element: "coverage/" + ref, Kind: "missing_protection", Ref: ref})
		}
		o.Coverage = append(o.Coverage, coverage)
	}
	return o, o.Validate()
}

func appendUniqueFinding(fs []authority.Finding, f authority.Finding) []authority.Finding {
	if !slices.ContainsFunc(fs, func(old authority.Finding) bool { return old.Element == f.Element }) {
		return append(fs, f)
	}
	return fs
}
