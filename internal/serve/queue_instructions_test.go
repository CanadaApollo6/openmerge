package serve

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

type instructionDriver struct {
	forge.Driver
	writes, comments int
	fail             bool
}

func (d *instructionDriver) ReportStatus(ctx context.Context, r forge.RepoRef, pr int, head string, status forge.Status) error {
	d.writes++
	if d.fail {
		d.fail = false
		return errors.New("lost report")
	}
	return d.Driver.ReportStatus(ctx, r, pr, head, status)
}

func (d *instructionDriver) Comment(ctx context.Context, r forge.RepoRef, pr int, key, body string) error {
	d.comments++
	return d.Driver.Comment(ctx, r, pr, key, body)
}

func TestQueueInstructionsReportingPathsDedupeRetryRollback(t *testing.T) {
	for _, approval := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "approval"}[approval], func(t *testing.T) {
			var s *Server
			var w *worker
			if approval {
				s, w, _ = shadowFixture(t, 1, "main", "instructions", false)
			} else {
				s, w, _, _ = fixture(t, repositoryConfig)
				must(t, s.reconcile(t.Context(), w, true))
			}
			d := &instructionDriver{Driver: w.Driver}
			w.Driver = d
			raw, err := os.ReadFile("../queue/testdata/wire/v1/event_policy_authority_applied.json")
			must(t, err)
			event, err := queue.DecodeEvent(raw)
			must(t, err)
			pin := event.(queue.PolicyAuthorityApplied)
			state := shadowState(t, s, w)
			pin.EffectiveDigest = state.ConfigDigest
			pin.OpenMerge.Binding.EffectiveDigest = state.ConfigDigest
			appendPin := func(p queue.PolicyAuthorityApplied) {
				t.Helper()
				_, err := s.store.Append(t.Context(), w.id, []queue.Event{p}, "")
				must(t, err)
			}
			must(t, s.report(t.Context(), w))
			// Use the same input status to prove the whole preexisting observation,
			// including title/state and summary, survives activation and rollback.
			head := w.prs[1].HeadSHA
			input := forge.Status{State: forge.Queued, Summary: "Waiting for a test batch"}
			must(t, s.status(t.Context(), w, 1, head, input))
			before, err := s.store.Observation(t.Context(), w.id, "status/1")
			must(t, err)
			appendPin(pin)
			d.fail = true
			if err := s.status(t.Context(), w, 1, head, input); err == nil {
				t.Fatal("report failure ignored")
			}
			unchanged, err := s.store.Observation(t.Context(), w.id, "status/1")
			must(t, err)
			if string(before) != string(unchanged) {
				t.Fatal("failed write cached")
			}
			must(t, s.status(t.Context(), w, 1, head, input))
			rendered, err := s.store.Observation(t.Context(), w.id, "status/1")
			must(t, err)
			if strings.Count(string(rendered), "@openmerge queue") != 1 {
				t.Fatalf("missing/duplicate instruction: %s", rendered)
			}
			if approval && !strings.Contains(string(rendered), "Reviewer brief:") {
				t.Fatal("lost approval brief")
			}
			writes := d.writes
			for range 3 {
				must(t, s.status(t.Context(), w, 1, head, input))
			}
			if writes != d.writes {
				t.Fatal("unchanged cycles wrote checks")
			}
			must(t, s.report(t.Context(), w))
			invitation, err := s.store.Observation(t.Context(), w.id, "status/1")
			must(t, err)
			if strings.Count(string(invitation), "@openmerge queue") != 1 || approval && !strings.Contains(string(invitation), "Reviewer brief:") {
				t.Fatalf("pre-admission brief lost: %s", invitation)
			}
			// Explicit readiness failures still use the same single-check suffix.
			w.managed = true
			w.ready = false
			w.waiting = "repository rules block admission"
			must(t, s.report(t.Context(), w))
			waiting, err := s.store.Observation(t.Context(), w.id, "status/1")
			must(t, err)
			must(t, s.reportWaiting(t.Context(), w))
			diagnostic, err := s.store.Observation(t.Context(), w.id, "status/1")
			must(t, err)
			if !reflect.DeepEqual(waiting, diagnostic) || strings.Count(string(waiting), "@openmerge queue") != 1 {
				t.Fatal("readiness paths differ")
			}
			if !strings.Contains(string(waiting), "Waiting for queue readiness: repository rules block admission") {
				t.Fatalf("readiness waiting reason missing: %s", waiting)
			}
			// A completed discovery attempt can leave the original readiness
			// string in place; both publishers must still agree on its bytes.
			w.validationAttempted = true
			w.waiting = "validation pending"
			must(t, s.report(t.Context(), w))
			waiting, err = s.store.Observation(t.Context(), w.id, "status/1")
			must(t, err)
			must(t, s.reportWaiting(t.Context(), w))
			diagnostic, err = s.store.Observation(t.Context(), w.id, "status/1")
			must(t, err)
			if !reflect.DeepEqual(waiting, diagnostic) || strings.Count(string(waiting), "@openmerge queue") != 1 || !strings.Contains(string(waiting), "Waiting for queue readiness: validation pending") {
				t.Fatalf("attempted validation readiness paths differ: %s / %s", waiting, diagnostic)
			}
			// Compare rollback bytes with the same readiness as the initial check.
			w.managed = false
			pin.Authority = authority.GitHub
			pin.OpenMerge = nil
			appendPin(pin)
			must(t, s.status(t.Context(), w, 1, head, input))
			after, err := s.store.Observation(t.Context(), w.id, "status/1")
			must(t, err)
			if d.comments != 0 {
				t.Fatal("reporting posted a comment")
			}
			if string(before) != string(after) {
				t.Fatalf("legacy bytes changed after rollback:\n%s\n%s", before, after)
			}
		})
	}
}

