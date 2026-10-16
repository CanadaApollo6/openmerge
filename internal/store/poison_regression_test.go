package store

import (
	"reflect"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func corruptTestedSHA(t *testing.T, s *Store, q QueueID, key string) {
	t.Helper()
	_, err := s.pool.Exec(t.Context(), `UPDATE actions SET payload=payload #- '{data,tested_sha}' WHERE queue_id=$1 AND key=$2`, q, key)
	must(t, err)
}

func startUncertainLanding(t *testing.T, s *Store) (QueueID, time.Time) {
	t.Helper()
	q, c := readyLand(t, s)
	ok, err := s.BeginConsequential(t.Context(), c, config, epoch)
	must(t, err)
	if !ok {
		t.Fatal("land not authorized")
	}
	must(t, s.Emit(t.Context(), q)(queue.LandingStarted{BatchID: "b1", AttemptID: "uncertain", FenceToken: "fence", At: epoch}))
	must(t, s.Complete(t.Context(), c, Done))
	return q, epoch.Add(2 * time.Minute)
}

func hasResolver(ps []PlannedAction) bool {
	for _, p := range ps {
		if _, ok := p.Action.(queue.ResolveLanding); ok {
			return true
		}
	}
	return false
}

func TestPoisonRepairUsesFreshPlannerPayload(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	appendEvents(t, s, q, seedEvents()...)
	ps, err := s.Plan(t.Context(), q, config, epoch)
	must(t, err)
	if len(ps) != 1 {
		t.Fatal(ps)
	}
	key := ps[0].Key
	_, err = s.pool.Exec(t.Context(), `UPDATE actions SET payload=payload #- '{data,base_sha}' WHERE queue_id=$1 AND key=$2`, q, key)
	must(t, err)
	cs, err := s.Claim(t.Context(), 1, time.Minute, epoch)
	must(t, err)
	if len(cs) != 0 {
		t.Fatal(cs)
	}
	cfg := config
	cfg.MergeMethod = queue.Rebase
	ps, err = s.Plan(t.Context(), q, cfg, epoch)
	must(t, err)
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	if len(ps) != 1 || !reflect.DeepEqual(ps[0].Action, queue.Plan(cfg, state, epoch)[0]) || ps[0].Action.(queue.BuildTestRef).Method != queue.Rebase {
		t.Fatalf("not fresh planner payload: %+v", ps)
	}
	t.Log("same key repaired with current Rebase configuration, old Squash payload discarded")
}

func TestObsoletePoisonNeverReactivates(t *testing.T) {
	for _, kind := range []string{"land", "resolve"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t)
			var q QueueID
			var key string
			now := epoch
			if kind == "land" {
				var c ClaimedAction
				q, c = readyLand(t, s)
				must(t, s.Complete(t.Context(), c, Failed))
				key = c.Key
			} else {
				q, now = startUncertainLanding(t, s)
				ps, err := s.Plan(t.Context(), q, config, now)
				must(t, err)
				key = ps[0].Key
			}
			corruptTestedSHA(t, s, q, key)
			cs, err := s.Claim(t.Context(), 1, time.Minute, now)
			must(t, err)
			if len(cs) != 0 {
				t.Fatal(cs)
			}
			if kind == "land" {
				appendEvents(t, s, q, queue.BaseMoved{SHA: "base2"})
			} else {
				appendEvents(t, s, q, queue.LandingAbandoned{BatchID: "b1", NewFence: "f2"})
			}
			for range 5 {
				ps, err := s.Plan(t.Context(), q, config, now)
				must(t, err)
				for _, p := range ps {
					if p.Key == key {
						t.Fatalf("obsolete %s revived", key)
					}
				}
			}
			if r2Status(t, s, q, key) != "poisoned" {
				t.Fatal("obsolete poison changed status")
			}
		})
	}
}

