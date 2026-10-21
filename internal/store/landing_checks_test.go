package store

import (
	"context"
	"encoding/json"
	"errors"
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
	e := queue.LandingChecksRecorded{BatchID: c.Start.BatchID, AttemptID: c.Start.AttemptID, ConfigCommit: state.ConfigCommit, ConfigDigest: state.ConfigDigest, PolicyDigest: c.Policy.Digest, TestedSHA: c.Land.TestedSHA, LandedSHA: c.Land.TestedSHA, Method: c.Land.Method, TestRef: c.Land.Ref, RequiredCheckSource: state.LandingCheckSources(), ObservedAt: c.Start.At, Checks: []queue.LandingCheck{}, Members: []queue.LandingCheckMember{{PR: c.Land.PRs[0].PR, HeadSHA: c.Land.PRs[0].HeadSHA, Conditions: condition.All().Record(condition.Snapshot{})}}}
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
	for _, kind := range []string{"missing", "source", "member", "sha", "config", "policy", "claim", "bypass", "final-start"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t)
			q, claim, c, d, e := authorityCheckLand(t, s)
			before := eventCount(t, s, q)
			evidence := []queue.LandingChecksRecorded{e}
			switch kind {
			case "missing":
				evidence = nil
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
