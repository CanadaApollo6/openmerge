package serve

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// Repository binds branch enumeration and per-base protection to one explicitly
// served repository. Target must return the same credentials for every base.
type Repository struct {
	Installation, Name string
	Branches           func(context.Context) ([]string, error)
	Target             func(base string) Target
}

// NewRepositories restores historical attempts before consulting branch/config
// reads. Workers await validation in memory without changing stored lifecycle.
func NewRepositories(ctx context.Context, db *store.Store, repos []Repository, options Options) (*Server, error) {
	if len(repos) == 0 {
		return nil, errors.New("at least one repository is required")
	}
	s, err := newServer(db, options)
	if err != nil {
		return nil, err
	}
	for _, repo := range repos {
		if repo.Installation == "" || repo.Name == "" || repo.Target == nil || repo.Branches == nil {
			return nil, errors.New("repository requires installation, name, target factory and branch listing")
		}
	}
	s.repositories = repos
	stored, err := db.StoredQueues(ctx)
	if err != nil {
		return nil, err
	}
	for _, historical := range stored {
		key := historical.Key
		var target Target
		found := false
		for _, repo := range repos {
			if repo.Installation == key.Installation && strings.EqualFold(repo.Name, key.Repository) {
				target, found = repo.Target(key.BaseBranch), true
				break
			}
		}
		if !found && options.RecoveryTarget != nil {
			target, found = options.RecoveryTarget(key)
		}
		if !found {
			continue
		}
		id := historical.ID
		state, _, err := db.Load(ctx, id)
		if err != nil {
			return nil, err
		}
		// Also retain configured repositories' dormant/retired queues in status.
		served := slices.ContainsFunc(repos, func(r Repository) bool {
			return r.Installation == key.Installation && strings.EqualFold(r.Name, key.Repository)
		})
		if !served && (state.Active == nil || state.Active.Phase != queue.Landing) {
			continue
		}
		target.Key = key
		w, err := s.registerID(ctx, target, id)
		if err != nil {
			return nil, err
		}
		w.recovery = !served
	}
	for _, w := range s.workers() {
		if _, err := s.settle(ctx, w); err != nil {
			s.options.Logger.Error("startup landing settlement failed; worker will retry", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "error", err)
		}
	}
	_ = s.RefreshBranches(ctx) // Failed reads are logged and retried without blocking settlement.
	return s, nil
}

func (s *Server) register(ctx context.Context, target Target) (*worker, error) {
	if target.Resolve == nil && (target.Driver == nil || target.Git == nil || target.BotLogin == "") {
		return nil, errors.New("target requires driver, git and App login")
	}
	id, key, err := s.canonicalQueue(ctx, target)
	target.Key = key
	if err != nil {
		return nil, err
	}
	s.registerMu.Lock()
	defer s.registerMu.Unlock()
	return s.registerID(ctx, target, id)
}

// Read the forge only when an uncached partition has competing fence histories.
// Workers use the resulting cache; a failed read leaves every variant waiting
// for the next discovery sweep while their landing settlement continues.
func (s *Server) canonicalQueue(ctx context.Context, target Target) (store.QueueID, store.QueueKey, error) {
	id, key, err := s.store.CanonicalQueue(ctx, target.Key)
	if !errors.Is(err, store.ErrFenceOwnershipRequired) {
		return id, key, err
	}
	readCtx, cancel := context.WithTimeout(ctx, s.options.ActionTimeout)
	defer cancel()
	if target.Git == nil && target.Resolve != nil {
		resolved, resolveErr := target.Resolve(readCtx)
		if resolveErr != nil {
			return 0, key, fmt.Errorf("%w: resolve fence reader: %w", err, resolveErr)
		}
		target.Git = resolved.Git
	}
	if target.Git == nil {
		return 0, key, err
	}
	sha, _, _, readErr := target.Git.ReadFence(readCtx, forge.RepoRef{Path: target.Key.Repository}, "openmerge/fence/"+target.Key.BaseBranch)
	if readErr != nil {
		return 0, key, fmt.Errorf("%w: read actual fence: %w", err, readErr)
	}
	return s.store.CanonicalQueueWithFence(ctx, target.Key, sha)
}

