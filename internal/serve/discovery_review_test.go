package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestCaseVariantHistoriesSettleByPersistedID(t *testing.T) {
	for _, bothLive := range []bool{false, true} {
		t.Run(map[bool]string{false: "newer landing", true: "conflicting live histories"}[bothLive], func(t *testing.T) {
			db, conn := databaseConnection(t)
			key := store.QueueKey{Installation: "7", Repository: repo.Path, BaseBranch: "main"}
			older, err := db.EnsureQueue(t.Context(), key)
			must(t, err)
			var newer store.QueueID
			_, err = conn.Exec(t.Context(), `UPDATE queues SET repository='Owner/Repo' WHERE id=$1`, older)
			must(t, err)
			err = conn.QueryRow(t.Context(), `INSERT INTO queues(installation,repository,base,lane) VALUES ('7','owner/repo','main','') RETURNING id`).Scan(&newer)
			must(t, err)
			// These are legacy histories from before canonical mappings existed.
			_, err = conn.Exec(t.Context(), `DELETE FROM canonical_queues`)
			must(t, err)
			if !bothLive {
				_, err = db.Append(t.Context(), older, []queue.Event{queue.FenceInitialized{Token: "stale-fence"}}, "")
				must(t, err)
			}
			f := fake.New()
			f.Init(repo, "main", "openmerge/fence/main")
			upper := forge.RepoRef{Path: "Owner/Repo"}
			f.Init(upper, "main", "openmerge/fence/main")
			_, err = f.PushBase(upper, "main", "config", map[string]string{".openmerge.yml": repositoryConfig})
			must(t, err)
			_, err = f.PushBase(repo, "main", "config", map[string]string{".openmerge.yml": repositoryConfig})
			must(t, err)
			target := managedRepository(f, repo.Path).Target("main")
			s, err := newServer(db, Options{})
			must(t, err)
			w, err := s.registerID(t.Context(), target, newer)
			must(t, err)
			must(t, s.initializeFence(t.Context(), w))
			recordLanding(t, s, w, f, true)
			f.SetCanWrite(repo, "writer", true)
			f.AddComment(repo, 41, "writer", "@openmerge queue")
			historicalComment := f.CommentRecords(repo, 41)[0]
			must(t, s.record(t.Context(), w, fmt.Sprintf("comment/%d", historicalComment.ID), true))
			must(t, s.record(t.Context(), w, "cursor/41", historicalComment.CreatedAt))
			_, err = db.Append(t.Context(), newer, []queue.Event{queue.PRObserved{PR: 98, HeadSHA: "new", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 98, At: time.Now()}}, "")
			must(t, err)
			if bothLive {
				olderTarget := target
				olderTarget.Key.Repository = upper.Path
				oldWorker, err := s.registerID(t.Context(), olderTarget, older)
				must(t, err)
				must(t, s.initializeFence(t.Context(), oldWorker))
				recordLanding(t, s, oldWorker, f, true)
				_, err = db.Append(t.Context(), older, []queue.Event{queue.PRObserved{PR: 99, HeadSHA: "old", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 99, At: time.Now()}}, "")
				must(t, err)
			}
			d := &discoveryFixture{f: f, repos: []Repository{managedRepository(f, repo.Path)}}
			d.installations = []Installation{{ID: "7", Repositories: func(context.Context) ([]Repository, error) { return d.repos, nil }}}
			restarted, err := NewDiscovery(t.Context(), db, d.source(), Options{})
			must(t, err)
			if len(restarted.workers()) != 2 {
				t.Fatal("historical queue lost")
			}
			if err := restarted.Refresh(t.Context()); err == nil {
				t.Fatal("admitted before case-variant landing settled")
			}
			for _, recovered := range restarted.workers() {
				must(t, restarted.cycle(t.Context(), recovered))
			}
			state, _, err := db.Load(t.Context(), newer)
			must(t, err)
			if state.Active != nil {
				t.Fatal("newer ID landing stranded")
			}
			err = restarted.Refresh(t.Context())
			if bothLive {
				if err == nil || !strings.Contains(err.Error(), "conflicting case-variant") {
					t.Fatalf("conflict not visible: %v", err)
				}
				for _, w := range restarted.workers() {
					if w.ready {
						t.Fatal("conflicting histories admitted")
					}
				}
			} else {
				must(t, err)
				// Drain the chosen newer history completely, then rediscover and
				// land again. A stale older fence must never become canonical.
				state = lifecycleState(t, restarted, w)
				for _, entry := range state.Entries {
					_, err = db.Append(t.Context(), newer, []queue.Event{queue.Dequeued{PR: entry.PR, AdmissionID: entry.AdmissionID, Reason: queue.Requested}}, "")
					must(t, err)
				}
				restarted, err = NewDiscovery(t.Context(), db, d.source(), Options{})
				must(t, err)
				must(t, restarted.Refresh(t.Context()))
				id, _, err := db.CanonicalQueue(t.Context(), key)
				must(t, err)
				if id != newer {
					t.Fatal("canonical history flipped after draining")
				}
				var canonical *worker
				for _, candidate := range restarted.workers() {
					if candidate.id == newer {
						canonical = candidate
					}
				}
				base, err := f.ReadRef(t.Context(), repo, "main")
				must(t, err)
				_, err = f.OpenPR(repo, 42, base, true)
				must(t, err)
				f.AddComment(repo, 42, "writer", "@openmerge queue")
				f.OnTestRef(func(sha string, _ []int) {
					f.SetCheckRuns(repo, sha, []forge.CheckRun{{ID: 1, Name: "ci", Source: "app:1", State: forge.Success, HeadBranch: "openmerge/queue/main/b2"}})
				})
				for range 3 {
					must(t, restarted.cycle(t.Context(), canonical))
					time.Sleep(2 * time.Millisecond)
				}
				state = lifecycleState(t, restarted, canonical)
				if state.Active != nil || !f.ContainsPR(repo, state.BaseSHA, 42) {
					t.Fatal("new landing failed after draining canonical history")
				}
				events, err := db.EventsAfter(t.Context(), newer, 0)
				must(t, err)
				requests := 0
				for _, event := range events {
					if e, ok := event.Event.(queue.EnqueueRequested); ok && e.PR == 41 {
						requests++
					}
				}
				if requests != 1 {
					t.Fatalf("historical queue command replayed: %d requests", requests)
				}
				for _, historical := range restarted.workers() {
					if !historical.settlementOnly {
						continue
					}
					historical.Driver = nil
					historical.Resolve = func(context.Context) (Target, error) {
						t.Fatal("settled historical variant resolved credentials")
						return Target{}, nil
					}
					must(t, restarted.cycle(t.Context(), historical))
				}
			}
		})
	}
}

