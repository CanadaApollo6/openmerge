package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

func TestPoisonedLandRepairsPausesAndResumes(t *testing.T) {
	for _, detectAt := range []string{"plan", "claim", "begin"} {
		for _, incidents := range []int{1, 3} {
			t.Run(fmt.Sprintf("%s/%d", detectAt, incidents), func(t *testing.T) {
				s := newStore(t)
				q, f, repo, x := r2ReadyForge(t, s)
				planned, err := s.Plan(t.Context(), q, config, epoch)
				must(t, err)
				if len(planned) != 1 {
					t.Fatal(planned)
				}
				key := planned[0].Key
				land := planned[0].Action.(queue.Land)
				var stale ClaimedAction
				for incident := 1; incident <= incidents; incident++ {
					if detectAt == "begin" {
						claims, err := s.Claim(t.Context(), 1, time.Minute, epoch)
						must(t, err)
						if len(claims) != 1 {
							t.Fatal(claims)
						}
						stale = claims[0]
					}
					// Alternate a wire decode error and a semantic validation error.
					corrupt := `17`
					if incident == 2 {
						corrupt = `""`
					}
					_, err := s.pool.Exec(t.Context(), `UPDATE actions SET payload = jsonb_set(payload, '{data,tested_sha}', $3::jsonb)
						WHERE queue_id = $1 AND key = $2`, q, key, corrupt)
					must(t, err)
					switch detectAt {
					case "plan":
						actions, err := s.Plan(t.Context(), q, config, epoch)
						must(t, err)
						if len(actions) != 0 {
							t.Fatal("returned malformed action")
						}
					case "claim":
						claims, err := s.Claim(t.Context(), 1, time.Minute, epoch)
						must(t, err)
						if len(claims) != 0 {
							t.Fatal("claimed malformed action")
						}
					case "begin":
						ok, err := s.BeginConsequential(t.Context(), stale, config, epoch)
						if ok || err == nil {
							t.Fatal("began malformed action")
						}
						if !errors.Is(s.Complete(t.Context(), stale, Done), ErrClaimLost) {
							t.Fatal("quarantined claim could complete")
						}
					}
					poison, err := s.Poisoned(t.Context())
					must(t, err)
					if len(poison) != 1 || poison[0].Key != key || poison[0].Incidents != incident {
						t.Fatalf("wrong poison evidence: %+v", poison)
					}
					if incident < incidents {
						planned, err = s.Plan(t.Context(), q, config, epoch)
						must(t, err)
						if len(planned) != 1 || planned[0].Key != key {
							t.Fatalf("not repaired: %+v", planned)
						}
					}
				}
				state, _, err := s.Load(t.Context(), q)
				must(t, err)
				if incidents == 3 {
					if state.PauseReason(config) != "action-poisoned" || state.Active == nil || state.Active.Phase != queue.Testing {
						t.Fatalf("poison pause lost active work: %+v", state)
					}
					for range 3 {
						planned, err = s.Plan(t.Context(), q, config, epoch)
						must(t, err)
						if len(planned) != 1 || r2Status(t, s, q, key) != "poisoned" {
							t.Fatalf("paused action reactivated or report missing: %+v", planned)
						}
					}
					if n := count(t, s, `SELECT count(*) FROM events WHERE queue_id = $1 AND type = 'block_source_blocked' AND payload->'data'->>'source' = 'action-poisoned'`, q); n != 1 {
						t.Fatalf("poison event count = %d", n)
					}
					claims, err := s.Claim(t.Context(), 1, time.Minute, epoch)
					must(t, err)
					if len(claims) != 1 {
						t.Fatal(claims)
					}
					must(t, x.Execute(t.Context(), claims[0].Action, s.Emit(t.Context(), claims[0], config)))
					must(t, s.Complete(t.Context(), claims[0], Done))
					status := f.Statuses(repo, 1)[land.PRs[0].HeadSHA]
					if status.State != forge.Paused || status.Reason != "action-poisoned" || !strings.Contains(status.Summary, key) {
						t.Fatalf("pause not reported on PR: %+v", status)
					}
					planned, err = s.Plan(t.Context(), q, config, epoch)
					must(t, err)
					if len(planned) != 0 {
						t.Fatal("acknowledged pause kept producing work")
					}
					clearActionPoison(t, s, q)
					if n := count(t, s, `SELECT poison_count FROM actions WHERE queue_id = $1 AND key = $2`, q, key); n != 0 {
						t.Fatal("resume did not reset incidents")
					}
				} else if state.PauseReason(config) != "" {
					t.Fatal("single poison incident paused queue")
				}
				planned, err = s.Plan(t.Context(), q, config, epoch)
				must(t, err)
				if len(planned) != 1 || planned[0].Key != key {
					t.Fatalf("land did not repair: %+v", planned)
				}
				claims, err := s.Claim(t.Context(), 1, time.Minute, epoch)
				must(t, err)
				if len(claims) != 1 || claims[0].Key != key || claims[0].AttemptID == stale.AttemptID {
					t.Fatalf("repair did not acquire a fresh claim: %+v", claims)
				}
				ok, err := s.BeginConsequential(t.Context(), claims[0], config, epoch)
				must(t, err)
				if !ok {
					t.Fatal("repaired landing not authorized")
				}
				must(t, x.Execute(t.Context(), claims[0].Action, s.Emit(t.Context(), claims[0], config)))
				must(t, s.Complete(t.Context(), claims[0], Done))
				state, _, err = s.Load(t.Context(), q)
				must(t, err)
				actualBase, err := f.ReadRef(t.Context(), repo, "main")
				must(t, err)
				if state.Active != nil || len(state.Entries) != 0 || state.PauseReason(config) != "" || state.BaseSHA != land.TestedSHA || actualBase != land.TestedSHA {
					t.Fatalf("repaired work did not land: %+v", state)
				}
			})
		}
	}
}

