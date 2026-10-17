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
// reads. Failed reads pause ordinary work but cannot prevent recovery startup.
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
	keys, err := db.QueueKeys(ctx)
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
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
		id, err := db.EnsureQueue(ctx, key)
		if err != nil {
			return nil, err
		}
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
		w, err := s.register(ctx, target)
		if err != nil {
			return nil, err
		}
		w.recovery = !served
		if err := s.lifecycle(ctx, w, "settling-only", "Recovering stored queue before branch validation"); err != nil {
			return nil, err
		}
	}
	for _, w := range s.workers() {
		if _, err := s.settle(ctx, w); err != nil {
			s.options.Logger.Error("startup landing settlement failed; worker will retry", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "error", err)
		}
	}
	if err := s.RefreshBranches(ctx); err != nil {
		s.options.Logger.Error("initial branch refresh failed; settlement remains enabled", "error", err)
	}
	return s, nil
}

func (s *Server) register(ctx context.Context, target Target) (*worker, error) {
	if target.Driver == nil || target.Git == nil || target.BotLogin == "" {
		return nil, errors.New("target requires driver, git and App login")
	}
	id, err := s.store.EnsureQueue(ctx, target.Key)
	if err != nil {
		return nil, err
	}
	for _, w := range s.workers() {
		if w.id == id {
			return w, nil
		}
	}
	state, watermark, err := s.store.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	w := &worker{Target: target, id: id, managed: true, logState: state, logWatermark: watermark, wake: make(chan struct{}, 1), prs: make(map[int]forge.PullRequest), summaries: make(map[int]forge.PullRequestSummary)}
	if err := s.lifecycle(ctx, w, "settling-only", "Waiting for branch and protection validation"); err != nil {
		return nil, err
	}
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
	event := queue.LifecycleChanged{State: state, Detail: detail}
	if current.LifecycleState() == state && (state == "active" || current.Lifecycle.Detail == detail) {
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
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	var result error
	for _, repo := range s.repositories {
		if err := s.refreshRepository(ctx, repo); err != nil {
			result = errors.Join(result, fmt.Errorf("%s: %w", repo.Name, err))
			for _, w := range s.workers() {
				if w.Key.Installation != repo.Installation || !strings.EqualFold(w.Key.Repository, repo.Name) {
					continue
				}
				w.mu.Lock()
				err := s.pause(ctx, w, "branch-refresh-failed", err.Error())
				w.mu.Unlock()
				if err != nil {
					return errors.Join(result, err)
				}
			}
		}
	}
	return result
}

func (s *Server) refreshRepository(ctx context.Context, repo Repository) error {
	target := repo.Target("")
	branch, err := target.Driver.DefaultBranch(ctx, forge.RepoRef{Path: repo.Name})
	if err != nil {
		return err
	}
	tip, err := target.Git.ReadRef(ctx, forge.RepoRef{Path: repo.Name}, branch)
	if err != nil {
		return err
	}
	cfg, err := s.repositoryConfig(ctx, target, tip)
	if err != nil {
		return err
	}
	branches, err := repo.Branches(ctx)
	if err != nil {
		return err
	}
	// Internal test and fence refs must never recursively become queue bases.
	branches = slices.DeleteFunc(branches, func(base string) bool { return strings.HasPrefix(base, "openmerge/") })
	slices.Sort(branches)
	branches = slices.Compact(branches)
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
	for _, base := range branches {
		if !s.allowedBase(base) || len(matchingQueues(cfg, base, branch)) == 0 {
			continue
		}
		target := repo.Target(base)
		// Reuse spelling and identity of any previously stored partition.
		for _, w := range s.workers() {
			if w.Key.Installation == repo.Installation && strings.EqualFold(w.Key.Repository, repo.Name) && w.Key.BaseBranch == base && w.Key.Lane == "" {
				target.Key = w.Key
				break
			}
		}
		if _, err := s.register(ctx, target); err != nil {
			return err
		}
	}
	for _, w := range s.workers() {
		if w.Key.Installation != repo.Installation || !strings.EqualFold(w.Key.Repository, repo.Name) {
			continue
		}
		w.mu.Lock()
		err := s.refreshWorker(ctx, w, cfg, branch, tip, branches, overlaps)
		if err != nil {
			s.options.Logger.Error("base validation failed; other queues remain served", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "error", err)
			err = s.lifecycle(ctx, w, "settling-only", "Base validation failed: "+err.Error())
		}
		w.mu.Unlock()
		if err != nil {
			return err
		}
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
	return nil
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

func (s *Server) refreshWorker(ctx context.Context, w *worker, cfg config.Config, branch, tip string, branches []string, overlaps map[string]bool) error {
	matches := matchingQueues(cfg, w.Key.BaseBranch, branch)
	switch {
	case !s.allowedBase(w.Key.BaseBranch) || len(matches) == 0 || w.Key.Lane != "":
		return s.lifecycle(ctx, w, "retired", "Queue removed, selector narrowed, or base excluded by allowlist")
	case !slices.Contains(branches, w.Key.BaseBranch):
		return s.lifecycle(ctx, w, "dormant", "Base branch is absent; log and fence retained")
	case slices.ContainsFunc(matches, func(q config.Queue) bool { return overlaps[q.Name] }):
		return s.lifecycle(ctx, w, "overlapping", "Queue selector overlaps another queue on an existing branch")
	}
	// Before reactivation, settle any old attempt. Failed protection reads leave
	// a new/recreated queue blocked until a complete validation succeeds.
	if unsettled, err := s.settle(ctx, w); err != nil || unsettled {
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
	w.rulesNextCheck = time.Time{}
	if err := s.repositoryRules(ctx, w); err != nil {
		return err
	}
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	if w.rulesFailures > 0 || len(state.RulesProblems()) > 0 {
		return s.lifecycle(ctx, w, "settling-only", "Waiting for per-base protection validation")
	}
	if err := s.initializeFence(ctx, w); err != nil {
		return err
	}
	if err := s.lifecycle(ctx, w, "active", ""); err != nil {
		return err
	}
	state, _, err = s.store.Load(ctx, w.id)
	if err == nil && state.PauseReason(queue.Config{}) == "branch-refresh-failed" {
		_, err = s.store.Append(ctx, w.id, []queue.Event{queue.ResumeRequested{}}, "")
	}
	return err
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
