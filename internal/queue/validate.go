package queue

import (
	"fmt"
	"strings"
)

func requireActionText(fields ...string) error {
	for i := 0; i < len(fields); i += 2 {
		if strings.TrimSpace(fields[i+1]) == "" {
			return fmt.Errorf("%s must not be empty", fields[i])
		}
	}
	return nil
}

func validateBatchPRs(prs []BatchPR) error {
	if len(prs) == 0 {
		return fmt.Errorf("prs must contain at least one PR")
	}
	seen := make(map[int]bool, len(prs))
	for _, pr := range prs {
		if pr.PR <= 0 || seen[pr.PR] {
			return fmt.Errorf("prs must contain distinct positive PR numbers: %d", pr.PR)
		}
		if err := requireActionText("head_sha", pr.HeadSHA); err != nil {
			return err
		}
		seen[pr.PR] = true
	}
	return nil
}

func validateMergeMethod(method MergeMethod) error {
	switch method {
	case Merge, Squash, Rebase:
		return nil
	default:
		return fmt.Errorf("invalid merge method %q", method)
	}
}

// Validate checks the pinned build inputs before execution or persistence.
func (a BuildTestRef) Validate() error {
	if err := requireActionText("batch_id", a.BatchID, "base_sha", a.BaseSHA, "ref", a.Ref); err != nil {
		return err
	}
	if err := validateBatchPRs(a.PRs); err != nil {
		return err
	}
	return validateMergeMethod(a.Method)
}

// Validate checks the subject of a checks refresh; epoch zero is the first read.
func (a RefreshChecks) Validate() error {
	return requireActionText("batch_id", a.BatchID, "tested_sha", a.TestedSHA)
}

// Validate requires the reopened commit and observation time.
func (e ChecksReopened) Validate() error {
	if err := requireActionText("tested_sha", e.TestedSHA); err != nil {
		return err
	}
	if e.At.IsZero() {
		return fmt.Errorf("at must not be zero")
	}
	return nil
}

// Validate requires both fences and the exact heads checked before landing.
func (a Land) Validate() error {
	if err := requireActionText("batch_id", a.BatchID, "expected_base_sha", a.ExpectedBaseSHA,
		"tested_sha", a.TestedSHA, "ref", a.Ref, "fence_token", a.FenceToken); err != nil {
		return err
	}
	if err := validateBatchPRs(a.PRs); err != nil {
		return err
	}
	return validateMergeMethod(a.Method)
}

// Validate checks the durable attempt and commit needed to resolve a landing.
func (a ResolveLanding) Validate() error {
	return requireActionText("batch_id", a.BatchID, "attempt_id", a.AttemptID,
		"tested_sha", a.TestedSHA, "fence_token", a.FenceToken)
}

// Validate checks the cleanup target.
func (a DeleteRef) Validate() error { return requireActionText("ref", a.Ref) }

// Validate requires an admission so an acknowledged dequeue cannot be a no-op.
func (a Dequeue) Validate() error {
	if a.PR <= 0 || a.AdmissionID == 0 {
		return fmt.Errorf("dequeue requires a positive PR and admission_id")
	}
	if err := requireActionText("head_sha", a.HeadSHA, "scope", a.scope); err != nil {
		return err
	}
	switch a.Reason {
	case NotEligible, Untrusted, HeadChanged, ChecksFailed, Conflict, Closed, Requested:
		return nil
	case ChecksTimeout:
		return requireActionText("batch_id", a.BatchID)
	default:
		return fmt.Errorf("invalid dequeue reason %q", a.Reason)
	}
}

// Validate allows a headless blocked report when enqueue preceded observation.
// Admission zero likewise identifies a report before admission to the queue.
func (a ReportStatus) Validate() error {
	if a.PR <= 0 || a.revision == 0 {
		return fmt.Errorf("report requires a positive PR and revision")
	}
	if err := requireActionText("detail", a.Detail); err != nil {
		return err
	}
	switch a.Status {
	case "queued", "testing", "landing", "merged", "dequeued", "blocked", "paused":
	default:
		return fmt.Errorf("invalid report status %q", a.Status)
	}
	if a.Status == "blocked" || a.Status == "paused" || a.Status == "dequeued" {
		if err := requireActionText("reason", a.Reason); err != nil {
			return err
		}
	}
	if a.Status == "blocked" && a.Reason == string(NotEligible) {
		return nil
	}
	return requireActionText("head_sha", a.HeadSHA)
}

// Validate pins closing to a positive PR, its tested head and landed commit.
func (a MarkLanded) Validate() error {
	if a.PR <= 0 {
		return fmt.Errorf("pr must be positive")
	}
	return requireActionText("head_sha", a.HeadSHA, "landed_sha", a.LandedSHA)
}

// Validate requires a durable composition, base and recorded clock origin.
func (e BatchStarted) Validate() error {
	if err := requireActionText("batch_id", e.BatchID, "base_sha", e.BaseSHA); err != nil {
		return err
	}
	if e.At.IsZero() {
		return fmt.Errorf("at must not be zero")
	}
	if len(e.PRs) > 128 {
		return fmt.Errorf("prs must contain at most 128 PRs")
	}
	return validateBatchPRs(e.PRs)
}

// Validate restricts planner-triggered failures to check timeouts.
func (a FailBatch) Validate() error {
	if a.Reason != ChecksTimeout {
		return fmt.Errorf("invalid batch failure reason %q", a.Reason)
	}
	return requireActionText("batch_id", a.BatchID)
}
