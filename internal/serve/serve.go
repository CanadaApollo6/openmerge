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
	// CheckRules is supplied by the forge adapter, without admin credentials.
	CheckRules func(context.Context) (problems, warnings []string, err error)
	// CheckPermissions checks the installation's grants independently of policy reads.
	CheckPermissions func(context.Context) (problems, warnings []string, err error)
	// CheckProtection also receives the configured commit shape. An empty method
	// means configuration has not been loaded yet during startup.
	CheckProtection func(context.Context, forge.MergeMethod) (problems, warnings []string, err error)
}

// Options bounds API polling and concurrent queue execution.
type Options struct {
	PollInterval      time.Duration
	RulesInterval     time.Duration
	DiscoveryInterval time.Duration
	BaseAllowlist     []string
	// RecoveryTarget supplies credentials for historical queues outside --repos.
	RecoveryTarget          func(store.QueueKey) (Target, bool)
	Concurrency             int
	ActionTimeout           time.Duration
	DisableDeleteHeadBranch bool
	Logger                  *slog.Logger
	// BackoffUntil exposes the driver's shared quota/cooldown deadline.
	BackoffUntil func() time.Time
}

// Server owns one serial reconciler/worker per target, with bounded concurrency.
// Each process must have its own git cache directory.
type Server struct {
	store           *store.Store
	options         Options
	queues          []*worker
	slots           chan struct{}
	mu              sync.RWMutex
	refreshMu       sync.Mutex
	repositories    []Repository
	refresh         chan struct{}
	refreshFailures int
	refreshRetryAt  time.Time
}

type worker struct {
	mu       sync.Mutex
	managed  bool
	ready    bool // Branch and policy validation is process-local, never a lifecycle event.
	recovery bool
	Target
	id               store.QueueID
	wake             chan struct{}
	cfg              config.Queue
	configBranch     string
	configTip        string
	configDigest     string
	configError      error
	deleteHeadBranch *bool
	prs              map[int]forge.PullRequest
	summaries        map[int]forge.PullRequestSummary
	deferred         []string
	logState         queue.State
	logWatermark     int64
	logChecks        string
	rulesNextCheck   time.Time
	rulesFailures    int
}

// New bootstraps fences without replacing a previously recorded landing token.
func New(ctx context.Context, db *store.Store, targets []Target, options Options) (*Server, error) {
	if len(targets) == 0 {
		return nil, errors.New("at least one repository is required")
	}
	s, err := newServer(db, options)
	if err != nil {
		return nil, err
	}
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
		state, watermark, err := db.Load(ctx, id)
		if err != nil {
			return nil, err
		}
		w := &worker{Target: target, id: id, logState: state, logWatermark: watermark, wake: make(chan struct{}, 1), prs: make(map[int]forge.PullRequest), summaries: make(map[int]forge.PullRequestSummary)}
		if err := s.repositoryRules(ctx, w); err != nil {
			return nil, err
		}
		if err := s.initializeFence(ctx, w); err != nil {
			return nil, err
		}
		s.queues = append(s.queues, w)
	}
	return s, nil
}

func newServer(db *store.Store, options Options) (*Server, error) {
	if options.PollInterval == 0 {
		options.PollInterval = time.Minute
	}
	if options.Concurrency == 0 {
		options.Concurrency = 4
	}
	if options.DiscoveryInterval == 0 {
		options.DiscoveryInterval = 10 * time.Minute
	}
	if options.RulesInterval == 0 {
		options.RulesInterval = 10 * time.Minute
	}
	if options.ActionTimeout == 0 {
		options.ActionTimeout = 2 * time.Minute
	}
	if options.DiscoveryInterval < 0 || options.PollInterval < 0 || options.RulesInterval < 0 || options.Concurrency < 1 || options.ActionTimeout < 0 {
		return nil, errors.New("poll interval, concurrency and action timeout must be positive")
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	return &Server{store: db, options: options, slots: make(chan struct{}, options.Concurrency), refresh: make(chan struct{}, 1)}, nil
}

// Notify durably deduplicates a webhook and wakes only matching targets. An empty
// repository identifies installation-wide changes. Payloads never grant trust.
func (s *Server) Notify(ctx context.Context, installation, repository, delivery string) error {
	for _, w := range s.workers() {
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

// NotifyBranches also requests a complete listing, including when the first
// matching branch has no worker yet. CI/comment notifications only wake workers.
func (s *Server) NotifyBranches(ctx context.Context, installation, repository, delivery string) error {
	if err := s.Notify(ctx, installation, repository, delivery); err != nil {
		return err
	}
	select {
	case s.refresh <- struct{}{}:
	default:
	}
	return nil
}

// Run stops intake on cancellation, cancels bounded I/O, and waits for claims to
// be released. Durable landing attempts remain resolvable after restart.
func (s *Server) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	started := make(map[store.QueueID]bool)
	start := func() {
		for _, w := range s.workers() {
			if !started[w.id] {
				started[w.id] = true
				wg.Go(func() { s.loop(ctx, w) })
			}
		}
	}
	start()
	timer := time.NewTimer(s.options.DiscoveryInterval)
	defer timer.Stop()
	defer wg.Wait()
	for {
		s.refreshMu.Lock()
		retryAt := s.refreshRetryAt
		s.refreshMu.Unlock()
		delay := s.options.DiscoveryInterval
		if !retryAt.IsZero() {
			delay = max(0, time.Until(retryAt))
		}
		timer.Reset(delay)
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		case <-s.refresh:
		}
		if time.Now().Before(retryAt) {
			continue
		}
		_ = s.RefreshBranches(ctx)
		start()
	}
}

func (s *Server) workers() []*worker {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]*worker(nil), s.queues...)
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
				s.options.Logger.Debug("quota backoff", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "until", until)
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
		if err == nil && w.recovery {
			state, _, loadErr := s.store.Load(ctx, w.id)
			if loadErr == nil && (state.Active == nil || state.Active.Phase != queue.Landing) {
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
		delay = s.options.PollInterval
		var quota *quotaBackoff
		if errors.As(err, &quota) {
			delay = max(time.Millisecond, time.Until(quota.until))
			s.options.Logger.Debug("quota backoff", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "until", quota.until)
		} else if err != nil {
			failures++
			delay = min(5*time.Second*time.Duration(1<<min(failures-1, 6)), 5*time.Minute)
			s.options.Logger.Error("queue cycle failed", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "error", err, "retry_in", delay)
		} else {
			failures = 0
			w.mu.Lock()
			state, _, err := s.store.Load(ctx, w.id)
			if err == nil {
				if at, ok := queue.NextWake(w.plannerConfig(s.options.PollInterval), state, time.Now()); ok {
					delay = min(delay, max(time.Millisecond, time.Until(at)))
				}
			}
			w.mu.Unlock()
		}
		w.mu.Lock()
		if w.rulesNextCheck.After(time.Now()) {
			delay = min(delay, max(time.Millisecond, time.Until(w.rulesNextCheck)))
		}
		w.mu.Unlock()
	}
}

