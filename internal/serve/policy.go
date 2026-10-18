package serve

import (
	"context"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

const reviewAttribute = "approved-reviews-by"

// ApprovalEvidence is the opt-in review input for the dormant approval-aware
// policy path. Known distinguishes an observed empty eligible review set from
// an unresolved eligibility observation: the former is condition.Strings(),
// while the latter is the zero condition.Value and must remain unknown under
// NOT. A nil *ApprovalEvidence keeps the legacy policy path unchanged.
type ApprovalEvidence struct {
	Reviewers []string
	Known     bool
}

func (e ApprovalEvidence) value() condition.Value {
	if !e.Known {
		return condition.Value{}
	}
	return condition.Strings(e.Reviewers...)
}

// PolicyOutcome keeps the full condition result separate from an approval
// wait. Terminal and Success retain the pair consumed by check observation;
// WaitingForApproval is an orthogonal landing-gate reason for the dormant
// approval-aware path.
//
// The future 11a consumer must branch on WaitingForApproval before it builds
// ChecksObserved or enters its timer/refresh path. Terminal=false alone is
// insufficient: the executor can otherwise time out pending checks against
// their original deadline and misclassify an approval wait as CI failure.
type PolicyOutcome struct {
	Terminal           bool
	Success            bool
	WaitingForApproval bool
}

func snapshot(pr forge.PullRequest) condition.Snapshot {
	var approvals []string
	for _, a := range pr.Approvals {
		if a.SHA == pr.HeadSHA {
			approvals = append(approvals, a.User)
		}
	}
	values := condition.Snapshot{"draft": condition.Bool(pr.Draft), "approved-reviews-by": condition.Strings(approvals...)}
	for name, value := range map[string]string{"base": pr.Base, "head": pr.HeadBranch, "title": pr.Title, "author": pr.Author} {
		if value != "" {
			values[name] = condition.String(value)
		}
	}
	if pr.LabelsKnown {
		values["label"] = condition.Strings(pr.Labels...)
	}
	if pr.FilesKnown {
		values["files"] = condition.Strings(pr.Files...)
	}
	if pr.ConflictKnown {
		values["conflict"] = condition.Bool(pr.Conflict)
	}
	return values
}

func hasChecks(expression condition.Expression) bool {
	return slices.ContainsFunc(expression.Attributes(), func(a string) bool { return strings.HasPrefix(a, "check-") })
}

// Check lists are built only after producer identity is checked. In particular,
// NOT must never turn an ambiguous producer's missing success into permission.
func checkSnapshot(expression condition.Expression, checks []forge.CheckRun) (condition.Snapshot, error) {
	sources := make(map[string]map[string]bool)
	latest := make(map[string]forge.CheckRun)
	for _, c := range checks {
		if sources[c.Name] == nil {
			sources[c.Name] = make(map[string]bool)
		}
		sources[c.Name][c.Source] = true
		if old, exists := latest[c.Name]; !exists || c.ID >= old.ID {
			latest[c.Name] = c
		}
	}
	names := make([]string, 0, len(latest))
	for name := range latest {
		names = append(names, name)
	}
	slices.Sort(names)
	lists := map[string][]string{"check-success": {}, "check-failure": {}, "check-pending": {}}
	for _, name := range names {
		if len(sources[name]) > 1 || sources[name][""] {
			if expression.ReferencesCheck(name) {
				var producers []string
				for source := range sources[name] {
					producers = append(producers, source)
				}
				slices.Sort(producers)
				return nil, &executor.CheckSourceAmbiguityError{Name: name, Sources: producers}
			}
			continue
		}
		attribute := "check-pending"
		switch latest[name].State {
		case forge.Success:
			attribute = "check-success"
		case forge.Failure, forge.Cancelled, forge.Neutral, forge.Skipped:
			attribute = "check-failure"
		}
		lists[attribute] = append(lists[attribute], name)
	}
	return condition.CheckResults(lists["check-success"], lists["check-failure"], lists["check-pending"]), nil
}

func policyValues(expression condition.Expression, pr forge.PullRequest, checks []forge.CheckRun) (condition.Snapshot, error) {
	values := snapshot(pr)
	if hasChecks(expression) {
		ci, err := checkSnapshot(expression, checks)
		if err != nil {
			return nil, err
		}
		for name, value := range ci {
			values[name] = value
		}
	}
	return values, nil
}

func policyValuesWithApprovals(expression condition.Expression, pr forge.PullRequest, checks []forge.CheckRun, approvals *ApprovalEvidence) (condition.Snapshot, error) {
	values, err := policyValues(expression, pr, checks)
	if err != nil {
		return nil, err
	}
	if approvals != nil {
		values[reviewAttribute] = approvals.value()
	}
	return values, nil
}

func evaluate(expression condition.Expression, pr forge.PullRequest, checks []forge.CheckRun) (bool, error) {
	_, success, err := mergeResult(expression, pr, checks)
	return success, err
}

func mergeResult(expression condition.Expression, pr forge.PullRequest, checks []forge.CheckRun) (terminal, success bool, err error) {
	values, err := policyValues(expression, pr, checks)
	if err != nil {
		return false, false, err
	}
	success, _ = expression.Evaluate(values)
	definitelyFalse, _ := condition.Not(expression).Evaluate(values)
	return success || definitelyFalse, success, nil
}

// mergeResultWithApprovals evaluates the full condition while retaining the
// CI-versus-approval distinction needed by PR 8. The nil opt-in argument is
// deliberately routed through mergeResult so legacy callers keep identical
// behavior. With approval evidence enabled, an expression that is blocked by
// review evidence remains non-terminal; an expression made definitely false
// by a real CI failure remains terminal and therefore eligible for the
// existing bounded retry machinery. 11a must consume WaitingForApproval
// before constructing ChecksObserved or entering timer/refresh handling;
// Terminal=false by itself does not prevent the original CI deadline from
// timing out.
func mergeResultWithApprovals(expression condition.Expression, pr forge.PullRequest, checks []forge.CheckRun, approvals *ApprovalEvidence) (PolicyOutcome, error) {
	if approvals == nil {
		terminal, success, err := mergeResult(expression, pr, checks)
		return PolicyOutcome{Terminal: terminal, Success: success}, err
	}
	values, err := policyValuesWithApprovals(expression, pr, checks, approvals)
	if err != nil {
		return PolicyOutcome{}, err
	}
	met, _ := expression.Evaluate(values)
	if met {
		return PolicyOutcome{Terminal: true, Success: true}, nil
	}
	pendingMet, pendingDefinitelyFalse, pendingFailures := approvalPendingEvaluation(expression, values)
	// For a non-met opt-in result, only the review-neutralized expression can
	// establish terminal CI failure. In particular, All(pending CI, known-empty
	// review) is unknown after review is neutralized and must remain non-terminal.
	outcome := PolicyOutcome{Terminal: pendingDefinitelyFalse}
	if !approvalWait(pendingMet, pendingDefinitelyFalse, pendingFailures) {
		return outcome, nil
	}
	// An approval wait is not a failed CI result. 11a must consume this reason
	// through ApprovalGateRejected before the executor constructs ChecksObserved
	// or enters its timer/refresh path; this outcome alone does not cancel the
	// original CI deadline.
	outcome.WaitingForApproval = true
	return outcome, nil
}

// approvalWait asks whether the current non-passing result can be resolved by
// the review attribute alone. Re-evaluating with that attribute unknown keeps
// all nested all/any/not semantics in condition.Expression rather than
// flattening review terms into a top-level conjunction. Unknown non-review
// attributes (including pending or missing CI) keep the result pending for
// their own reason and do not claim an approval-only wait.
func approvalPendingEvaluation(expression condition.Expression, values condition.Snapshot) (met, definitelyFalse bool, failures []condition.Failure) {
	pending := make(condition.Snapshot, len(values))
	for name, value := range values {
		pending[name] = value
	}
	if slices.Contains(expression.Attributes(), reviewAttribute) {
		pending[reviewAttribute] = condition.Value{}
	}
	met, failures = expression.Evaluate(pending)
	definitelyFalse, _ = condition.Not(expression).Evaluate(pending)
	return met, definitelyFalse, failures
}

func approvalWait(pendingMet, pendingDefinitelyFalse bool, failures []condition.Failure) bool {
	if pendingMet || pendingDefinitelyFalse || !failureHasAttribute(failures, reviewAttribute) {
		return false
	}
	return !failureHasUnknownNonReview(failures)
}

func failureHasAttribute(failures []condition.Failure, attribute string) bool {
	for _, failure := range failures {
		if !failure.Unknown {
			continue
		}
		if failure.Attribute == attribute || failureHasAttribute(failure.Children, attribute) {
			return true
		}
	}
	return false
}

func failureHasUnknownNonReview(failures []condition.Failure) bool {
	for _, failure := range failures {
		if !failure.Unknown {
			continue
		}
		if failure.Unknown && failure.Attribute != "" && failure.Attribute != reviewAttribute {
			return true
		}
		if failureHasUnknownNonReview(failure.Children) {
			return true
		}
	}
	return false
}

func (s *Server) eligible(ctx context.Context, w *worker, pr forge.PullRequest) (bool, error) {
	if w.configError != nil || pr.Closed || pr.Base != w.Key.BaseBranch {
		return false, nil
	}
	var checks []forge.CheckRun
	if hasChecks(w.cfg.EnterWhen) {
		var err error
		checks, err = w.Driver.CheckRuns(ctx, w.repo(), pr.HeadSHA)
		if err != nil {
			return false, err
		}
	}
	met, err := evaluate(w.cfg.EnterWhen, pr, checks)
	if err != nil {
		return false, s.block(ctx, w, queue.CheckSources, err.Error(), "")
	}
	return met, nil
}

func retryPolicyFailure(expression condition.Expression, prs []forge.PullRequest, checks []forge.CheckRun, name string) bool {
	for _, pr := range prs {
		values, err := policyValues(expression, pr, checks)
		if err == nil && expression.BlocksCheck(values, name) {
			return true
		}
	}
	return false
}

// retryPolicyFailureWithApprovals is the dormant approval-aware counterpart
// to retryPolicyFailure. It keeps retry targeting tied to the full nested
// expression, so a failed check is retried only when it blocks the expression
// after the current review evidence is applied.
func retryPolicyFailureWithApprovals(expression condition.Expression, prs []forge.PullRequest, checks []forge.CheckRun, name string, approvals *ApprovalEvidence) bool {
	if approvals == nil {
		return retryPolicyFailure(expression, prs, checks, name)
	}
	for _, pr := range prs {
		values, err := policyValuesWithApprovals(expression, pr, checks, approvals)
		if err != nil {
			continue
		}
		outcome, err := mergeResultWithApprovals(expression, pr, checks, approvals)
		if err == nil && outcome.Terminal && !outcome.Success && expression.BlocksCheck(values, name) {
			return true
		}
	}
	return false
}
