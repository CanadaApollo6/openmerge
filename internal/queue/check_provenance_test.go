package queue

import (
	"strings"
	"testing"
)

func TestCheckProvenanceIsDiagnosticAndBatchScoped(t *testing.T) {
	s := testingState()
	checks := []UnboundCheck{{Name: "ci", Source: "app:11"}}
	event := CheckProvenanceObserved{BatchID: s.Active.ID, TestedSHA: s.Active.TestedSHA, Checks: checks}
	next := Apply(s, event)
	checks[0].Name = "mutated"
	if s.CheckProvenanceDetail() != "" || !strings.Contains(next.CheckProvenanceDetail(), `required check "ci"`) || next.ChecksPassed(next.Active.TestedSHA) {
		t.Fatal("diagnostic mutated its inputs or authorized checks")
	}
	observed := next.UnboundChecks()
	observed[0].Name = "mutated"
	if strings.Contains(next.CheckProvenanceDetail(), "mutated") {
		t.Fatal("diagnostic accessor leaked mutable state")
	}
	for _, stale := range []CheckProvenanceObserved{
		{BatchID: "other", TestedSHA: next.Active.TestedSHA},
		{BatchID: next.Active.ID, TestedSHA: "other"},
	} {
		if Apply(next, stale).CheckProvenanceDetail() != next.CheckProvenanceDetail() {
			t.Fatal("stale diagnostic changed active explanation")
		}
	}
	cleared := Apply(next, CheckProvenanceObserved{BatchID: next.Active.ID, TestedSHA: next.Active.TestedSHA})
	if cleared.CheckProvenanceDetail() != "" || next.CheckProvenanceDetail() == "" {
		t.Fatal("recovery did not clear only its own projection")
	}
	dequeued := Apply(next, Dequeued{PR: next.Entries[0].PR, AdmissionID: next.Entries[0].AdmissionID, BatchID: next.Active.ID, Reason: ChecksTimeout})
	for _, action := range Plan(testConfig, dequeued, epoch) {
		if report, ok := action.(ReportStatus); ok && report.Reason == string(ChecksTimeout) {
			if !strings.Contains(report.Detail, "checks-timeout: unbound-check-provenance:") || !strings.Contains(report.Detail, "head_branch=null") {
				t.Fatalf("durable timeout report lost evidence: %+v", report)
			}
			return
		}
	}
	t.Fatal("missing timeout report")
}