func (w *worker) repo() forge.RepoRef { return forge.RepoRef{Path: w.Key.Repository} }

// Validate landing grants before the first write. A blocked new installation
// can serve diagnostics and initialize its fence after a successful recheck.
func (s *Server) initializeFence(ctx context.Context, w *worker) error {
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil || state.Fence != "" || len(state.RulesProblems()) > 0 {
		return err
	}
	token, err := w.Git.EnsureFence(ctx, w.repo(), "openmerge/fence/"+w.Key.BaseBranch)
	if err != nil {
		return err
	}
	_, err = s.store.Append(ctx, w.id, []queue.Event{queue.FenceInitialized{Token: token}}, "")
	return err
}

func (w *worker) plannerConfig(interval time.Duration) queue.Config {
	cfg := w.cfg.QueueConfig()
	cfg.ConfigDigest = w.configDigest
	cfg.RefreshInterval = interval
	return cfg
}

func (s *Server) cycle(ctx context.Context, w *worker) (result error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s.options.Logger.Debug("reconcile cycle started", "repository", w.Key.Repository, "base", w.Key.BaseBranch)
	defer func() {
		s.logCommitted(ctx, w)
		s.options.Logger.Debug("reconcile cycle finished", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "error", result)
	}()
	unsettled, err := s.settle(ctx, w)
	if err != nil || unsettled {
		return err
	}
	if w.managed {
		state, _, err := s.store.Load(ctx, w.id)
		if err != nil {
			return err
		}
		if w.recovery {
			return s.lifecycle(ctx, w, "retired", "Recovered landing settled; queue excluded by repository allowlist")
		}
		if !w.ready || state.LifecycleState() != "active" {
			return nil
		}
	}
	force, err := s.store.TakeNotification(ctx, w.id)
	if err != nil {
		return err
	}
	if err := s.repositoryRules(ctx, w); err != nil {
		return err
	}
	if err := s.initializeFence(ctx, w); err != nil {
		return err
	}
	if err := s.reconcile(ctx, w, force); err != nil {
		return err
	}
	if w.managed && !w.ready {
		return nil
	}
	// Loading a different configuration invalidates method-dependent protection
	// checks, including the first read after startup's method was unknown.
	if err := s.repositoryRules(ctx, w); err != nil {
		return err
	}
	if err := s.initializeFence(ctx, w); err != nil {
		return err
	}
	s.logCommitted(ctx, w)
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	if state.PauseReason(w.plannerConfig(s.options.PollInterval)) == "" {
		w.deferred = nil
	}
	if force {
		if state.Active != nil && state.Active.Phase == queue.Testing {
			if _, err := s.store.Append(ctx, w.id, []queue.Event{queue.RefreshRequested{}}, ""); err != nil {
				return err
			}
		}
	}
	// Bound a hot queue's turn so other targets and cancellation get service.
	for range 64 {
		if w.managed && !w.ready {
			break
		}
		if err := s.quota(); err != nil {
			return err
		}
		cfg := w.plannerConfig(s.options.PollInterval)
		if _, err := s.store.Plan(ctx, w.id, cfg, time.Now()); err != nil {
			return err
		}
		claims, err := s.store.ClaimQueue(ctx, w.id, 1, s.options.ActionTimeout+time.Minute, time.Now(), w.deferred...)
		if err != nil {
			return err
		}
		if len(claims) == 0 {
			break
		}
		if err := s.execute(ctx, w, claims[0]); err != nil {
			if err == store.ErrActionDeferred {
				w.deferred = append(w.deferred, claims[0].Key)
				continue
			}
			return err
		}
	}
	return s.report(ctx, w)
}

func (s *Server) quota() error {
	if s.options.BackoffUntil != nil {
		if until := s.options.BackoffUntil(); until.After(time.Now()) {
			return &quotaBackoff{until: until}
		}
	}
	return nil
}

type quotaBackoff struct{ until time.Time }

func (e *quotaBackoff) Error() string {
	return fmt.Sprintf("forge quota reserved until %s", e.until.UTC().Format(time.RFC3339))
}
