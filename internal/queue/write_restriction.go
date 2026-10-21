package queue

import (
	"fmt"
	"slices"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
)

// WriteRestrictionStatus is an owned projection. A never-seen source starts
// unverified. Operator verification time and runtime observation time differ.
type WriteRestrictionStatus struct {
	State                          string
	Findings                       []authority.Finding
	LastObservedAt, LastVerifiedAt time.Time
	Revision                       uint64
}

// WriteRestrictionStatus returns findings and ages without sharing evidence.
func (s State) WriteRestrictionStatus() WriteRestrictionStatus {
	result := WriteRestrictionStatus{State: "unverified", Revision: s.RulesSourceRevision(WriteRestriction)}
	if s.authority == nil || s.authority.Pin == nil || s.authority.Pin.OpenMerge == nil {
		return result
	}
	p := cloneAuthority(s.authority)
	result.LastObservedAt, result.LastVerifiedAt = p.LastObservedAt, p.LastVerifiedAt
	for _, f := range p.Findings {
		result.Findings = append(result.Findings, f)
	}
	slices.SortFunc(result.Findings, func(a, b authority.Finding) int {
		if a.Element < b.Element {
			return -1
		}
		if a.Element > b.Element {
			return 1
		}
		return 0
	})
	if len(result.Findings) > 0 {
		result.State = "violated"
	} else if p.Complete && p.ReadError == "" {
		result.State = "verified"
	}
	return result
}

func (s State) foldWriteRestriction(basis uint64, at time.Time, evidence authority.Observation, operator bool, readError string) State {
	// A failed read cannot refresh ages or clear old findings. Definite partial
	// negatives still invalidate any earlier pass, regardless of their basis.
	failed := evidence.Inventory == authority.ReadFailure
	freshRead := basis == s.RulesSourceRevision(WriteRestriction) && !at.Before(s.authority.LastObservedAt)
	if (!freshRead || failed && (readError == "" || readError == s.authority.ReadError)) && len(evidence.Findings) == 0 {
		return s
	}
	s = cloneState(s)
	p := s.authority
	if p.Findings == nil {
		p.Findings = map[string]authority.Finding{}
	}
	current := s.rulesSources[WriteRestriction]
	hadProblems := len(current.problems) > 0
	fresh := freshRead
	if fresh && failed && readError != "" {
		p.ReadError = readError
	}
	if fresh && !failed {
		p.ReadError = ""
		for element, f := range p.Findings {
			if findingSeen(f, evidence) {
				delete(p.Findings, element)
			}
		}
	}
	for _, f := range evidence.Findings {
		p.Findings[f.Element] = *cloneAuthority(&f)
	}
	if !failed && !at.Before(p.LastObservedAt) {
		p.LastObservedAt = at
	}
	if fresh && !failed {
		p.Complete = evidence.SeeingComplete()
		if operator {
			p.LastVerifiedAt = at
		}
	}
	if len(evidence.Findings) > 0 {
		p.Complete = false
	}
	current.revision++
	current.problems = nil
	if p.ReadError != "" {
		current.problems = append(current.problems, p.ReadError)
	}
	status := s.WriteRestrictionStatus()
	for _, f := range status.Findings {
		current.problems = append(current.problems, fmt.Sprintf("%s: %s", f.Element, f.Kind))
	}
	s.rulesSources[WriteRestriction] = current
	// Revisions only affect planner keys when the visible block changes or a
	// definite negative invalidates an earlier in-flight pass.
	if len(evidence.Findings) > 0 || len(current.problems) == 0 && hadProblems || len(current.problems) > 0 && !hadProblems {
		s.revision++
		s.rulesPauseRevision = s.revision
	}
	return s
}

// Each violated element needs its own positive visibility. Disappearance from
// a complete inventory proves removal, but a missing item in a failed read does not.
func findingSeen(f authority.Finding, o authority.Observation) bool {
	if f.Kind == "missing_protection" {
		return slices.ContainsFunc(o.Coverage, func(c authority.Coverage) bool {
			return c.Ref == f.Ref && (c.Visibility == authority.Visible || c.Visibility == authority.Absent)
		})
	}
	for _, p := range o.Protections {
		if p.Source+"/"+p.ID != f.ProtectionID {
			continue
		}
		switch f.Kind {
		case "target_mismatch":
			return p.TargetsVisibility == authority.Visible
		case "missing_rule", "unsupported_rule":
			return p.RulesVisibility == authority.Visible
		case "missing_bypass", "unexpected_bypass":
			return p.BypassVisibility == authority.Visible
		}
	}
	return o.Inventory == authority.Visible
}

// Hypothetical only: never encode a legacy clear for the internal source.
func (s State) withoutAuthorityBlock() State {
	s = cloneState(s)
	current := s.rulesSources[WriteRestriction]
	current.problems = nil
	s.rulesSources[WriteRestriction] = current
	return s
}
