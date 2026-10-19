package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestApprovalReadInFlightAppend(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	appendEvents(t, s, q, seedEvents()...)
	tx, err := s.pool.Begin(t.Context())
	must(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	must(t, s.lockQueue(t.Context(), tx, q))
	raw, err := os.ReadFile("../queue/testdata/wire/v1/event_approval_eligibility_observed.json")
	must(t, err)
	e, err := queue.DecodeEvent(raw)
	must(t, err)
	f := e.(queue.ApprovalEligibilityObserved)
	name, payload, err := queue.EncodeEvent(e)
	must(t, err)
	var id int64
	must(t, tx.QueryRow(t.Context(), `INSERT INTO events(queue_id,type,payload) VALUES ($1,$2,$3::jsonb) RETURNING id`, q, name, string(payload)).Scan(&id))
	// Another queue may commit a higher global sequence ID while this transaction is open.
	other := ensureQueue(t, s, "other")
	higher := appendEvents(t, s, other, queue.ResumeRequested{})
	if higher <= id {
		t.Fatal("expected cross-queue sequence inversion")
	}
	read, err := s.BeginApprovalRead(t.Context(), q, []queue.ChangeID{f.Basis.ChangeID})
	must(t, err)
	if read.watermark >= id {
		t.Fatal("uncommitted event visible")
	}
	done := make(chan error, 1)
	wait := queueLockContenders(t, s, 1)
	go func() {
		_, err := s.Append(read.Context(t.Context()), q, []queue.Event{queue.ResumeRequested{}}, "")
		done <- err
	}()
	wait()
	must(t, tx.Commit(t.Context()))
	if err := <-done; !errors.Is(err, ErrLandingDecisionStale) {
		t.Fatalf("late commit escaped fence: %v", err)
	}
	s.beforeQueueLock = nil
}
func TestApprovalReadUnrelatedRejection(t *testing.T) {
	s := newStore(t)
	q, claim, c, _ := decisionLand(t, s)
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	expected := []string{state.ApprovalFingerprint(c.Fresh.Basis.ChangeID)}
	c.Fresh.Actors = map[string]cp.Actor{}
	rejection, err := state.PrepareLandingRejection(c.Policy, c.Land, []queue.ApprovalEligibilityObserved{c.Fresh})
	must(t, err)
	member := queue.ChangeID("chg1_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if rejection.Waiting[0].Basis.ChangeID == member {
		t.Fatal("bad fixture")
	}
	read, err := s.BeginApprovalRead(t.Context(), q, []queue.ChangeID{member})
	must(t, err)
	must(t, s.RejectLanding(t.Context(), claim, c.Config, c.Policy, []queue.ApprovalEligibilityObserved{c.Fresh}, expected, c.Start, rejection))
	_, err = s.Append(read.Context(t.Context()), q, []queue.Event{queue.ResumeRequested{}}, "")
	if err != nil {
		t.Fatalf("unrelated change rejection invalidated read: %v", err)
	}
}
func TestApprovalReadFenceControls(t *testing.T) {
	for _, kind := range []string{"own-append", "unrelated-resume", "other-member", "intervening-relevant"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t)
			q := ensureQueue(t, s, "")
			raw, err := os.ReadFile("../queue/testdata/wire/v1/event_approval_eligibility_observed.json")
			must(t, err)
			e, err := queue.DecodeEvent(raw)
			must(t, err)
			f := e.(queue.ApprovalEligibilityObserved)
			read, err := s.BeginApprovalRead(t.Context(), q, []queue.ChangeID{f.Basis.ChangeID})
			must(t, err)
			switch kind {
			case "own-append":
				_, err = s.Append(read.Context(t.Context()), q, []queue.Event{f}, "")
				must(t, err)
			case "unrelated-resume":
				appendEvents(t, s, q, queue.ResumeRequested{})
			case "other-member":
				f.Basis.ChangeID = "chg1_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				appendEvents(t, s, q, f)
			case "intervening-relevant":
				appendEvents(t, s, q, f)
			}
			_, err = s.Append(read.Context(t.Context()), q, []queue.Event{queue.ResumeRequested{}}, "")
			if kind == "intervening-relevant" {
				if !errors.Is(err, ErrLandingDecisionStale) {
					t.Fatalf("missed competing event: %v", err)
				}
			} else {
				must(t, err)
			}
		})
	}
}

