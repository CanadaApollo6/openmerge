package serve

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// Installation binds a complete repository listing and budget to its own
// credentials. Suspended installations need no working installation token.
type Installation struct {
	ID           string
	Suspended    bool
	Repositories func(context.Context) ([]Repository, error)
	Budget       func() status.Budget
}

// Discovery is the App boundary. Listings are complete or fail; webhook payload
// lists are never passed here. Target resolves historical credentials lazily,
// including installations outside the current allowlists.
type Discovery struct {
	App                   string
	Installations         func(context.Context) ([]Installation, error)
	Target                func(context.Context, store.QueueKey) (Target, error)
	InstallationAllowlist string
	RepositoryAllowlist   []string
}

// NewDiscovery restores historical partitions before attempting any listing.
// Workers remain available through removal and reinstatement, including when
// credentials prevent settlement. Configuration and membership are independent.
func NewDiscovery(ctx context.Context, db *store.Store, discovery Discovery, options Options) (*Server, error) {
	if discovery.Installations == nil || discovery.Target == nil {
		return nil, errors.New("discovery requires installations and historical targets")
	}
	for _, pattern := range discovery.RepositoryAllowlist {
		if _, err := condition.CompileGlob(strings.ToLower(pattern)); err != nil {
			return nil, err
		}
	}
	s, err := newServer(db, options)
	if err != nil {
		return nil, err
	}
	s.discovery = &discovery
	s.discoveryBudgets = make(map[string]func() status.Budget)
	s.discoveryBackoffs = make(map[string]time.Time)
	s.dormantRepositories = make(map[string]bool)
	s.discoveryStatus = status.Discovery{Repositories: []status.Repository{}, Installations: []status.Installation{}}
	owners, err := db.InstallationApps(ctx)
	if err != nil {
		return nil, err
	}
	stored, err := db.StoredQueues(ctx)
	if err != nil {
		return nil, err
	}
	for _, historical := range stored {
		key := historical.Key
		owner := owners[key.Installation]
		if discovery.App != "" && owner != "" && owner != discovery.App {
			continue
		}
		target := Target{Key: key, Resolve: func(ctx context.Context) (Target, error) { return discovery.Target(ctx, key) }}
		w, err := s.registerID(ctx, target, historical.ID)
		if err != nil {
			return nil, err
		}

		state, _, err := s.store.Load(ctx, w.id)
		if err != nil {
			return nil, err
		}
		if discovery.App != "" && owner == "" {
			continue
		}
		r := status.Repository{Installation: key.Installation, Name: key.Repository, State: "validating"}
		if state.SourceBlocked(queue.RepositoryAccess) {
			r.State = "removed"
			r.Detail = state.BlockSourceDetail(queue.RepositoryAccess)
		}
		s.setRepositoryStatus(r)
	}
	select {
	case s.refresh <- struct{}{}:
	default:
	}
	return s, nil
}

// NotifyDiscovery coalesces verified installation notifications into a complete
// sweep and wakes affected workers. Partial payload lists never change access.
func (s *Server) NotifyDiscovery(ctx context.Context, installation, repository, delivery string) error {
	return s.notifyRefresh(ctx, installation, repository, delivery)
}