func TestPausedLandingStillResolves(t *testing.T) {
	for _, stage := range []string{"not-planned", "already-claimed", "resolver-poisoned-three-times", "pause-during-plan"} {
		t.Run(stage, func(t *testing.T) {
			s := newStore(t)
			q, now := startUncertainLanding(t, s)
			if stage == "already-claimed" {
				_, err := s.Plan(t.Context(), q, config, now)
				must(t, err)
				cs, err := s.Claim(t.Context(), 1, time.Minute, now)
				must(t, err)
				if len(cs) != 1 {
					t.Fatal(cs)
				}
				appendEvents(t, s, q, queue.ActionPoisoned{Key: "unrelated-effect", Reason: "invalid payload"})
				ok, err := s.BeginConsequential(t.Context(), cs[0], config, now)
				must(t, err)
				if !ok {
					t.Fatalf("pause cancelled in-flight recovery: status=%s", r2Status(t, s, q, cs[0].Key))
				}
			} else {
				switch stage {
				case "pause-during-plan":
					_, err := s.pool.Exec(t.Context(), `UPDATE actions SET status = 'planned', poison_count = 2,
						payload = payload #- '{data,tested_sha}' WHERE queue_id = $1 AND type = 'land'`, q)
					must(t, err)
				case "not-planned":
					appendEvents(t, s, q, queue.ActionPoisoned{Key: "unrelated-effect", Reason: "invalid payload"})
				default:
					for range 3 {
						ps, err := s.Plan(t.Context(), q, config, now)
						must(t, err)
						if len(ps) != 1 {
							t.Fatal(ps)
						}
						corruptTestedSHA(t, s, q, ps[0].Key)
						cs, err := s.Claim(t.Context(), 1, time.Minute, now)
						must(t, err)
						if len(cs) != 0 {
							t.Fatal(cs)
						}
					}
					appendEvents(t, s, q, queue.ActionPoisoned{Key: "unrelated-effect", Reason: "invalid payload"})
				}
				ps, err := s.Plan(t.Context(), q, config, now)
				must(t, err)
				if !hasResolver(ps) {
					t.Fatalf("paused Landing has no resolver: %+v", ps)
				}
			}
		})
	}
}

func TestPoisonCountsStayBoundedUntilResume(t *testing.T) {
	s := newStore(t)
	q, c := readyLand(t, s)
	must(t, s.Complete(t.Context(), c, Failed))
	for cycle := 0; cycle < 2; cycle++ {
		for incident := 1; incident <= 3; incident++ {
			_, err := s.Plan(t.Context(), q, config, epoch)
			must(t, err)
			corruptTestedSHA(t, s, q, c.Key)
			_, err = s.Claim(t.Context(), 1, time.Minute, epoch)
			must(t, err)
			if n := count(t, s, `SELECT poison_count FROM actions WHERE queue_id=$1 AND key=$2`, q, c.Key); n != incident {
				t.Fatalf("count %d, want %d", n, incident)
			}
		}
		for range 30 {
			_, err := s.Plan(t.Context(), q, config, epoch)
			must(t, err)
			_, err = s.Claim(t.Context(), 10, time.Minute, epoch)
			must(t, err)
		}
		if n := count(t, s, `SELECT poison_count FROM actions WHERE queue_id=$1 AND key=$2`, q, c.Key); n != 3 {
			t.Fatalf("counter grew while idle: %d", n)
		}
		if n := count(t, s, `SELECT count(*) FROM events WHERE queue_id=$1 AND type='action_poisoned'`, q); n != cycle+1 {
			t.Fatalf("event flood: %d", n)
		}
		appendEvents(t, s, q, queue.ResumeRequested{})
		if n := count(t, s, `SELECT poison_count FROM actions WHERE queue_id=$1 AND key=$2`, q, c.Key); n != 0 {
			t.Fatal("resume failed")
		}
	}
	t.Log("one notification per three incidents, stable across 30 idle plan/claim cycles; resume resets counter")
}

// Corrupt the pause report past its own cap, then keep planning and claiming.
// Neither another pause event nor another report key may appear until resume.
func TestPoisonedPauseReportsDoNotCreateNewPauses(t *testing.T) {
	s := newStore(t)
	q, c := readyLand(t, s)
	must(t, s.Complete(t.Context(), c, Failed))
	for cycle := 0; cycle < 2; cycle++ {
		for range 3 {
			_, err := s.Plan(t.Context(), q, config, epoch)
			must(t, err)
			corruptTestedSHA(t, s, q, c.Key)
			claims, err := s.Claim(t.Context(), 1, time.Minute, epoch)
			must(t, err)
			if len(claims) != 0 {
				t.Fatal(claims)
			}
		}
		var reportKey string
		for incident := 1; incident <= 12; incident++ {
			planned, err := s.Plan(t.Context(), q, config, epoch)
			must(t, err)
			if incident <= 3 {
				if len(planned) != 1 {
					t.Fatal(planned)
				}
				if _, ok := planned[0].Action.(queue.ReportStatus); !ok {
					t.Fatal(planned)
				}
				if reportKey == "" {
					reportKey = planned[0].Key
				}
				if planned[0].Key != reportKey {
					t.Fatal("pause corruption minted another report key")
				}
			} else if len(planned) != 0 {
				t.Fatalf("pause report bypassed cap: %+v", planned)
			}
			_, err = s.pool.Exec(t.Context(), `UPDATE actions SET payload=payload #- '{data,detail}' WHERE queue_id=$1 AND key=$2`, q, reportKey)
			must(t, err)
			claims, err := s.Claim(t.Context(), 10, time.Minute, epoch)
			must(t, err)
			if len(claims) != 0 {
				t.Fatal(claims)
			}
			if n := count(t, s, `SELECT count(*) FROM events WHERE queue_id=$1 AND type='action_poisoned'`, q); n != cycle+1 {
				t.Fatalf("pause event count=%d, want %d", n, cycle+1)
			}
			if n := count(t, s, `SELECT count(*) FROM actions WHERE queue_id=$1 AND type='report_status'`, q); n != cycle+1 {
				t.Fatalf("pause report count=%d, want %d", n, cycle+1)
			}
		}
		if n := count(t, s, `SELECT poison_count FROM actions WHERE queue_id=$1 AND key=$2`, q, reportKey); n != 3 {
			t.Fatalf("report incidents=%d, want 3", n)
		}
		state, _, err := s.Load(t.Context(), q)
		must(t, err)
		pure := queue.Plan(config, state, epoch)
		if len(pure) != 1 || pure[0].Key() != reportKey {
			t.Fatalf("pause report changed: %+v", pure)
		}
		appendEvents(t, s, q, queue.ResumeRequested{})
	}
}

