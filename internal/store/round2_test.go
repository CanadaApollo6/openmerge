package store

import (
	"errors"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestR2HistoricalWording(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	wm := appendEvents(t, s, q, queue.PRObserved{PR: 1, HeadSHA: "head"}, queue.EnqueueRequested{PR: 1, At: epoch})
	const old = `{"version":1,"type":"report_status","data":{"pr":1,"head_sha":"head","status":"blocked","reason":"not-eligible","detail":"PR does not satisfy queue entry conditions.","revision":2,"admission":0}}`
	a, err := queue.DecodeAction([]byte(old))
	must(t, err)
	_, err = s.pool.Exec(t.Context(), `INSERT INTO actions(queue_id,key,type,payload,watermark) VALUES ($1,$2,'report_status',$3::jsonb,$4)`, q, a.Key(), old, wm)
	must(t, err)
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	current := queue.Plan(config, state, epoch)[0].(queue.ReportStatus)
	if current.Detail == a.(queue.ReportStatus).Detail {
		t.Fatal("historical wording should differ from current wording")
	}
	planned, err := s.Plan(t.Context(), q, config, epoch)
	must(t, err)
	if len(planned) != 1 || planned[0].Action != a {
		t.Fatalf("old payload changed: %+v", planned)
	}
	b := ensureQueue(t, s, "healthy")
	appendEvents(t, s, b, seedEvents()...)
	_, err = s.Plan(t.Context(), b, config, epoch)
	must(t, err)
	claims, err := s.Claim(t.Context(), 2, time.Minute, epoch)
	must(t, err)
	if len(claims) != 2 || claims[0].Action != a || claims[1].QueueID != b {
		t.Fatalf("historical action blocked claims: %+v", claims)
	}
	t.Log("FIXED: historical wording difference preserves old report and healthy queue claim")
}

func r2Status(t *testing.T, s *Store, q QueueID, key string) string {
	t.Helper()
	var status string
	must(t, s.pool.QueryRow(t.Context(), `SELECT status FROM actions WHERE queue_id=$1 AND key=$2`, q, key).Scan(&status))
	return status
}

func TestR2DeadLandNotReactivated(t *testing.T) {
	s := newStore(t)
	q, c := readyLand(t, s)
	appendEvents(t, s, q, queue.BaseMoved{SHA: "base2"})
	ok, err := s.BeginConsequential(t.Context(), c, config, epoch)
	must(t, err)
	if ok {
		t.Fatal("discarded land authorized")
	}
	for _, base := range []string{"base", "base3", "base"} {
		appendEvents(t, s, q, queue.BaseMoved{SHA: base})
		_, err = s.Plan(t.Context(), q, config, epoch)
		must(t, err)
		if r2Status(t, s, q, c.Key) != "cancelled" {
			t.Fatal("resurrected obsolete Land")
		}
	}
	if !errors.Is(s.Complete(t.Context(), c, Done), ErrClaimLost) {
		t.Fatal("old token completed cancelled work")
	}
	t.Log("SAFE: discarded Land stays cancelled, including after the base returns to its old SHA")
}

func TestR2SettledResolveNotReactivated(t *testing.T) {
	s := newStore(t)
	q, c := readyLand(t, s)
	ok, err := s.BeginConsequential(t.Context(), c, config, epoch)
	must(t, err)
	if !ok {
		t.Fatal("land blocked")
	}
	must(t, s.Emit(t.Context(), q)(queue.LandingStarted{BatchID: "b1", AttemptID: "old", FenceToken: "f1", At: epoch}))
	must(t, s.Complete(t.Context(), c, Done))
	now := epoch.Add(2 * time.Minute)
	_, err = s.Plan(t.Context(), q, config, now)
	must(t, err)
	cs, err := s.Claim(t.Context(), 1, time.Minute, now)
	must(t, err)
	if len(cs) != 1 {
		t.Fatal(cs)
	}
	resolve := cs[0]
	appendEvents(t, s, q, queue.PRObserved{PR: 99, HeadSHA: "other"})
	later := config
	later.ResolveAfter = 5 * time.Minute
	ok, err = s.BeginConsequential(t.Context(), resolve, later, now)
	must(t, err)
	if ok {
		t.Fatal("not cancelled")
	}
	appendEvents(t, s, q, queue.LandingAbandoned{BatchID: "b1", NewFence: "f2"})
	_, err = s.Plan(t.Context(), q, later, epoch.Add(6*time.Minute))
	must(t, err)
	if r2Status(t, s, q, resolve.Key) != "cancelled" {
		t.Fatal("settled resolver resurrected")
	}
	t.Log("SAFE: cancelled resolution stays dead after its landing settles")
}

func TestR2PoisonedActiveWork(t *testing.T) {
	for _, kind := range []string{"land", "resolve_landing"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t)
			q, c := readyLand(t, s)
			now := epoch
			if kind == "resolve_landing" {
				ok, err := s.BeginConsequential(t.Context(), c, config, now)
				must(t, err)
				if !ok {
					t.Fatal("land blocked")
				}
				must(t, s.Emit(t.Context(), q)(queue.LandingStarted{BatchID: "b1", AttemptID: "uncertain", FenceToken: "f1", At: epoch}))
				must(t, s.Complete(t.Context(), c, Done))
				now = epoch.Add(2 * time.Minute)
				_, err = s.Plan(t.Context(), q, config, now)
				must(t, err)
			} else {
				must(t, s.Complete(t.Context(), c, Failed))
			}
			_, err := s.pool.Exec(t.Context(), `UPDATE actions SET payload=jsonb_set(payload,'{data,tested_sha}','17'::jsonb) WHERE queue_id=$1 AND type=$2`, q, kind)
			must(t, err)
			cs, err := s.Claim(t.Context(), 10, time.Minute, now)
			must(t, err)
			if len(cs) != 0 {
				t.Fatal(cs)
			}
			ps, err := s.Poisoned(t.Context())
			must(t, err)
			if len(ps) != 1 || ps[0].QueueID != q || ps[0].Error == "" || ps[0].Incidents != 1 {
				t.Fatalf("missing poison: %+v", ps)
			}
			planned, err := s.Plan(t.Context(), q, config, now)
			must(t, err)
			if len(planned) != 1 {
				t.Fatalf("active action not repaired: %+v", planned)
			}
			must(t, planned[0].Action.Validate())
			cs, err = s.Claim(t.Context(), 1, time.Minute, now)
			must(t, err)
			if len(cs) != 1 || cs[0].Key != planned[0].Key {
				t.Fatalf("repaired action not claimable: %+v", cs)
			}
			ok, err := s.BeginConsequential(t.Context(), cs[0], config, now)
			must(t, err)
			if !ok {
				t.Fatal("repaired action not authorized")
			}
			ps, err = s.Poisoned(t.Context())
			must(t, err)
			if len(ps) != 0 {
				t.Fatal(ps)
			}
		})
	}
}

