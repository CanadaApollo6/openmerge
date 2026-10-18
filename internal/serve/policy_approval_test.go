package serve

import (
	"reflect"
	"strings"
	"testing"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func parsePolicyExpression(t *testing.T, text string) condition.Expression {
	t.Helper()
	expression, err := condition.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	return expression
}

func policyPR() forge.PullRequest {
	return forge.PullRequest{
		HeadSHA: "head", Base: "main", HeadBranch: "feature",
		ConflictKnown: true, Conflict: false,
	}
}

func policyPRWithApproval(sha string, conflictKnown bool) forge.PullRequest {
	pr := policyPR()
	pr.ConflictKnown = conflictKnown
	if sha != "" {
		pr.Approvals = []forge.Approval{{User: "reviewer", SHA: sha}}
	}
	return pr
}

func policyCheck(state forge.CheckState) []forge.CheckRun {
	return []forge.CheckRun{{ID: 1, Name: "ci", Source: "app:ci", State: state}}
}

func TestApprovalAwarePolicyPreservesLegacyNoReviewExpressions(t *testing.T) {
	tests := []struct {
		name   string
		expr   condition.Expression
		checks []forge.CheckRun
	}{
		{"success", parsePolicyExpression(t, "check-success = ci"), policyCheck(forge.Success)},
		{"failure", parsePolicyExpression(t, "check-success = ci"), policyCheck(forge.Failure)},
		{"pending", parsePolicyExpression(t, "check-success = ci"), policyCheck(forge.Pending)},
		{"nested all", condition.All(
			parsePolicyExpression(t, "check-success = ci"),
			condition.Not(parsePolicyExpression(t, "draft")),
		), policyCheck(forge.Success)},
		{"nested any", condition.Any(
			parsePolicyExpression(t, "check-success = ci"),
			parsePolicyExpression(t, "conflict"),
		), policyCheck(forge.Failure)},
		{"nested not", condition.Not(condition.Any(
			parsePolicyExpression(t, "check-failure = ci"),
			parsePolicyExpression(t, "draft"),
		)), policyCheck(forge.Failure)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := policyPR()
			legacyTerminal, legacySuccess, err := mergeResult(tt.expr, pr, tt.checks)
			if err != nil {
				t.Fatal(err)
			}
			for _, approvals := range []*ApprovalEvidence{
				{Known: true}, // observed known-empty review evidence
				{},            // unresolved eligibility remains irrelevant here
				nil,           // explicit legacy opt-out
			} {
				got, err := mergeResultWithApprovals(tt.expr, pr, tt.checks, approvals)
				if err != nil {
					t.Fatal(err)
				}
				want := PolicyOutcome{Terminal: legacyTerminal, Success: legacySuccess}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("outcome = %+v, want %+v", got, want)
				}
			}
		})
	}
}

func TestApprovalAwarePolicyNilOptInPreservesReviewAndMetadata(t *testing.T) {
	ci := parsePolicyExpression(t, "check-success = ci")
	review := parsePolicyExpression(t, "approved-reviews-by = reviewer")
	conflict := parsePolicyExpression(t, "conflict")
	currentHead := policyPR().HeadSHA
	unknownConflict := policyPRWithApproval(currentHead, false)
	tests := []struct {
		name       string
		expression condition.Expression
		pr         forge.PullRequest
		checks     []forge.CheckRun
	}{
		{"all missing approval", condition.All(ci, review), policyPRWithApproval("", true), policyCheck(forge.Success)},
		{"all current approval", condition.All(ci, review), policyPRWithApproval(currentHead, true), policyCheck(forge.Success)},
		{"all wrong-head approval", condition.All(ci, review), policyPRWithApproval("old-head", true), policyCheck(forge.Success)},
		{"any missing approval", condition.Any(ci, review), policyPRWithApproval("", true), policyCheck(forge.Failure)},
		{"any current approval", condition.Any(ci, review), policyPRWithApproval(currentHead, true), policyCheck(forge.Failure)},
		{"any wrong-head approval", condition.Any(ci, review), policyPRWithApproval("old-head", true), policyCheck(forge.Failure)},
		{"not missing approval", condition.Not(review), policyPRWithApproval("", true), nil},
		{"not current approval", condition.Not(review), policyPRWithApproval(currentHead, true), nil},
		{"not wrong-head approval", condition.Not(review), policyPRWithApproval("old-head", true), nil},
		{"not all current approval", condition.Not(condition.All(ci, review)), policyPRWithApproval(currentHead, true), policyCheck(forge.Success)},
		{"not any wrong-head approval", condition.Not(condition.Any(ci, review)), policyPRWithApproval("old-head", true), policyCheck(forge.Failure)},
		{"known other metadata", condition.All(ci, review, conflict), policyPRWithApproval(currentHead, true), policyCheck(forge.Success)},
		{"unknown other metadata", condition.All(ci, review, conflict), unknownConflict, policyCheck(forge.Success)},
		{"unknown other metadata in alternative", condition.Any(ci, review, conflict), unknownConflict, policyCheck(forge.Failure)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			legacyTerminal, legacySuccess, err := mergeResult(tt.expression, tt.pr, tt.checks)
			if err != nil {
				t.Fatal(err)
			}
			got, err := mergeResultWithApprovals(tt.expression, tt.pr, tt.checks, nil)
			if err != nil {
				t.Fatal(err)
			}
			want := PolicyOutcome{Terminal: legacyTerminal, Success: legacySuccess}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("nil opt-in outcome = %+v, legacy pair = %+v", got, want)
			}
		})
	}
}

