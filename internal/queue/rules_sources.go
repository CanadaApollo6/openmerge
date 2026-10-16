package queue

import (
	"fmt"
	"slices"
	"strings"
)

// RulesSource identifies an independently observed landing requirement.
type RulesSource string

// Supported sources correspond to independently readable landing requirements.
const (
	Rulesets          RulesSource = "rulesets"
	ClassicProtection RulesSource = "classic_protection"
	Permissions       RulesSource = "permissions"
)

var rulesSourceOrder = [...]RulesSource{Rulesets, ClassicProtection, Permissions}

// RulesSourceBlocked records definite findings, even if the read's basis is stale.
type RulesSourceBlocked struct {
	Source   RulesSource
	Problems []string
	Basis    uint64
}

// RulesSourceCleared clears only Source, and only at its current revision.
type RulesSourceCleared struct {
	Source RulesSource
	Basis  uint64
}

func (RulesSourceBlocked) queueEvent() {}
func (RulesSourceCleared) queueEvent() {}

// Validate requires a known source, definite problems, and an incrementable basis.
func (e RulesSourceBlocked) Validate() error {
	if err := e.Source.validate(); err != nil {
		return err
	}
	return (RulesBlocked{Problems: e.Problems, Basis: e.Basis}).Validate()
}

// Validate requires a known source and an incrementable basis.
func (e RulesSourceCleared) Validate() error {
	if err := e.Source.validate(); err != nil {
		return err
	}
	return (RulesCleared{Basis: e.Basis}).Validate()
}

func (source RulesSource) validate() error {
	if !slices.Contains(rulesSourceOrder[:], source) {
		return fmt.Errorf("unknown rules source %q", source)
	}
	return nil
}

type rulesSourceState struct {
	problems []string
	revision uint64
}

func (s *State) blockRulesSource(source RulesSource, problems []string) {
	current := s.rulesSources[source]
	current.problems = slices.Clone(problems)
	current.revision++
	s.rulesSources[source] = current
	s.rulesPauseRevision = s.revision
}

func (s *State) clearRulesSource(source RulesSource, basis uint64) {
	current := s.rulesSources[source]
	if basis == current.revision {
		current.problems = nil
		current.revision++
		s.rulesSources[source] = current
		s.rulesPauseRevision = s.revision
	}
}

// RulesSourceStatus exposes one source's current findings and observation revision.
type RulesSourceStatus struct {
	Source   RulesSource
	Problems []string
	Revision uint64
}

// RulesSources returns observed sources in a stable order, with owned slices.
func (s State) RulesSources() []RulesSourceStatus {
	var result []RulesSourceStatus
	for _, source := range rulesSourceOrder {
		if current, ok := s.rulesSources[source]; ok {
			result = append(result, RulesSourceStatus{source, append([]string{}, current.problems...), current.revision})
		}
	}
	return result
}

// RulesSourceProblems returns an owned copy of one source’s active findings.
func (s State) RulesSourceProblems(source RulesSource) []string {
	return slices.Clone(s.rulesSources[source].problems)
}

// RulesSourceRevision identifies the last applied observation for this source.
func (s State) RulesSourceRevision(source RulesSource) uint64 {
	return s.rulesSources[source].revision
}

// RulesProblems returns all active findings in source order.
func (s State) RulesProblems() []string {
	var problems []string
	for _, source := range rulesSourceOrder {
		problems = append(problems, s.rulesSources[source].problems...)
	}
	return problems
}

// RulesRevision retains the legacy rulesets revision accessor.
func (s State) RulesRevision() uint64 { return s.RulesSourceRevision(Rulesets) }

func (s State) hasRulesProblems() bool {
	for _, current := range s.rulesSources {
		if len(current.problems) > 0 {
			return true
		}
	}
	return false
}

func (s State) rulesSourceDetail(source RulesSource) string {
	detail := strings.Join(s.rulesSources[source].problems, "; ")
	if source != Rulesets {
		detail = string(source) + ": " + detail
	}
	return detail
}

func (s State) rulesDetail() string {
	var details []string
	for _, source := range rulesSourceOrder {
		if len(s.rulesSources[source].problems) > 0 {
			details = append(details, s.rulesSourceDetail(source))
		}
	}
	return strings.Join(details, "; ")
}