// Exercise the SQL filter independently of projection admission, using encoded
// wire payloads. Each probe has a fresh queue so malformed semantic fixtures
// never enter the next acquisition's projection load.
func TestApprovalReadFilterScopes(t *testing.T) {
	s := newStore(t)
	basis := func(id string) queue.ApprovalBasis { return queue.ApprovalBasis{ChangeID: queue.ChangeID(id)} }
	scoped := map[string]func(string) any{
		"change_revision_observed": func(id string) any { return map[string]any{"change_id": id} },
		"change_classified": func(id string) any {
			return map[string]any{"evidence": map[string]any{"Classification": map[string]any{"Inputs": map[string]any{"ChangeID": id}}}}
		},
		"change_accountability_observed": func(id string) any { return queue.ChangeAccountabilityObserved{Basis: basis(id)} },
		"change_intent_observed":         func(id string) any { return queue.ChangeIntentObserved{Basis: basis(id)} },
		"review_stance_observed":         func(id string) any { return queue.ReviewStanceObserved{Basis: basis(id)} },
		"approval_receipt_recorded":      func(id string) any { return queue.ApprovalReceiptRecorded{Basis: basis(id)} },
		"approval_eligibility_observed":  func(id string) any { return queue.ApprovalEligibilityObserved{Basis: basis(id)} },
		"approval_inputs_changed":        func(id string) any { return queue.ApprovalInputsChanged{Basis: basis(id)} },
		"approval_gate_rejected": func(id string) any {
			return queue.ApprovalGateRejected{Waiting: []queue.ApprovalWait{{Basis: basis(id)}}}
		},
	}
	probe := func(t *testing.T, typ string, data any, stale bool, configure func(*ApprovalRead)) {
		t.Helper()
		q := ensureQueue(t, s, t.Name())
		read, err := s.BeginApprovalRead(t.Context(), q, []queue.ChangeID{queue.ChangeID("chg1_" + strings.Repeat("a", 64))})
		must(t, err)
		if configure != nil {
			configure(read)
		}
		raw, err := json.Marshal(map[string]any{"version": 1, "type": typ, "data": data})
		must(t, err)
		_, err = s.pool.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload) VALUES ($1,$2,$3::jsonb)`, q, typ, string(raw))
		must(t, err)
		_, err = s.Append(read.Context(t.Context()), q, []queue.Event{queue.ResumeRequested{}}, "")
		if errors.Is(err, ErrLandingDecisionStale) != stale {
			t.Fatalf("stale=%v: %v", stale, err)
		}
		if !stale {
			must(t, err)
		}
	}
	for typ, makeData := range scoped {
		for _, id := range []string{"chg1_" + strings.Repeat("a", 64), "other"} {
			t.Run(typ+"/"+id, func(t *testing.T) { probe(t, typ, makeData(id), id == "chg1_"+strings.Repeat("a", 64), nil) })
		}
	}
	for _, typ := range []string{"base_moved", "config_applied"} {
		for _, changed := range []bool{false, true} {
			name := "same"
			value := ""
			if changed {
				name = "changed"
				value = "new"
			}
			t.Run(typ+"/"+name, func(t *testing.T) { probe(t, typ, map[string]any{"sha": value, "digest": value}, changed, nil) })
		}
	}
	// Rejection of an ancestor invalidates a member's descendant composition;
	// a later, unrelated composition cannot affect that member.
	for _, rejected := range []string{"ancestor", "member-batch", "later"} {
		t.Run("composition/"+rejected, func(t *testing.T) {
			probe(t, "approval_gate_rejected", queue.ApprovalGateRejected{BatchID: rejected, Waiting: []queue.ApprovalWait{}}, rejected != "later", func(read *ApprovalRead) {
				read.state = queue.Apply(read.state, queue.ChangeRevisionObserved{PR: 1, ChangeID: queue.ChangeID("chg1_" + strings.Repeat("a", 64)), Revision: 1, HeadSHA: "h", Basis: queue.RevisionObservationBasis{BaseSHA: "base", At: time.Now()}})
				read.state.Active = &queue.Batch{ID: "ancestor"}
				read.state.Following = []*queue.Batch{{ID: "member-batch", PRs: []queue.BatchPR{{PR: 1, HeadSHA: "h"}}}, {ID: "later"}}
			})
		})
	}
	for _, typ := range []string{"pr_observed", "checks_observed", "block_source_blocked", "landing_decision_recorded", "outside_push_recorded", "pr_metadata_observed"} {
		t.Run(typ, func(t *testing.T) {
			probe(t, typ, map[string]any{"basis": basis("chg1_" + strings.Repeat("a", 64)), "change_id": "chg1_" + strings.Repeat("a", 64)}, false, nil)
		})
	}
}

func TestApprovalReadOwnAppendAdvancesScope(t *testing.T) {
	for _, typ := range []string{"base", "config"} {
		t.Run(typ, func(t *testing.T) {
			s := newStore(t)
			q := ensureQueue(t, s, "")
			var initial, changed queue.Event = queue.BaseMoved{SHA: "initial"}, queue.BaseMoved{SHA: "changed"}
			if typ == "config" {
				initial = queue.ConfigApplied{Commit: "initial", Digest: strings.Repeat("a", 64)}
				changed = queue.ConfigApplied{Commit: "changed", Digest: strings.Repeat("b", 64)}
			}
			appendEvents(t, s, q, initial)
			read, err := s.BeginApprovalRead(t.Context(), q, []queue.ChangeID{"member"})
			must(t, err)
			_, err = s.Append(read.Context(t.Context()), q, []queue.Event{changed}, "")
			must(t, err)
			appendEvents(t, s, q, changed)
			_, err = s.Append(read.Context(t.Context()), q, []queue.Event{queue.ResumeRequested{}}, "")
			must(t, err)
			// Returning to the original baseline is a competing change, even
			// though it equals the snapshot taken before our own append.
			appendEvents(t, s, q, initial)
			_, err = s.Append(read.Context(t.Context()), q, []queue.Event{queue.ResumeRequested{}}, "")
			if !errors.Is(err, ErrLandingDecisionStale) {
				t.Fatalf("ABA escaped updated scope: %v", err)
			}
		})
	}
}