func TestPoisonedResolverAlwaysReactivates(t *testing.T) {
	for _, detectAt := range []string{"plan", "claim", "begin"} {
		t.Run(detectAt, func(t *testing.T) {
			s := newStore(t)
			q, now := startUncertainLanding(t, s)
			state, _, err := s.Load(t.Context(), q)
			must(t, err)
			expected := queue.Plan(config, state, now)[0]
			for incident := 1; incident <= 5; incident++ {
				planned, err := s.Plan(t.Context(), q, config, now)
				must(t, err)
				if len(planned) != 1 || !reflect.DeepEqual(planned[0].Action, expected) {
					t.Fatalf("resolver not repaired from planner: %+v", planned)
				}
				var claim ClaimedAction
				if detectAt == "begin" {
					claims, err := s.Claim(t.Context(), 1, time.Minute, now)
					must(t, err)
					if len(claims) != 1 {
						t.Fatal(claims)
					}
					claim = claims[0]
				}
				corruptTestedSHA(t, s, q, expected.Key())
				switch detectAt {
				case "plan":
					planned, err = s.Plan(t.Context(), q, config, now)
					must(t, err)
					if len(planned) != 0 {
						t.Fatal("returned corrupt resolver")
					}
				case "claim":
					claims, err := s.Claim(t.Context(), 1, time.Minute, now)
					must(t, err)
					if len(claims) != 0 {
						t.Fatal("claimed corrupt resolver")
					}
				case "begin":
					ok, err := s.BeginConsequential(t.Context(), claim, config, now)
					if ok || err == nil {
						t.Fatal("began corrupt resolver")
					}
				}
				if n := count(t, s, `SELECT poison_count FROM actions WHERE queue_id=$1 AND key=$2`, q, expected.Key()); n != incident {
					t.Fatalf("resolver incidents=%d, want %d", n, incident)
				}
				state, _, err = s.Load(t.Context(), q)
				must(t, err)
				if state.PauseReason(config) != "" {
					t.Fatal("resolver poison paused queue")
				}
				if n := count(t, s, `SELECT count(*) FROM events WHERE queue_id=$1 AND type='action_poisoned'`, q); n != 0 {
					t.Fatal("resolver poison appended pause event")
				}
			}
			planned, err := s.Plan(t.Context(), q, config, now)
			must(t, err)
			if len(planned) != 1 || !reflect.DeepEqual(planned[0].Action, expected) {
				t.Fatal(planned)
			}
			claims, err := s.Claim(t.Context(), 1, time.Minute, now)
			must(t, err)
			if len(claims) != 1 {
				t.Fatal(claims)
			}
			ok, err := s.BeginConsequential(t.Context(), claims[0], config, now)
			must(t, err)
			if !ok {
				t.Fatal("repaired resolver not authorized")
			}
			appendEvents(t, s, q, queue.LandingAbandoned{BatchID: "b1", NewFence: "settled"})
			must(t, s.Complete(t.Context(), claims[0], Done))
			state, _, err = s.Load(t.Context(), q)
			must(t, err)
			if state.Active != nil || len(queue.Plan(config, state, now)) == 0 || state.Fence != "settled" {
				t.Fatalf("resolver did not settle landing: %+v", state)
			}
		})
	}
}
