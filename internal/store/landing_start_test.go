package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

type landingCase struct {
	Events []json.RawMessage
	Policy cp.Policy
	Fresh  queue.ApprovalEligibilityObserved
	Land   queue.Land
	Start  queue.LandingStarted
	Config queue.Config
}

func decisionLand(t *testing.T, s *Store) (QueueID, ClaimedAction, landingCase, queue.LandingDecisionRecorded) {
	t.Helper()
	raw, err := os.ReadFile("../queue/testdata/landing-start.json")
	must(t, err)
	return decisionLandInput(t, s, raw)
}
func decisionLandInput(t *testing.T, s *Store, raw []byte) (QueueID, ClaimedAction, landingCase, queue.LandingDecisionRecorded) {
	t.Helper()
	var c landingCase
	must(t, json.Unmarshal(raw, &c))
	q, err := s.EnsureQueue(t.Context(), QueueKey{Installation: "7", Repository: "Owner/Repo", BaseBranch: "release"})
	must(t, err)
	must(t, s.InitializeFence(t.Context(), q, c.Start.FenceToken))
	for _, raw := range c.Events {
		e, err := queue.DecodeEvent(raw)
		must(t, err)
		if receipt, ok := e.(queue.ApprovalReceiptRecorded); ok {
			state, _, err := s.Load(t.Context(), q)
			must(t, err)
			fact, err := state.RecordedBrief(c.Policy, receipt.Basis)
			must(t, err)
			identity, err := brief.Identity(fact)
			must(t, err)
			e, err = state.PrepareBareReceipt(c.Policy, receipt.Basis, receipt.Actor, receipt.CommentID, receipt.CreatedAt, identity.BriefDigest)
			must(t, err)
		}
		appendEvents(t, s, q, e)
	}
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	d, err := state.PrepareLandingDecision(c.Policy, c.Fresh, c.Start, c.Land)
	must(t, err)
	_, err = s.Plan(t.Context(), q, c.Config, c.Start.At)
	must(t, err)
	claims, err := s.ClaimQueue(t.Context(), q, 1, time.Minute, c.Start.At)
	must(t, err)
	if len(claims) != 1 {
		t.Fatal("missing claim", claims)
	}
	ok, err := s.BeginConsequential(t.Context(), claims[0], c.Config, c.Start.At)
	must(t, err)
	if !ok || !reflect.DeepEqual(claims[0].Action, c.Land) {
		t.Fatal("unexpected claim", claims[0])
	}
	return q, claims[0], c, d
}
func eventCount(t *testing.T, s *Store, q QueueID) int {
	t.Helper()
	var count int
	must(t, s.pool.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE queue_id=$1`, q).Scan(&count))
	return count
}
func assertNoLandingSideEffects(t *testing.T, s *Store, q QueueID, claim ClaimedAction, before int) {
	t.Helper()
	if got := eventCount(t, s, q); got != before {
		t.Fatalf("partial start: %d events, wanted %d", got, before)
	}
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	if len(state.LandingDecisions()) != 0 || state.Active != nil && state.Active.Phase == queue.Landing {
		t.Fatal("partial landing projection")
	}
	var status string
	must(t, s.pool.QueryRow(t.Context(), `SELECT status FROM actions WHERE queue_id=$1 AND key=$2`, q, claim.Key).Scan(&status))
	if status != "in_flight" {
		t.Fatal("authorization side effects committed", status)
	}
}
func commitDecision(t *testing.T, s *Store, claim ClaimedAction, c landingCase, d queue.LandingDecisionRecorded) error {
	t.Helper()
	return s.StartLanding(t.Context(), claim, c.Config, c.Policy, []queue.ApprovalEligibilityObserved{c.Fresh}, []queue.LandingDecisionRecorded{d}, c.Start)
}

func TestAtomicLandingStartRollbackAndRetry(t *testing.T) {
	s := newStore(t)
	q, claim, c, d := decisionLand(t, s)
	before := eventCount(t, s, q)
	// Failing the LAST insert proves preceding eligibility and decision INSERTs
	// are in the same Postgres transaction, not independent callback emissions.
	_, err := s.pool.Exec(t.Context(), `CREATE FUNCTION reject_landing_start() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='landing_started' THEN RAISE EXCEPTION 'injected last insert failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_start BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION reject_landing_start()`)
	must(t, err)
	if err := commitDecision(t, s, claim, c, d); err == nil {
		t.Fatal("injected failure ignored")
	}
	assertNoLandingSideEffects(t, s, q, claim, before)
	_, err = s.pool.Exec(t.Context(), `DROP TRIGGER reject_start ON events`)
	must(t, err)
	must(t, commitDecision(t, s, claim, c, d))
	if got := eventCount(t, s, q); got != before+3 {
		t.Fatal("wrong complete decision set", got-before)
	}
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	if len(state.LandingDecisions()) != 1 || state.Active.Phase != queue.Landing {
		t.Fatal("start not atomic")
	}
	facts, _ := state.ApprovalFacts(c.Fresh.Basis.ChangeID)
	if !reflect.DeepEqual(facts.Eligibility, &c.Fresh) {
		t.Fatal("evaluated fresh eligibility not journaled")
	}
	if err := commitDecision(t, s, claim, c, d); !errors.Is(err, ErrStale) {
		t.Fatal("duplicate start", err)
	}
	if eventCount(t, s, q) != before+3 {
		t.Fatal("duplicate inserted events")
	}
}

func TestAtomicLandingLastStartStale(t *testing.T) {
	t.Run("last-start-stale", func(t *testing.T) {
		s := newStore(t)
		q, claim, c, d := decisionLand(t, s)
		before := eventCount(t, s, q)
		calls := 0
		authorize := func(ctx context.Context, tx pgx.Tx, e emission, event queue.Event) (bool, error) {
			calls++
			if calls == 2 {
				// Only final authorization fails. Its cancellation must also roll back.
				_, err := tx.Exec(ctx, `UPDATE actions SET status='cancelled', attempt_id=NULL, lease_until=NULL WHERE queue_id=$1 AND key=$2`, q, claim.Key)
				return true, err
			}
			return s.authorizeStart(ctx, tx, e, event)
		}
		err := s.startLanding(t.Context(), claim, c.Config, c.Policy, []queue.ApprovalEligibilityObserved{c.Fresh}, []queue.LandingDecisionRecorded{d}, c.Start, authorize)
		if !errors.Is(err, ErrStale) || calls != 2 {
			t.Fatal("final start authorization not exercised", err, calls)
		}
		assertNoLandingSideEffects(t, s, q, claim, before)
		must(t, commitDecision(t, s, claim, c, d))
	})
}

func TestAtomicLandingRejectsWholeMemberSet(t *testing.T) {
	for _, kind := range []string{"missing", "duplicate", "extra", "mismatch", "claim-lost", "lease-lost", "legacy-bypass", "policy"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t)
			q, claim, c, d := decisionLand(t, s)
			fresh := []queue.ApprovalEligibilityObserved{c.Fresh}
			ds := []queue.LandingDecisionRecorded{d}
			switch kind {
			case "missing":
				ds = nil
			case "duplicate":
				fresh = append(fresh, c.Fresh)
				ds = append(ds, d)
			case "extra":
				ds = append(ds, d)
			case "mismatch":
				ds[0].Approval.Identity.Head = strings.Repeat("f", 40)
			case "claim-lost":
				claim.AttemptID = "other-worker"
			case "lease-lost":
				c.Start.At = c.Start.At.Add(2 * time.Minute)
			case "policy":
				c.Policy.Digest = "other-policy"
			}
			before := eventCount(t, s, q)
			var err error
			if kind == "legacy-bypass" {
				err = s.EmitWithPolicy(t.Context(), claim, c.Config, c.Policy)(c.Start)
			} else {
				err = s.StartLanding(t.Context(), claim, c.Config, c.Policy, fresh, ds, c.Start)
			}
			if err == nil {
				t.Fatal("invalid start accepted")
			}
			if kind == "legacy-bypass" {
				var required LandingDecisionRequiredError
				if !errors.As(err, &required) || errors.Is(err, ErrStale) {
					t.Fatal("wrong bypass error", err)
				}
			}
			if kind == "policy" && !errors.Is(err, ErrLandingDecisionStale) {
				t.Fatal("stale decision did not request re-plan", err)
			}
			assertNoLandingSideEffects(t, s, q, claim, before)
		})
	}
}