func TestApprovalAwarePolicyWaitsAfterCIWithoutReview(t *testing.T) {
	expression := condition.All(
		parsePolicyExpression(t, "check-success = ci"),
		parsePolicyExpression(t, "approved-reviews-by = reviewer"),
	)
	approvals := &ApprovalEvidence{Known: true}
	outcome, err := mergeResultWithApprovals(expression, policyPR(), policyCheck(forge.Success), approvals)
	if err != nil {
		t.Fatal(err)
	}
	if want := (PolicyOutcome{WaitingForApproval: true}); !reflect.DeepEqual(outcome, want) {
		t.Fatalf("outcome = %+v, want %+v", outcome, want)
	}
	if retryPolicyFailureWithApprovals(expression, []forge.PullRequest{policyPR()}, policyCheck(forge.Success), "ci", approvals) {
		t.Fatal("passing CI with missing review selected a retry")
	}
}

func TestApprovalAwarePolicyRetriesRealCIFailureWithMissingReview(t *testing.T) {
	expression := condition.All(
		parsePolicyExpression(t, "check-success = ci"),
		parsePolicyExpression(t, "approved-reviews-by = reviewer"),
	)
	// Unknown eligibility is deliberately not represented as a known empty
	// list. A real failed CI branch still remains terminal and retryable.
	approvals := &ApprovalEvidence{}
	outcome, err := mergeResultWithApprovals(expression, policyPR(), policyCheck(forge.Failure), approvals)
	if err != nil {
		t.Fatal(err)
	}
	want := PolicyOutcome{Terminal: true}
	if !reflect.DeepEqual(outcome, want) {
		t.Fatalf("outcome = %+v, want %+v", outcome, want)
	}
	if !retryPolicyFailureWithApprovals(expression, []forge.PullRequest{policyPR()}, policyCheck(forge.Failure), "ci", approvals) {
		t.Fatal("real CI failure was not selected for retry")
	}
}

