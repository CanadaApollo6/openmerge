// Package serve connects polling observations and durable queue workers.
package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// Target identifies an explicitly configured repository and base branch.
type Target struct {
	Key      store.QueueKey
	Driver   forge.Driver
	Git      forge.Git
	BotLogin string
}

// Options bounds API polling and concurrent queue execution.
type Options struct {
	PollInterval  time.Duration
	Concurrency   int
	ActionTimeout time.Duration
	Logger        *slog.Logger
	// BackoffUntil exposes the driver's shared quota/cooldown deadline.
	BackoffUntil func() time.Time
}

// Server owns one serial reconciler/worker per target, with bounded concurrency.
// Each process must have its own git cache directory.
type Server struct {
	store   *store.Store
	options Options
	queues  []*worker
	slots   chan struct{}
}

type worker struct {
	Target
	id            store.QueueID
	wake          chan struct{}
	cfg           config.Queue
	configTip     string
	configError   error
	blockedSHA    string
	blockedDetail string
	prs           map[int]forge.PullRequest
	summaries     map[int]forge.PullRequestSummary
}

// New bootstraps fences without replacing a previously recorded landing token.
func New(ctx context.Context, db *store.Store, targets []Target, options Options) (*Server, error) {
	if len(targets) == 0 {
		return nil, errors.New("at least one repository is required")
	}
	if options.PollInterval == 0 {
		options.PollInterval = time.Minute
	}
	if options.Concurrency == 0 {
		options.Concurrency = 4
	}
	if options.ActionTimeout == 0 {
		options.ActionTimeout = 2 * time.Minute
	}
	if options.PollInterval < 0 || options.Concurrency < 1 || options.ActionTimeout < 0 {
		return nil, errors.New("poll interval, concurrency and action timeout must be positive")
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	s := &Server{store: db, options: options, slots: make(chan struct{}, options.Concurrency)}
	seen := make(map[store.QueueID]bool)
	for _, target := range targets {
		if target.Driver == nil || target.Git == nil || target.BotLogin == "" {
			return nil, errors.New("target requires driver, git and App login")
		}
		id, err := db.EnsureQueue(ctx, target.Key)
		if err != nil {
			return nil, err
		}
		if seen[id] {
			return nil, fmt.Errorf("duplicate target %s/%s", target.Key.Repository, target.Key.BaseBranch)
		}
		seen[id] = true
		token, err := target.Git.EnsureFence(ctx, forge.RepoRef{Path: target.Key.Repository}, "openmerge/fence/"+target.Key.BaseBranch)
		if err != nil {
			return nil, err
		}
		state, _, err := db.Load(ctx, id)
		if err != nil {
			return nil, err
		}
		if state.Fence == "" {
			if err := db.InitializeFence(ctx, id, token); err != nil {
				return nil, err
			}
		}
		s.queues = append(s.queues, &worker{Target: target, id: id, wake: make(chan struct{}, 1), prs: make(map[int]forge.PullRequest), summaries: make(map[int]forge.PullRequestSummary)})
	}
	return s, nil
}

// Notify durably deduplicates a webhook and wakes only matching targets. An empty
// repository identifies installation-wide changes. Payloads never grant trust.
func (s *Server) Notify(ctx context.Context, installation, repository, delivery string) error {
	for _, w := range s.queues {
		if w.Key.Installation != installation || repository != "" && w.Key.Repository != repository {
			continue
		}
		if err := s.store.Notify(ctx, w.id, delivery); err != nil {
			return err
		}
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
	return nil
}

// Run stops intake on cancellation, cancels bounded I/O, and waits for claims to
// be released. Durable landing attempts remain resolvable after restart.
func (s *Server) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for _, w := range s.queues {
		wg.Go(func() { s.loop(ctx, w) })
	}
	wg.Wait()
	return nil
}

func (s *Server) loop(ctx context.Context, w *worker) {
	delay := time.Duration(0)
	failures := 0
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-w.wake:
			timer.Stop()
		}
		if ctx.Err() != nil {
			return
		}
		if s.options.BackoffUntil != nil {
			if until := s.options.BackoffUntil(); until.After(time.Now()) {
				delay = time.Until(until)
				continue
			}
		}
		select {
		case s.slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		err := s.cycle(ctx, w)
		<-s.slots
		if ctx.Err() != nil {
			return
		}
		delay = s.options.PollInterval
		if err != nil {
			failures++
			delay = min(5*time.Second*time.Duration(1<<min(failures-1, 6)), 5*time.Minute)
			s.options.Logger.Error("queue cycle failed", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "error", err, "retry_in", delay)
		} else {
			failures = 0
			state, _, err := s.store.Load(ctx, w.id)
			if err == nil {
				if at, ok := queue.NextWake(w.plannerConfig(s.options.PollInterval), state, time.Now()); ok {
					delay = min(delay, max(time.Millisecond, time.Until(at)))
				}
			}
		}
	}
}

func (w *worker) repo() forge.RepoRef { return forge.RepoRef{Path: w.Key.Repository} }
func (w *worker) plannerConfig(interval time.Duration) queue.Config {
	cfg := w.cfg.QueueConfig()
	cfg.RefreshInterval = interval
	return cfg
}

func (s *Server) cycle(ctx context.Context, w *worker) error {
	force, err := s.store.TakeNotification(ctx, w.id)
	if err != nil {
		return err
	}
	if err := s.reconcile(ctx, w, force); err != nil {
		return err
	}
	if force {
		state, _, err := s.store.Load(ctx, w.id)
		if err != nil {
			return err
		}
		if state.Active != nil && state.Active.Phase == queue.Testing {
			if _, err := s.store.Append(ctx, w.id, []queue.Event{queue.RefreshRequested{}}, ""); err != nil {
				return err
			}
		}
	}
	// Bound a hot queue's turn so other targets and cancellation get service.
	for range 64 {
		if err := s.quota(); err != nil {
			return err
		}
		cfg := w.plannerConfig(s.options.PollInterval)
		if _, err := s.store.Plan(ctx, w.id, cfg, time.Now()); err != nil {
			return err
		}
		claims, err := s.store.ClaimQueue(ctx, w.id, 1, s.options.ActionTimeout+time.Minute, time.Now())
		if err != nil {
			return err
		}
		if len(claims) == 0 {
			break
		}
		if err := s.execute(ctx, w, claims[0]); err != nil {
			return err
		}
	}
	return s.report(ctx, w)
}

func (s *Server) quota() error {
	if s.options.BackoffUntil != nil {
		if until := s.options.BackoffUntil(); until.After(time.Now()) {
			return fmt.Errorf("forge quota reserved until %s", until.UTC().Format(time.RFC3339))
		}
	}
	return nil
}
