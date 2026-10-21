package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"os"
	"reflect"
	"strings"
	"testing"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

func authorityCheckLand(t *testing.T, s *Store) (QueueID, ClaimedAction, landingCase, queue.LandingDecisionRecorded, queue.LandingChecksRecorded) {
	t.Helper()
	rawCase, err := os.ReadFile("../queue/testdata/landing-start.json")
	must(t, err)
	return authorityCheckLandInput(t, s, rawCase)
}
func authorityCheckLandInput(t *testing.T, s *Store, rawCase []byte) (QueueID, ClaimedAction, landingCase, queue.LandingDecisionRecorded, queue.LandingChecksRecorded) {
	t.Helper()
	var input landingCase
	must(t, json.Unmarshal(rawCase, &input))
	input.Config.ConfigDigest = strings.Repeat("c", 64)
	_, configRaw, err := queue.EncodeEvent(queue.ConfigApplied{Commit: "config-commit", Digest: input.Config.ConfigDigest})
	must(t, err)
	input.Events = append([]json.RawMessage{configRaw}, input.Events...)
	rawCase, err = json.Marshal(input)
	must(t, err)
	q, claim, c, d := decisionLandInput(t, s, rawCase)
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	raw, err := os.ReadFile("../queue/testdata/wire/v1/event_policy_authority_applied.json")
	must(t, err)
	event, err := queue.DecodeEvent(raw)
	must(t, err)
	pin := event.(queue.PolicyAuthorityApplied)
	pin.EffectiveDigest = state.ConfigDigest
	pin.GoverningCommit = state.ConfigCommit
	pin.OpenMerge.Binding.EffectiveDigest = state.ConfigDigest
	appendEvents(t, s, q, pin)
	state, _, err = s.Load(t.Context(), q)
	must(t, err)
	if state.AuthorityPin() == nil {
		t.Fatal("pin did not apply")
	}
	e := queue.LandingChecksRecorded{BatchID: c.Start.BatchID, AttemptID: c.Start.AttemptID, ConfigCommit: state.ConfigCommit, ConfigDigest: state.ConfigDigest, PolicyDigest: c.Policy.Digest, TestedSHA: c.Land.TestedSHA, LandedSHA: c.Land.TestedSHA, Method: c.Land.Method, TestRef: c.Land.Ref, RequiredCheckSource: state.LandingCheckSources(), ObservedAt: c.Start.At, Checks: []queue.LandingCheck{}, CheckOrder: []int{}, Expressions: []string{condition.All().String()}, Inputs: []condition.ObservedInput{}, Members: []queue.LandingCheckMember{{PR: c.Land.PRs[0].PR, HeadSHA: c.Land.PRs[0].HeadSHA, Conditions: queue.LandingCheckCondition{Expression: 0, Inputs: []int{}}}}}
	must(t, e.Validate())
	return q, claim, c, d, e
}

