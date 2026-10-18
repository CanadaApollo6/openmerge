package store

import (
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
			var required LandingDecisionRequiredError
			if !errors.As(err, &required) || errors.Is(err, ErrStale) || errors.Is(err, ErrLandingDecisionStale) {
				t.Fatal("wrong policy wiring failure", err)
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
	evidence, err := s.LandingDecisionEvidence(t.Context(), q, c.Start.BatchID, c.Start.AttemptID)
	must(t, err)
	if len(evidence) != 1 || !reflect.DeepEqual(evidence[0].Decision, d) || evidence[0].Eligibility == nil || !reflect.DeepEqual(*evidence[0].Eligibility, c.Fresh) {
		t.Fatal("exact settled evidence not recovered", evidence)
	}
	for _, scope := range [][2]string{{"other-batch", c.Start.AttemptID}, {c.Start.BatchID, "other-attempt"}} {
		got, err := s.LandingDecisionEvidence(t.Context(), q, scope[0], scope[1])
		must(t, err)
		if len(got) != 0 {
			t.Fatal("unrelated evidence returned")
		}
	}
	// Unrelated or future-version payloads must never be decoded by this read.
	_, err = s.pool.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload) VALUES
 ($1,'landing_decision_recorded','{"version":999,"type":"landing_decision_recorded","data":{"batch_id":"other","attempt_id":"other"}}'),
 ($1,'future_event','{"version":999,"type":"future_event","data":{}}')`, q)
	must(t, err)
	evidence, err = s.LandingDecisionEvidence(t.Context(), q, c.Start.BatchID, c.Start.AttemptID)
	must(t, err)
	if len(evidence) != 1 {
		t.Fatal("unrelated journal payload affected evidence")
	}
	// An attempt cannot have more than the queue's 128-member limit.
	_, raw, err := queue.EncodeEvent(d)
	must(t, err)
	_, err = s.pool.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload) SELECT $1,'landing_decision_recorded',$2::jsonb FROM generate_series(1,128)`, q, string(raw))
	must(t, err)
	if _, err := s.LandingDecisionEvidence(t.Context(), q, c.Start.BatchID, c.Start.AttemptID); err == nil {
		t.Fatal("unbounded member set accepted")
	}

}

func TestLandingRejectionPolicyMiswiring(t *testing.T) {
	s := newStore(t)
	q, claim, c, _ := decisionLand(t, s)
	before := eventCount(t, s, q)
	err := s.RejectLanding(t.Context(), claim, c.Config, cp.Policy{}, nil, nil, c.Start, queue.ApprovalGateRejected{})
	var required LandingDecisionRequiredError
	if !errors.As(err, &required) || errors.Is(err, ErrStale) || errors.Is(err, ErrLandingDecisionStale) {
		t.Fatal("wrong rejection wiring error", err)
	}
	assertNoLandingSideEffects(t, s, q, claim, before)
}