func TestR2WireShape(t *testing.T) {
	for _, typ := range []string{"build_test_ref", "refresh_checks", "land", "resolve_landing", "delete_ref", "dequeue", "report_status", "mark_landed"} {
		if a, err := queue.DecodeAction([]byte(`{"version":1,"type":"` + typ + `","data":{}}`)); err == nil {
			t.Fatalf("accepted omitted fields: %+v", a)
		}
	}
	for _, p := range []string{
		`{"version":1,"type":"land","data":{"batch_id":"b1","unexpected":1}}`,
		`{"version":1,"type":"land","data":{"prs":[{"pr":1,"head_sha":"h","unexpected":1}]}}`,
		`{"version":1,"type":"land","data":{},"unexpected":1}`,
		`{"version":1,"type":"land","data":{"fence_token":null,"prs":null}}`,
	} {
		if _, err := queue.DecodeAction([]byte(p)); err == nil {
			t.Fatalf("accepted invalid payload: %s", p)
		}
	}
}

func r2ReadyForge(t *testing.T, s *Store) (QueueID, *fake.Forge, forge.RepoRef, *executor.Executor) {
	t.Helper()
	q := ensureQueue(t, s, "")
	f := fake.New()
	repo := forge.RepoRef{Path: "owner/repo"}
	base, fence := f.Init(repo, "main", "openmerge/fence/main")
	must(t, s.InitializeFence(t.Context(), q, fence))
	pr, err := f.OpenPR(repo, 1, base, true)
	must(t, err)
	appendEvents(t, s, q, queue.BaseMoved{SHA: base}, queue.PRObserved{PR: 1, HeadSHA: pr.HeadSHA, Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: epoch})
	f.OnTestRef(func(sha string, _ []int) {
		f.SetCheckRuns(repo, sha, []forge.CheckRun{{ID: 1, Name: "ci", State: forge.Success}})
	})
	x := &executor.Executor{Driver: f, Git: f, Repo: repo, Base: "main", RequiredChecks: []string{"ci"}, Now: func() time.Time { return epoch }}
	for i := 0; i < 2; i++ {
		_, err = s.Plan(t.Context(), q, config, epoch)
		must(t, err)
		cs, err := s.Claim(t.Context(), 1, time.Minute, epoch)
		must(t, err)
		if len(cs) != 1 {
			t.Fatal(cs)
		}
		c := cs[0]
		if Consequential(c.Action) {
			ok, err := s.BeginConsequential(t.Context(), c, config, epoch)
			must(t, err)
			if !ok {
				t.Fatal("blocked")
			}
		}
		must(t, x.Execute(t.Context(), c.Action, s.Emit(t.Context(), q)))
		must(t, s.Complete(t.Context(), c, Done))
	}
	return q, f, repo, x
}

