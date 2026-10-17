package serve

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
)

func TestWebhookHealthSchedulingExpiryAndRestart(t *testing.T) {
	s, err := newServer(nil, Options{WebhooksConfigured: true})
	must(t, err)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	if got := s.pollDelay("7", repo.Path, queue.State{}, now); got != time.Minute {
		t.Fatalf("fresh listener delay = %s", got)
	}
	s.webhooks.observe("7", "owner/repo", now)
	for _, tc := range []struct {
		name          string
		state         queue.State
		installation  string
		elapsed, want time.Duration
	}{
		{"idle healthy", queue.State{}, "7", 0, 10 * time.Minute},
		{"building is idle", queue.State{Active: &queue.Batch{Phase: queue.Building}}, "7", 0, 10 * time.Minute},
		{"queued is idle", queue.State{Entries: []queue.Entry{{PR: 1}}}, "7", 0, 10 * time.Minute},
		{"other installation", queue.State{}, "8", 0, time.Minute},
		{"testing", queue.State{Active: &queue.Batch{Phase: queue.Testing}}, "7", 0, time.Minute},
		{"landing", queue.State{Active: &queue.Batch{Phase: queue.Landing}}, "7", 0, time.Minute},
		{"unsettled", queue.State{Active: &queue.Batch{LandingAttempt: queue.LandingAttempt{ID: "attempt"}}}, "7", 0, time.Minute},
		{"before expiry", queue.State{}, "7", 29 * time.Minute, time.Minute},
		{"at expiry", queue.State{}, "7", 30 * time.Minute, time.Minute},
		{"after expiry", queue.State{}, "7", time.Hour, time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.pollDelay(tc.installation, repo.Path, tc.state, now.Add(tc.elapsed)); got != tc.want {
				t.Fatalf("delay = %s, want %s", got, tc.want)
			}
		})
	}
	s.webhooks.observe("7", "owner/repo", now.Add(29*time.Minute))
	if got := s.pollDelay("7", repo.Path, queue.State{}, now.Add(30*time.Minute)); got != 10*time.Minute {
		t.Fatalf("delivery did not renew health: %s", got)
	}
	s.options.WebhooksConfigured = false
	if got := s.pollDelay("7", repo.Path, queue.State{}, now); got != time.Minute {
		t.Fatal("disabled listener slowed polling")
	}
	s.options.WebhooksConfigured = true
	restarted, err := newServer(nil, s.options)
	must(t, err)
	if got := restarted.pollDelay("7", repo.Path, queue.State{}, now); got != time.Minute {
		t.Fatal("restart retained health")
	}
}

func TestWebhookBackstopRequiresRecentRepositoryDelivery(t *testing.T) {
	s, err := newServer(nil, Options{WebhooksConfigured: true})
	must(t, err)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	s.webhooks.observe("7", "owner/busy", now)
	if got := s.pollDelay("7", "owner/quiet", queue.State{}, now); got != s.options.PollInterval {
		t.Fatalf("quiet repository slowed before first delivery: %s", got)
	}
	s.webhooks.observe("7", "OWNER/QUIET", now)
	if got := s.pollDelay("7", "owner/quiet", queue.State{}, now); got != s.options.BackstopInterval {
		t.Fatalf("first verified delivery did not enable backstop: %s", got)
	}
	s.webhooks.observe("7", "owner/busy", now.Add(29*time.Minute))
	if got := s.pollDelay("7", "owner/quiet", queue.State{}, now.Add(29*time.Minute)); got != time.Minute {
		t.Fatalf("backstop extends beyond repository health expiry: %s", got)
	}
	if got := s.pollDelay("7", "owner/quiet", queue.State{}, now.Add(30*time.Minute)); got != s.options.PollInterval {
		t.Fatalf("busy repository hid quiet repository health expiry: %s", got)
	}
}