func TestLandingChecksAtomicRollbackRetryAndRetention(t *testing.T) {
	s := newStore(t)
	q, claim, c, d, e := authorityCheckLand(t, s)
	before := eventCount(t, s, q)
	commit := func() error {
		return s.StartLanding(t.Context(), claim, c.Config, c.Policy, []queue.ApprovalEligibilityObserved{c.Fresh}, []queue.LandingDecisionRecorded{d}, c.Start, e)
	}
	_, err := s.pool.Exec(t.Context(), `CREATE FUNCTION reject_check_start() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='landing_started' THEN RAISE EXCEPTION 'crash before last insert'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_start BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION reject_check_start()`)
	must(t, err)
	if commit() == nil {
		t.Fatal("injected start failure ignored")
	}
	assertNoLandingSideEffects(t, s, q, claim, before)
	_, err = s.pool.Exec(t.Context(), `DROP TRIGGER reject_start ON events`)
	must(t, err)
	must(t, commit())
	if got := eventCount(t, s, q); got != before+4 {
		t.Fatal("wrong atomic event volume", got-before)
	}
	// The commit response can be lost before the remote push. A retry cannot
	// duplicate either the evidence or the start; fence settlement owns recovery.
	if err := commit(); !errors.Is(err, ErrStale) {
		t.Fatal("repeat accepted", err)
	}
	if eventCount(t, s, q) != before+4 {
		t.Fatal("retry duplicated evidence")
	}
	records, err := s.EventsAfter(t.Context(), q, 0)
	must(t, err)
	n := len(records)
	if got, ok := records[n-2].Event.(queue.LandingChecksRecorded); !ok || !reflect.DeepEqual(got, e) {
		t.Fatal("exact checks not adjacent to start")
	}
	if _, ok := records[n-1].Event.(queue.LandingStarted); !ok {
		t.Fatal("start not last")
	}
	appendEvents(t, s, q, queue.Landed{BatchID: c.Land.BatchID, NewBaseSHA: c.Land.TestedSHA, NewFence: "settled"}, queue.LifecycleChanged{State: "retired", Detail: "removed"})
	records, err = s.EventsAfter(t.Context(), q, 0)
	must(t, err)
	found := 0
	for _, r := range records {
		if got, ok := r.Event.(queue.LandingChecksRecorded); ok {
			found++
			if !reflect.DeepEqual(got, e) {
				t.Fatal("settlement rewrote evidence")
			}
		}
	}
	if found != 1 {
		t.Fatal("retired queue lost checks")
	}
}

func TestLandingChecksValidateLockedBindings(t *testing.T) {
	for _, kind := range []string{"missing", "source", "member", "sha", "config", "policy", "claim", "bypass", "final-start", "overflow"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t)
			q, claim, c, d, e := authorityCheckLand(t, s)
			before := eventCount(t, s, q)
			evidence := []queue.LandingChecksRecorded{e}
			switch kind {
			case "missing":
				evidence = nil
			case "overflow":
				evidence[0].Expressions[0] = strings.Repeat("x", condition.MaxEvidenceExpressionBytes+1)
			case "source":
				evidence[0].RequiredCheckSource.RulesRevision++
			case "member":
				evidence[0].Members[0].PR++
			case "sha":
				evidence[0].TestedSHA = c.Land.ExpectedBaseSHA
			case "config":
				evidence[0].ConfigCommit = "other"
			case "policy":
				evidence[0].PolicyDigest = evidence[0].ConfigDigest
			case "claim":
				claim.AttemptID = "lost"
			}
			var err error
			switch kind {
			case "bypass":
				err = s.Emit(t.Context(), claim, c.Config)(c.Start)
			case "final-start":
				calls := 0
				err = s.startLanding(t.Context(), claim, c.Config, c.Policy, []queue.ApprovalEligibilityObserved{c.Fresh}, []queue.LandingDecisionRecorded{d}, c.Start, func(ctx context.Context, tx pgx.Tx, x emission, event queue.Event) (bool, error) {
					calls++
					if calls == 2 {
						return true, nil
					}
					return s.authorizeStart(ctx, tx, x, event)
				}, evidence...)
			default:
				err = s.StartLanding(t.Context(), claim, c.Config, c.Policy, []queue.ApprovalEligibilityObserved{c.Fresh}, []queue.LandingDecisionRecorded{d}, c.Start, evidence...)
			}
			if err == nil {
				t.Fatal("invalid check evidence admitted", kind)
			}
			if kind == "overflow" {
				var tooLarge queue.LandingChecksTooLargeError
				if !errors.Is(err, queue.ErrLandingChecksTooLarge) || !errors.As(err, &tooLarge) || err != tooLarge || errors.Is(err, ErrStale) || errors.Is(err, ErrLandingDecisionStale) {
					t.Fatalf("overflow misclassified: %v", err)
				}
			}
			if kind == "missing" {
				// A gate built against a pre-pin basis omits checks, but the locked
				// store must require them after authority is installed.
				var required LandingChecksRequiredError
				if !errors.As(err, &required) || errors.Is(err, ErrStale) {
					t.Fatalf("pin race misclassified: %v", err)
				}
			}
			assertNoLandingSideEffects(t, s, q, claim, before)
		})
	}
}

