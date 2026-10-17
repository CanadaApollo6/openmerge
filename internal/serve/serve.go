// Package serve connects polling observations and durable queue workers.
package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// Target identifies an explicitly configured repository and base branch.
type Target struct {
	// Resolve binds historical credentials lazily; failures never discard a landing.
	Resolve  func(context.Context) (Target, error)
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
	PollInterval        time.Duration
	BackstopInterval    time.Duration
	WebhookHealthWindow time.Duration
	WebhooksConfigured  bool
	RulesInterval       time.Duration
	DiscoveryInterval   time.Duration
	BaseAllowlist       []string
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
	store               *store.Store
	webhooks            webhookObservations
	options             Options
	queues              []*worker
	slots               chan struct{}
	mu                  sync.RWMutex
	refreshMu           sync.Mutex
	notificationMu      sync.Mutex
	refreshDeliveries   map[string]bool
	registerMu          sync.Mutex
	discovery           *Discovery
	discoveryStatus     status.Discovery
	discoveryBudgets    map[string]func() status.Budget
	discoveryBackoffs   map[string]time.Time
	dormantRepositories map[string]bool
	repositories        []Repository
	refresh             chan struct{}
	refreshFailures     int
	refreshNextAt       time.Time
	refreshRetryAt      time.Time
	refreshNotBefore    time.Time
}