func TestAtomicLandingPreflightRaces(t *testing.T) {
	for _, kind := range []string{"config", "head", "intent", "check", "classification", "permission", "receipt", "pause", "removal"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t)
			q, claim, c, d := decisionLand(t, s)
			// Hold the actual queue lock in a competing Postgres transaction. The stale
			// worker has prepared its snapshot; its start waits until the race commits.
			tx, err := s.pool.Begin(t.Context())
			must(t, err)
			defer func() { _ = tx.Rollback(context.Background()) }()
			must(t, s.lockQueue(t.Context(), tx, q))
			var event queue.Event
			b := c.Fresh.Basis
			switch kind {
			case "config":
				event = queue.ConfigApplied{Commit: "new-policy", Digest: strings.Repeat("f", 64)}
			case "head":
				event = queue.PRObserved{PR: 12, HeadSHA: strings.Repeat("f", 40), Eligible: true, Trusted: true}
			case "intent":
				event = queue.Intent(b, "new title", "body")
			case "check":
				event = queue.ChecksReopened{TestedSHA: c.Land.TestedSHA, At: c.Start.At}
			case "classification":
				state, _, err := load(t.Context(), tx, q)
				must(t, err)
				classified, ok := state.Classification(b.ChangeID, b.Revision)
				if !ok {
					t.Fatal("classification missing")
				}
				classified.Evidence.Classification.Inputs.Generation++
				event = classified
			case "permission":
				f := c.Fresh
				f.Actors = map[string]cp.Actor{}
				event = f
			case "receipt":
				event = queue.ReviewStanceObserved{ /* use an authentic withdrawal from the retained receipt */ }
				state, _, err := load(t.Context(), tx, q)
				must(t, err)
				facts, _ := state.ApprovalFacts(b.ChangeID)
				for _, v := range facts.Sources {
					if v.Event.Stance.Receipt != nil {
						e := v.Event
						e.Stance.Kind = cp.Withdraw
						e.Stance.Receipt = nil
						e.Stance.Source.ID = "21"
						e.Stance.Source.Sequence = 21
						e.Stance.Source.Time = c.Start.At.Add(time.Second)
						e.Stance.Source.UpperTimeBound = e.Stance.Source.Time.Add(time.Second)
						e.SourceTime.Time = e.Stance.Source.Time
						e.SourceTime.UpperBound = e.Stance.Source.UpperTimeBound
						event = e
						break
					}
				}
			case "pause":
				event = queue.PauseRequested{Reason: "operator", Detail: "paused by operator"}
			case "removal":
				event = queue.LifecycleChanged{State: "retired", Detail: "configuration removed"}
			}
			name, payload, err := queue.EncodeEvent(event)
			must(t, err)
			_, err = tx.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload) VALUES ($1,$2,$3::jsonb)`, q, name, string(payload))
			must(t, err)
			started := make(chan struct{})
			result := make(chan error, 1)
			go func() { close(started); result <- commitDecision(t, s, claim, c, d) }()
			<-started
			must(t, tx.Commit(t.Context()))
			err = <-result
			if !errors.Is(err, ErrStale) {
				t.Fatal("stale preflight accepted", err)
			}
			assertNoLandingSideEffects(t, s, q, claim, len(c.Events)+1)
		})
	}
}

func TestLegacyStartOnly(t *testing.T) {
	t.Run("legacy-start-only", func(t *testing.T) {
		s := newStore(t)
		q, claim := readyLand(t, s)
		ok, err := s.BeginConsequential(t.Context(), claim, config, epoch)
		must(t, err)
		if !ok {
			t.Fatal("legacy claim rejected")
		}
		start := queue.LandingStarted{BatchID: "b1", AttemptID: "legacy-attempt", FenceToken: "fence", At: epoch}
		before := eventCount(t, s, q)
		must(t, s.StartLanding(t.Context(), claim, config, cp.Policy{}, nil, nil, start))
		if eventCount(t, s, q) != before+1 {
			t.Fatal("legacy start synthesized decision evidence")
		}
		_, want, err := queue.EncodeEvent(start)
		must(t, err)
		var raw []byte
		must(t, s.pool.QueryRow(t.Context(), `SELECT payload FROM events WHERE queue_id=$1 ORDER BY id DESC LIMIT 1`, q).Scan(&raw))
		var got, expected any
		must(t, json.Unmarshal(raw, &got))
		must(t, json.Unmarshal(want, &expected))
		if !reflect.DeepEqual(got, expected) {
			t.Fatal("legacy start payload changed")
		}
		state, _, err := s.Load(t.Context(), q)
		must(t, err)
		if len(state.LandingDecisions()) != 0 {
			t.Fatal("legacy decision invented")
		}
	})
}

func TestDecisionSnapshotSettlesAfterPauseRemovalAndHeadChange(t *testing.T) {
	for _, kind := range []string{"pause", "removal", "head"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t)
			q, claim, c, d := decisionLand(t, s)
			must(t, commitDecision(t, s, claim, c, d))
			switch kind {
			case "pause":
				appendEvents(t, s, q, queue.PauseRequested{Reason: "operator", Detail: "paused by operator"})
			case "removal":
				appendEvents(t, s, q, queue.LifecycleChanged{State: "retired", Detail: "configuration removed"})
			case "head":
				appendEvents(t, s, q, queue.PRObserved{PR: 12, HeadSHA: strings.Repeat("f", 40), Eligible: true, Trusted: true})
			}
			state, _, err := s.Load(t.Context(), q)
			must(t, err)
			planned := queue.Plan(c.Config, state, c.Start.At.Add(time.Hour))
			found := false
			for _, a := range planned {
				if _, ok := a.(queue.ResolveLanding); ok {
					found = true
				}
			}
			if !found {
				t.Fatal("settlement stopped", fmt.Sprint(planned))
			}
			appendEvents(t, s, q, queue.Landed{BatchID: c.Start.BatchID, NewBaseSHA: c.Land.TestedSHA, NewFence: "settled-fence"})
			state, _, err = s.Load(t.Context(), q)
			must(t, err)
			if len(state.LandingDecisions()) != 0 {
				t.Fatal("settled decision retained")
			}
			records, err := s.EventsAfter(t.Context(), q, 0)
			must(t, err)
			var historical []queue.LandingDecisionRecorded
			for _, r := range records {
				if d, ok := r.Event.(queue.LandingDecisionRecorded); ok {
					historical = append(historical, d)
				}
			}
			if !reflect.DeepEqual(historical, []queue.LandingDecisionRecorded{d}) {
				t.Fatal("settlement lost journal evidence")
			}
			if kind == "head" {
				r, _ := state.PRRevision(12)
				if r.HeadSHA == d.Approval.Identity.Head {
					t.Fatal("new head was lost")
				}
			}
		})
	}
}

func TestAtomicDecisionExecutorCrashWindows(t *testing.T) {
	type crashCase struct {
		window       string
		recordChecks bool
	}
	var cases []crashCase
	for _, recordChecks := range []bool{false, true} {
		for _, window := range []string{"last-start-stale", "before-push", "after-push", "trailers-mismatch", "lease-expires-during-forge", "fresh-read-completes-later"} {
			cases = append(cases, crashCase{window, recordChecks})
		}
	}
	for _, cse := range cases {
		window, recordChecks := cse.window, cse.recordChecks
		t.Run(fmt.Sprintf("%s/checks-%t", window, recordChecks), func(t *testing.T) {
			s := newStore(t)
			f := fake.New()
			repo := forge.RepoRef{Path: "Owner/Repo"}
			base, fence := f.Init(repo, "release", "openmerge/fence/release")
			pr, err := f.OpenPR(repo, 12, base, true)
			must(t, err)
			id, err := (QueueKey{Installation: "7", Repository: "Owner/Repo", BaseBranch: "release"}).ChangeID(12)
			must(t, err)
			meta := &forge.ChangeMetadata{ChangeID: string(id), Revision: 1, Labels: []string{"docs", "owned:user:owner", "unknown"}}
			tested, err := f.BuildTestRef(t.Context(), repo, forge.BuildRequest{Ref: "openmerge/queue/release/b1", BaseSHA: base, PRs: []forge.BuildPR{{Number: 12, HeadSHA: pr.HeadSHA, Metadata: meta}}, Method: forge.Squash, BatchID: "b1"}, forge.PushOptions{})
			must(t, err)
			raw, err := os.ReadFile("../queue/testdata/landing-start.json")
			must(t, err)
			for old, next := range map[string]string{strings.Repeat("a", 40): base, strings.Repeat("b", 40): pr.HeadSHA, strings.Repeat("e", 40): tested, `"fence"`: `"` + fence + `"`} {
				raw = bytes.ReplaceAll(raw, []byte(old), []byte(next))
			}
			var q QueueID
			var claim ClaimedAction
			var c landingCase
			var checkEvidence queue.LandingChecksRecorded
			if recordChecks {
				q, claim, c, _, checkEvidence = authorityCheckLandInput(t, s, raw)
			} else {
				q, claim, c, _ = decisionLandInput(t, s, raw)
			}
			before := eventCount(t, s, q)
			pushes := &startGit{Git: f}
			f.SetCheckRuns(repo, tested, []forge.CheckRun{{Name: "ci", State: forge.Success}})
			injected := errors.New("injected crash")
			clock := c.Start.At
			gate := &executor.LandingDecisionGate{Policy: c.Policy,
				Load: func(ctx context.Context) (queue.State, error) { state, _, err := s.Load(ctx, q); return state, err },
				ReadTestedMessages: func(_ context.Context, _ queue.Land) ([]string, error) {
					commit, err := f.Commit(repo, tested)
					if err != nil {
						return nil, err
					}
					message := commit.Message
					if window == "trailers-mismatch" {
						message = strings.ReplaceAll(message, "docs,owned:user:owner,unknown", "unknown")
					}
					return []string{message}, nil
				},
				ReadInputs: func(_ context.Context, _ queue.Land, _ queue.State) ([]queue.ApprovalEligibilityObserved, error) {
					if window == "lease-expires-during-forge" {
						clock = clock.Add(2 * time.Minute)
						c.Fresh.Basis.At = clock
					}
					if window == "fresh-read-completes-later" {
						clock = clock.Add(time.Millisecond)
						c.Fresh.Basis.At = clock
					}
					return []queue.ApprovalEligibilityObserved{c.Fresh}, nil
				},
				Commit: func(ctx context.Context, fresh []queue.ApprovalEligibilityObserved, ds []queue.LandingDecisionRecorded, start queue.LandingStarted) error {
					var checks []queue.LandingChecksRecorded
					if recordChecks {
						checkEvidence.AttemptID = start.AttemptID
						checks = append(checks, checkEvidence)
					}
					if window == "last-start-stale" {
						n := 0
						return s.startLanding(ctx, claim, c.Config, c.Policy, fresh, ds, start, func(ctx context.Context, tx pgx.Tx, e emission, event queue.Event) (bool, error) {
							n++
							if n == 2 {
								return true, nil
							}
							return s.authorizeStart(ctx, tx, e, event)
						}, checks...)
					}
					return s.StartLanding(ctx, claim, c.Config, c.Policy, fresh, ds, start, checks...)
				},
			}
			x := executor.Executor{Driver: f, Git: pushes, Repo: repo, Base: "release", RequiredChecks: []string{"ci"}, LandingDecisions: gate, Now: func() time.Time { return clock }, NewID: func() (string, error) { return c.Start.AttemptID, nil }}
			err = x.Execute(t.Context(), c.Land, func(e queue.Event) error {
				if _, ok := e.(queue.LandingCleanupPolicyRecorded); ok && window == "before-push" {
					return injected
				}
				if _, ok := e.(queue.Landed); ok && window == "after-push" {
					return injected
				}
				return s.Emit(t.Context(), claim, c.Config)(e)
			})
			if window == "last-start-stale" || window == "trailers-mismatch" || window == "lease-expires-during-forge" {
				if err == nil || pushes.calls != 0 {
					t.Fatal("stale evidence pushed", err, pushes.calls)
				}
				assertNoLandingSideEffects(t, s, q, claim, before)
				return
			}
			if window == "fresh-read-completes-later" {
				if err != nil || pushes.calls != 1 {
					t.Fatal("fresh observation after initial preflight clock was rejected", err, pushes.calls)
				}
				records, err := s.EventsAfter(t.Context(), q, 0)
				must(t, err)
				var frozen queue.LandingDecisionRecorded
				for _, record := range records {
					if v, ok := record.Event.(queue.LandingDecisionRecorded); ok {
						frozen = v
					}
				}
				if !frozen.Approval.Basis.At.Equal(clock) {
					t.Fatal("fresh basis not frozen")
				}
				return
			}
			if !errors.Is(err, injected) {
				t.Fatal("crash not injected", err)
			}
			if pushes.calls != map[string]int{"before-push": 0, "after-push": 1}[window] {
				t.Fatal("wrong push boundary", pushes.calls)
			}
			// A crash after commit must settle through the fence even after removal.
			appendEvents(t, s, q, queue.LifecycleChanged{State: "retired", Detail: "removed during crash"})
			state, _, err := s.Load(t.Context(), q)
			must(t, err)
			var resolve queue.Action
			for _, a := range queue.Plan(c.Config, state, c.Start.At.Add(time.Hour)) {
				if _, ok := a.(queue.ResolveLanding); ok {
					resolve = a
				}
			}
			if resolve == nil {
				t.Fatal("crashed attempt not recoverable")
			}
			x.LandingDecisions = nil
			must(t, x.Execute(t.Context(), resolve, func(e queue.Event) error { appendEvents(t, s, q, e); return nil }))
			state, _, err = s.Load(t.Context(), q)
			must(t, err)
			if len(state.LandingDecisions()) != 0 {
				t.Fatal("crash recovery retained settled decisions")
			}
			if state.Active != nil && state.Active.Phase == queue.Landing {
				t.Fatal("fence did not settle crash")
			}
			if recordChecks {
				records, err := s.EventsAfter(t.Context(), q, 0)
				must(t, err)
				found := 0
				for _, r := range records {
					if e, ok := r.Event.(queue.LandingChecksRecorded); ok {
						found++
						if !reflect.DeepEqual(e, checkEvidence) {
							t.Fatal("crash recovery rewrote check evidence")
						}
					}
				}
				if found != 1 {
					t.Fatal("crash recovery lost or duplicated check evidence", found)
				}
			}
		})
	}
}

func TestLandingRejectionJournalsFreshBeforeWaiting(t *testing.T) {
	s := newStore(t)
	q, claim, c, _ := decisionLand(t, s)
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	expected := []string{state.ApprovalFingerprint(c.Fresh.Basis.ChangeID)}
	c.Fresh.Actors = map[string]cp.Actor{}
	rejection, err := state.PrepareLandingRejection(c.Policy, c.Land, []queue.ApprovalEligibilityObserved{c.Fresh})
	must(t, err)
	before := eventCount(t, s, q)
	must(t, s.RejectLanding(t.Context(), claim, c.Config, c.Policy, []queue.ApprovalEligibilityObserved{c.Fresh}, expected, c.Start, rejection))
	if eventCount(t, s, q) != before+2 {
		t.Fatal("rejection did not journal exact fresh observation")
	}
	state, _, err = s.Load(t.Context(), q)
	must(t, err)
	waits := state.ApprovalWaits()
	facts, _ := state.ApprovalFacts(c.Fresh.Basis.ChangeID)
	if len(waits) != 1 || facts.Eligibility == nil || !reflect.DeepEqual(*facts.Eligibility, c.Fresh) || facts.Eligibility.Basis.At.After(waits[0].Basis.At) || len(state.LandingDecisions()) != 0 {
		t.Fatal("waiting basis precedes eligibility or partial decision committed")
	}
}

func TestAtomicLandingAllMembersValidatedBeforeInsert(t *testing.T) {
	s := newStore(t)
	raw, err := os.ReadFile("../queue/testdata/landing-start.json")
	must(t, err)
	var c landingCase
	must(t, json.Unmarshal(raw, &c))
	id, err := (QueueKey{Installation: "7", Repository: "Owner/Repo", BaseBranch: "release"}).ChangeID(13)
	must(t, err)
	rewrite := func(v []byte) []byte {
		var compact bytes.Buffer
		must(t, json.Compact(&compact, v))
		v = compact.Bytes()
		v = bytes.ReplaceAll(v, []byte(c.Fresh.Basis.ChangeID), []byte(id))
		v = bytes.ReplaceAll(v, []byte(c.Fresh.Basis.Head), []byte(strings.Repeat("9", 40)))
		v = bytes.ReplaceAll(v, []byte(`"pr":12`), []byte(`"pr":13`))
		return v
	}
	var events []json.RawMessage
	for _, raw := range c.Events {
		e, err := queue.DecodeEvent(raw)
		must(t, err)
		if start, ok := e.(queue.BatchStarted); ok {
			start.PRs = append(start.PRs, queue.BatchPR{PR: 13, HeadSHA: strings.Repeat("9", 40)})
			_, raw, err = queue.EncodeEvent(start)
			must(t, err)
		}
		events = append(events, raw)
		switch e.(type) {
		case queue.ChangeRevisionObserved, queue.ChangeClassified, queue.ChangeAccountabilityObserved, queue.ChangeIntentObserved, queue.ReviewStanceObserved, queue.PRObserved, queue.EnqueueRequested, queue.ApprovalReceiptRecorded:
			events = append(events, rewrite(raw))
		}
	}
	c.Events = events
	c.Land.PRs = append(c.Land.PRs, queue.BatchPR{PR: 13, HeadSHA: strings.Repeat("9", 40)})
	raw, err = json.Marshal(c)
	must(t, err)
	q, claim, c, d := decisionLandInput(t, s, raw)
	data, err := json.Marshal(c.Fresh)
	must(t, err)
	var second queue.ApprovalEligibilityObserved
	must(t, json.Unmarshal(rewrite(data), &second))
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	d2, err := state.PrepareLandingDecision(c.Policy, second, c.Start, c.Land)
	must(t, err)
	// Only the last member changes after preflight. Neither the first member's
	// eligibility nor its valid decision may commit ahead of that failure.
	d2.Approval.Identity.Generation++
	before := eventCount(t, s, q)
	err = s.StartLanding(t.Context(), claim, c.Config, c.Policy, []queue.ApprovalEligibilityObserved{c.Fresh, second}, []queue.LandingDecisionRecorded{d, d2}, c.Start)
	if !errors.Is(err, ErrStale) {
		t.Fatal("stale final member accepted", err)
	}
	assertNoLandingSideEffects(t, s, q, claim, before)
	d2, err = state.PrepareLandingDecision(c.Policy, second, c.Start, c.Land)
	must(t, err)
	must(t, s.StartLanding(t.Context(), claim, c.Config, c.Policy, []queue.ApprovalEligibilityObserved{c.Fresh, second}, []queue.LandingDecisionRecorded{d, d2}, c.Start))
	if eventCount(t, s, q) != before+5 {
		t.Fatal("complete two-member decision set not committed")
	}
}

func TestLegacyStartAfterApprovalPolicyRemoved(t *testing.T) {
	s := newStore(t)
	q, claim, c, _ := decisionLand(t, s)
	// Historical enriched facts do not turn a currently legacy queue into an
	// approval queue. Activation supplies the current nil approval policy.
	before := eventCount(t, s, q)
	must(t, s.StartLanding(t.Context(), claim, c.Config, cp.Policy{}, nil, nil, c.Start))
	if eventCount(t, s, q) != before+1 {
		t.Fatal("legacy-start-only wrote extra evidence after opt-out")
	}
}

func TestLandingLeaseExpiresWhileWaitingForQueueLock(t *testing.T) {
	s := newStore(t)
	q, claim, c, d := decisionLand(t, s)
	before := eventCount(t, s, q)
	tx, err := s.pool.Begin(t.Context())
	must(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	must(t, s.lockQueue(t.Context(), tx, q))
	_, err = tx.Exec(t.Context(), `UPDATE actions SET lease_until=$3 WHERE queue_id=$1 AND key=$2`, q, claim.Key, c.Start.At.Add(20*time.Millisecond))
	must(t, err)
	entering := make(chan struct{})
	s.beforeQueueLock = func() { close(entering) }
	result := make(chan error, 1)
	go func() { result <- commitDecision(t, s, claim, c, d) }()
	<-entering
	time.Sleep(50 * time.Millisecond)
	must(t, tx.Commit(t.Context()))
	err = <-result
	s.beforeQueueLock = nil
	if !errors.Is(err, ErrClaimLost) {
		t.Fatal("expired lease authorized using pre-lock time", err)
	}
	assertNoLandingSideEffects(t, s, q, claim, before)
}

func TestAtomicLandingRequiresCurrentPolicyBeforeEligibility(t *testing.T) {
	s := newStore(t)
	q, claim, c, d := decisionLand(t, s)
	before := eventCount(t, s, q)
	c.Policy.Digest = strings.Repeat("e", 64)
	if err := commitDecision(t, s, claim, c, d); err == nil {
		t.Fatal("old classification accepted under changed approvals")
	}
	assertNoLandingSideEffects(t, s, q, claim, before)
}