func TestLandingChecksAuthorityWithoutReviewFloor(t *testing.T) {
	s := newStore(t)
	q, claim, c, _, e := authorityCheckLand(t, s)
	// PR 9 will supply authority activation independently of approvals. The
	// store seam must accept checks without synthesizing eligibility/reviewers.
	p := cp.Policy{Digest: c.Policy.Digest}
	before := eventCount(t, s, q)
	must(t, s.StartLanding(t.Context(), claim, c.Config, p, nil, nil, c.Start, e))
	if eventCount(t, s, q) != before+2 {
		t.Fatal("invented approval decision")
	}
}

func TestLandingChecksSourceRevisionRace(t *testing.T) {
	s := newStore(t)
	q, claim, c, d, e := authorityCheckLand(t, s)
	before := eventCount(t, s, q)
	tx, err := s.pool.Begin(t.Context())
	must(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	must(t, s.lockQueue(t.Context(), tx, q))
	event := queue.RulesSourceCleared{Source: queue.Permissions, Basis: 0}
	name, raw, err := queue.EncodeEvent(event)
	must(t, err)
	_, err = tx.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload) VALUES ($1,$2,$3::jsonb)`, q, name, string(raw))
	must(t, err)
	done := make(chan error, 1)
	go func() {
		done <- s.StartLanding(t.Context(), claim, c.Config, c.Policy, []queue.ApprovalEligibilityObserved{c.Fresh}, []queue.LandingDecisionRecorded{d}, c.Start, e)
	}()
	must(t, tx.Commit(t.Context()))
	if err := <-done; !errors.Is(err, ErrStale) {
		t.Fatal("source revision race admitted", err)
	}
	assertNoLandingSideEffects(t, s, q, claim, before+1)
}

func TestLandingChecksByteCapSurvivesJSONB(t *testing.T) {
	s := newStore(t)
	q, claim, c, d, e := authorityCheckLand(t, s)
	// Build exactly a 1-MiB compact event, below every individual string cap.
	const envelopeBytes = len(`{"version":1,"type":"landing_checks_recorded","data":}`)
	for {
		raw, err := json.Marshal(e)
		must(t, err)
		gap := queue.MaxLandingChecksBytes - envelopeBytes - len(raw)
		if gap <= 60000 {
			e.Expressions[0] += strings.Repeat("x", gap)
			break
		}
		index := len(e.Checks)
		e.Checks = append(e.Checks, queue.LandingCheck{Name: fmt.Sprintf("%04d%s", index, strings.Repeat("x", 4092)), ProducerKind: "unavailable", Result: forge.Success})
		e.CheckOrder = append(e.CheckOrder, index)
	}
	before := eventCount(t, s, q)
	e.Expressions[0] += "x"
	err := s.StartLanding(t.Context(), claim, c.Config, c.Policy, []queue.ApprovalEligibilityObserved{c.Fresh}, []queue.LandingDecisionRecorded{d}, c.Start, e)
	if !errors.Is(err, queue.ErrLandingChecksTooLarge) || errors.Is(err, ErrStale) {
		t.Fatalf("cap+1 classification: %v", err)
	}
	assertNoLandingSideEffects(t, s, q, claim, before)
	e.Expressions[0] = e.Expressions[0][:len(e.Expressions[0])-1]
	_, raw, err := queue.EncodeEvent(e)
	must(t, err)
	if len(raw) != queue.MaxLandingChecksBytes {
		t.Fatalf("event size %d", len(raw))
	}
	must(t, s.StartLanding(t.Context(), claim, c.Config, c.Policy, []queue.ApprovalEligibilityObserved{c.Fresh}, []queue.LandingDecisionRecorded{d}, c.Start, e))
	records, err := s.EventsAfter(t.Context(), q, 0)
	must(t, err)
	if !reflect.DeepEqual(records[len(records)-2].Event, e) {
		t.Fatal("JSONB cap boundary did not replay exactly")
	}
}
