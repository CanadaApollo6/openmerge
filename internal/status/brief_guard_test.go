package status

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestReviewerCheckGuardsEligibilityAfterConfigApplied(t *testing.T) {
	j, policy, eligibility := eligibilityJournal(t)
	appendEligibilityEvents(&j, eligibility)
	before, err := ReviewerCheckSummary(j, 12, policy)
	if err != nil || !strings.Contains(before, "met=true") {
		t.Fatalf("fixture not approved: %v %s", err, before)
	}
	appendEligibilityEvents(&j, queue.ConfigApplied{Commit: strings.Repeat("e", 40), Digest: strings.Repeat("e", 64)})
	eligibility.Basis.ReadID = "after-config"
	appendEligibilityEvents(&j, eligibility)
	for _, reuse := range []bool{false, true} {
		var summary string
		if reuse {
			q, _, _ := Project(j, 0, 0, nil)
			summary, err = ReviewerCheckSummaryFromProjection(j, &q, 12, policy, nil)
		} else {
			summary, err = ReviewerCheckSummary(j, 12, policy)
		}
		if err != nil || strings.Contains(summary, "met=true") || !strings.Contains(summary, "met=false") || !strings.Contains(summary, "eligibility") {
			t.Fatalf("reuse=%t: old policy credited new eligibility: %v %s", reuse, err, summary)
		}
	}
}

type guardPolicySource struct {
	source
	policy   cp.Policy
	err      error
	failCall int
	calls    int
}

func (s *guardPolicySource) BriefPolicy(context.Context, store.QueueKey, BriefPolicyRequest) (cp.Policy, error) {
	s.calls++
	if s.calls == s.failCall {
		return cp.Policy{}, s.err
	}
	return s.policy, nil
}

func TestBriefComponentGuardErrorLabels(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"busy", diagnostic.BusyError("cache busy"), "policy_busy"},
		{"canceled", fmt.Errorf("local read: %w", context.Canceled), "policy_busy"},
		{"deadline", fmt.Errorf("local read: %w", context.DeadlineExceeded), "policy_busy"},
		{"unavailable", errors.New("local object missing"), "eligibility_stale"},
		{"mismatch", queue.BriefPolicyMismatchError{Recorded: "old"}, "eligibility_stale"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j, policy, eligibility := eligibilityJournal(t)
			appendEligibilityEvents(&j, queue.ConfigApplied{Commit: strings.Repeat("e", 40), Digest: strings.Repeat("e", 64)}, eligibility)
			q, _, _ := Project(j, 0, 0, nil)
			facts, _ := q.state.ApprovalFacts(eligibility.Basis.ChangeID)
			src := &guardPolicySource{policy: policy, err: tc.err, failCall: 2}
			f, code := briefPolicyProjection(t.Context(), src, j, &q)(q.state, facts.Intent.Basis)
			if src.calls != 2 {
				t.Fatalf("guard not exercised: %d calls", src.calls)
			}
			if tc.code == "policy_busy" {
				if code != tc.code {
					t.Fatalf("got %q, want %q", code, tc.code)
				}
			} else if code != "" || len(f.Obligations) == 0 || f.Obligations[0].Code != tc.code {
				t.Fatalf("got code=%q obligations=%+v", code, f.Obligations)
			}
			for _, obligation := range f.Obligations {
				if obligation.Met {
					t.Fatal("error exposed met obligation")
				}
			}
			if code != "" && f.Classification != nil {
				t.Fatal("busy result exposed a reconstructed fact")
			}
		})
	}
}

func TestBriefHistoricalPolicyContextErrorsAreBusy(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		j, policy, eligibility := eligibilityJournal(t)
		q, _, _ := Project(j, 0, 0, nil)
		src := &guardPolicySource{policy: policy, err: err, failCall: 1}
		f, code := briefPolicyProjection(t.Context(), src, j, &q)(q.state, eligibility.Basis)
		if code != "policy_busy" || len(f.Obligations) != 0 || f.Classification != nil {
			t.Fatalf("got %q %+v", code, f)
		}
	}
}

func TestReviewerCheckUnknownPolicyPinIsStale(t *testing.T) {
	j, policy, eligibility := eligibilityJournal(t)
	appendEligibilityEvents(&j, eligibility)
	q, _, _ := Project(j, 0, 0, nil)
	// Simulate a projection without the classification's config mapping. The
	// seed digest equals the current digest, so digest comparison cannot hide
	// an unknown historical governing commit.
	q.policyPins = nil
	j.Seed.ConfigDigest = q.state.ConfigDigest
	j.Seed.ConfigCommit = q.state.ConfigCommit
	for _, known := range []bool{true, false} {
		t.Run(fmt.Sprint("known=", known), func(t *testing.T) {
			journal := j
			if !known {
				journal.Seed.ConfigCommit = strings.Repeat("e", 40)
			}
			stored, _ := q.state.Classification(eligibility.Basis.ChangeID, eligibility.Basis.Revision)
			_, pinKnown := recordedBriefPolicyPin(journal, &q, stored.Evidence)
			if pinKnown != known {
				t.Fatal("fixture did not exercise pinKnown")
			}
			summary, err := ReviewerCheckSummaryFromProjection(journal, &q, 12, policy, nil)
			if err != nil {
				t.Fatal(err)
			}
			if known {
				if !strings.Contains(summary, "met=true") {
					t.Fatal(summary)
				}
			} else if strings.Contains(summary, "met=true") || !strings.Contains(summary, "met=false") || !strings.Contains(summary, `eligibility\_stale`) {
				t.Fatalf("unknown pin credited approval: %s", summary)
			}
		})
	}
}