func TestFailedNotificationDoesNotRenewHealth(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	s.options.WebhooksConfigured = true
	before := time.Now().Add(-time.Hour)
	s.webhooks.observe("7", repo.Path, before)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.NotifyDiscovery(ctx, "7", repo.Path, "retry"); err == nil {
		t.Fatal("failed persistence acknowledged")
	}
	installation, repository := s.webhooks.snapshot("7", repo.Path)
	if !installation.Equal(before) || !repository.Equal(before) {
		t.Fatal("failed persistence renewed webhook health")
	}
	select {
	case <-w.wake:
		t.Fatal("failed persistence woke worker")
	case <-s.refresh:
		t.Fatal("failed persistence requested discovery")
	default:
	}
	must(t, s.NotifyDiscovery(t.Context(), "7", repo.Path, "retry"))
	select {
	case <-s.refresh:
	default:
		t.Fatal("failed attempt consumed discovery dedupe ID")
	}
	if got := s.pollDelay("7", repo.Path, queue.State{}, time.Now()); got != s.options.BackstopInterval {
		t.Fatalf("successful retry did not record health: %s", got)
	}
}

func TestDiscoveryAndBranchRedeliveriesDoNotRepeatSweeps(t *testing.T) {
	for _, kind := range []string{"discovery", "branches"} {
		for _, workers := range []bool{false, true} {
			t.Run(kind+"/workers="+fmt.Sprint(workers), func(t *testing.T) {
				s, err := newServer(nil, Options{WebhooksConfigured: true})
				must(t, err)
				if workers {
					s, _, _, _ = fixture(t, repositoryConfig)
				}
				notify := s.NotifyDiscovery
				if kind == "branches" {
					notify = s.NotifyBranches
				}
				must(t, notify(t.Context(), "7", repo.Path, "delivery"))
				select {
				case <-s.refresh:
				default:
					t.Fatal("first delivery did not request sweep")
				}
				// Draining represents a completed sweep, rather than channel coalescing.
				s.webhooks.observe("7", repo.Path, time.Now().Add(-time.Hour))
				must(t, notify(t.Context(), "7", repo.Path, "delivery"))
				select {
				case <-s.refresh:
					t.Fatal("redelivery requested another sweep")
				default:
				}
				_, last := s.webhooks.snapshot("7", repo.Path)
				if time.Since(last) > time.Minute {
					t.Fatal("redelivery did not renew health")
				}
				must(t, notify(t.Context(), "7", repo.Path, "next-delivery"))
				select {
				case <-s.refresh:
				default:
					t.Fatal("distinct delivery did not request sweep")
				}
			})
		}
	}
}

func TestIgnoredNotificationWakesAndRefreshesWithoutDurableWrites(t *testing.T) {
	s, w, f, d := fixture(t, repositoryConfig)
	s.options.PollInterval = time.Hour
	f.AddComment(repo, 1, "writer", "@openmerge queue")
	must(t, s.cycle(t.Context(), w))
	_, before, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	must(t, s.NotifyIgnored(t.Context(), "7", repo.Path, "ignored"))
	select {
	case <-w.wake:
	default:
		t.Fatal("ignored delivery did not wake queue")
	}
	_, after, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if before != after {
		t.Fatal("ignored delivery wrote journal events")
	}
	force, err := s.store.TakeNotification(t.Context(), w.id)
	must(t, err)
	if force {
		t.Fatal("ignored delivery wrote a durable refresh hint")
	}
	checks := d.checks
	must(t, s.cycle(t.Context(), w))
	if d.checks <= checks {
		t.Fatal("ignored delivery failed to refresh active checks")
	}
	// Reusing this ID through the durable path must still be a fresh delivery.
	must(t, s.Notify(t.Context(), "7", repo.Path, "ignored"))
	force, err = s.store.TakeNotification(t.Context(), w.id)
	must(t, err)
	if !force {
		t.Fatal("ignored delivery consumed a durable dedupe ID")
	}
}

func TestDiscoveryDedupeSurvivesWorkerRegistration(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	s.queues = nil
	must(t, s.NotifyDiscovery(t.Context(), "7", repo.Path, "before-registration"))
	<-s.refresh
	_, last := s.webhooks.snapshot("7", repo.Path)
	if last.IsZero() {
		t.Fatal("delivery before worker registration lost health")
	}
	s.queues = []*worker{w}
	must(t, s.NotifyDiscovery(t.Context(), "7", repo.Path, "before-registration"))
	select {
	case <-s.refresh:
		t.Fatal("worker registration lost discovery delivery dedupe")
	default:
	}
}

