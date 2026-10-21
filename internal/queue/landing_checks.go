package queue

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// LandingChecksRecorded is journal-only evidence for one admitted landing
// attempt. It has no planner projection and survives settlement in the journal.
// PR 9 activates emission on authority paths; ordinary github queues omit it.
type LandingChecksRecorded struct {
	BatchID             string                    `json:"batch_id"`
	AttemptID           string                    `json:"attempt_id"`
	ConfigCommit        string                    `json:"config_commit"`
	ConfigDigest        string                    `json:"config_digest"`
	PolicyDigest        string                    `json:"policy_digest"`
	TestedSHA           string                    `json:"tested_sha"`
	LandedSHA           string                    `json:"landed_sha"`
	Method              MergeMethod               `json:"method"`
	TestRef             string                    `json:"test_ref"`
	RequiredCheckSource LandingCheckSource        `json:"required_check_source"`
	Members             []LandingCheckMember      `json:"members"`
	Checks              []LandingCheck            `json:"checks"`
	CheckOrder          []int                     `json:"check_order"`
	Expressions         []string                  `json:"expressions"`
	Inputs              []condition.ObservedInput `json:"inputs"`
	ObservedAt          time.Time                 `json:"observed_at"`
}

// LandingCheckSource identifies the configured condition set and its read basis.
type LandingCheckSource struct {
	Kind            string                       `json:"kind"`
	RulesRevision   uint64                       `json:"rules_revision"`
	SourceRevisions []LandingCheckSourceRevision `json:"source_revisions"`
}

// LandingCheckSourceRevision pins one independent source observation.
type LandingCheckSourceRevision struct {
	Source   RulesSource `json:"source"`
	Revision uint64      `json:"revision"`
}

// LandingCheckMember binds evaluator inputs to a pinned batch member.
type LandingCheckMember struct {
	PR         int                   `json:"pr"`
	HeadSHA    string                `json:"head_sha"`
	Conditions LandingCheckCondition `json:"conditions"`
}

// LandingCheckCondition refers to shared event tables. Inputs retain attribute
// order; CheckOrder is the shared exact evaluator read, including repetitions.
type LandingCheckCondition struct {
	Expression int   `json:"expression"`
	Inputs     []int `json:"inputs"`
}

// LandingCheck is a provider observation used by the landing evaluator.
// IDs and branch provenance are null when the provider did not supply them.
// Result is the normalized result used by policy evaluation, not an invented
// success conclusion for a negative or alternative condition.
type LandingCheck struct {
	Name             string           `json:"name"`
	Source           string           `json:"source"`
	ProducerKind     string           `json:"producer_kind"`
	ProducerIdentity *string          `json:"producer_identity"`
	Result           forge.CheckState `json:"result"`
	RunID            *uint64          `json:"run_id"`
	Attempt          *uint64          `json:"attempt"`
	WorkflowRunID    *uint64          `json:"workflow_run_id"`
	HeadBranch       *string          `json:"head_branch"`
}

func (LandingChecksRecorded) queueEvent() {}

