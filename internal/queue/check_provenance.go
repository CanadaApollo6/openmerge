package queue

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// UnboundCheck identifies a required check observed only outside the queue push.
// An empty HeadBranch represents a missing/null suite branch.
type UnboundCheck struct {
	Name       string `json:"name"`
	Source     string `json:"source"`
	HeadBranch string `json:"head_branch"`
}

// CheckProvenanceObserved records diagnostic evidence, never CI authorization.
// An empty Checks list clears an earlier observation for this batch and SHA.
type CheckProvenanceObserved struct {
	BatchID   string         `json:"batch_id"`
	TestedSHA string         `json:"tested_sha"`
	Checks    []UnboundCheck `json:"checks"`
}

func (CheckProvenanceObserved) queueEvent() {}

// Validate pins diagnostic evidence to a batch and commit.
func (e CheckProvenanceObserved) Validate() error {
	if err := requireActionText("batch_id", e.BatchID, "tested_sha", e.TestedSHA); err != nil {
		return err
	}
	for _, check := range e.Checks {
		if err := requireActionText("name", check.Name); err != nil {
			return err
		}
	}
	return nil
}

// UnboundChecks returns a copy of the active batch's latest diagnostic evidence.
func (s State) UnboundChecks() []UnboundCheck {
	if s.Active == nil {
		return nil
	}
	return slices.Clone(s.Active.unboundChecks)
}

// CheckProvenanceDetail explains why observed check runs cannot count.
func (s State) CheckProvenanceDetail() string {
	if s.Active == nil || len(s.Active.unboundChecks) == 0 {
		return ""
	}
	var details []string
	for _, check := range s.Active.unboundChecks {
		branch := "null"
		if check.HeadBranch != "" {
			branch = strconv.Quote(check.HeadBranch)
		}
		details = append(details, fmt.Sprintf("required check %q from %q has no bound run: observed head_branch=%s; expected %q", check.Name, check.Source, branch, s.Active.TestRef))
	}
	return "unbound-check-provenance: " + strings.Join(details, "; ")
}

// DequeueDetail retains the stable reason while explaining a provenance timeout.
func (s State) DequeueDetail(pr int, reason DequeueReason) string {
	detail := dequeueDetail(reason)
	if (reason == ChecksTimeout || reason == ChecksFailed) && s.inBatch(pr) {
		if retry := s.CheckRetryDetail(); retry != "" {
			detail += ": " + retry
		}
	}
	if reason == ChecksTimeout && s.inBatch(pr) {
		if provenance := s.CheckProvenanceDetail(); provenance != "" {
			detail += ": " + provenance
		}
	}
	return detail
}