func (s *Server) registerID(ctx context.Context, target Target, id store.QueueID) (*worker, error) {
	for _, w := range s.workers() {
		if w.id == id {
			w.mu.Lock()
			if w.Driver == nil && target.Driver != nil {
				w.bind(target)
			}
			w.mu.Unlock()
			return w, nil
		}
	}
	state, watermark, err := s.store.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	w := &worker{Target: target, id: id, managed: true, logState: state, logWatermark: watermark, lifecycleState: state.LifecycleState(), lifecycleDetail: state.Lifecycle.Detail, accessBlocked: state.SourceBlocked(queue.RepositoryAccess), wake: make(chan struct{}, 1), prs: make(map[int]forge.PullRequest), summaries: make(map[int]forge.PullRequestSummary)}
	s.publishHealth(w, state, false, nil, time.Now())
	s.initializeOutsideWarnings(ctx, w)
	s.mu.Lock()
	s.queues = append(s.queues, w)
	s.mu.Unlock()
	return w, nil
}

func (s *Server) lifecycle(ctx context.Context, w *worker, state, detail string) error {
	current, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	w.lifecycleState, w.lifecycleDetail = state, detail
	if state != "active" {
		w.ready = false
		w.waiting = detail
		w.validationNext = time.Time{}
	}
	event := queue.LifecycleChanged{State: state, Detail: detail}
	if current.LifecycleState() == state {
		return nil
	}
	_, err = s.store.Append(ctx, w.id, []queue.Event{event}, "")
	return err
}

func (s *Server) allowedBase(base string) bool {
	if len(s.options.BaseAllowlist) == 0 {
		return true
	}
	return slices.ContainsFunc(s.options.BaseAllowlist, func(pattern string) bool { return (config.Queue{Base: pattern}).MatchesBase(base, "") })
}

// RefreshBranches serializes successful listings, while each worker continues
// settling independently during slow discovery. No partial listing removes work.
func (s *Server) RefreshBranches(ctx context.Context) error {
	ctx = forge.WithRequestClass(ctx, forge.DiscoveryRequest)
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	var result error
	for _, repo := range s.repositories {
		if err := s.refreshRepository(ctx, repo); err != nil {
			result = errors.Join(result, fmt.Errorf("%s: %w", repo.Name, err))
			s.options.Logger.Warn("branch refresh failed; keeping previous expansion", "repository", repo.Name, "error", err)
		}
	}
	s.refreshResult(result)
	return result
}