// Validate checks event identity, member bindings, and explicit evidence shapes.
func (e LandingChecksRecorded) Validate() error {
	if err := e.validateHeader(); err != nil {
		return err
	}
	for _, count := range []struct {
		field       string
		size, limit int
	}{
		{"members", len(e.Members), maxLandingCheckMembers}, {"checks", len(e.Checks), maxLandingCheckRuns}, {"check order", len(e.CheckOrder), maxLandingCheckRuns}, {"expressions", len(e.Expressions), maxLandingCheckMembers}, {"inputs", len(e.Inputs), maxLandingCheckInputs},
	} {
		if count.size > count.limit {
			return LandingChecksTooLargeError{count.field, count.limit}
		}
	}
	if err := e.checkSize(); err != nil {
		return err
	}
	if len(e.Members) < 1 || e.Checks == nil || e.CheckOrder == nil || e.Expressions == nil || e.Inputs == nil {
		return fmt.Errorf("invalid landing check members/runs")
	}
	usedChecks := make([]bool, len(e.Checks))
	for _, index := range e.CheckOrder {
		if index < 0 || index >= len(e.Checks) {
			return fmt.Errorf("invalid landing check reference")
		}
		usedChecks[index] = true
	}
	for _, used := range usedChecks {
		if !used {
			return fmt.Errorf("unused landing check")
		}
	}
	usedExpressions := make([]bool, len(e.Expressions))
	usedInputs := make([]bool, len(e.Inputs))
	for i, expression := range e.Expressions {
		if len(expression) > condition.MaxEvidenceExpressionBytes {
			return LandingChecksTooLargeError{"expression", condition.MaxEvidenceExpressionBytes}
		}
		if expression == "" || slices.Contains(e.Expressions[:i], expression) {
			return fmt.Errorf("invalid or duplicate expression")
		}
	}
	for i, v := range e.Inputs {
		if err := v.Validate(); err != nil {
			if errors.Is(err, condition.ErrEvidenceTooLarge) {
				return LandingChecksTooLargeError{"condition input", condition.MaxEvidenceStringBytes}
			}
			return err
		}
		if v.List == nil || v.Observed == nil || v.Unsettled == nil {
			return fmt.Errorf("null condition input lists")
		}
		for _, prior := range e.Inputs[:i] {
			if prior.Equal(v) {
				return fmt.Errorf("duplicate condition input")
			}
		}
	}
	seen := map[int]bool{}
	for _, m := range e.Members {
		if len(m.HeadSHA) > MaxLandingCheckStringBytes {
			return LandingChecksTooLargeError{"member head", MaxLandingCheckStringBytes}
		}
		if len(m.Conditions.Inputs) > maxLandingCheckMemberInputs {
			return LandingChecksTooLargeError{"member inputs", maxLandingCheckMemberInputs}
		}
		if m.PR < 1 || seen[m.PR] || !forge.ValidDiffObjectID(m.HeadSHA) || m.Conditions.Expression < 0 || m.Conditions.Expression >= len(e.Expressions) || m.Conditions.Inputs == nil {
			return fmt.Errorf("invalid landing check member")
		}
		seen[m.PR] = true
		usedExpressions[m.Conditions.Expression] = true
		prior := ""
		for _, index := range m.Conditions.Inputs {
			if index < 0 || index >= len(e.Inputs) || e.Inputs[index].Attribute <= prior {
				return fmt.Errorf("invalid condition input reference")
			}
			prior = e.Inputs[index].Attribute
			usedInputs[index] = true
		}
	}
	if e.Method == FastForward {
		if e.LandedSHA != e.Members[len(e.Members)-1].HeadSHA {
			return fmt.Errorf("invalid fast-forward landing SHA")
		}
	} else if e.LandedSHA != e.TestedSHA {
		return fmt.Errorf("invalid strict landing SHA")
	}
	for _, used := range usedExpressions {
		if !used {
			return fmt.Errorf("unused expression")
		}
	}
	for _, used := range usedInputs {
		if !used {
			return fmt.Errorf("unused condition input")
		}
	}
	seenChecks := map[landingCheckKey]bool{}
	for _, c := range e.Checks {
		if err := c.validate(); err != nil {
			return err
		}
		key := c.key()
		if seenChecks[key] {
			return fmt.Errorf("duplicate landing check")
		}
		seenChecks[key] = true
	}

	return nil
}

// validateHeader bounds and validates the fixed envelope before a builder retains it.
func (e LandingChecksRecorded) validateHeader() error {
	for _, text := range []string{e.BatchID, e.AttemptID, e.ConfigCommit, e.ConfigDigest, e.PolicyDigest, e.TestedSHA, e.LandedSHA, e.TestRef} {
		if len(text) > MaxLandingCheckStringBytes {
			return LandingChecksTooLargeError{"identity", MaxLandingCheckStringBytes}
		}
	}
	if requireActionText("batch_id", e.BatchID, "attempt_id", e.AttemptID, "config_commit", e.ConfigCommit, "test_ref", e.TestRef) != nil || !digestHex(e.ConfigDigest) || !digestHex(e.PolicyDigest) || !forge.ValidDiffObjectID(e.TestedSHA) || !forge.ValidDiffObjectID(e.LandedSHA) || e.ObservedAt.IsZero() || !slices.Contains([]MergeMethod{Merge, Squash, Rebase, FastForward}, e.Method) {
		return fmt.Errorf("invalid landing checks identity")
	}
	if len(e.RequiredCheckSource.Kind) > MaxLandingCheckStringBytes {
		return LandingChecksTooLargeError{"source kind", MaxLandingCheckStringBytes}
	}
	if len(e.RequiredCheckSource.SourceRevisions) > len(rulesSourceOrder) {
		return LandingChecksTooLargeError{"source revisions", len(rulesSourceOrder)}
	}
	if e.RequiredCheckSource.Kind != "merge_when" || len(e.RequiredCheckSource.SourceRevisions) != len(rulesSourceOrder) {
		return fmt.Errorf("invalid required check source")
	}
	for i, r := range e.RequiredCheckSource.SourceRevisions {
		if len(r.Source) > MaxLandingCheckStringBytes {
			return LandingChecksTooLargeError{"source revision", MaxLandingCheckStringBytes}
		}
		if r.Source != rulesSourceOrder[i] {
			return fmt.Errorf("invalid check source revisions")
		}
	}
	return nil
}

