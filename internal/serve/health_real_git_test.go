package serve

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

type healthRealDriver struct{ *fake.Forge }

func (healthRealDriver) DefaultBranch(context.Context, forge.RepoRef) (string, error) {
	return "main", nil
}
func (healthRealDriver) ReadFile(_ context.Context, _ forge.RepoRef, _ string, path string) ([]byte, error) {
	if path == ".openmerge.yml" {
		return []byte(repositoryConfig), nil
	}
	return nil, forge.ErrFileNotFound
}

// Use production Git against a disposable bare remote and cache. Removing the
// remote must fail a real reconciliation/settlement without refreshing success.
func TestHealthRealGitUnavailableRemote(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "settled", true: "pending"}[pending], func(t *testing.T) {
			f := newCompositionFixture(t, []int{1})
			driver := healthRealDriver{fake.New()}
			driver.Init(f.w.repo(), "main", "fixture-fence")
			target := Target{Key: f.w.Key, Git: f.g, Driver: driver, BotLogin: fake.Identity}
			s, err := New(t.Context(), database(t), []Target{target}, Options{PollInterval: time.Minute, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			must(t, err)
			w := s.queues[0]
			must(t, s.cycle(t.Context(), w))
			{
				state, _, err := s.store.Load(t.Context(), w.id)
				must(t, err)
				_, err = s.store.Append(t.Context(), w.id, []queue.Event{
					queue.PRObserved{PR: 1, HeadSHA: f.prs[0].HeadSHA, Eligible: true, Trusted: true},
					queue.EnqueueRequested{PR: 1, At: time.Now()},
					queue.BatchStarted{BatchID: "b1", BaseSHA: f.base, PRs: f.pins, At: time.Now()},
					queue.TestRefBuilt{BatchID: "b1", TestedSHA: f.prs[0].HeadSHA, Method: queue.Merge, At: time.Now()},
					queue.LandingStarted{BatchID: "b1", AttemptID: "pending", FenceToken: state.Fence, At: time.Now().Add(-time.Hour)},
				}, "")
				must(t, err)
				state, _, err = s.store.Load(t.Context(), w.id)
				must(t, err)
				if state.Active == nil || state.Active.Phase != queue.Landing {
					t.Fatal("fixture lacks pending start")
				}
			}
			if !pending {
				must(t, s.cycle(t.Context(), w))
				state, watermark, err := s.store.Load(t.Context(), w.id)
				must(t, err)
				if state.Active != nil {
					t.Fatal("production real-git settlement left an active attempt")
				}
				t.Logf("settled pending attempt: base=%s head=%s fence=%s watermark=%d", f.base, f.prs[0].HeadSHA, state.Fence, watermark)
			}
			success := w.health.Load().LastSuccess
			if success == nil {
				t.Fatal("healthy real-git reconciliation did not publish success")
			}
			must(t, os.Rename(f.remote, f.remote+".offline"))
			defer func() { must(t, os.Rename(f.remote+".offline", f.remote)) }()
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() { defer close(done); s.loop(ctx, w) }()
			defer func() { cancel(); <-done }()
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				q := w.health.Load()
				if q.Failures == 1 && q.NextDeadline != nil && q.NextDeadline.After(time.Now()) {
					if q.LastSuccess == nil || !q.LastSuccess.Equal(*success) {
						t.Fatal("unavailable remote refreshed success")
					}
					until := time.Until(*q.NextDeadline)
					if until < 4*time.Second || until > 5*time.Second {
						t.Fatalf("wrong real error backoff: %s", until)
					}
					if !q.Watched {
						t.Fatal("unavailable queue lost health watch")
					}
					state, watermark, err := s.store.Load(t.Context(), w.id)
					must(t, err)
					t.Logf("pending=%t base=%s head=%s fence=%s watermark=%d last_success=%s next_deadline=%s failures=%d", pending, f.base, f.prs[0].HeadSHA, state.Fence, watermark, q.LastSuccess.UTC().Format(time.RFC3339Nano), q.NextDeadline.UTC().Format(time.RFC3339Nano), q.Failures)
					break
				}
				select {
				case <-ticker.C:
				case <-timer.C:
					t.Fatal("worker did not publish failed real-git schedule")
				}
			}
			cancel()
			<-done
			// Restart discards process telemetry. A closed done channel can be read again.
			restarted, err := New(t.Context(), s.store, []Target{target}, s.options)
			must(t, err)
			h, err := restarted.Health(t.Context())
			must(t, err)
			if len(h.Queues) != 1 || h.Queues[0].LastSuccess != nil {
				t.Fatal("restart reused success from journal")
			}
			if pending {
				state, _, err := s.store.Load(t.Context(), w.id)
				must(t, err)
				if state.Active == nil || state.Active.Phase != queue.Landing {
					t.Fatal("failed remote settled a pending attempt")
				}
			}
		})
	}
}