func TestWebhookDeliveryAgeIsPerRepositoryAndLocal(t *testing.T) {
	s, err := newServer(nil, Options{WebhooksConfigured: true})
	must(t, err)
	s.discoveryStatus = status.Discovery{
		Installations: []status.Installation{{ID: "7"}, {ID: "8"}},
		Repositories:  []status.Repository{{Installation: "7", Name: "Owner/Active"}, {Installation: "7", Name: "owner/quiet"}},
	}
	s.webhooks.observe("7", "owner/active", time.Now().Add(-2*time.Minute))
	d := s.DiscoveryStatus()
	if !d.Installations[0].WebhookHealthy || d.Installations[0].LastVerifiedDelivery == nil || d.Installations[1].WebhookHealthy {
		t.Fatalf("installation health: %+v", d.Installations)
	}
	if age := d.Repositories[0].LastDeliveryAgeSeconds; age == nil || *age < 120 || *age > 125 {
		t.Fatalf("repository delivery age: %+v", d.Repositories[0])
	}
	if d.Repositories[1].LastDeliveryAgeSeconds != nil || d.Repositories[1].LastVerifiedDelivery != nil {
		t.Fatal("one repository's health hid another's missing delivery")
	}
	*d.Repositories[0].LastDeliveryAgeSeconds = 0
	if *s.DiscoveryStatus().Repositories[0].LastDeliveryAgeSeconds < 120 {
		t.Fatal("status caller changed local observations")
	}
}

func TestVerifiedDeliveryWakesOnlyAffectedQueueAndDedupeSurvivesRestart(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	s.options.WebhooksConfigured = true
	_, before, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	must(t, s.Notify(t.Context(), "8", repo.Path, "wrong-installation"))
	must(t, s.Notify(t.Context(), "7", "owner/other", "wrong-repo"))
	select {
	case <-w.wake:
		t.Fatal("unrelated delivery woke queue")
	default:
	}
	must(t, s.Notify(t.Context(), "7", "OWNER/REPO", "delivery"))
	select {
	case <-w.wake:
	default:
		t.Fatal("verified delivery did not wake queue immediately")
	}
	force, err := s.store.TakeNotification(t.Context(), w.id)
	must(t, err)
	if !force {
		t.Fatal("wake lost durable refresh hint")
	}
	must(t, s.Notify(t.Context(), "7", repo.Path, "delivery"))
	select {
	case <-w.wake:
		t.Fatal("redelivery woke queue again")
	default:
	}
	_, after, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if before != after {
		t.Fatal("health wrote journal events")
	}
	restarted, err := New(t.Context(), s.store, []Target{w.Target}, s.options)
	must(t, err)
	if got := restarted.pollDelay("7", repo.Path, queue.State{}, time.Now()); got != s.options.PollInterval {
		t.Fatal("restart retained health")
	}
	must(t, restarted.Notify(t.Context(), "7", repo.Path, "delivery"))
	select {
	case <-restarted.workers()[0].wake:
		t.Fatal("restart lost delivery deduplication")
	default:
	}
}

type pollSignalDriver struct {
	forge.Driver
	polls chan struct{}
	err   error
}

func (d pollSignalDriver) ListOpenPullRequests(ctx context.Context, repo forge.RepoRef, base string) ([]forge.PullRequestSummary, error) {
	select {
	case d.polls <- struct{}{}:
	default:
	}
	if d.err != nil {
		return nil, d.err
	}
	return d.Driver.ListOpenPullRequests(ctx, repo, base)
}

