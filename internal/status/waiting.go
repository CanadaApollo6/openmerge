package status

import (
	"fmt"
	"slices"
	"strings"
)

// WaitingCode classifies the current presentation reason, never queue authority.
type WaitingCode string

// Waiting reason codes are stable presentation categories.
const (
	WaitingDependencies WaitingCode = "dependencies"
	WaitingDone         WaitingCode = "done"
	WaitingPaused       WaitingCode = "paused"
	WaitingApprovals    WaitingCode = "approvals"
	WaitingTrust        WaitingCode = "trust"
	WaitingEntry        WaitingCode = "entry-conditions"
	WaitingOperator     WaitingCode = "operator-action"
	WaitingQueue        WaitingCode = "queue"
	WaitingBuild        WaitingCode = "build"
	WaitingChecks       WaitingCode = "checks"
	WaitingConflict     WaitingCode = "conflict"
	WaitingDequeued     WaitingCode = "dequeued"
	WaitingLanding      WaitingCode = "landing"
)

// WaitingReason is derived solely from recorded projections and runtime gates.
// Details and URL remain absent when the recorded evidence cannot identify them.
type DependencyDiagnostic struct {
	Code   string `json:"code"`
	Source string `json:"source"`
}

type WaitingReason struct {
	Diagnostics []DependencyDiagnostic `json:"diagnostics,omitempty"`
	Code        WaitingCode            `json:"code"`
	Text        string                 `json:"text"`
	Details     []string               `json:"details,omitempty"`
	URL         string                 `json:"url,omitempty"`
}

// DeriveWaitingReason chooses one reason in precedence order. Terminal outcomes
// survive queue pauses; an in-flight landing still needs settlement during one.
func DeriveWaitingReason(q *Queue, e Entry) WaitingReason {
	return deriveWaitingReason(q, e, true)
}

func deriveWaitingReason(q *Queue, e Entry, position bool) WaitingReason {
	reason := func(code WaitingCode, text string) WaitingReason { return WaitingReason{Code: code, Text: text} }
	switch e.Status {
	case "merged":
		return reason(WaitingDone, "Landed")
	case "closed":
		return reason(WaitingDone, "PR is closed")
	}
	if e.Status == "landing" || e.Gate == "landing-settlement" || landingPR(q, e.PR) {
		return reason(WaitingLanding, "Landing in progress; resolving the durable landing attempt")
	}
	if e.Status == "dequeued" || e.Status == "dequeue-pending" {
		r := reason(WaitingDequeued, "Dequeued: "+e.Reason)
		if e.Status == "dequeue-pending" {
			r.Text = "Dequeue pending: " + e.Reason
		}
		if e.Reason == "conflict" {
			r.Code = WaitingConflict
			r.Text += "; conflict with the base or another queued change"
		}
		if e.Detail != "" {
			r.Details = append(r.Details, e.Detail)
		}
		if e.Reason == "checks-failed" || e.Reason == "checks-timeout" {
			addWaitingChecks(&r, e, true)
		}
		return r
	}
	if q.State != "" && q.State != "active" || q.Paused || e.Status == "paused" {
		r := reason(WaitingPaused, "Queue paused")
		if q.PauseReason != "" {
			r.Text += ": " + q.PauseReason
		}
		if q.State != "" && q.State != "active" {
			r.Text = "Queue is " + q.State
		}
		for _, b := range q.Blocks {
			r.Details = append(r.Details, b.Source+": "+b.Reason)
		}
		return r
	}
	if q.Readiness != nil && !q.Readiness.Ready {
		return reason(WaitingOperator, "Waiting for queue readiness: "+q.Readiness.Reason)
	}
	if e.Status == "invalidating" {
		return reason(WaitingQueue, "Waiting for batch invalidation: "+e.Reason)
	}
	if e.BatchID != "" {
		switch e.Gate {
		case "predecessor-settlement":
			return reason(WaitingQueue, fmt.Sprintf("Waiting at speculative chain position %d behind %d batches", e.BatchPosition, batchesAhead(e)))
		case "landing-preflight":
			return reason(WaitingLanding, "Waiting for landing preflight on batch "+e.BatchID)
		}
		if e.Status == "building" {
			return reason(WaitingBuild, "Building batch "+e.BatchID+" test ref")
		}
		r := reason(WaitingChecks, "Waiting for CI on batch "+e.BatchID)
		if e.Reason != "" {
			r.Details = append(r.Details, e.Reason)
		}
		if e.Detail != "" {
			r.Details = append(r.Details, e.Detail)
		}
		addWaitingChecks(&r, e, false)
		return r
	}
	// Exact-head approval waits record the admission decision; they explain
	// the missing evidence more precisely than generic trust/entry observations.
	if e.Change != nil && len(e.Change.WaitingReasons) > 0 {
		return WaitingReason{Code: WaitingApprovals, Text: "Waiting for required approval evidence", Details: slices.Clone(e.Change.WaitingReasons), URL: e.Change.BriefURL}
	}
	if e.Reason == "untrusted" {
		return reason(WaitingTrust, "Waiting for the exact-head trust gate")
	}
	if e.Status == "waiting-entry" {
		return reason(WaitingEntry, "Waiting for queue entry conditions: "+e.Detail)
	}
	if e.Status == "blocked" {
		return reason(WaitingEntry, "Waiting for queue entry conditions: "+e.Reason)
	}
	if e.Position == 0 {
		return reason(WaitingOperator, "Not queued; waiting for a queue request from a writer")
	}
	if e.Dependency != nil && e.dependencyWaiting {
		return *e.Dependency
	}
	text := fmt.Sprintf("In queue behind %d changes; waiting for the next test batch", max(0, e.Position-1))
	if !position {
		text = "In queue; waiting for the next test batch"
	}
	switch e.Gate {
	case "max_wait":
		text += " (batch fill timer)"
	case "window-full":
		text += " (speculative window full)"
	case "bisection":
		text += " (isolating failed changes)"
	case "predecessor-checks":
		text += " (predecessor checks)"
	case "predecessor-build":
		text += " (predecessor build)"
	}
	return reason(WaitingQueue, text)
}