func TestApprovalAwarePolicyPreservesNestedReviewSemantics(t *testing.T) {
	review := parsePolicyExpression(t, "approved-reviews-by = reviewer")
	ci := parsePolicyExpression(t, "check-success = ci")
	draft := parsePolicyExpression(t, "draft")
	tests := []struct {
		name       string
		expression condition.Expression
		checks     []forge.CheckRun
		approvals  ApprovalEvidence
		want       PolicyOutcome
		wantRetry  bool
	}{
		{
			name:       "any can wait for review after a failed alternative",
			expression: condition.Any(ci, review),
			checks:     policyCheck(forge.Failure),
			approvals:  ApprovalEvidence{},
			want:       PolicyOutcome{WaitingForApproval: true},
		},
		{
			name:       "any known-empty review alternative does not retry CI",
			expression: condition.Any(ci, review),
			checks:     policyCheck(forge.Failure),
			approvals:  ApprovalEvidence{Known: true},
			want:       PolicyOutcome{WaitingForApproval: true},
			wantRetry:  false,
		},
		{
			name:       "any ignores unknown CI below a definitely-false all branch",
			expression: condition.Any(condition.All(draft, ci), review),
			checks:     policyCheck(forge.Pending),
			approvals:  ApprovalEvidence{Known: true},
			want:       PolicyOutcome{WaitingForApproval: true},
		},
		{
			name:       "negated any keeps the same review-only cause",
			expression: condition.Not(condition.Any(condition.All(draft, ci), review)),
			checks:     policyCheck(forge.Pending),
			approvals:  ApprovalEvidence{},
			want:       PolicyOutcome{WaitingForApproval: true},
		},
		{
			name:       "negated all preserves the definite false branch",
			expression: condition.Not(condition.All(condition.All(draft, ci), review)),
			checks:     policyCheck(forge.Pending),
			approvals:  ApprovalEvidence{},
			want:       PolicyOutcome{Terminal: true, Success: true},
		},
		{
			name:       "not keeps unknown eligibility unknown",
			expression: condition.Not(review),
			approvals:  ApprovalEvidence{},
			want:       PolicyOutcome{WaitingForApproval: true},
		},
		{
			name:       "known empty keeps normal negation",
			expression: condition.Not(review),
			approvals:  ApprovalEvidence{Known: true},
			want:       PolicyOutcome{Terminal: true, Success: true},
		},
		{
			name:       "not can pass from a definite CI failure",
			expression: condition.Not(condition.All(ci, review)),
			checks:     policyCheck(forge.Failure),
			approvals:  ApprovalEvidence{},
			want:       PolicyOutcome{Terminal: true, Success: true},
		},
		{
			name:       "pending CI is not an approval-only wait",
			expression: condition.All(ci, review),
			checks:     policyCheck(forge.Pending),
			approvals:  ApprovalEvidence{Known: true},
			want:       PolicyOutcome{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mergeResultWithApprovals(tt.expression, policyPR(), tt.checks, &tt.approvals)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("outcome = %+v, want %+v", got, tt.want)
			}
			gotRetry := retryPolicyFailureWithApprovals(tt.expression, []forge.PullRequest{policyPR()}, tt.checks, "ci", &tt.approvals)
			if gotRetry != tt.wantRetry {
				t.Fatalf("retry = %v, want %v", gotRetry, tt.wantRetry)
			}
		})
	}
}

func TestApprovalAwarePolicyLeavesPendingOrMissingCIUnknownWithMissingReview(t *testing.T) {
	ci := parsePolicyExpression(t, "check-success = ci")
	review := parsePolicyExpression(t, "approved-reviews-by = reviewer")
	expressions := []struct {
		name       string
		expression condition.Expression
	}{
		{"all", condition.All(ci, review)},
		{"any", condition.Any(ci, review)},
		{"not all", condition.Not(condition.All(ci, review))},
		{"not any", condition.Not(condition.Any(ci, review))},
	}
	checks := []struct {
		name   string
		checks []forge.CheckRun
	}{
		{"pending CI", policyCheck(forge.Pending)},
		{"missing CI", nil},
	}
	for _, checkCase := range checks {
		for _, expressionCase := range expressions {
			t.Run(checkCase.name+"/"+expressionCase.name, func(t *testing.T) {
				got, err := mergeResultWithApprovals(expressionCase.expression, policyPR(), checkCase.checks, &ApprovalEvidence{})
				if err != nil {
					t.Fatal(err)
				}
				if got != (PolicyOutcome{}) {
					t.Fatalf("outcome = %+v, want non-terminal unknown", got)
				}
				if retryPolicyFailureWithApprovals(expressionCase.expression, []forge.PullRequest{policyPR()}, checkCase.checks, "ci", &ApprovalEvidence{}) {
					t.Fatal("unresolved CI was selected for retry")
				}
			})
		}
	}
}

func TestApprovalAwarePolicyDoesNotHideOtherUnknownInputs(t *testing.T) {
	expression := condition.All(
		parsePolicyExpression(t, "conflict"),
		parsePolicyExpression(t, "approved-reviews-by = reviewer"),
	)
	outcome, err := mergeResultWithApprovals(expression, forge.PullRequest{}, nil, &ApprovalEvidence{Known: true})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.WaitingForApproval || outcome.Terminal || outcome.Success {
		t.Fatalf("other unknown input was classified as approval wait: %+v", outcome)
	}
}

func approvalQueueEligibility(basis queue.ApprovalBasis) queue.ApprovalEligibilityObserved {
	return queue.ApprovalEligibilityObserved{
		Basis: basis, Actors: map[string]cp.Actor{}, Groups: map[string]cp.IdentitySet{},
		OwnerPools: map[string]cp.IdentitySet{}, Known: true, Reasons: []string{},
	}
}