func TestRulesStartupRetryAndReadinessVisibility(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(map[bool]string{false: "recovers", true: "persistent"}[persistent], func(t *testing.T) {
			s, d := newDiscoveryFixture(t)
			original := d.repos[0].Target
			calls := 0
			d.repos[0].Target = func(base string) Target {
				target := original(base)
				target.CheckRules = func(context.Context) ([]string, []string, error) {
					calls++
					if persistent || calls == 1 {
						return nil, nil, errors.New("offline")
					}
					return nil, nil, nil
				}
				return target
			}
			var logs bytes.Buffer
			restarted, err := NewDiscovery(t.Context(), s.store, d.source(), Options{PollInterval: 10 * time.Millisecond, RulesInterval: 20 * time.Millisecond, DiscoveryInterval: time.Hour, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
			must(t, err)
			if restarted.DiscoveryStatus().Repositories[0].State == "served" {
				t.Fatal("optimistic startup inventory")
			}
			if err := restarted.Refresh(t.Context()); err == nil {
				t.Fatal("failed validation accepted")
			}
			w := restarted.workers()[0]
			base, err := d.f.ReadRef(t.Context(), repo, "main")
			must(t, err)
			_, err = d.f.OpenPR(repo, 1, base, true)
			must(t, err)
			d.f.SetCanWrite(repo, "writer", true)
			d.f.AddComment(repo, 1, "writer", "@openmerge queue")
			must(t, restarted.cycle(t.Context(), w))
			if !strings.Contains(logs.String(), "ignored @openmerge queue") {
				t.Fatal("ignored command not logged")
			}
			recorder := httptest.NewRecorder()
			status.DynamicHandler(restarted, restarted.Keys, "", nil, restarted.DiscoveryStatus).ServeHTTP(recorder, httptest.NewRequest("GET", "/v1/queues", nil))
			var response status.Response
			must(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			if len(response.Queues) != 1 || response.Queues[0].Readiness == nil || response.Queues[0].Readiness.Ready || !strings.Contains(response.Queues[0].Readiness.Reason, "rulesets read failed") || response.Discovery.Repositories[0].State != "validating" {
				t.Fatalf("readiness missing: %s", recorder.Body.String())
			}
			if !strings.Contains(logs.String(), "rulesets read failed") {
				t.Fatal("missing waiting reason")
			}
			state := lifecycleState(t, restarted, w)
			if len(state.Blocks(queue.Config{})) != 0 {
				t.Fatal("transient failure wrote durable block")
			}
			checks := d.f.Statuses(repo, 1)
			found := false
			for _, check := range checks {
				if check.Reason == "validation-pending" && strings.Contains(check.Summary, "rulesets read failed") {
					found = true
				}
			}
			if !found {
				t.Fatalf("PR check hid readiness: %+v", checks)
			}
			if persistent {
				return
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- restarted.Run(ctx) }()
			admitted := false
			for ctx.Err() == nil {
				state, _, err := s.store.Load(ctx, w.id)
				if err == nil && len(state.Entries) > 0 {
					admitted = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			ready := restarted.QueueReadiness(w.Key).Ready
			cancel()
			must(t, <-done)
			if !admitted || !ready || calls < 2 {
				t.Fatal("not-ready queue failed to recover before discovery interval")
			}
		})
	}
}

type reportingQuota struct{ until time.Time }

func (e *reportingQuota) Error() string         { return "reporting budget exhausted" }
func (e *reportingQuota) RetryAfter() time.Time { return e.until }

type throttledReporting struct {
	forge.Driver
	reports int
}

func (d *throttledReporting) ReportStatus(context.Context, forge.RepoRef, int, string, forge.Status) error {
	d.reports++
	return &reportingQuota{time.Now().Add(time.Hour)}
}

func TestReportingQuotaDoesNotStopActiveLanding(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, f, _ := fixture(t, repositoryConfig)
		driver := &throttledReporting{Driver: w.Driver}
		w.Driver = driver
		f.AddComment(repo, 1, "writer", "@openmerge queue")
		must(t, s.cycle(t.Context(), w))
		state := lifecycleState(t, s, w)
		if state.Active == nil || state.Active.TestedSHA == "" {
			t.Fatal("reporting quota stopped build")
		}
		sha := state.Active.TestedSHA
		f.SetCheckRuns(repo, sha, []forge.CheckRun{{ID: 1, Name: "ci", Source: "app:1", State: forge.Success, HeadBranch: state.Active.TestRef}})
		time.Sleep(2 * time.Millisecond)
		must(t, s.cycle(t.Context(), w))
		state = lifecycleState(t, s, w)
		if state.Active != nil || state.BaseSHA != sha || driver.reports != 1 {
			t.Fatalf("reporting quota stopped landing: %+v reports=%d", state, driver.reports)
		}
	})
}

func TestExcludedInstallationAndRemovedWorkerAvoidCredentials(t *testing.T) {
	s, d := newDiscoveryFixture(t)
	s.discovery.InstallationAllowlist = "8"
	d.installations[0].Repositories = func(context.Context) ([]Repository, error) {
		t.Fatal("excluded installation enumerated")
		return nil, nil
	}
	must(t, s.Refresh(t.Context()))
	w := s.workers()[0]
	w.Driver = nil
	w.Resolve = func(context.Context) (Target, error) {
		t.Fatal("removed worker resolved credentials")
		return Target{}, nil
	}
	must(t, s.cycle(t.Context(), w))
	d.installations = nil
	must(t, s.Refresh(t.Context()))
	if len(s.DiscoveryStatus().Installations) != 0 {
		t.Fatal("uninstalled status row retained")
	}
}

func TestRepeatedInvalidConfigDoesNotAppendDuplicateBlocks(t *testing.T) {
	s, d := newDiscoveryFixture(t)
	w := s.workers()[0]
	_, err := d.f.PushBase(repo, "main", "invalid", map[string]string{".openmerge.yml": "version: broken"})
	must(t, err)
	must(t, s.Refresh(t.Context()))
	_, before, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	must(t, s.Refresh(t.Context()))
	_, after, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if before != after {
		t.Fatal("duplicate config blocks")
	}
}

func TestQuotaErrorsPreserveResetDeadline(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	until := time.Now().Add(time.Hour)
	w.Resolve = func(context.Context) (Target, error) { return Target{}, &reportingQuota{until} }
	w.Driver = nil
	var backoff *quotaBackoff
	if err := s.cycle(t.Context(), w); !errors.As(err, &backoff) || !backoff.until.Equal(until) {
		t.Fatalf("quota error not mapped: %v", err)
	}
	s.refreshResult(&reportingQuota{until})
	if !s.refreshRetryAt.Equal(until) {
		t.Fatal("discovery reset deadline lost")
	}
}

func TestDiscoveryQuotaBackoffIsScopedToInstallation(t *testing.T) {
	s, d := newDiscoveryFixture(t)
	calls, other := 0, 0
	until := time.Now().Add(time.Hour)
	d.installations[0].Repositories = func(context.Context) ([]Repository, error) { calls++; return nil, &reportingQuota{until} }
	d.installations = append(d.installations, Installation{ID: "8", Repositories: func(context.Context) ([]Repository, error) { other++; return nil, nil }})
	for range 2 {
		if err := s.Refresh(t.Context()); err == nil {
			t.Fatal("quota ignored")
		}
	}
	if calls != 1 || other != 2 {
		t.Fatalf("quota crossed installation boundary: limited=%d healthy=%d", calls, other)
	}
}

func TestRulesQuotaHonorsResetDeadline(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	until := time.Now().Add(time.Hour)
	w.CheckRules = func(context.Context) ([]string, []string, error) { return nil, nil, &reportingQuota{until} }
	var backoff *quotaBackoff
	if err := s.cycle(t.Context(), w); !errors.As(err, &backoff) || !backoff.until.Equal(until) {
		t.Fatalf("rules quota lost reset: %v", err)
	}
	if !w.rulesNextCheck.Equal(until) {
		t.Fatal("rules retry precedes reset")
	}
}

func TestCanonicalAdmissionCannotBypassIndependentHistoricalBlock(t *testing.T) {
	db, conn := databaseConnection(t)
	key := store.QueueKey{Installation: "7", Repository: "Owner/Repo", BaseBranch: "main"}
	older, err := db.EnsureQueue(t.Context(), key)
	must(t, err)
	var newer store.QueueID
	err = conn.QueryRow(t.Context(), `INSERT INTO queues(installation,repository,base,lane) VALUES ('7','owner/repo','main','') RETURNING id`).Scan(&newer)
	must(t, err)
	_, err = db.Append(t.Context(), older, []queue.Event{queue.BlockSourceBlocked{Source: queue.Operator, Detail: "hold"}}, "")
	must(t, err)
	_, err = db.Append(t.Context(), newer, []queue.Event{queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: time.Now()}}, "")
	must(t, err)
	if _, _, err := db.CanonicalQueue(t.Context(), key); err == nil {
		t.Fatal("canonical admission bypassed independent operator hold")
	}
}