type worker struct {
	settlementOnly  bool
	mu              sync.Mutex
	managed         bool
	ready           bool // Branch and policy validation is process-local, never a lifecycle event.
	waiting         string
	lifecycleState  string
	lifecycleDetail string
	accessBlocked   bool
	rulesWaiting    string
	validationNext  time.Time
	reportingNext   time.Time
	recovery        bool
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
	diagnosticHeads  map[int]string
	summaries        map[int]forge.PullRequestSummary
	deferred         []string
	logState         queue.State
	logWatermark     int64
	logChecks        string
	rulesNextCheck   time.Time
	rulesFailures    int

	notificationPending atomic.Bool
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
		id, key, err := s.canonicalQueue(ctx, target)
		target.Key = key
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
	if options.BackstopInterval == 0 {
		options.BackstopInterval = 10 * time.Minute
	}
	if options.WebhookHealthWindow == 0 {
		options.WebhookHealthWindow = 30 * time.Minute
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
	if options.BackstopInterval < 0 || options.WebhookHealthWindow < 0 || options.DiscoveryInterval < 0 || options.PollInterval < 0 || options.RulesInterval < 0 || options.Concurrency < 1 || options.ActionTimeout < 0 {
		return nil, errors.New("intervals, health window, concurrency and action timeout must be positive")
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
		if w.Key.Installation != installation || repository != "" && !strings.EqualFold(w.Key.Repository, repository) {
			continue
		}
		fresh, err := s.store.NotifyDelivery(ctx, w.id, delivery)
		if err != nil {
			return err
		}
		if !fresh {
			continue
		}
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
	s.webhooks.observe(installation, repository, time.Now())
	return nil
}

// NotifyIgnored observes authenticated unsupported/incomplete payloads and wakes
// matching workers without adding durable delivery IDs or refresh hints.
func (s *Server) NotifyIgnored(_ context.Context, installation, repository, _ string) error {
	for _, w := range s.workers() {
		if w.Key.Installation != installation || repository != "" && !strings.EqualFold(w.Key.Repository, repository) {
			continue
		}
		w.notificationPending.Store(true)
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
	s.webhooks.observe(installation, repository, time.Now())
	return nil
}

// NotifyBranches also requests a complete listing, including when the first
// matching branch has no worker yet. CI/comment notifications only wake workers.
func (s *Server) NotifyBranches(ctx context.Context, installation, repository, delivery string) error {
	return s.notifyRefresh(ctx, installation, repository, delivery)
}

const refreshDeliveryLimit = 4096

// Discovery dedupe is listener-local and independent of worker registration.
// Startup already performs a full discovery sweep; queue hints remain durable.
func (s *Server) notifyRefresh(ctx context.Context, installation, repository, delivery string) error {
	if err := s.Notify(ctx, installation, repository, delivery); err != nil {
		return err
	}
	s.notificationMu.Lock()
	defer s.notificationMu.Unlock()
	key := installation + "/" + delivery
	if s.refreshDeliveries[key] {
		return nil
	}
	// Bound listener-local memory. An evicted redelivery can request another
	// coalesced sweep; durable worker dedupe and refresh hints are unaffected.
	if s.refreshDeliveries == nil || len(s.refreshDeliveries) >= refreshDeliveryLimit {
		s.refreshDeliveries = make(map[string]bool)
	}
	s.refreshDeliveries[key] = true
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
		nextAt := s.refreshNextAt
		retryAt := s.refreshRetryAt
		notBefore := s.refreshNotBefore
		s.refreshMu.Unlock()
		delay := s.options.DiscoveryInterval
		if !nextAt.IsZero() {
			delay = max(0, time.Until(maxTime(nextAt, maxTime(retryAt, notBefore))))
		}
		timer.Reset(delay)
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		case <-s.refresh:
		}
		if deadline := maxTime(retryAt, notBefore); time.Now().Before(deadline) {
			s.refreshMu.Lock()
			s.refreshNextAt = deadline
			s.refreshMu.Unlock()
			continue
		}
		_ = s.Refresh(ctx)
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
	var retryNotBefore time.Time
	var activityWatermark int64
	checkActivity := false
	for {
		err := s.waitForCycle(ctx, w, delay, checkActivity, activityWatermark)
		checkActivity = false
		if ctx.Err() != nil {
			return
		}
		if time.Now().Before(retryNotBefore) {
			delay = time.Until(retryNotBefore)
			continue
		}
		if err == nil {
			select {
			case s.slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			err = s.cycle(ctx, w)
			<-s.slots
		}
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
			state, watermark, loadErr := s.store.Load(ctx, w.id)
			if loadErr == nil {
				activityWatermark = watermark
				checkActivity = true
				delay = s.pollDelay(w.Key.Installation, w.Key.Repository, state, time.Now())
				if at, ok := queue.NextWake(w.plannerConfig(s.options.PollInterval), state, time.Now()); ok {
					delay = min(delay, max(time.Millisecond, time.Until(at)))
				}
			}
			w.mu.Unlock()
		}
		w.mu.Lock()
		if err == nil && w.validationPending() && w.validationNext.After(time.Now()) {
			delay = min(delay, max(time.Millisecond, time.Until(w.validationNext)))
		}
		if err == nil && w.rulesNextCheck.After(time.Now()) {
			delay = min(delay, max(time.Millisecond, time.Until(w.rulesNextCheck)))
		}
		w.mu.Unlock()
		retryNotBefore = time.Time{}
		if err != nil {
			retryNotBefore = time.Now().Add(delay)
		}
	}
}

// While the forge backstop waits, inspect only the indexed event watermark at
// poll_interval. Any changed history requires a normal reconcile to re-plan work
// and recompute deadlines. Unchanged checks never take an API slot or write state.
func (s *Server) waitForCycle(ctx context.Context, w *worker, delay time.Duration, checkActivity bool, watermark int64) error {
	deadline := time.Now().Add(delay)
	for {
		remaining := time.Until(deadline)
		wait := remaining
		if checkActivity {
			wait = min(wait, s.options.PollInterval)
		}
		timer := time.NewTimer(max(0, wait))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-w.wake:
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if !checkActivity || !time.Now().Before(deadline) {
			return nil
		}
		latest, err := s.store.Watermark(ctx, w.id)
		if err != nil {
			return err
		}
		if latest == watermark {
			continue
		}
		return nil
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
		var rate interface{ RetryAfter() time.Time }
		if errors.As(result, &rate) {
			result = &quotaBackoff{until: rate.RetryAfter()}
		}
		if !w.validationPending() {
			w.validationNext = time.Time{}
		} else if !w.validationNext.After(time.Now()) {
			w.validationNext = time.Now().Add(5 * time.Second)
		}
		s.logCommitted(ctx, w)
		s.options.Logger.Debug("reconcile cycle finished", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "error", result)
	}()
	current, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	w.accessBlocked = current.SourceBlocked(queue.RepositoryAccess)
	if (w.settlementOnly || current.SourceBlocked(queue.RepositoryAccess)) && (current.Active == nil || current.Active.Phase != queue.Landing) {
		return nil
	}
	resolveCtx := ctx
	if current.Active != nil && current.Active.Phase == queue.Landing {
		resolveCtx = forge.WithRequestClass(ctx, forge.SettlementRequest)
	}
	if err := s.resolveTarget(resolveCtx, w); err != nil {
		return err
	}
	unsettled, err := s.settle(forge.WithRequestClass(ctx, forge.SettlementRequest), w)
	if err != nil || unsettled {
		return err
	}
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	class := forge.IdleRequest
	if state.Active != nil {
		class = forge.ActiveRequest
	}
	ctx = forge.WithRequestClass(ctx, class)
	if w.managed {
		state, _, err := s.store.Load(ctx, w.id)
		if err != nil {
			return err
		}
		if w.recovery {
			return s.lifecycle(ctx, w, "retired", "Recovered landing settled; queue excluded by repository allowlist")
		}
		if state.SourceBlocked(queue.RepositoryAccess) {
			_ = s.reportRepositoryRemoval(ctx, w, state)
			return nil
		}
		if !w.ready || state.LifecycleState() != "active" {
			if w.validationPending() && !time.Now().Before(w.validationNext) {
				w.validationNext = time.Now().Add(5 * time.Second)
				select {
				case s.refresh <- struct{}{}:
				default:
				}
			}
			return s.reportWaiting(ctx, w)
		}
	}
	force, err := s.store.TakeNotification(ctx, w.id)
	if err != nil {
		return err
	}
	force = w.notificationPending.Swap(false) || force
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
	state, _, err = s.store.Load(ctx, w.id)
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

func (e *quotaBackoff) RetryAfter() time.Time { return e.until }

func (w *worker) validationPending() bool {
	return w.managed && !w.ready && !w.settlementOnly && !w.accessBlocked && (w.lifecycleState == "" || w.lifecycleState == "active")
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