func approvalQueueTestingState(t *testing.T) (queue.State, queue.Config, queue.ApprovalBasis, time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	base := strings.Repeat("a", 40)
	head := strings.Repeat("b", 40)
	tested := strings.Repeat("c", 40)
	changeID, err := queue.PRChangeID("7", "Owner/Repo", "main", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	basis := queue.ApprovalBasis{ChangeID: changeID, Revision: 1, Head: head, ReadID: "read-1", At: now}
	s := queue.Apply(queue.State{BaseBranch: "main"}, queue.BaseMoved{SHA: base})
	s = queue.Apply(s, queue.ChangeRevisionObserved{
		PR: 1, ChangeID: changeID, Revision: 1, HeadSHA: head,
		Basis: queue.RevisionObservationBasis{BaseSHA: base, At: now.Add(-time.Hour)},
	})
	s = queue.Apply(s, queue.PRObserved{PR: 1, HeadSHA: head, Eligible: true, Trusted: true})
	s = queue.Apply(s, queue.EnqueueRequested{PR: 1, At: now})
	s = queue.Apply(s, queue.Intent(basis, "title", "body"))
	s = queue.Apply(s, approvalQueueEligibility(basis))
	cfg := queue.Config{BatchMaxSize: 1, MergeMethod: queue.Merge, CheckTimeout: time.Minute, ChecksRetryFailed: 1}
	var build queue.BuildTestRef
	for _, action := range queue.Plan(cfg, s, now) {
		if candidate, ok := action.(queue.BuildTestRef); ok {
			build = candidate
			break
		}
	}
	if build.BatchID == "" {
		t.Fatal("queue fixture did not plan a build")
	}
	s = queue.Apply(s, queue.BatchStarted{BatchID: build.BatchID, BaseSHA: build.BaseSHA, PRs: build.PRs, At: now})
	s = queue.Apply(s, queue.TestRefBuilt{BatchID: build.BatchID, TestedSHA: tested, Method: queue.Merge, At: now})
	if s.Active == nil || s.Active.ID != build.BatchID || s.Active.TestedSHA != tested {
		t.Fatalf("queue fixture did not enter testing: %+v", s.Active)
	}
	return s, cfg, basis, now
}

func approvalWaitForbiddenAction(action queue.Action) bool {
	switch action.(type) {
	case queue.BuildTestRef, queue.BuildChainedTestRef, queue.RefreshChecks,
		queue.RecordCheckRerun, queue.RerunChecks, queue.ConfirmCheckFailure,
		queue.FailBatch, queue.DiagnoseBatch, queue.Dequeue, queue.InvalidateBatches:
		return true
	default:
		return false
	}
}

func TestApprovalAwarePolicyApprovalWaitReducerDoesNotReenterCIPlan(t *testing.T) {
	expression := condition.All(
		parsePolicyExpression(t, "check-success = ci"),
		parsePolicyExpression(t, "approved-reviews-by = reviewer"),
	)
	policyOutcome, err := mergeResultWithApprovals(expression, policyPR(), policyCheck(forge.Success), &ApprovalEvidence{Known: true})
	if err != nil {
		t.Fatal(err)
	}
	if !policyOutcome.WaitingForApproval || policyOutcome.Terminal || policyOutcome.Success {
		t.Fatalf("passing CI did not produce an approval wait: %+v", policyOutcome)
	}

	s, cfg, basis, now := approvalQueueTestingState(t)
	batch := s.Active
	rejection := queue.ApprovalGateRejected{
		BatchID: batch.ID, TestedSHA: batch.TestedSHA, PRs: append([]queue.BatchPR(nil), batch.PRs...),
		Waiting: []queue.ApprovalWait{{Basis: basis, Fingerprint: s.ApprovalFingerprint(basis.ChangeID), Reasons: []string{"approval-required"}}},
	}
	s = queue.Apply(s, rejection)
	if s.Active == nil || s.Active.InvalidationReason != "approval-wait" || len(s.ApprovalWaits()) != 1 {
		t.Fatalf("approval rejection was not reduced to a wait: active=%+v waits=%+v", s.Active, s.ApprovalWaits())
	}
	var invalidation queue.InvalidateBatches
	for _, action := range queue.Plan(cfg, s, now.Add(24*time.Hour)) {
		if candidate, ok := action.(queue.InvalidateBatches); ok {
			invalidation = candidate
			break
		}
	}
	if len(invalidation.BatchIDs) == 0 {
		t.Fatal("approval rejection did not plan durable invalidation")
	}
	s = queue.Apply(s, queue.BatchesInvalidated(invalidation))
	if len(s.Batches()) != 0 || len(s.Entries) != 1 || len(s.ApprovalWaits()) != 1 || s.Entries[0].DequeueRequested {
		t.Fatalf("approval wait changed queue admission: batches=%v entries=%v waits=%v", s.Batches(), s.Entries, s.ApprovalWaits())
	}
	for i := 0; i < 5; i++ {
		for _, action := range queue.Plan(cfg, s, now.Add(24*time.Hour+time.Duration(i)*time.Hour)) {
			if approvalWaitForbiddenAction(action) {
				t.Fatalf("elapsed approval wait planned %T: %#v", action, action)
			}
		}
		if len(s.Batches()) != 0 || len(s.ApprovalWaits()) != 1 || s.Entries[0].DequeueRequested {
			t.Fatal("elapsed planning changed approval wait state")
		}
	}
}

func TestApprovalAwarePolicyFailedCIUsesIdentifiedBoundedRetry(t *testing.T) {
	expression := condition.All(
		parsePolicyExpression(t, "check-success = ci"),
		parsePolicyExpression(t, "approved-reviews-by = reviewer"),
	)
	approvals := &ApprovalEvidence{}
	policyOutcome, err := mergeResultWithApprovals(expression, policyPR(), policyCheck(forge.Failure), approvals)
	if err != nil {
		t.Fatal(err)
	}
	if !policyOutcome.Terminal || policyOutcome.Success || policyOutcome.WaitingForApproval ||
		!retryPolicyFailureWithApprovals(expression, []forge.PullRequest{policyPR()}, policyCheck(forge.Failure), "ci", approvals) {
		t.Fatalf("failed CI was not classified as a bounded retry candidate: %+v", policyOutcome)
	}

	s, cfg, _, now := approvalQueueTestingState(t)
	batch := s.Active
	failed := queue.CheckRun{ID: 7, Attempt: 1, Name: "ci", Source: "app:ci", State: "failure"}
	deadline := batch.StartedTestingAt.Add(cfg.CheckTimeout)
	s = queue.Apply(s, queue.ChecksObserved{
		BatchID: batch.ID, TestedSHA: batch.TestedSHA, Runs: []queue.CheckRun{failed},
		Terminal: true, Success: false, OriginalDeadline: deadline, Timeout: cfg.CheckTimeout,
	})
	var request queue.RecordCheckRerun
	for _, action := range queue.Plan(cfg, s, now) {
		if candidate, ok := action.(queue.RecordCheckRerun); ok {
			request = candidate
			break
		}
	}
	if request.BatchID == "" || len(request.Runs) != 1 || request.Runs[0].ID != failed.ID {
		t.Fatalf("identified failed CI did not consume the retry budget: %+v", request)
	}
	requestAt := now.Add(time.Second)
	timeout := request.Timeout
	if timeout == 0 {
		timeout = queue.DefaultCheckTimeout
	}
	s = queue.Apply(s, queue.CheckRerunRequested{
		BatchID: request.BatchID, TestedSHA: request.TestedSHA, Generation: request.Generation,
		Runs: request.Runs, OriginalDeadline: request.OriginalDeadline, At: requestAt,
		Deadline: requestAt.Add(timeout), Timeout: request.Timeout,
	})
	var dispatch queue.RerunChecks
	for _, action := range queue.Plan(cfg, s, requestAt) {
		if candidate, ok := action.(queue.RerunChecks); ok {
			dispatch = candidate
			break
		}
	}
	if dispatch.BatchID == "" {
		t.Fatal("identified retry was not dispatched")
	}
	s = queue.Apply(s, queue.CheckRerunDispatched{BatchID: dispatch.BatchID, TestedSHA: dispatch.TestedSHA, Generation: dispatch.Generation})
	secondFailure := failed
	secondFailure.ID = 8
	s = queue.Apply(s, queue.ChecksObserved{
		BatchID: batch.ID, TestedSHA: batch.TestedSHA, Runs: []queue.CheckRun{secondFailure},
		Terminal: true, Success: false, OriginalDeadline: deadline, Timeout: cfg.CheckTimeout,
	})
	var confirmation queue.ConfirmCheckFailure
	for _, action := range queue.Plan(cfg, s, requestAt.Add(time.Second)) {
		switch candidate := action.(type) {
		case queue.RecordCheckRerun, queue.RerunChecks:
			t.Fatalf("bounded retry planned another CI attempt: %#v", candidate)
		case queue.ConfirmCheckFailure:
			confirmation = candidate
		}
	}
	if confirmation.BatchID == "" || confirmation.Reason != queue.ChecksFailed {
		t.Fatalf("failed rerun was not confirmed with checks-failed: %+v", confirmation)
	}
}