func TestPlanPublishesInterruptedPoisonNotification(t *testing.T) {
	s := newStore(t)
	q, claim := readyLand(t, s)
	// Simulate the committed quarantine before Claim's follow-up transaction.
	_, err := s.pool.Exec(t.Context(), `UPDATE actions SET status = 'poisoned', error = 'invalid payload',
		poison_count = 3, attempt_id = NULL, lease_until = NULL WHERE queue_id = $1 AND key = $2`, q, claim.Key)
	must(t, err)
	planned, err := s.Plan(t.Context(), q, config, epoch)
	must(t, err)
	if len(planned) != 1 {
		t.Fatal(planned)
	}
	if report, ok := planned[0].Action.(queue.ReportStatus); !ok || report.Reason != "action-poisoned" {
		t.Fatalf("missed pending pause: %+v", planned)
	}
	if n := count(t, s, `SELECT count(*) FROM events WHERE queue_id = $1 AND type = 'block_source_blocked' AND payload->'data'->>'source' = 'action-poisoned'`, q); n != 1 {
		t.Fatal("interrupted notification was lost")
	}
}

func TestResumeDeduplicationDoesNotResetNewIncidents(t *testing.T) {
	s := newStore(t)
	q, claim := readyLand(t, s)
	_, err := s.Append(t.Context(), q, []queue.Event{queue.ResumeRequested{}}, "resume/delivery")
	must(t, err)
	_, err = s.pool.Exec(t.Context(), `UPDATE actions SET poison_count = 2 WHERE queue_id = $1 AND key = $2`, q, claim.Key)
	must(t, err)
	_, err = s.Append(t.Context(), q, []queue.Event{queue.ResumeRequested{}}, "resume/delivery")
	must(t, err)
	if n := count(t, s, `SELECT poison_count FROM actions WHERE queue_id = $1 AND key = $2`, q, claim.Key); n != 2 {
		t.Fatal("duplicate resume reset incidents")
	}
}