func (s *Server) refreshRepository(ctx context.Context, repo Repository) error {
	target := repo.Target("")
	branch, err := target.Driver.DefaultBranch(ctx, forge.RepoRef{Path: repo.Name})
	if err != nil {
		return &repositoryReadError{"default branch", err}
	}
	tip, err := target.Git.ReadRef(ctx, forge.RepoRef{Path: repo.Name}, branch)
	if err != nil {
		return &repositoryReadError{"default-branch tip", err}
	}
	cfg, err := s.repositoryConfig(ctx, target, branch, tip, nil)
	acquisitionErr := err
	if err != nil {
		_, active := s.discoveryBindings(target)
		if active && len(cfg.Queues) == 0 {
			var invalid config.Errors
			if errors.As(err, &invalid) || errors.Is(err, config.ErrNotFound) {
				for _, w := range s.workers() {
					if w.Key.Installation != repo.Installation || !strings.EqualFold(w.Key.Repository, repo.Name) {
						continue
					}
					w.mu.Lock()
					if w.AuthorityRequired && w.resolveAuthorityLifecycle != nil {
						var mutable *config.GoverningIdentityError
						if !errors.As(err, &mutable) {
							w.configFailureRetryAt = time.Time{}
						} else if w.configFailureBranch != branch || w.configFailureTip != tip || w.configFailureRetryAt.IsZero() {
							w.configFailureRetryAt = time.Now().Add(s.options.RulesInterval)
						}
						w.configError, w.configFailure, w.configFailureBranch, w.configFailureTip = err, err, branch, tip
						err = errors.Join(err, s.block(ctx, w, queue.ConfigSource, "Default-branch configuration: "+w.configError.Error(), ""))
					}
					w.mu.Unlock()
				}
			}
		}
		if !active || len(cfg.Queues) == 0 {
			return &repositoryReadError{"configuration", err}
		}
	}
	// Capture bindings after worker activation so the next unchanged refresh is
	// already warm. Failures retain no successful acquisition cache.
	defer func() {
		bindings, _ := s.discoveryBindings(target)
		key := target.Key.Installation + "/" + strings.ToLower(target.Key.Repository)
		s.mu.Lock()
		if entry, ok := s.discoveryAcquisitions[key]; ok && entry.branch == branch && entry.tip == tip {
			entry.bindings = bindings
			s.discoveryAcquisitions[key] = entry
		}
		s.mu.Unlock()
	}()
	branches, err := repo.Branches(ctx)
	if err != nil {
		return &repositoryReadError{"branch listing", err}
	}
	// Internal test and fence refs must never recursively become queue bases.
	branches = slices.DeleteFunc(branches, func(base string) bool { return strings.HasPrefix(base, "openmerge/") })
	slices.Sort(branches)
	branches = slices.Compact(branches)
	if s.discovery != nil {
		dormant := true
		for _, base := range branches {
			if s.allowedBase(base) && len(matchingQueues(cfg, base, branch)) > 0 {
				dormant = false
				break
			}
		}
		s.mu.Lock()
		s.dormantRepositories[repo.Installation+"/"+strings.ToLower(repo.Name)] = dormant
		s.mu.Unlock()
	}
	// An overlap invalidates the involved queue entries, including their other
	// bases, without blocking independent queues in the same policy file.
	overlaps := make(map[string]bool)
	for _, base := range branches {
		matches := matchingQueues(cfg, base, branch)
		if len(matches) > 1 {
			for _, q := range matches {
				overlaps[q.Name] = true
			}
		}
	}
	// Finish base expansion before returning acquisition read failures. Their
	// causes must still reach discovery's existing quota/backoff scheduler.
	var result error
	var invalid config.Errors
	if acquisitionErr != nil && !errors.As(acquisitionErr, &invalid) {
		result = acquisitionErr
	}
	for _, base := range branches {
		if !s.allowedBase(base) || len(matchingQueues(cfg, base, branch)) == 0 {
			continue
		}
		target := repo.Target(base)
		if _, err := s.register(ctx, target); err != nil {
			result = errors.Join(result, fmt.Errorf("base %s registration: %w", base, err))
		}
	}
	for _, w := range s.workers() {
		if w.Key.Installation != repo.Installation || !strings.EqualFold(w.Key.Repository, repo.Name) {
			continue
		}
		w.mu.Lock()
		if w.AuthorityRequired && w.resolveAuthorityLifecycle != nil {
			w.discoveryAuthorityError = acquisitionErr
		}
		err := s.refreshWorker(ctx, w, cfg, branch, tip, branches, overlaps)
		if err != nil {
			s.options.Logger.Warn("base validation failed; keeping previous state", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "error", err)
			result = errors.Join(result, err)
		}
		w.mu.Unlock()
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
	return result
}

func matchingQueues(cfg config.Config, base, defaultBranch string) []config.Queue {
	var matches []config.Queue
	for _, q := range cfg.Queues {
		if q.MatchesBase(base, defaultBranch) {
			matches = append(matches, q)
		}
	}
	return matches
}

func (s *Server) refreshWorker(ctx context.Context, w *worker, cfg config.Config, branch, tip string, branches []string, overlaps map[string]bool) (result error) {
	if !w.ready {
		w.waiting = "validation pending"
	}
	w.validationNext = time.Time{}
	defer func() {
		if result != nil {
			w.ready = false
			w.waiting = result.Error()
		}
		if w.validationPending() && w.validationNext.IsZero() {
			w.validationNext = time.Now().Add(5 * time.Second)
		}
	}()
	id, _, err := s.store.CanonicalQueue(ctx, w.Key)
	if err != nil {
		w.ready = false
		w.waiting = err.Error()
		return &validationPending{w.waiting}
	}
	if id != w.id {
		w.settlementOnly = true
		w.ready = false
		w.waiting = "historical case-variant queue; settlement only"
		return nil
	}
	w.settlementOnly = false
	matches := matchingQueues(cfg, w.Key.BaseBranch, branch)
	switch {
	case !s.allowedBase(w.Key.BaseBranch) || len(matches) == 0 || w.Key.Lane != "":
		return s.lifecycle(ctx, w, "retired", "Queue removed, selector narrowed, or base excluded by allowlist")
	case !slices.Contains(branches, w.Key.BaseBranch):
		return s.lifecycle(ctx, w, "dormant", "Base branch is absent; log and fence retained")
	case slices.ContainsFunc(matches, func(q config.Queue) bool { return overlaps[q.Name] }):
		return s.lifecycle(ctx, w, "overlapping", "Queue selector overlaps another queue on an existing branch")
	}
	if w.discoveryAuthorityError != nil {
		w.ready = false
		w.waiting = w.discoveryAuthorityError.Error()
		return s.block(ctx, w, queue.ConfigSource, "Authority discovery acquisition: "+w.waiting, "")
	}
	w.lifecycleState = "active"
	if err := s.resolveTarget(ctx, w); err != nil {
		return err
	}
	// Before reactivation, settle any old attempt. Failed protection reads leave
	// a new/recreated queue blocked until a complete validation succeeds.
	if unsettled, err := s.settle(ctx, w); err != nil || unsettled {
		w.ready = false
		w.waiting = "landing settlement pending"
		if err == nil {
			return &validationPending{w.waiting}
		}
		return err
	}
	if err := s.configuration(ctx, w); err != nil {
		return err
	}
	if w.configError != nil {
		return nil
	}
	if w.configTip != tip || w.configBranch != branch {
		return errors.New("default-branch policy changed during branch refresh")
	}
	if err := s.observeWorkerAuthority(ctx, w); err != nil {
		return err
	}
	if err := s.repositoryRules(ctx, w); err != nil {
		return err
	}
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	if w.rulesWaiting != "" && !w.ready || len(state.RulesProblems()) > 0 {
		w.waiting = w.rulesWaiting
		if w.waiting == "" {
			w.waiting = "repository rules block admission"
		}
		w.validationNext = w.rulesNextCheck
		if w.rulesWaiting != "" {
			return &validationPending{w.waiting}
		}
		return nil
	}
	if err := s.initializeFence(ctx, w); err != nil {
		return err
	}
	if err := s.lifecycle(ctx, w, "active", ""); err != nil {
		return err
	}
	w.ready = true
	w.validationNext = time.Time{}
	w.waiting = ""
	return nil
}

// Keys returns a stable snapshot for the read-only status API.
func (s *Server) Keys() []store.QueueKey {
	workers := s.workers()
	keys := make([]store.QueueKey, 0, len(workers))
	for _, w := range workers {
		keys = append(keys, w.Key)
	}
	return keys
}

// Journals supplies persisted history alongside this process's readiness snapshot.
func (s *Server) Journals(ctx context.Context, keys []store.QueueKey) ([]store.Journal, error) {
	return s.store.Journals(ctx, keys)
}

// QueueReadiness returns the current admission gate without persisting transient failures.
func (s *Server) QueueReadiness(key store.QueueKey) *status.Readiness {
	for _, w := range s.workers() {
		if w.Key == key {
			if !w.lockStatus() {
				return &status.Readiness{Ready: false, Reason: "policy_busy"}
			}
			defer w.mu.Unlock()
			r := &status.Readiness{Ready: !w.managed || w.ready}
			if !r.Ready {
				r.Reason = w.readinessReason()
			}
			return r
		}
	}
	return nil
}

type validationPending struct{ reason string }

func (e *validationPending) Error() string { return e.reason }

// repositoryReadError identifies failures before independent base validation.
type repositoryReadError struct {
	phase string
	err   error
}

func (e *repositoryReadError) Error() string { return e.phase + " could not be read: " + e.err.Error() }
func (e *repositoryReadError) Unwrap() error { return e.err }

// ChangeDecisions exposes only retained, queue-scoped historical evidence.
func (s *Server) ChangeDecisions(ctx context.Context, key store.QueueKey, id queue.ChangeID, revision uint64, after int64, limit int) ([]store.DecisionRecord, int64, bool, error) {
	return s.store.ChangeDecisions(ctx, key, id, revision, after, limit)
}