func TestQueueInstructionsBeforeAdmissionAndTerminalPreservation(t *testing.T) {
	for _, eligible := range []bool{true, false} {
		t.Run(map[bool]string{true: "eligible", false: "ineligible"}[eligible], func(t *testing.T) {
			s, w, _, _ := fixture(t, repositoryConfig)
			must(t, s.reconcile(t.Context(), w, true))
			d := &instructionDriver{Driver: w.Driver}
			w.Driver = d
			head := w.prs[1].HeadSHA
			if !eligible {
				_, err := s.store.Append(t.Context(), w.id, []queue.Event{queue.PRObserved{PR: 1, HeadSHA: head, Eligible: false, Trusted: false}}, "")
				must(t, err)
			}
			must(t, s.report(t.Context(), w))
			if d.writes != 0 {
				t.Fatal("legacy unqueued PR gained a check")
			}
			raw, err := os.ReadFile("../queue/testdata/wire/v1/event_policy_authority_applied.json")
			must(t, err)
			event, err := queue.DecodeEvent(raw)
			must(t, err)
			pin := event.(queue.PolicyAuthorityApplied)
			pin.EffectiveDigest = shadowState(t, s, w).ConfigDigest
			pin.OpenMerge.Binding.EffectiveDigest = pin.EffectiveDigest
			appendEvents := func(events ...queue.Event) {
				t.Helper()
				_, err := s.store.Append(t.Context(), w.id, events, "")
				must(t, err)
			}
			appendEvents(pin)
			for range 3 {
				must(t, s.report(t.Context(), w))
			}
			if d.writes != 1 || d.comments != 0 {
				t.Fatalf("unqueued report writes=%d comments=%d", d.writes, d.comments)
			}
			rendered, err := s.store.Observation(t.Context(), w.id, "status/1")
			must(t, err)
			if strings.Count(string(rendered), "@openmerge queue") != 1 || !strings.Contains(string(rendered), head) || !strings.Contains(string(rendered), "Not queued") {
				t.Fatalf("no pre-admission instruction on current head: %s", rendered)
			}
			pin.Authority = authority.GitHub
			pin.OpenMerge = nil
			appendEvents(pin)
			for range 3 {
				must(t, s.report(t.Context(), w))
			}
			rollback, err := s.store.Observation(t.Context(), w.id, "status/1")
			must(t, err)
			if d.writes != 2 || strings.Contains(string(rollback), "@openmerge queue") {
				t.Fatalf("rollback did not refresh pre-admission check: %s", rollback)
			}
			// Rollback refresh changes only the instruction paragraph, once.
			var prior, refreshed struct {
				Head   string
				Status forge.Status
			}
			must(t, json.Unmarshal(rendered, &prior))
			must(t, json.Unmarshal(rollback, &refreshed))
			prior.Status.Summary = strings.Replace(prior.Status.Summary, "\n\nThis repository merges through OpenMerge. To request a merge, post a comment whose entire body is `@openmerge queue` (no additional text).", "", 1)
			if !reflect.DeepEqual(prior, refreshed) {
				t.Fatalf("rollback changed cached status: before=%+v after=%+v", prior, refreshed)
			}
			// Newly observed eligible and ineligible PRs stay quiet after rollback.
			pr := w.prs[1]
			pr.HeadSHA = "after-rollback"
			w.prs[2] = pr
			w.prs[3] = pr
			appendEvents(queue.PRObserved{PR: 2, HeadSHA: pr.HeadSHA, Eligible: true, Trusted: true}, queue.PRObserved{PR: 3, HeadSHA: pr.HeadSHA, Eligible: false, Trusted: false})
			for range 3 {
				must(t, s.report(t.Context(), w))
			}
			for _, key := range []string{"status/2", "status/3"} {
				optional, err := s.store.Observation(t.Context(), w.id, key)
				must(t, err)
				if d.writes != 2 || len(optional) != 0 {
					t.Fatalf("rollback created invitation %s: %s (writes=%d)", key, optional, d.writes)
				}
			}
			delete(w.prs, 2)
			delete(w.prs, 3)
			if !eligible {
				return
			}
			// A terminal ledger entry must not become a new queue invitation while
			// the forge's open-PR observation catches up with the landing outcome.
			now := time.Now()
			appendEvents(queue.EnqueueRequested{PR: 1, At: now}, queue.BatchStarted{BatchID: "b1", BaseSHA: shadowState(t, s, w).BaseSHA, PRs: []queue.BatchPR{{PR: 1, HeadSHA: head}}, At: now}, queue.TestRefBuilt{BatchID: "b1", TestedSHA: "landed", Method: queue.Merge, At: now}, queue.ChecksCompleted{TestedSHA: "landed", Success: true}, queue.LandingStarted{BatchID: "b1", AttemptID: "attempt", FenceToken: shadowState(t, s, w).Fence, At: now}, queue.Landed{BatchID: "b1", NewBaseSHA: "landed", NewFence: "next"})
			if state := shadowState(t, s, w); len(state.Entries) != 0 || state.BaseSHA != "landed" {
				t.Fatalf("fixture did not land: %+v", state)
			}
			before := d.writes
			must(t, s.report(t.Context(), w))
			if before != d.writes {
				t.Fatal("terminal PR overwritten by invitation")
			}
		})
	}
}