func TestPoisonPauseAndQuarantineRollbackTogether(t *testing.T) {
	s := newStore(t)
	q, claim := readyLand(t, s)
	rollback := errors.New("rollback")
	err := pgx.BeginFunc(t.Context(), s.pool, func(tx pgx.Tx) error {
		must(t, s.lockQueue(t.Context(), tx, q))
		_, err := tx.Exec(t.Context(), `UPDATE actions SET status = 'poisoned', error = 'bad', poison_count = 3,
			attempt_id = NULL, lease_until = NULL WHERE queue_id = $1 AND key = $2`, q, claim.Key)
		must(t, err)
		published, err := publishPoisonPauses(t.Context(), tx, q)
		must(t, err)
		if !published {
			t.Fatal("missing pause event")
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if r2Status(t, s, q, claim.Key) != "claimed" || count(t, s, `SELECT count(*) FROM events WHERE type = 'block_source_blocked' AND payload->'data'->>'source' = 'action-poisoned'`) != 0 {
		t.Fatal("partial poison transaction committed")
	}
}

func TestPoisonMigrationCountsExistingQuarantine(t *testing.T) {
	url := databaseURL(t)
	conn, err := pgx.Connect(t.Context(), url)
	must(t, err)
	defer func() { must(t, conn.Close(t.Context())) }()
	sql, err := migrations.ReadFile("migrations/001_initial.sql")
	must(t, err)
	_, err = conn.Exec(t.Context(), string(sql))
	must(t, err)
	_, err = conn.Exec(t.Context(), `CREATE TABLE schema_migrations (version text PRIMARY KEY);
		INSERT INTO schema_migrations VALUES ('migrations/001_initial.sql');
		INSERT INTO queues(installation, repository, base) VALUES ('installation/1', 'owner/repo', 'main');
		INSERT INTO actions(queue_id, key, type, payload, watermark, status, error)
		VALUES (1, 'batch/b1/land', 'land', '{"version":1,"type":"land","data":{}}', 0, 'poisoned', 'bad payload')`)
	must(t, err)
	s := openTestStore(t, url)
	poison, err := s.Poisoned(t.Context())
	must(t, err)
	if len(poison) != 1 || poison[0].Incidents != 1 || poison[0].Error != "bad payload" {
		t.Fatalf("upgrade lost quarantine history: %+v", poison)
	}
}

func clearActionPoison(t *testing.T, s *Store, q QueueID) {
	t.Helper()
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	appendEvents(t, s, q, queue.BlockSourceCleared{Source: queue.ActionPoison, Basis: state.BlockSourceRevision(queue.ActionPoison)})
}

func TestOnlyCurrentPoisonSourceClearResetsIncidents(t *testing.T) {
	s := newStore(t)
	q, claim := readyLand(t, s)
	appendEvents(t, s, q, queue.BlockSourceBlocked{Source: queue.ActionPoison, Detail: "poisoned", Subject: claim.Key}, queue.BlockSourceBlocked{Source: queue.ConfigSource, Detail: "config failure"})
	_, err := s.pool.Exec(t.Context(), `UPDATE actions SET poison_count = 3, poison_notified = true WHERE queue_id = $1 AND key = $2`, q, claim.Key)
	must(t, err)
	appendEvents(t, s, q, queue.BlockSourceCleared{Source: queue.ConfigSource, Basis: 1}, queue.BlockSourceCleared{Source: queue.ActionPoison, Basis: 0})
	if n := count(t, s, `SELECT poison_count FROM actions WHERE queue_id = $1 AND key = $2`, q, claim.Key); n != 3 {
		t.Fatal("unrelated/stale clear reset poison incidents")
	}
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	if !state.SourceBlocked(queue.ActionPoison) {
		t.Fatal("stale clear removed poison block")
	}
	clearActionPoison(t, s, q)
	if n := count(t, s, `SELECT poison_count FROM actions WHERE queue_id = $1 AND key = $2`, q, claim.Key); n != 0 {
		t.Fatal("current poison clear did not reset incidents")
	}
}