func TestR2MissingLandFields(t *testing.T) {
	for _, field := range []string{"fence_token", "prs"} {
		t.Run(field, func(t *testing.T) {
			s := newStore(t)
			q, f, repo, _ := r2ReadyForge(t, s)
			_, err := s.Plan(t.Context(), q, config, epoch)
			must(t, err)
			_, err = s.pool.Exec(t.Context(), `UPDATE actions SET payload=payload #- ARRAY['data',$2]::text[] WHERE queue_id=$1 AND type='land'`, q, field)
			must(t, err)
			if field == "prs" {
				_, err = f.PushPR(repo, 1, "new unobserved head")
				must(t, err)
			}
			cs, err := s.Claim(t.Context(), 1, time.Minute, epoch)
			must(t, err)
			if len(cs) != 0 {
				t.Fatalf("malformed land claimed: %+v", cs)
			}
			state, _, err := s.Load(t.Context(), q)
			must(t, err)
			if state.Active == nil || state.Active.Phase != queue.Testing || len(state.Entries) != 1 {
				t.Fatalf("malformed land changed queue: %+v", state)
			}
			base, err := f.ReadRef(t.Context(), repo, "main")
			must(t, err)
			if base != state.BaseSHA {
				t.Fatal("malformed land changed forge base")
			}
			poisons, err := s.Poisoned(t.Context())
			must(t, err)
			if len(poisons) != 1 || poisons[0].Incidents != 1 {
				t.Fatalf("missing quarantine: %+v", poisons)
			}
		})
	}
}

func TestR2MissingDequeueAdmission(t *testing.T) {
	s := newStore(t)
	q, _, _, x := r2ReadyForge(t, s)
	appendEvents(t, s, q, queue.DequeueRequested{PR: 1})
	_, err := s.Plan(t.Context(), q, config, epoch)
	must(t, err)
	_, err = s.pool.Exec(t.Context(), `UPDATE actions SET payload=payload #- '{data,admission_id}' WHERE queue_id=$1 AND type='dequeue'`, q)
	must(t, err)
	cs, err := s.Claim(t.Context(), 10, time.Minute, epoch)
	must(t, err)
	for _, c := range cs {
		if _, bad := c.Action.(queue.Dequeue); bad {
			t.Fatal("dequeue without admission was claimed")
		}
		must(t, x.Execute(t.Context(), c.Action, s.Emit(t.Context(), q)))
		must(t, s.Complete(t.Context(), c, Done))
	}
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	if len(state.Entries) != 1 {
		t.Fatal("invalid dequeue changed admission")
	}
	p, err := s.Poisoned(t.Context())
	must(t, err)
	if len(p) != 1 {
		t.Fatalf("missing poison: %+v", p)
	}
	planned, err := s.Plan(t.Context(), q, config, epoch)
	must(t, err)
	if len(planned) != 1 {
		t.Fatalf("dequeue not repaired: %+v", planned)
	}
	cs, err = s.Claim(t.Context(), 1, time.Minute, epoch)
	must(t, err)
	if len(cs) != 1 {
		t.Fatal(cs)
	}
	if a, ok := cs[0].Action.(queue.Dequeue); !ok || a.AdmissionID != 1 {
		t.Fatal("repaired dequeue lost admission")
	}
	ok, err := s.BeginConsequential(t.Context(), cs[0], config, epoch)
	must(t, err)
	if !ok {
		t.Fatal("repaired dequeue cancelled")
	}
	must(t, x.Execute(t.Context(), cs[0].Action, s.Emit(t.Context(), q)))
	must(t, s.Complete(t.Context(), cs[0], Done))
	state, _, err = s.Load(t.Context(), q)
	must(t, err)
	if len(state.Entries) != 0 {
		t.Fatal("repaired dequeue did not finish")
	}
}