func batchesAhead(e Entry) int {
	if e.BatchesAhead != nil {
		return *e.BatchesAhead
	}
	return max(0, int(e.BatchPosition)-1)
}

func landingPR(q *Queue, pr int) bool {
	for _, b := range q.Chain {
		if b.Phase == "landing" {
			for _, pin := range b.PRs {
				if pin.PR == pr {
					return true
				}
			}
		}
	}
	return false
}

func addWaitingChecks(r *WaitingReason, e Entry, failed bool) {
	for _, run := range e.RunLedger {
		if !currentBriefRun(run, e.RunLedger) || e.BatchID != "" && run.BatchID != e.BatchID {
			continue
		}
		if run.Outcome == "success" || run.Outcome == "skipped" || run.Outcome == "neutral" {
			continue
		}
		if failed && run.Outcome != "failure" && run.Outcome != "cancelled" && run.Outcome != "timed_out" {
			continue
		}
		name := "aggregate CI (check identity unavailable)"
		if run.Identity != nil {
			name = run.Identity.Name
			if r.URL == "" {
				r.URL = run.Identity.URL
			}
		}
		r.Details = append(r.Details, name+": "+run.Outcome)
	}
}

// WaitingSummary is a single line shared by human-facing status displays.
func WaitingSummary(r WaitingReason) string {
	return strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(waitingText(r))
}

func waitingText(r WaitingReason) string {
	text := r.Text
	if len(r.Details) > 0 {
		text += "; " + strings.Join(r.Details, "; ")
	}
	if r.URL != "" {
		text += "; " + r.URL
	}
	return text
}

func attachDependency(q *Queue, e *Entry) {
	if v, ok := q.dependencies[e.PR]; ok {
		e.dependencyWaiting = v.Blocking
		e.dependencyRedacted = v.Redacted
		e.Dependency = &WaitingReason{Code: WaitingDependencies, Text: v.Text, Details: slices.Clone(v.Details), URL: v.URL}
		if v.Redacted {
			maskDependencyEntry(e)
		}
		for _, d := range v.Diagnostics {
			e.Dependency.Diagnostics = append(e.Dependency.Diagnostics, DependencyDiagnostic{Code: d.Code, Source: d.Source})
		}
	}
}