func TestHealthyIdleLoopUsesBackstopAndDeliveryInterruptsTimer(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	s.options.PollInterval = 10 * time.Millisecond
	s.options.BackstopInterval = time.Hour
	s.options.WebhooksConfigured = true
	s.webhooks.observe("7", repo.Path, time.Now())
	polls := make(chan struct{}, 8)
	w.Driver = pollSignalDriver{Driver: w.Driver, polls: polls}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); s.loop(ctx, w) }()
	defer func() { cancel(); <-done }()
	select {
	case <-polls:
	case <-time.After(5 * time.Second):
		t.Fatal("initial poll missing")
	}
	select {
	case <-polls:
		t.Fatal("healthy idle queue polled at fast interval")
	case <-time.After(80 * time.Millisecond):
	}
	_, err := s.store.Append(t.Context(), w.id, []queue.Event{queue.PauseRequested{Reason: "operator", Detail: "idle change"}}, "")
	must(t, err)
	select {
	case <-polls:
		t.Fatal("idle journal change caused forge polling")
	case <-time.After(80 * time.Millisecond):
	}
	must(t, s.Notify(t.Context(), "7", repo.Path, "wake"))
	select {
	case <-polls:
	case <-time.After(5 * time.Second):
		t.Fatal("delivery did not interrupt backstop timer")
	}
}

func TestBackstopDetectsAnotherServerStartingTesting(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	s.options.PollInterval = 200 * time.Millisecond
	s.options.BackstopInterval = time.Hour
	s.options.WebhooksConfigured = true
	s.webhooks.observe("7", repo.Path, time.Now())
	target := w.Target
	target.Driver = f
	other, err := New(t.Context(), s.store, []Target{target}, s.options)
	must(t, err)
	polls := make(chan struct{}, 8)
	w.Driver = pollSignalDriver{Driver: f, polls: polls}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); s.loop(ctx, w) }()
	defer func() { cancel(); <-done }()
	select {
	case <-polls:
	case <-time.After(5 * time.Second):
		t.Fatal("initial poll missing")
	}
	select {
	case <-polls:
		t.Fatal("idle server did not enter backstop")
	case <-time.After(3 * s.options.PollInterval):
	}
	f.AddComment(repo, 1, "writer", "@openmerge queue")
	must(t, other.cycle(t.Context(), other.workers()[0]))
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.Active == nil || state.Active.Phase != queue.Testing {
		t.Fatalf("second server did not start testing: %+v", state.Active)
	}
	// Allow one poll_interval plus DB/CI scheduling overhead, far below the
	// hour-long forge backstop. No webhook is sent to the waiting server.
	for range 2 {
		select {
		case <-polls:
		case <-time.After(s.options.PollInterval + 200*time.Millisecond):
			t.Fatal("server failed to resume poll_interval polling for shared active work")
		}
	}
}

func TestIdleActivityChecksPreserveForgeBackstopDeadline(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	s.options.PollInterval = 10 * time.Millisecond
	s.options.BackstopInterval = 100 * time.Millisecond
	s.options.WebhooksConfigured = true
	s.webhooks.observe("7", repo.Path, time.Now())
	polls := make(chan struct{}, 8)
	w.Driver = pollSignalDriver{Driver: w.Driver, polls: polls}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); s.loop(ctx, w) }()
	defer func() { cancel(); <-done }()
	for range 2 {
		select {
		case <-polls:
		case <-time.After(time.Second):
			t.Fatal("database activity checks postponed forge backstop")
		}
	}
}

func TestHealthyDeliveryDoesNotShortenErrorOrQuotaBackoff(t *testing.T) {
	for _, name := range []string{"error", "quota"} {
		t.Run(name, func(t *testing.T) {
			s, w, _, _ := fixture(t, repositoryConfig)
			s.options.WebhooksConfigured = true
			s.options.PollInterval = time.Millisecond
			s.webhooks.observe("7", repo.Path, time.Now())
			polls := make(chan struct{}, 8)
			err := errors.New("temporary read failure")
			if name == "quota" {
				err = &quotaBackoff{until: time.Now().Add(time.Hour)}
			}
			w.Driver = pollSignalDriver{Driver: w.Driver, polls: polls, err: err}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() { defer close(done); s.loop(ctx, w) }()
			defer func() { cancel(); <-done }()
			select {
			case <-polls:
			case <-time.After(5 * time.Second):
				t.Fatal("initial cycle missing")
			}
			must(t, s.Notify(t.Context(), "7", repo.Path, "during-backoff"))
			select {
			case <-polls:
				t.Fatal("delivery bypassed backoff")
			case <-time.After(80 * time.Millisecond):
			}
		})
	}
}