func (c LandingCheck) validate() error {
	for _, text := range []string{c.Name, c.ProducerKind, string(c.Result)} {
		if len(text) > MaxLandingCheckStringBytes {
			return LandingChecksTooLargeError{"check string", MaxLandingCheckStringBytes}
		}
	}
	if c.HeadBranch != nil && len(*c.HeadBranch) > MaxLandingCheckStringBytes {
		return LandingChecksTooLargeError{"branch provenance", MaxLandingCheckStringBytes}
	}
	if len(c.Source) > MaxLandingCheckStringBytes {
		return LandingChecksTooLargeError{"check source", MaxLandingCheckStringBytes}
	}
	if c.ProducerIdentity != nil {
		if len(*c.ProducerIdentity) > MaxLandingCheckStringBytes {
			return LandingChecksTooLargeError{"producer identity", MaxLandingCheckStringBytes}
		}
		if c.ProducerKind == "other" {
			if c.Source != *c.ProducerIdentity {
				return fmt.Errorf("mismatched producer identity")
			}
		} else if c.Source != c.ProducerKind+":"+*c.ProducerIdentity {
			return fmt.Errorf("mismatched producer identity")
		}
	}
	if strings.TrimSpace(c.Name) == "" || !slices.Contains([]forge.CheckState{forge.Pending, forge.Success, forge.Failure, forge.Cancelled, forge.Skipped, forge.Neutral}, c.Result) {
		return fmt.Errorf("invalid landing check result")
	}
	switch c.ProducerKind {
	case "app", "status", "other":
		if c.ProducerIdentity == nil || strings.TrimSpace(*c.ProducerIdentity) == "" || *c.ProducerIdentity == "0" {
			return fmt.Errorf("invalid check producer")
		}
	case "unavailable":
		if c.ProducerIdentity != nil {
			return fmt.Errorf("unavailable check producer has identity")
		}
	default:
		return fmt.Errorf("invalid producer kind")
	}
	for _, id := range []*uint64{c.RunID, c.Attempt, c.WorkflowRunID} {
		if id != nil && *id == 0 {
			return fmt.Errorf("fabricated check ID/attempt")
		}
	}
	if c.HeadBranch != nil && strings.TrimSpace(*c.HeadBranch) == "" {
		return fmt.Errorf("empty branch provenance")
	}
	return nil
}

// LandingCheckSources pins all independent rule sources before the network read.
func (s State) LandingCheckSources() LandingCheckSource {
	out := LandingCheckSource{Kind: "merge_when", RulesRevision: s.RulesRevision(), SourceRevisions: []LandingCheckSourceRevision{}}
	for _, source := range rulesSourceOrder {
		out.SourceRevisions = append(out.SourceRevisions, LandingCheckSourceRevision{source, s.RulesSourceRevision(source)})
	}
	return out
}

// ValidateLandingChecks runs under the same queue lock as CanStart and approval
// validation. The caller supplies only the exact successful read, never a later
// enrichment. Authority without approvals is supported without inventing reviews.
func (s State) ValidateLandingChecks(policyDigest string, land Land, start LandingStarted, e LandingChecksRecorded) error {
	if err := e.Validate(); err != nil {
		return err
	}
	pin := s.AuthorityPin()
	b := s.Batch(land.BatchID)
	if pin == nil || pin.Authority != authority.OpenMerge || pin.EffectiveDigest != s.ConfigDigest || e.ConfigCommit != pin.GoverningCommit || e.ConfigDigest != s.ConfigDigest || e.PolicyDigest != policyDigest || b == nil || b.ConfigDigest != s.ConfigDigest || e.BatchID != land.BatchID || e.BatchID != start.BatchID || e.AttemptID != start.AttemptID || e.TestedSHA != land.TestedSHA || e.TestedSHA != b.TestedSHA || e.TestRef != land.Ref || e.TestRef != b.TestRef || e.Method != land.Method || e.Method != b.Method || e.ObservedAt.After(start.At) || e.ObservedAt.Before(b.StartedTestingAt) || !reflect.DeepEqual(e.RequiredCheckSource, s.LandingCheckSources()) || len(e.Members) != len(land.PRs) || !slices.Equal(b.PRs, land.PRs) {
		return fmt.Errorf("stale landing check binding")
	}
	for i, m := range e.Members {
		if m.PR != land.PRs[i].PR || m.HeadSHA != land.PRs[i].HeadSHA {
			return fmt.Errorf("stale landing check member")
		}
	}
	return nil
}

func readLandingChecksWire(raw json.RawMessage) (LandingChecksRecorded, error) {
	// JSONB read-back adds spaces; the compact encoded size is rechecked by
	// Validate. This coarse pre-decode bound also rejects arbitrary padding.
	if len(raw) > 2*MaxLandingChecksBytes {
		return LandingChecksRecorded{}, LandingChecksTooLargeError{"wire event", MaxLandingChecksBytes}
	}
	if err := strictAuthorityFields(raw, reflect.TypeFor[LandingChecksRecorded]()); err != nil {
		return LandingChecksRecorded{}, err
	}
	return readApprovalWire[LandingChecksRecorded](raw)
}