func TestQueueInstructionsDequeuedActivationRollback(t *testing.T) {
	for _, terminal := range []string{"dequeued", "merged"} {
		for _, closed := range []bool{false, true} {
			t.Run(terminal+"/"+map[bool]string{false: "open", true: "closed"}[closed], func(t *testing.T) {
				s, w, f, _ := fixture(t, repositoryConfig)
				must(t, s.reconcile(t.Context(), w, true))
				d := &instructionDriver{Driver: w.Driver}
				w.Driver = d
				head := w.prs[1].HeadSHA
				appendEvents := func(events ...queue.Event) {
					t.Helper()
					_, err := s.store.Append(t.Context(), w.id, events, "")
					must(t, err)
				}
				appendEvents(queue.EnqueueRequested{PR: 1, At: time.Now()})
				if terminal == "dequeued" {
					appendEvents(queue.Dequeued{PR: 1, AdmissionID: shadowState(t, s, w).Entries[0].AdmissionID, Reason: queue.Requested})
				} else {
					now := time.Now()
					appendEvents(queue.BatchStarted{BatchID: "b1", BaseSHA: shadowState(t, s, w).BaseSHA, PRs: []queue.BatchPR{{PR: 1, HeadSHA: head}}, At: now}, queue.TestRefBuilt{BatchID: "b1", TestedSHA: "landed", Method: queue.Merge, At: now}, queue.ChecksCompleted{TestedSHA: "landed", Success: true}, queue.LandingStarted{BatchID: "b1", AttemptID: "attempt", FenceToken: shadowState(t, s, w).Fence, At: now}, queue.Landed{BatchID: "b1", NewBaseSHA: "landed", NewFence: "next"})
				}
				claim := claimStageAAction(t, s, w, w.plannerConfig(s.options.PollInterval), func(a queue.Action) bool { r, ok := a.(queue.ReportStatus); return ok && r.Status == terminal })
				must(t, s.execute(t.Context(), w, claim))
				before := f.Statuses(repo, 1)[head]
				if string(before.State) != terminal || terminal == "dequeued" && before.Reason != string(queue.Requested) {
					t.Fatalf("terminal status: %+v", before)
				}
				raw, err := os.ReadFile("../queue/testdata/wire/v1/event_policy_authority_applied.json")
				must(t, err)
				event, err := queue.DecodeEvent(raw)
				must(t, err)
				pin := event.(queue.PolicyAuthorityApplied)
				pin.EffectiveDigest = shadowState(t, s, w).ConfigDigest
				pin.OpenMerge.Binding.EffectiveDigest = pin.EffectiveDigest
				pr := w.prs[1]
				pr.Closed = closed
				w.prs[1] = pr
				writes := d.writes
				appendEvents(pin)
				for range 3 {
					appendEvents(queue.PRObserved{PR: 1, HeadSHA: head, Eligible: true, Trusted: true})
					must(t, s.report(t.Context(), w))
				}
				active := f.Statuses(repo, 1)[head]
				if closed {
					if d.writes != writes || !reflect.DeepEqual(before, active) {
						t.Fatal("closed check rewritten")
					}
				} else {
					expected := before
					expected.Summary += "\n\nThis repository merges through OpenMerge. To request a merge, post a comment whose entire body is `@openmerge queue` (no additional text)."
					if d.writes != writes+1 || !reflect.DeepEqual(expected, active) {
						t.Fatalf("activation changed terminal result: %+v", active)
					}
				}
				pin.Authority = authority.GitHub
				pin.OpenMerge = nil
				appendEvents(pin)
				for range 3 {
					appendEvents(queue.PRObserved{PR: 1, HeadSHA: head, Eligible: true, Trusted: true})
					must(t, s.report(t.Context(), w))
				}
				wantWrites := writes + 2
				if closed {
					wantWrites = writes
				}
				if d.writes != wantWrites || !reflect.DeepEqual(before, f.Statuses(repo, 1)[head]) {
					t.Fatal("rollback failed to restore exact terminal check with one update")
				}
			})
		}
	}

}

func TestQueueInstructionsMissingLedgerJournal(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	must(t, s.reconcile(t.Context(), w, true))
	raw, err := os.ReadFile("../queue/testdata/wire/v1/event_policy_authority_applied.json")
	must(t, err)
	event, err := queue.DecodeEvent(raw)
	must(t, err)
	pin := event.(queue.PolicyAuthorityApplied)
	pin.EffectiveDigest = shadowState(t, s, w).ConfigDigest
	pin.OpenMerge.Binding.EffectiveDigest = pin.EffectiveDigest
	_, err = s.store.Append(t.Context(), w.id, []queue.Event{pin}, "")
	must(t, err)
	// A valid queue ID with no journal for the requested key exercises the
	// ledgerSummary empty-result path without mocking the store.
	w.Key.Repository = "missing/repository"
	w.ledgerCache = nil
	d := &instructionDriver{Driver: w.Driver}
	w.Driver = d
	must(t, s.report(t.Context(), w))
	if w.ledgerCache != nil || d.writes != 0 {
		t.Fatal("missing journal created a check")
	}
}
