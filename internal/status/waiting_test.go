package status

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestWaitingReasonPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name string
		q    Queue
		e    Entry
		code WaitingCode
		text string
	}{
		{"landed survives pause", Queue{Paused: true}, Entry{Status: "merged"}, WaitingDone, "Landed"},
		{"closed", Queue{}, Entry{Status: "closed"}, WaitingDone, "closed"},
		{"landing survives pause", Queue{Paused: true, Chain: []*Batch{{Phase: "landing", PRs: []BatchPR{{PR: 1}}}}}, Entry{PR: 1, Status: "paused"}, WaitingLanding, "Landing in progress"},
		{"dequeued survives pause", Queue{Paused: true}, Entry{Status: "dequeued", Reason: "checks-failed"}, WaitingDequeued, "checks-failed"},
		{"conflict", Queue{}, Entry{Status: "dequeued", Reason: "conflict"}, WaitingConflict, "base or another queued change"},
		{"pending dequeue", Queue{}, Entry{Status: "dequeue-pending", Reason: "requested"}, WaitingDequeued, "Dequeue pending"},
		{"retired candidate", Queue{State: "retired"}, Entry{Status: "not-queued"}, WaitingPaused, "retired"},
		{"empty pause reason", Queue{Paused: true}, Entry{Position: 1}, WaitingPaused, "Queue paused"},
		{"paused", Queue{Paused: true, PauseReason: "manual"}, Entry{Position: 1}, WaitingPaused, "manual"},
		{"readiness", Queue{Readiness: &Readiness{Reason: "config-unavailable"}}, Entry{Position: 1}, WaitingOperator, "config-unavailable"},
		{"invalidating", Queue{}, Entry{Status: "invalidating", Reason: "base-moved"}, WaitingQueue, "base-moved"},
		{"build", Queue{}, Entry{Status: "building", BatchID: "b2"}, WaitingBuild, "b2"},
		{"checks", Queue{}, Entry{Status: "testing", BatchID: "b2"}, WaitingChecks, "CI on batch b2"},
		{"preflight", Queue{}, Entry{Status: "testing", BatchID: "b2", Gate: "landing-preflight"}, WaitingLanding, "preflight"},
		{"chain", Queue{}, Entry{Status: "testing", BatchID: "b2", BatchPosition: 2, Gate: "predecessor-settlement"}, WaitingQueue, "position 2 behind 1 batches"},
		{"approval before queue", Queue{}, Entry{Position: 3, Change: &ChangeReport{WaitingReasons: []string{"approval-required"}}}, WaitingApprovals, "required approval evidence"},
		{"approval before trust", Queue{}, Entry{Status: "blocked", Reason: "untrusted", Change: &ChangeReport{WaitingReasons: []string{"approval-required"}}}, WaitingApprovals, "required approval evidence"},
		{"approval before entry", Queue{}, Entry{Status: "blocked", Reason: "not-eligible", Change: &ChangeReport{WaitingReasons: []string{"approval-required"}}}, WaitingApprovals, "required approval evidence"},
		{"trust", Queue{}, Entry{Status: "blocked", Reason: "untrusted"}, WaitingTrust, "exact-head"},
		{"entry", Queue{}, Entry{Status: "blocked", Reason: "not-eligible"}, WaitingEntry, "not-eligible"},
		{"request", Queue{}, Entry{Status: "not-queued"}, WaitingOperator, "queue request"},
		{"queue", Queue{}, Entry{Position: 4}, WaitingQueue, "behind 3 changes"},
		{"timer", Queue{}, Entry{Position: 1, Gate: "max_wait"}, WaitingQueue, "batch fill timer"},
		{"window", Queue{}, Entry{Position: 1, Gate: "window-full"}, WaitingQueue, "window full"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := DeriveWaitingReason(&tc.q, tc.e)
			if tc.name == "empty pause reason" && got.Text != "Queue paused" {
				t.Fatalf("got %q", got.Text)
			}
			if got.Code != tc.code || !strings.Contains(got.Text, tc.text) {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestWaitingCheckEvidence(t *testing.T) {
	runs := []Run{
		{BatchID: "old", Outcome: "failure", Identity: &queue.CheckRun{Name: "obsolete"}},
		{BatchID: "b2", Outcome: "failure", producerKey: "ci", Identity: &queue.CheckRun{ID: 1, Name: "old attempt"}},
		{BatchID: "b2", Outcome: "pending", producerKey: "ci", Identity: &queue.CheckRun{ID: 2, Name: "ci", URL: "https://example.test/run/2"}},
		{BatchID: "b2", Outcome: "success", producerKey: "lint", Identity: &queue.CheckRun{ID: 3, Name: "lint"}},
	}
	e := Entry{Status: "testing", BatchID: "b2", RunLedger: runs}
	got := DeriveWaitingReason(&Queue{}, e)
	if strings.Join(got.Details, ",") != "ci: pending" || got.URL != "https://example.test/run/2" {
		t.Fatalf("got %+v", got)
	}
	e.Status, e.Reason, e.BatchID = "dequeued", "checks-failed", ""
	e.RunLedger[2].Outcome = "failure"
	got = DeriveWaitingReason(&Queue{}, e)
	if len(got.Details) != 1 || got.Details[0] != "ci: failure" {
		t.Fatalf("got %+v", got)
	}
	e.RunLedger = []Run{{BatchID: "b2", Outcome: "failure"}}
	got = DeriveWaitingReason(&Queue{}, e)
	if got.URL != "" || !strings.Contains(WaitingSummary(got), "identity unavailable") {
		t.Fatalf("got %+v", got)
	}
}

func TestWaitingProjectionAndPolicy(t *testing.T) {
	q, e, _ := Project(journal(testingEvents()...), 1, 0, nil)
	if e.WaitingReason.Code != WaitingChecks {
		t.Fatalf("got %+v", e.WaitingReason)
	}
	q.Readiness = &Readiness{Reason: "config-unavailable"}
	ApplyPolicy(&q, e, queue.Config{}, time.Time{})
	for _, entry := range []*Entry{e, q.LedgerEntry(1), &q.Entries[0]} {
		raw, err := json.Marshal(entry)
		if err != nil || !strings.Contains(string(raw), `"waiting_reason":{"code":"operator-action"`) {
			t.Fatalf("%s: %v", raw, err)
		}
	}
}

func TestWaitingCheckEscaping(t *testing.T) {
	e := Entry{BatchID: "b1", Detail: "CI <script>\n[link]" + strings.Repeat("*", 2000)}
	got := WaitingCheckText(&Queue{}, e, nil)
	if strings.Contains(got, "<script>") || strings.Contains(got, "\n") || !strings.Contains(got, `\[link\]`) || !strings.Contains(got, "…") {
		t.Fatalf("unsafe or unbounded: %q", got)
	}
}

func TestWaitingCheckRedactsBeforeFormatting(t *testing.T) {
	for _, secret := range []string{"opaque_secret*`value`", strings.Repeat("z", 1300) + "_tail*`"} {
		sanitizer := diagnostic.NewSanitizer(secret)
		e := Entry{BatchID: "b1", Detail: secret}
		got := WaitingCheckText(&Queue{}, e, sanitizer)
		if !strings.Contains(got, "REDACTED") || strings.Contains(got, "opaque") || strings.Contains(got, "zzzz") || strings.Contains(got, "tail") {
			t.Fatalf("secret survived formatting: %q", got)
		}
	}
}

func TestWaitingCheckReviewerBriefUsesSharedSanitizer(t *testing.T) {
	secret := "opaque_secret*`value`"
	sanitizer := diagnostic.NewSanitizer(secret)
	j, _, basis, _ := briefRegressionBase(t)
	j.Events[1].Event = queue.Intent(basis, secret, secret)
	q, _, _ := Project(j, 0, 0, sanitizer)
	text, err := ReviewerCheckSummaryFromProjection(j, &q, 1, cp.Policy{Approvals: &cp.Approvals{Default: cp.Requirement{Selector: cp.Human, Count: 1}}}, nil)
	if err != nil || !strings.Contains(text, "Claimed intent:") || !strings.Contains(text, "REDACTED") || strings.Contains(text, "opaque") || strings.Contains(text, "value") {
		t.Fatalf("unsafe brief: %s; %v", text, err)
	}
}