// Refresh serializes discovery with branch expansion. A failure preserves the
// prior state of its scope; successful installations still make progress.
func (s *Server) Refresh(ctx context.Context) error {
	if s.discovery == nil {
		return s.RefreshBranches(ctx)
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	ctx = forge.WithRequestClass(ctx, forge.DiscoveryRequest)
	result := s.discover(ctx)
	s.refreshResult(result)
	return result
}

func (s *Server) refreshResult(err error) {
	now := time.Now()
	s.refreshNotBefore = now.Add(min(time.Second, s.options.DiscoveryInterval))
	s.refreshNextAt = now.Add(s.options.DiscoveryInterval)
	s.refreshRetryAt = time.Time{}
	if err == nil {
		s.refreshFailures = 0
		return
	}
	var rate interface{ RetryAfter() time.Time }
	schedulingErr := discoverySchedulingError(err, false)
	limited := errors.As(schedulingErr, &rate)
	pending := schedulingErr == nil || onlyValidationPending(schedulingErr)
	delay := min(5*time.Second, s.options.DiscoveryInterval)
	if schedulingErr == nil {
		delay = s.options.DiscoveryInterval
	}
	if limited {
		delay = max(time.Millisecond, time.Until(rate.RetryAfter()))
	} else if !pending {
		s.refreshFailures++
		delay = min(5*time.Second*time.Duration(1<<min(s.refreshFailures-1, 6)), 5*time.Minute, s.options.DiscoveryInterval)
	}
	retryAt := now.Add(delay)
	if limited {
		retryAt = rate.RetryAfter()
	}
	if !limited {
		for _, until := range s.discoveryBackoffs {
			if until.After(now) && until.Before(retryAt) {
				retryAt = until
			}
		}
	}
	if pending && !limited {
		for _, w := range s.workers() {
			w.mu.Lock()
			if w.validationPending() && w.validationNext.After(now) && !now.Before(s.discoveryBackoffs[w.Key.Installation]) && w.validationNext.Before(retryAt) {
				retryAt = w.validationNext
			}
			w.mu.Unlock()
		}
	}
	s.refreshNextAt = maxTime(retryAt, s.refreshNotBefore)
	// Scheduled installation/validation retries must not suppress later wakes
	// from healthy installations. Only global quota or failure backoff does.
	if limited || !pending {
		s.refreshRetryAt = s.refreshNextAt
	}
	if limited {
		s.options.Logger.Debug("discovery quota backoff", "until", s.refreshRetryAt)
	} else {
		s.options.Logger.Warn("discovery failed; retaining previous observations", "error", err)
	}
}

// Installation quota deadlines live in discoveryBackoffs, so they must not
// become an App-wide retry barrier when joined with another installation's work.
type installationDiscoveryError struct{ err error }

func (e *installationDiscoveryError) Error() string { return e.err.Error() }
func (e *installationDiscoveryError) Unwrap() error { return e.err }

func discoverySchedulingError(err error, installation bool) error {
	if err == nil {
		return nil
	}
	if local, ok := err.(*installationDiscoveryError); ok {
		return discoverySchedulingError(local.err, true)
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var result error
		for _, child := range joined.Unwrap() {
			result = errors.Join(result, discoverySchedulingError(child, installation))
		}
		return result
	}
	if _, limited := err.(interface{ RetryAfter() time.Time }); installation && limited {
		return nil
	}
	if wrapped := errors.Unwrap(err); wrapped != nil {
		return discoverySchedulingError(wrapped, installation)
	}
	return err
}

func onlyValidationPending(err error) bool {
	if _, ok := err.(*validationPending); ok {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			if !onlyValidationPending(child) {
				return false
			}
		}
		return true
	}
	if wrapped := errors.Unwrap(err); wrapped != nil {
		return onlyValidationPending(wrapped)
	}
	return false
}

