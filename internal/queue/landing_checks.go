package queue

import (
	"encoding/json"
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
	BatchID             string               `json:"batch_id"`
	AttemptID           string               `json:"attempt_id"`
	ConfigCommit        string               `json:"config_commit"`
	ConfigDigest        string               `json:"config_digest"`
	PolicyDigest        string               `json:"policy_digest"`
	TestedSHA           string               `json:"tested_sha"`
	LandedSHA           string               `json:"landed_sha"`
	Method              MergeMethod          `json:"method"`
	TestRef             string               `json:"test_ref"`
	RequiredCheckSource LandingCheckSource   `json:"required_check_source"`
	Members             []LandingCheckMember `json:"members"`
	Checks              []LandingCheck       `json:"checks"`
	ObservedAt          time.Time            `json:"observed_at"`
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
	PR         int                `json:"pr"`
	HeadSHA    string             `json:"head_sha"`
	Conditions condition.Evidence `json:"conditions"`
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
	if requireActionText("batch_id", e.BatchID, "attempt_id", e.AttemptID, "config_commit", e.ConfigCommit, "test_ref", e.TestRef) != nil || !digestHex(e.ConfigDigest) || !digestHex(e.PolicyDigest) || !forge.ValidDiffObjectID(e.TestedSHA) || !forge.ValidDiffObjectID(e.LandedSHA) || e.ObservedAt.IsZero() || !slices.Contains([]MergeMethod{Merge, Squash, Rebase, FastForward}, e.Method) {
		return fmt.Errorf("invalid landing checks identity")
	}
	if e.RequiredCheckSource.Kind != "merge_when" || len(e.RequiredCheckSource.SourceRevisions) != len(rulesSourceOrder) {
		return fmt.Errorf("invalid required check source")
	}
	for i, r := range e.RequiredCheckSource.SourceRevisions {
		if r.Source != rulesSourceOrder[i] {
			return fmt.Errorf("invalid check source revisions")
		}
	}
	if len(e.Members) < 1 || len(e.Members) > 128 || e.Checks == nil || len(e.Checks) > 10000 {
		return fmt.Errorf("invalid landing check members/runs")
	}
	seen := map[int]bool{}
	for _, m := range e.Members {
		if m.PR < 1 || seen[m.PR] || !forge.ValidDiffObjectID(m.HeadSHA) || m.Conditions.Validate() != nil {
			return fmt.Errorf("invalid landing check member")
		}
		seen[m.PR] = true
	}
	if e.Method == FastForward {
		if e.LandedSHA != e.Members[len(e.Members)-1].HeadSHA {
			return fmt.Errorf("invalid fast-forward landing SHA")
		}
	} else if e.LandedSHA != e.TestedSHA {
		return fmt.Errorf("invalid strict landing SHA")
	}
	for _, c := range e.Checks {
		if len(c.Source) > 4096 {
			return fmt.Errorf("invalid check source")
		}
		if c.ProducerIdentity != nil {
			if len(*c.ProducerIdentity) > 4096 {
				return fmt.Errorf("invalid producer identity")
			}
			if c.ProducerKind == "other" {
				if c.Source != *c.ProducerIdentity {
					return fmt.Errorf("mismatched producer identity")
				}
			} else if c.Source != c.ProducerKind+":"+*c.ProducerIdentity {
				return fmt.Errorf("mismatched producer identity")
			}
		}
		if strings.TrimSpace(c.Name) == "" || len(c.Name) > 4096 || !slices.Contains([]forge.CheckState{forge.Pending, forge.Success, forge.Failure, forge.Cancelled, forge.Skipped, forge.Neutral}, c.Result) {
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
	if pin == nil || pin.Authority != authority.OpenMerge || pin.EffectiveDigest != s.ConfigDigest || pin.GoverningCommit != s.ConfigCommit || e.ConfigCommit != s.ConfigCommit || e.ConfigDigest != s.ConfigDigest || e.PolicyDigest != policyDigest || b == nil || b.ConfigDigest != s.ConfigDigest || e.BatchID != land.BatchID || e.BatchID != start.BatchID || e.AttemptID != start.AttemptID || e.TestedSHA != land.TestedSHA || e.TestedSHA != b.TestedSHA || e.TestRef != land.Ref || e.TestRef != b.TestRef || e.Method != land.Method || e.Method != b.Method || e.ObservedAt.After(start.At) || e.ObservedAt.Before(b.StartedTestingAt) || !reflect.DeepEqual(e.RequiredCheckSource, s.LandingCheckSources()) || len(e.Members) != len(land.PRs) || !slices.Equal(b.PRs, land.PRs) {
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
	if err := strictAuthorityFields(raw, reflect.TypeFor[LandingChecksRecorded]()); err != nil {
		return LandingChecksRecorded{}, err
	}
	return readApprovalWire[LandingChecksRecorded](raw)
}
