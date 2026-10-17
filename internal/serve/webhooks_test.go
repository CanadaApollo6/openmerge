package serve

import (
	"context"
	"errors"
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
	if got := s.pollDelay("7", queue.State{}, now); got != time.Minute {
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
		{"other installation", queue.State{}, "8", 0, time.Minute},
		{"testing", queue.State{Active: &queue.Batch{Phase: queue.Testing}}, "7", 0, time.Minute},
		{"landing", queue.State{Active: &queue.Batch{Phase: queue.Landing}}, "7", 0, time.Minute},
		{"unsettled", queue.State{Active: &queue.Batch{LandingAttempt: queue.LandingAttempt{ID: "attempt"}}}, "7", 0, time.Minute},
		{"before expiry", queue.State{}, "7", 29 * time.Minute, time.Minute},
		{"at expiry", queue.State{}, "7", 30 * time.Minute, time.Minute},
		{"after expiry", queue.State{}, "7", time.Hour, time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.pollDelay(tc.installation, tc.state, now.Add(tc.elapsed)); got != tc.want {
				t.Fatalf("delay = %s, want %s", got, tc.want)
			}
		})
	}
	s.webhooks.observe("7", "owner/repo", now.Add(29*time.Minute))
	if got := s.pollDelay("7", queue.State{}, now.Add(30*time.Minute)); got != 10*time.Minute {
		t.Fatalf("delivery did not renew health: %s", got)
	}
	s.options.WebhooksConfigured = false
	if got := s.pollDelay("7", queue.State{}, now); got != time.Minute {
		t.Fatal("disabled listener slowed polling")
	}
	s.options.WebhooksConfigured = true
	restarted, err := newServer(nil, s.options)
	must(t, err)
	if got := restarted.pollDelay("7", queue.State{}, now); got != time.Minute {
		t.Fatal("restart retained health")
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
	if got := restarted.pollDelay("7", queue.State{}, time.Now()); got != s.options.PollInterval {
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
	must(t, s.Notify(t.Context(), "7", repo.Path, "wake"))
	select {
	case <-polls:
	case <-time.After(5 * time.Second):
		t.Fatal("delivery did not interrupt backstop timer")
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