func (s *Server) discover(ctx context.Context) error {
	readCtx, cancel := context.WithTimeout(ctx, s.options.ActionTimeout)
	installations, err := s.discovery.Installations(readCtx)
	cancel()
	if err != nil {
		return err
	}
	present := make(map[string]bool)
	var result error
	for _, installation := range installations {
		id, err := strconv.ParseInt(installation.ID, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != installation.ID {
			return errors.New("discovery returned a noncanonical installation ID")
		}
		present[installation.ID] = true
	}
	if s.discovery.App != "" {
		for _, installation := range installations {
			if err := s.store.BindInstallation(ctx, installation.ID, s.discovery.App); err != nil {
				return err
			}
		}
	}
	// A legacy partition is attributed only when authenticated App membership
	// confirms its installation. Unbound historical attempts still settle.
	for _, w := range s.workers() {
		if present[w.Key.Installation] {
			r := s.repositoryStatus(w.Key.Installation, w.Key.Repository)
			if r.LastSuccessfulSweep == nil && r.State == "not configured" {
				r.State = "validating"
				s.setRepositoryStatus(r)
			}
		}
	}

	for _, r := range s.DiscoveryStatus().Repositories {
		if !present[r.Installation] {
			result = errors.Join(result, s.removeRepository(ctx, r, "removed", "Installation absent from successful App listing"))
		}
	}
	for _, installation := range installations {
		if err := s.discoverInstallation(ctx, installation); err != nil {
			result = errors.Join(result, &installationDiscoveryError{err})
		}
	}

	s.mu.Lock()
	s.discoveryStatus.Installations = slices.DeleteFunc(s.discoveryStatus.Installations, func(i status.Installation) bool { return !present[i.ID] })
	for id := range s.discoveryBudgets {
		if !present[id] {
			delete(s.discoveryBudgets, id)
		}
	}
	s.mu.Unlock()
	if result == nil {
		now := time.Now().UTC()
		s.mu.Lock()
		s.discoveryStatus.LastSuccessfulSweep = &now
		s.mu.Unlock()
	}
	return result
}

func (s *Server) allowedRepository(repo Repository) bool {
	if s.discovery.InstallationAllowlist != "" && s.discovery.InstallationAllowlist != repo.Installation {
		return false
	}
	if len(s.discovery.RepositoryAllowlist) == 0 {
		return true
	}
	return slices.ContainsFunc(s.discovery.RepositoryAllowlist, func(pattern string) bool {
		glob, _ := condition.CompileGlob(strings.ToLower(pattern))
		return glob.MatchString(strings.ToLower(repo.Name))
	})
}

func (s *Server) repositoryWorkers(installation, name string) []*worker {
	return slices.DeleteFunc(s.workers(), func(w *worker) bool {
		return w.Key.Installation != installation || !strings.EqualFold(w.Key.Repository, name)
	})
}

func (s *Server) removeRepository(ctx context.Context, r status.Repository, state, detail string) error {
	var result error
	for _, w := range s.repositoryWorkers(r.Installation, r.Name) {
		current, _, err := s.store.Load(ctx, w.id)
		if err == nil && (!current.SourceBlocked(queue.RepositoryAccess) || current.BlockSourceDetail(queue.RepositoryAccess) != detail) {
			_, err = s.store.Append(ctx, w.id, []queue.Event{queue.BlockSourceBlocked{Source: queue.RepositoryAccess, Detail: detail, Basis: current.BlockSourceRevision(queue.RepositoryAccess)}}, "")
		}
		w.mu.Lock()
		w.ready = false
		w.accessBlocked = true
		w.validationNext = time.Time{}
		w.waiting = detail
		if err == nil && w.Driver != nil {
			reportCtx, cancel := context.WithTimeout(ctx, s.options.ActionTimeout)
			// Access loss must persist even if its diagnostic cannot be written.
			current, _, loadErr := s.store.Load(ctx, w.id)
			if loadErr == nil {
				_ = s.reportRepositoryRemoval(reportCtx, w, current)
			}
			cancel()
		}
		w.mu.Unlock()
		result = errors.Join(result, err)
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
	if result == nil {
		now := time.Now().UTC()
		r.LastSuccessfulSweep = &now
		r.State, r.Detail = state, detail
		s.setRepositoryStatus(r)
	}
	return result
}

func (s *Server) blockRepositoryConfig(ctx context.Context, repo Repository, cause error) error {
	for _, w := range s.repositoryWorkers(repo.Installation, repo.Name) {
		w.mu.Lock()
		w.ready = false
		w.waiting = cause.Error()
		current, _, err := s.store.Load(ctx, w.id)
		if err == nil && (!current.SourceBlocked(queue.ConfigSource) || current.BlockSourceDetail(queue.ConfigSource) != cause.Error()) {
			err = s.block(ctx, w, queue.ConfigSource, cause.Error(), "")
		}
		w.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) reportRepositoryRemoval(ctx context.Context, w *worker, state queue.State) error {
	heads := make(map[int]string)
	for _, entry := range state.Entries {
		heads[entry.PR] = entry.HeadSHA
	}
	if state.Active != nil {
		for _, pr := range state.Active.PRs {
			heads[pr.PR] = pr.HeadSHA
		}
	}
	for n, pr := range w.prs {
		if !pr.Closed {
			heads[n] = pr.HeadSHA
		}
	}
	for pr, head := range w.diagnosticHeads {
		heads[pr] = head
	}
	for pr, head := range heads {
		if err := s.status(ctx, w, pr, head, forge.Status{State: forge.Paused, Reason: "repository-removed", Summary: state.BlockSourceDetail(queue.RepositoryAccess)}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) repositoryStatus(installation, name string) status.Repository {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.discoveryStatus.Repositories {
		if r.Installation == installation && strings.EqualFold(r.Name, name) {
			return r
		}
	}
	return status.Repository{Installation: installation, Name: name, State: "not configured"}
}

func (s *Server) setRepositoryStatus(r status.Repository) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, old := range s.discoveryStatus.Repositories {
		if old.Installation == r.Installation && strings.EqualFold(old.Name, r.Name) {
			r.Name = old.Name
			s.discoveryStatus.Repositories[i] = r
			return
		}
	}
	s.discoveryStatus.Repositories = append(s.discoveryStatus.Repositories, r)
}

func (s *Server) setInstallationStatus(installation Installation, success bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := status.Installation{ID: installation.ID, Suspended: installation.Suspended}
	s.discoveryBudgets[installation.ID] = installation.Budget
	if success {
		now := time.Now().UTC()
		r.LastSuccessfulSweep = &now
	}
	if installation.Budget != nil {
		r.Budget = installation.Budget()
	}
	for i, old := range s.discoveryStatus.Installations {
		if old.ID == r.ID {
			if !success {
				r.LastSuccessfulSweep = old.LastSuccessfulSweep
			}
			s.discoveryStatus.Installations[i] = r
			return
		}
	}
	s.discoveryStatus.Installations = append(s.discoveryStatus.Installations, r)
}

// DiscoveryStatus returns copies; budget snapshots are refreshed without forge
// requests and never hold inventory locks across queue or network work.
func (s *Server) DiscoveryStatus() status.Discovery {
	s.mu.RLock()
	result := s.discoveryStatus
	result.Repositories = slices.Clone(result.Repositories)
	result.Installations = slices.Clone(result.Installations)
	budgets := make(map[string]func() status.Budget, len(s.discoveryBudgets))
	for id, read := range s.discoveryBudgets {
		budgets[id] = read
	}
	s.mu.RUnlock()
	now := time.Now()
	for i := range result.Repositories {
		r := &result.Repositories[i]
		_, last := s.webhooks.snapshot(r.Installation, r.Name)
		if !last.IsZero() {
			age := max(0, now.Sub(last).Seconds())
			r.LastVerifiedDelivery = &last
			r.LastDeliveryAgeSeconds = &age
		}
		if r.State != "served" && r.State != "validating" {
			continue
		}
		for _, w := range s.repositoryWorkers(r.Installation, r.Name) {
			if !w.lockStatus() {
				// Retain the inventory snapshot until local validation can be read.
				continue
			}
			if w.validationPending() {
				r.State, r.Detail = "validating", w.readinessReason()
			}
			w.mu.Unlock()
		}
	}
	for i := range result.Installations {
		installation := &result.Installations[i]
		last, _ := s.webhooks.snapshot(installation.ID, "")
		installation.WebhooksConfigured = s.options.WebhooksConfigured
		installation.WebhookHealthy = s.webhookHealthy(last, now)
		if !last.IsZero() {
			installation.LastVerifiedDelivery = &last
		}
		if read := budgets[result.Installations[i].ID]; read != nil {
			result.Installations[i].Budget = read()
		}
	}
	slices.SortFunc(result.Installations, func(a, b status.Installation) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(result.Repositories, func(a, b status.Repository) int {
		return strings.Compare(a.Installation+"/"+a.Name, b.Installation+"/"+b.Name)
	})
	return result
}

func (s *Server) resolveTarget(ctx context.Context, w *worker) error {
	if w.Driver != nil {
		return nil
	}
	if w.Resolve == nil {
		return errors.New("queue credentials are unavailable")
	}
	resolveCtx, cancel := context.WithTimeout(ctx, s.options.ActionTimeout)
	defer cancel()
	target, err := w.Resolve(resolveCtx)
	if err != nil {
		return err
	}
	if target.Driver == nil || target.Git == nil || target.BotLogin == "" {
		return errors.New("resolved target requires credentials and App identity")
	}
	target.Key = w.Key
	w.bind(target)
	return nil
}

// bind changes credentials while Key remains immutable for notifications/status.
func (w *worker) bind(target Target) {
	w.Driver, w.Git, w.BotLogin = target.Driver, target.Git, target.BotLogin
	w.CheckRules, w.CheckPermissions, w.CheckProtection = target.CheckRules, target.CheckPermissions, target.CheckProtection
	w.Resolve = target.Resolve
	w.AuthorityRequired = target.AuthorityRequired
	w.PrepareAuthorityPush, w.PinAuthorityPush, w.ObserveAuthorityPush = target.PrepareAuthorityPush, target.PinAuthorityPush, target.ObserveAuthorityPush
}

func (s *Server) recordDiscoveryBackoff(installation string, err error) {
	var quota interface{ RetryAfter() time.Time }
	if errors.As(err, &quota) {
		s.discoveryBackoffs[installation] = quota.RetryAfter()
	}
}

func (s *Server) discoverInstallation(ctx context.Context, installation Installation) error {
	var result error
	s.setInstallationStatus(installation, false)
	if s.discovery.InstallationAllowlist != "" && s.discovery.InstallationAllowlist != installation.ID {
		for _, r := range s.DiscoveryStatus().Repositories {
			if r.Installation == installation.ID {
				result = errors.Join(result, s.removeRepository(ctx, r, "removed", "Excluded by repository or installation allowlist"))
			}
		}
		s.setInstallationStatus(installation, result == nil)
		return result
	}
	if installation.Suspended {
		for _, r := range s.DiscoveryStatus().Repositories {
			if r.Installation == installation.ID {
				result = errors.Join(result, s.removeRepository(ctx, r, "suspended", "Installation suspended"))
			}
		}
		s.setInstallationStatus(installation, result == nil)
		return result
	}
	if until := s.discoveryBackoffs[installation.ID]; time.Now().Before(until) {
		result = errors.Join(result, &quotaBackoff{until: until})
		return result
	}
	if installation.Repositories == nil {
		result = errors.Join(result, errors.New("installation requires repository listing"))
		return result
	}
	readCtx, cancel := context.WithTimeout(ctx, s.options.ActionTimeout)
	repos, err := installation.Repositories(readCtx)
	cancel()
	if err != nil {
		s.recordDiscoveryBackoff(installation.ID, err)
		result = errors.Join(result, fmt.Errorf("installation %s listing: %w", installation.ID, err))
		return result
	}
	seen := make(map[string]bool)
	for _, repo := range repos {
		seen[strings.ToLower(repo.Name)] = true
	}
	for _, r := range s.DiscoveryStatus().Repositories {
		if r.Installation == installation.ID && !seen[strings.ToLower(r.Name)] {
			result = errors.Join(result, s.removeRepository(ctx, r, "removed", "Repository absent from successful installation listing"))
		}
	}
	for _, repo := range repos {
		repo.Installation = installation.ID
		if repo.Name == "" || repo.Target == nil || repo.Branches == nil {
			result = errors.Join(result, errors.New("discovered repository requires name, target and branches"))
			continue
		}
		r := s.repositoryStatus(repo.Installation, repo.Name)
		now := time.Now().UTC()
		if !s.allowedRepository(repo) {
			result = errors.Join(result, s.removeRepository(ctx, r, "removed", "Excluded by repository or installation allowlist"))
			continue
		}
		basis := make(map[store.QueueID]uint64)
		basisFailed := false
		for _, w := range s.repositoryWorkers(repo.Installation, repo.Name) {
			state, _, err := s.store.Load(ctx, w.id)
			if err != nil {
				result = errors.Join(result, err)
				basisFailed = true
				break
			}
			basis[w.id] = state.BlockSourceRevision(queue.RepositoryAccess)
		}
		if basisFailed {
			continue
		}
		readCtx, cancel := context.WithTimeout(ctx, s.options.ActionTimeout)
		err := s.refreshRepository(readCtx, repo)
		cancel()
		s.recordDiscoveryBackoff(installation.ID, err)
		var invalid config.Errors
		var readFailure *repositoryReadError
		if err == nil || !errors.As(err, &readFailure) {
			for _, w := range s.repositoryWorkers(repo.Installation, repo.Name) {
				w.mu.Lock()
				state, _, loadErr := s.store.Load(ctx, w.id)
				if loadErr == nil && state.SourceBlocked(queue.RepositoryAccess) {
					_, loadErr = s.store.Append(ctx, w.id, []queue.Event{queue.BlockSourceCleared{Source: queue.RepositoryAccess, Basis: basis[w.id]}}, "")
				}
				if loadErr == nil {
					w.accessBlocked = false
				}
				w.mu.Unlock()
				result = errors.Join(result, loadErr)
			}
		}
		switch {
		case errors.Is(err, config.ErrNotFound):
			// A complete four-path read proves deletion; transport failures cannot.
			state := "not configured"
			if len(s.repositoryWorkers(repo.Installation, repo.Name)) > 0 {
				state = "removed"
			}
			result = errors.Join(result, s.removeRepository(ctx, r, state, "No default-branch OpenMerge configuration"))
		case errors.As(err, &invalid):
			r.State, r.Detail = "invalid config", err.Error()
			r.LastSuccessfulSweep = &now
			result = errors.Join(result, s.blockRepositoryConfig(ctx, repo, err))
			s.setRepositoryStatus(r)
		case err != nil:
			r.State, r.Detail = "validating", err.Error()
			if readFailure != nil {
				r.State = "unavailable"
				// Only repository-wide reads invalidate every base. Independent
				// registration/validation failures leave healthy bases running.
				for _, w := range s.repositoryWorkers(repo.Installation, repo.Name) {
					w.mu.Lock()
					w.ready = false
					w.waiting = r.Detail
					if w.validationPending() {
						w.validationNext = time.Now().Add(5 * time.Second)
					}
					w.mu.Unlock()
				}
			}
			s.setRepositoryStatus(r)
			result = errors.Join(result, fmt.Errorf("%s: %w", repo.Name, err))
		default:
			r.State, r.Detail = "served", ""
			r.LastSuccessfulSweep = &now
			s.mu.RLock()
			dormant := s.dormantRepositories[repo.Installation+"/"+strings.ToLower(repo.Name)]
			s.mu.RUnlock()
			if dormant {
				r.State = "dormant base"
			}
			for _, w := range s.repositoryWorkers(repo.Installation, repo.Name) {
				w.mu.Lock()
				if w.validationPending() {
					r.State, r.Detail = "validating", w.readinessReason()
				}
				w.mu.Unlock()
			}
			s.setRepositoryStatus(r)
		}
	}
	s.setInstallationStatus(installation, result == nil)
	return result
}
