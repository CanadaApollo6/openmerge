package store

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestLandingGatePolicyMiswiring(t *testing.T) {
	for _, side := range []string{"store-only", "executor-only"} {
		t.Run(side, func(t *testing.T) {
			s := newStore(t)
			q, claim, c, x := legacyExecutorInput(t, s)
			before := eventCount(t, s, q)
			if side == "executor-only" {
				state, _, err := s.Load(t.Context(), q)
				must(t, err)
				d, err := state.PrepareLandingDecision(c.Policy, c.Fresh, c.Start, c.Land)
				must(t, err)
				m := &forge.ChangeMetadata{ChangeID: string(c.Fresh.Basis.ChangeID), Revision: uint64(c.Fresh.Basis.Revision), Labels: d.Approval.Labels}
				pair, err := m.Trailers()
				must(t, err)
				x.LandingDecisions = &executor.LandingDecisionGate{Policy: c.Policy,
					Load: func(context.Context) (queue.State, error) { return state, nil },
					ReadInputs: func(context.Context, queue.Land, queue.State) ([]queue.ApprovalEligibilityObserved, error) {
						return []queue.ApprovalEligibilityObserved{c.Fresh}, nil
					},
					ReadTestedMessages: func(context.Context, queue.Land) ([]string, error) {
						return []string{"tested fixture\n\n" + strings.Join(pair, "\n")}, nil
					},
					Commit: func(ctx context.Context, f []queue.ApprovalEligibilityObserved, d []queue.LandingDecisionRecorded, start queue.LandingStarted) error {
						return s.StartLanding(ctx, claim, c.Config, cp.Policy{}, f, d, start)
					},
				}
			}
			err := x.Execute(t.Context(), c.Land, s.EmitWithPolicy(t.Context(), claim, c.Config, c.Policy))
			if side == "store-only" {
				var required LandingDecisionRequiredError
				if !errors.As(err, &required) || errors.Is(err, ErrStale) {
					t.Fatal("wrong policy failure", err)
				}
			} else if !errors.Is(err, ErrLandingDecisionStale) {
				t.Fatal("gate without store policy did not fail", err)
			}
			assertNoLandingSideEffects(t, s, q, claim, before)
		})
	}
}

func TestLandingDecisionJournalReadbackAfterSettlement(t *testing.T) {
	s := newStore(t)
	q, claim, c, d := decisionLand(t, s)
	must(t, commitDecision(t, s, claim, c, d))
	newer := c.Fresh
	newer.Basis.ReadID = "later-read"
	newer.Basis.At = newer.Basis.At.Add(time.Second)
	appendEvents(t, s, q, newer, queue.Landed{BatchID: c.Start.BatchID, NewBaseSHA: c.Land.TestedSHA, NewFence: "settled"})
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	if len(state.LandingDecisions()) != 0 {
		t.Fatal("historical projection retained")
	}
	facts, ok := state.ApprovalFacts(c.Fresh.Basis.ChangeID)
	if !ok || facts.Eligibility == nil || facts.Eligibility.Basis.ReadID != "later-read" {
		t.Fatal("latest eligibility not projected")
	}
	journals, err := s.Journals(t.Context(), []QueueKey{{Installation: "7", Repository: "Owner/Repo", BaseBranch: "release"}})
	must(t, err)
	if len(journals) != 1 {
		t.Fatal("missing journal")
	}
	var decision queue.LandingDecisionRecorded
	var fresh queue.ApprovalEligibilityObserved
	replay := journals[0].Seed
	for _, record := range journals[0].Events {
		replay = queue.Apply(replay, record.Event)
		if v, ok := record.Event.(queue.LandingDecisionRecorded); ok && v.BatchID == c.Start.BatchID && v.AttemptID == c.Start.AttemptID {
			decision = v
		}
	}
	if !reflect.DeepEqual(decision, d) {
		t.Fatal("decision not readable from settled journal")
	}
	for _, record := range journals[0].Events {
		if v, ok := record.Event.(queue.ApprovalEligibilityObserved); ok && v.Basis.ChangeID == decision.Approval.Basis.ChangeID && v.Basis.ReadID == decision.Approval.EligibilityReadID {
			fresh = v
		}
	}
	if !reflect.DeepEqual(fresh, c.Fresh) {
		t.Fatal("exact fresh read not recoverable")
	}
	want, err := queue.EncodeSnapshot(state)
	must(t, err)
	got, err := queue.EncodeSnapshot(replay)
	must(t, err)
	if !bytes.Equal(want, got) {
		t.Fatal("journal readback differs from incremental projection")
	}
}
