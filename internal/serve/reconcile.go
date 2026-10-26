package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func (s *Server) record(ctx context.Context, w *worker, key string, value any, events ...queue.Event) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	err = s.store.Record(ctx, w.id, key, data, events...)
	if err == nil {
		w.publishProgress(time.Now())
	}
	return err
}

func (s *Server) configuration(ctx context.Context, w *worker) error {
	if w.discoveryAuthorityError != nil {
		w.configError = w.discoveryAuthorityError
		return s.block(ctx, w, queue.ConfigSource, "Authority discovery acquisition: "+w.discoveryAuthorityError.Error(), "")
	}
	basis, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	branch, loadErr := w.Driver.DefaultBranch(ctx, w.repo())
	if loadErr == nil {
		w.publishProgress(time.Now())
	}
	if loadErr == nil && branch == "" {
		loadErr = errors.New("repository default branch is missing")
	}
	tip, baseErr := w.Git.ReadRef(ctx, w.repo(), w.Key.BaseBranch)
	if baseErr == nil {
		w.publishProgress(time.Now())
	}
	if baseErr != nil {
		loadErr = errors.Join(loadErr, fmt.Errorf("read queue base %q (default branch %q): %w", w.Key.BaseBranch, branch, baseErr))
	}
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	configTip := ""
	if loadErr == nil {
		if branch == w.Key.BaseBranch {
			configTip = tip
		} else {
			configTip, loadErr = w.Git.ReadRef(ctx, w.repo(), branch)
			if loadErr == nil {
				w.publishProgress(time.Now())
			}
		}
	}
	if loadErr == nil && w.configCandidate.Authority.Mode == authority.OpenMerge && w.configFailure != nil && w.configFailureBranch == branch && w.configFailureTip == configTip && (w.configFailureRetryAt.IsZero() || time.Now().Before(w.configFailureRetryAt)) {
		w.configError = w.configFailure
		return s.block(ctx, w, queue.ConfigSource, "Default-branch configuration: "+w.configError.Error(), "")
	}
	if loadErr == nil && (configTip != w.configTip || branch != w.configBranch) {
		loadErr = s.loadConfiguration(ctx, w, branch, configTip)
	}
	// Read failures stop this cycle in memory, without writing block/clear or
	// base/config observations. A parsed invalid file or proven deletion is a
	// definite condition and can be recorded independently.
	var invalid config.Errors
	var mutable *config.GoverningIdentityError
	if loadErr != nil && !errors.Is(loadErr, config.ErrNotFound) && !errors.As(loadErr, &invalid) && !errors.As(loadErr, &mutable) && (w.stageA == nil || !stageADefiniteIdentityFailure(loadErr)) {
		w.configError = loadErr
		w.configBranch, w.configTip = "", ""
		if w.managed {
			w.ready = false
			w.waiting = "configuration could not be read; discovery will retry"
			select {
			case s.refresh <- struct{}{}:
			default:
			}
		}
		return loadErr
	}
	stageARefresh := loadErr == nil && w.stageA != nil
	if stageARefresh {
		loadErr = s.refreshStageAPolicy(ctx, w, configTip)
		if loadErr != nil && w.configCandidate.Authority.Mode == authority.OpenMerge && stageADefiniteIdentityFailure(loadErr) {
			loadErr = &config.GoverningIdentityError{Cause: &config.GoverningReadError{Boundary: "identities", Cause: loadErr}}
		}
	}
	// Identity absence/mismatch is a proved policy failure and must be durable.
	// Transport, rate-limit and incomplete-membership errors abort entirely: a
	// stale in-memory policy is never used, but neither is an uncertain result
	// written as a durable configuration failure.
	if loadErr != nil && stageARefresh && !errors.As(loadErr, &invalid) && !errors.As(loadErr, &mutable) && !stageADefiniteIdentityFailure(loadErr) {
		w.configError = loadErr
		w.configBranch, w.configTip = "", ""
		if w.managed {
			w.ready = false
			w.waiting = "configuration could not be read; discovery will retry"
			select {
			case s.refresh <- struct{}{}:
			default:
			}
		}
		return loadErr
	}
	var activation *authorityActivation
	if loadErr == nil && w.AuthorityRequired && w.resolveAuthorityLifecycle != nil {
		activation, loadErr = s.prepareAuthorityLifecycle(ctx, w, state, branch, configTip)
		if loadErr != nil {
			if errors.As(loadErr, &mutable) {
				w.configFailure, w.configFailureBranch, w.configFailureTip = loadErr, branch, configTip
				w.configFailureRetryAt = time.Now().Add(s.options.RulesInterval)
			}
			w.configError = loadErr
			w.configBranch, w.configTip = "", ""
			s.authorityConfigurationFailed(w)
			return loadErr
		}
	}
	if baseErr == nil && state.BaseSHA != tip {
		if w.stageA != nil {
			if loadErr == nil {
				if err := s.observeStageABase(ctx, w, state, tip, configTip); err != nil {
					return err
				}
			}
		} else if _, err := s.store.Append(ctx, w.id, []queue.Event{queue.BaseMoved{SHA: tip}}, ""); err != nil {
			return err
		}
		// Conflict observations can change when this base moves even if the
		// default branch (and therefore the policy) stays at the same commit.
		w.summaries = make(map[int]forge.PullRequestSummary)
	}
	if loadErr == nil && (activation != nil || state.ConfigDigest != w.configDigest || w.cfg.QueueConfig().MaxDepth() > 1 && !state.WindowInitialized()) {
		// Publish the policy before using it. Replay invalidates work built under
		// another digest, and start gates reject workers holding an older policy.
		events := []queue.Event{}
		if state.ConfigDigest != "" && state.ConfigDigest != w.configDigest && !state.WindowInitialized() && w.cfg.QueueConfig().MaxDepth() > 1 {
			// Old journals omit bounds. Recover the fixed window from the policy
			// that actually ran before clamping it under the replacement policy.
			previousCandidate, readErr := s.repositoryCandidate(ctx, w.Target, state.ConfigCommit, w)
			if readErr != nil {
				return fmt.Errorf("read historical window policy: %w", readErr)
			}
			selected, diagnostics := previousCandidate.Config.ResolveBranch(w.Key.BaseBranch, branch)
			if len(diagnostics) != 0 {
				return diagnostics
			}
			if selected.Approvals != nil {
				w.configError = fmt.Errorf("historical approval policy has a folded digest without recorded window bounds; drain this queue and restore a verified pre-activation backup, or recover at verified legacy depth 1 before changing speculation depth")
				return s.block(ctx, w, queue.ConfigSource, "Default-branch configuration: "+w.configError.Error(), "")
			}
			// The serve cleanup default may have changed since the old policy.
			matches := selected.Digest(true) == state.ConfigDigest || selected.Digest(false) == state.ConfigDigest
			if pin := state.AuthorityPin(); !matches && pin != nil && pin.Authority == authority.OpenMerge && pin.EffectiveDigest == state.ConfigDigest && previousCandidate.Authority.Mode == authority.OpenMerge {
				binding := pin.OpenMerge.Binding
				policy := authority.Policy{Mode: authority.OpenMerge, BreakGlass: authority.ActorBinding{Declared: binding.DeclaredActor, Resolved: binding.ResolvedActor}}
				if previousCandidate.Authority.BreakGlass != nil && reflect.DeepEqual(*previousCandidate.Authority.BreakGlass, binding.DeclaredActor) {
					for _, cleanup := range []bool{true, false} {
						digest, err := authority.EffectiveDigest(authority.DigestInputs{LegacyDigest: selected.Digest(cleanup), Policy: policy, DefaultBranch: pin.DefaultBranch, MergeMethod: authority.MergeMethod(selected.MergeMethod), Selectors: previousCandidate.BaseSelectors()})
						matches = matches || err == nil && digest == state.ConfigDigest
					}
				}
			}
			if !matches {
				return fmt.Errorf("historical window policy does not match recorded digest")
			}
			if selected.QueueConfig().MaxDepth() > 1 {
				events = append(events, selected.QueueConfig().Applied(state.ConfigCommit, state.ConfigDigest))
			}
		}
		applied := w.cfg.QueueConfig().Applied(configTip, w.configDigest)
		if w.stageA != nil {
			applied.Policy = w.stageA.pin
			if applied.Policy != nil && applied.Window == nil {
				applied.Window = &queue.WindowBounds{MinDepth: 1, MaxDepth: 1}
			}
		}
		if activation != nil {
			for _, event := range events {
				activation.Historical = append(activation.Historical, event.(queue.ConfigApplied))
			}
			activation.Config = &applied
			_, err = s.activateAuthority(ctx, w, *activation)
		} else {
			events = append(events, applied)
			_, err = s.store.Append(ctx, w.id, events, "")
		}
		if err != nil {
			if activation != nil {
				w.configError = err
				w.configBranch, w.configTip = "", ""
				s.authorityConfigurationFailed(w)
			}
			return err
		}
	}
	// Retry failures even at the same tip. Failed metadata/ref/file reads must
	// pause admission and must never leave a previously valid policy usable.
	w.configFailure = nil
	w.configFailureRetryAt = time.Time{}
	if w.AuthorityRequired && w.resolveAuthorityLifecycle != nil && w.configCandidate.Authority.Mode == authority.OpenMerge && loadErr != nil && (errors.As(loadErr, &invalid) || errors.As(loadErr, &mutable) || errors.Is(loadErr, config.ErrNotFound)) {
		w.configFailure, w.configFailureBranch, w.configFailureTip = loadErr, branch, configTip
		if errors.As(loadErr, &mutable) {
			w.configFailureRetryAt = time.Now().Add(s.options.RulesInterval)
		}
	}
	w.configError = loadErr
	if loadErr == nil {
		w.configBranch, w.configTip = branch, configTip
	} else {
		w.configBranch, w.configTip = "", ""
	}
	if w.configError != nil {
		return s.block(ctx, w, queue.ConfigSource, "Default-branch configuration: "+w.configError.Error(), "")
	} else if basis.SourceBlocked(queue.ConfigSource) {
		_, err = s.store.Append(ctx, w.id, []queue.Event{queue.BlockSourceCleared{Source: queue.ConfigSource, Basis: basis.BlockSourceRevision(queue.ConfigSource)}}, "")
		if err == nil {
			s.logCommitted(ctx, w)
		}
	}
	return err
}

func stageADefiniteIdentityFailure(err error) bool {
	return errors.Is(err, forge.ErrIdentityUnavailable) || errors.Is(err, forge.ErrIdentityMismatch)
}

// Discovery acquisition is separate from the fresh reads at activation and
// admission. Reuse only the same immutable policy and current binding set.
type discoveryAcquisition struct {
	branch, tip, bindings string
	cfg                   config.Config
	err                   error
	acquiredAt            time.Time
}

func (s *Server) discoveryBindings(target Target) (string, bool) {
	bindings := []string{}
	active := false
	for _, w := range s.workers() {
		if w.Key.Installation != target.Key.Installation || !strings.EqualFold(w.Key.Repository, target.Key.Repository) {
			continue
		}
		if pin := w.authorityDiscoveryPin.Load(); pin != nil {
			data, _ := json.Marshal(pin)
			bindings = append(bindings, string(data))
			active = true
		}
	}
	slices.Sort(bindings)
	data, _ := json.Marshal(bindings)
	return string(data), active
}

func (s *Server) repositoryConfig(ctx context.Context, target Target, branch, tip string, w *worker) (config.Config, error) {
	target = s.publicAuthorityTarget(target)
	key := target.Key.Installation + "/" + strings.ToLower(target.Key.Repository)
	bindings, _ := s.discoveryBindings(target)
	s.mu.RLock()
	cached, ok := s.discoveryAcquisitions[key]
	s.mu.RUnlock()
	if ok && cached.branch == branch && cached.tip == tip && cached.bindings == bindings && (durableGoverningFailure(cached.err) || time.Now().Before(cached.acquiredAt.Add(s.options.RulesInterval))) {
		return cached.cfg, cached.err
	}
	candidate, err := s.repositoryCandidate(ctx, target, tip, w)
	if err != nil || candidate.Authority.Mode != authority.OpenMerge {
		_, active := s.discoveryBindings(target)
		var invalid config.Errors
		if active && (errors.As(err, &invalid) || errors.Is(err, config.ErrNotFound)) {
			s.rememberDiscoveryAcquisition(key, discoveryAcquisition{branch: branch, tip: tip, bindings: bindings, cfg: candidate.Config, err: err})
		}
		return candidate.Config, err
	}
	err = acquireAuthorityGoverning(ctx, target, candidate, branch, tip)
	if err == nil {
		resolved, resolveErr := target.resolveAuthorityLifecycle(ctx, candidate, branch, tip)
		if resolveErr != nil {
			err = &authorityObservationReadFailure{err: resolveErr}
		} else if resolved.Activation.Read == nil {
			err = errors.New("authority governing protection reader unavailable")
		} else {
			evidence, readErr := resolved.Activation.Read(ctx)
			if readErr != nil {
				err = &authorityObservationReadFailure{err: readErr}
			} else {
				err = forge.ValidateAuthorityAcquisition(evidence)
			}
		}
	}
	var invalid config.Errors
	var mutable *config.GoverningIdentityError
	if err == nil || errors.As(err, &invalid) || errors.As(err, &mutable) {
		s.rememberDiscoveryAcquisition(key, discoveryAcquisition{branch: branch, tip: tip, bindings: bindings, cfg: candidate.Config, err: err})
	}
	return candidate.Config, err
}

func (s *Server) rememberDiscoveryAcquisition(key string, entry discoveryAcquisition) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.discoveryAcquisitions == nil {
		s.discoveryAcquisitions = map[string]discoveryAcquisition{}
	}
	entry.acquiredAt = time.Now()
	s.discoveryAcquisitions[key] = entry
}

func (s *Server) repositoryCandidate(ctx context.Context, target Target, tip string, w *worker) (config.AuthorityCandidate, error) {
	authorityLanded := false
	if target.AuthorityRequired && target.resolveAuthorityLifecycle != nil {
		if w != nil {
			authorityLanded = w.configCandidate.Authority.Mode == authority.OpenMerge
		} else {
			_, authorityLanded = s.discoveryBindings(target)
		}
	}
	_, data, err := config.Find(func(path string) ([]byte, bool, error) {
		data, err := target.Driver.ReadFile(ctx, forge.RepoRef{Path: target.Key.Repository}, tip, path)
		if w != nil && err == nil {
			w.publishProgress(time.Now())
		}
		if errors.Is(err, forge.ErrFileNotFound) {
			return nil, false, nil
		}
		return data, err == nil, err
	})
	if err != nil {
		if authorityLanded && !errors.Is(err, config.ErrNotFound) {
			if errors.Is(err, config.ErrMultipleFound) {
				return config.AuthorityCandidate{}, config.Errors{{NoLine: true, Path: "configuration", Message: err.Error()}}
			}
			return config.AuthorityCandidate{}, &config.GoverningReadError{Boundary: "config", Cause: err}
		}
		return config.AuthorityCandidate{}, err
	}
	var candidate config.AuthorityCandidate
	var warnings []config.Diagnostic
	if target.publicAuthority {
		// Keep original bytes and line numbers for opted-in GitHub workers. Only
		// an actual OpenMerge declaration selects the authority candidate parser.
		candidate.Config, warnings, err = config.Load(data)
		if err == nil && candidate.Config.PolicyAuthority == authority.OpenMerge {
			candidate, warnings, err = config.LoadAuthorityCandidate(data)
		}
	} else if target.AuthorityRequired && target.resolveAuthorityLifecycle != nil {
		candidate, warnings, err = config.LoadAuthorityCandidate(data)
	} else {
		candidate.Config, warnings, err = config.Load(data)
		if err == nil && candidate.Config.PolicyAuthority == authority.OpenMerge {
			return config.AuthorityCandidate{}, config.Errors{{Path: "policy_authority", Message: "authority production opt-in required"}}
		}
	}
	if err != nil {
		if authorityLanded {
			return config.AuthorityCandidate{}, config.SanitizedAuthorityErrors(err)
		}
		return config.AuthorityCandidate{}, err
	}
	for _, warning := range warnings {
		s.options.Logger.Warn("repository configuration", "repository", target.Key.Repository, "diagnostic", warning.Error())
	}
	return candidate, nil
}

func (s *Server) loadConfiguration(ctx context.Context, w *worker, branch, tip string) error {
	candidate, err := s.repositoryCandidate(ctx, w.Target, tip, w)
	cfg := candidate.Config
	if err != nil {
		return err
	}
	if candidate.Authority.Mode == authority.OpenMerge {
		if err := acquireAuthorityGoverning(ctx, w.Target, candidate, branch, tip); err != nil {
			return err
		}
	}
	if w.managed && len(matchingQueues(cfg, w.Key.BaseBranch, branch)) == 0 {
		if err := s.lifecycle(ctx, w, "retired", "Queue removed or base selector narrowed"); err != nil {
			return err
		}
	}
	selected, diagnostics := cfg.ResolveBranch(w.Key.BaseBranch, branch)
	if len(diagnostics) != 0 {
		return diagnostics
	}
	if w.CheckProtection != nil && w.cfg.MergeMethod != selected.MergeMethod {
		w.rulesNextCheck = time.Time{}
	}
	w.stageA = nil
	requireOwners := w.AuthorityRequired && w.resolveAuthorityLifecycle != nil && candidate.Authority.Mode == authority.OpenMerge
	if selected.Approvals != nil || requireOwners {
		for _, candidate := range cfg.Policy.Queues {
			if candidate.Name == selected.Name {
				w.stageA = &stageAPolicy{repository: cfg.Policy, candidate: candidate, defaultBranch: branch, policy: cp.Policy{RequirePolicyOwners: requireOwners}}
				break
			}
		}
		if w.stageA == nil && requireOwners && len(cfg.Policy.Queues) == 0 {
			w.stageA = &stageAPolicy{repository: cfg.Policy, candidate: cp.CandidateQueue{Name: selected.Name}, defaultBranch: branch, policy: cp.Policy{RequirePolicyOwners: true}}
		}
		if w.stageA == nil {
			return config.Errors{{Path: "queues", Message: "approval queue has no parsed policy candidate"}}
		}
	}
	w.configCandidate = candidate
	w.cfg = selected
	w.deleteHeadBranch = cfg.DeleteHeadBranch
	deleteHeadBranch := !s.options.DisableDeleteHeadBranch
	if cfg.DeleteHeadBranch != nil {
		deleteHeadBranch = *cfg.DeleteHeadBranch
	}
	digest := selected.Digest(deleteHeadBranch)
	if digest != w.configDigest && (!w.AuthorityRequired || w.resolveAuthorityLifecycle == nil) {
		// Authority lifecycle invalidates against the final folded digest below.
		// Ordinary GitHub keeps its existing invalidation behavior.
		w.summaries = make(map[int]forge.PullRequestSummary)
		if w.managed {
			w.ready = false
			select {
			case s.refresh <- struct{}{}:
			default:
			}
		}
	}
	w.configDigest = digest
	w.publishApprovalStatus(s.options.PollInterval)
	return nil
}

func (s *Server) reconcile(ctx context.Context, w *worker, force bool) error {
	if err := s.configuration(ctx, w); err != nil {
		return err
	}
	if err := s.observeWorkerAuthority(ctx, w); err != nil {
		return err
	}
	// An authority worker must not classify or acquire policy inputs through
	// the last good config after a definite landed-config failure. Settlement
	// remains owned by cycle/execute; github retains its existing path.
	if w.AuthorityRequired && w.resolveAuthorityLifecycle != nil && w.configCandidate.Authority.Mode == authority.OpenMerge && w.configError != nil {
		return s.report(ctx, w)
	}
	if w.managed {
		state, _, err := s.store.Load(ctx, w.id)
		if err != nil {
			return err
		}
		if w.configError != nil || !w.ready || state.LifecycleState() != "active" {
			return s.report(ctx, w)
		}
	}
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	if err := s.probeApprovalInputs(ctx, w, state); err != nil {
		return err
	}
	force = force || state.SourceBlocked(queue.CheckSources)
	summaries, err := w.Driver.ListOpenPullRequests(ctx, w.repo(), w.Key.BaseBranch)
	if err == nil {
		w.publishProgress(time.Now())
	}
	if err != nil {
		return err
	}
	open := make(map[int]bool)
	for _, summary := range summaries {
		if err := s.quota(); err != nil {
			return err
		}
		open[summary.Number] = true
		old, known := w.summaries[summary.Number]
		needsConflict := slices.Contains(w.cfg.EnterWhen.Attributes(), "conflict") || slices.Contains(w.cfg.MergeWhen.Attributes(), "conflict")
		_, held := state.HeldRequest(summary.Number)
		if w.stageA == nil && !held && !force && known && old.HeadSHA == summary.HeadSHA && old.UpdatedAt.Equal(summary.UpdatedAt) && (!needsConflict || w.prs[summary.Number].ConflictKnown) {
			continue
		}
		if _, err := s.observe(ctx, w, summary.Number); err != nil {
			return err
		}
		if err := s.commands(ctx, w, summary.Number); err != nil {
			return err
		}
		w.summaries[summary.Number] = summary
		w.publishProgress(time.Now())
	}
	state, _, err = s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	closed := make(map[int]bool)
	for n := range w.prs {
		if !open[n] {
			closed[n] = true
		}
	}
	for _, entry := range state.Entries {
		if !open[entry.PR] {
			closed[entry.PR] = true
		}
	}
	for _, held := range state.HeldRequests() {
		if !open[held.PR] {
			closed[held.PR] = true
		}
	}
	for n := range closed {
		if err := s.record(ctx, w, fmt.Sprintf("pr/%d", n), "closed", queue.PRClosed{PR: n}); err != nil {
			return err
		}
		delete(w.prs, n)
		delete(w.summaries, n)
		w.publishProgress(time.Now())
	}
	if err := s.recheckCheckSources(ctx, w); err != nil {
		return err
	}
	return s.report(ctx, w)
}

func (s *Server) observe(ctx context.Context, w *worker, number int) (forge.PullRequest, error) {
	return s.observeWithClassification(ctx, w, number, true)
}

func (s *Server) observeWithClassification(ctx context.Context, w *worker, number int, refreshClassification bool) (forge.PullRequest, error) {
	read := w.Driver.PullRequest
	needsFiles := slices.Contains(w.cfg.EnterWhen.Attributes(), "files") || slices.Contains(w.cfg.MergeWhen.Attributes(), "files")
	if metadata, ok := w.Driver.(forge.PullRequestMetadataReader); ok && !needsFiles {
		read = metadata.PullRequestMetadata
	}
	observedAt := time.Now().UTC()
	if w.stageA != nil {
		id, err := w.Key.ChangeID(number)
		if err != nil {
			return forge.PullRequest{}, err
		}
		acquisition, err := s.store.BeginApprovalRead(ctx, w.id, []queue.ChangeID{id})
		if err != nil {
			return forge.PullRequest{}, err
		}
		ctx = acquisition.Context(ctx)
		observedAt = time.Now().UTC()
	}
	pr, err := read(ctx, w.repo(), number)
	if err == nil {
		w.publishProgress(time.Now())
	}
	if err != nil {
		return pr, err
	}
	if w.stageA == nil && !pr.HeadInBaseRepo {
		for i, approval := range pr.Approvals {
			if approval.SHA != pr.HeadSHA {
				continue
			}
			allowed, err := w.Driver.CanWrite(ctx, w.repo(), approval.User)
			if err == nil {
				w.publishProgress(time.Now())
			}
			if err != nil {
				return pr, err
			}
			pr.Approvals[i].CanWrite = allowed
		}
	}
	if w.stageA != nil {
		if err := s.observeStageA(ctx, w, pr, refreshClassification, observedAt); err != nil {
			return pr, err
		}
		if !pr.HeadInBaseRepo {
			pr.Approvals = nil
			state, _, err := s.store.Load(ctx, w.id)
			if err != nil {
				return pr, err
			}
			r, _ := state.PRRevision(number)
			fresh, err := s.readStageAInputs(ctx, w, state, approvalBasis(r))
			if err != nil {
				return pr, err
			}
			facts, _ := state.ApprovalFacts(r.ChangeID)
			for id, key := range facts.Latest {
				source := facts.Sources[key]
				if source.Revision == r.Revision && source.Event.Stance.Head == pr.HeadSHA && source.Event.Stance.Kind == cp.Approve && source.Event.Stance.Source.Channel == cp.NativeReview && fresh.Actors[id].Grant.Permits(id) == cp.True {
					pr.Approvals = append(pr.Approvals, forge.Approval{User: source.Event.Reviewer.Login, SHA: pr.HeadSHA, CanWrite: true})
				}
			}
		}
	}
	w.prs[number] = pr
	eligible, pending, err := s.entryEligibility(ctx, w, pr)
	if err != nil {
		return pr, err
	}
	observation := queue.PRObserved{PR: number, HeadSHA: pr.HeadSHA, Eligible: eligible, Trusted: trusted(pr), PendingConditions: pending}
	metadata := observedMetadata(pr)
	events := []queue.Event{metadata, observation}
	if pr.Closed || pr.Base != w.Key.BaseBranch {
		events = append(events, queue.PRClosed{PR: number})
	}
	err = s.record(ctx, w, fmt.Sprintf("pr/%d", number), struct {
		Metadata queue.PRMetadataObserved
		Decision queue.PRObserved
	}{metadata, observation}, events...)
	return pr, err
}

func observedMetadata(pr forge.PullRequest) queue.PRMetadataObserved {
	metadata := queue.PRMetadataObserved{
		PR: pr.Number, HeadSHA: pr.HeadSHA, Base: pr.Base, Head: pr.HeadBranch, Title: pr.Title, Author: pr.Author, Draft: pr.Draft,
		ApprovedReviewsBy: []string{}, Labels: []string{}, Files: []string{},
		LabelsKnown: pr.LabelsKnown, FilesKnown: pr.FilesKnown, ConflictKnown: pr.ConflictKnown, Conflict: pr.Conflict,
	}
	for _, approval := range pr.Approvals {
		if approval.SHA == pr.HeadSHA {
			metadata.ApprovedReviewsBy = append(metadata.ApprovedReviewsBy, approval.User)
		}
	}
	if pr.LabelsKnown {
		metadata.Labels = append(metadata.Labels, pr.Labels...)
	}
	if pr.FilesKnown {
		metadata.Files = append(metadata.Files, pr.Files...)
	}
	return metadata
}

func trusted(pr forge.PullRequest) bool {
	if pr.Closed {
		return false
	}
	if pr.HeadInBaseRepo {
		return true
	}
	return slices.ContainsFunc(pr.Approvals, func(a forge.Approval) bool { return a.SHA == pr.HeadSHA && a.CanWrite })
}

func (s *Server) commands(ctx context.Context, w *worker, number int) error {
	if w.stageA != nil {
		return s.stageACommands(ctx, w, number)
	}
	key := fmt.Sprintf("cursor/%d", number)
	raw, err := s.store.Observation(ctx, w.id, key)
	if err != nil {
		return err
	}
	var since time.Time
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &since); err != nil {
			return err
		}
	}
	comments, err := w.Driver.ListComments(ctx, w.repo(), number, since)
	if err == nil {
		w.publishProgress(time.Now())
	}
	if err != nil {
		return err
	}
	slices.SortStableFunc(comments, func(a, b forge.Comment) int {
		if at := a.CreatedAt.Compare(b.CreatedAt); at != 0 {
			return at
		}
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	for _, c := range comments {
		stop, err := s.handleLegacyCommand(ctx, w, number, c)
		if err != nil {
			return err
		}
		w.publishProgress(time.Now())
		if stop {
			return nil
		}
		if c.CreatedAt.After(since) {
			since = c.CreatedAt
		}
	}
	return s.record(ctx, w, key, since)
}

type queueReceipt struct {
	QueuedSHA string
}

func (s *Server) replyQueued(ctx context.Context, w *worker, number int, receipt string) error {
	raw, err := s.store.Observation(ctx, w.id, receipt)
	if err != nil || len(raw) == 0 || raw[0] != '{' {
		return err
	}
	var outcome queueReceipt
	if err := json.Unmarshal(raw, &outcome); err != nil {
		return err
	}
	if outcome.QueuedSHA == "" {
		return nil
	}
	return w.Driver.Comment(ctx, w.repo(), number, receipt+"/queued", "Queue request accepted for exact SHA "+outcome.QueuedSHA+"; entry conditions and the fresh trust gate still apply.")
}

// Failed authority acquisition must be visible to discovery as well as pushes.
func (s *Server) authorityConfigurationFailed(w *worker) {
	w.ready = false
	w.waiting = "authority configuration could not be resolved; discovery will retry"
	select {
	case s.refresh <- struct{}{}:
	default:
	}
}

func acquireAuthorityGoverning(ctx context.Context, target Target, candidate config.AuthorityCandidate, branch, tip string) error {
	files, _ := target.Git.(forge.GoverningFilesReader)
	ids, _ := target.Driver.(forge.IdentityReader)
	return candidate.AcquireGoverning(ctx, forge.RepoRef{Path: target.Key.Repository}, branch, tip, files, ids)
}

func durableGoverningFailure(err error) bool {
	var mutable *config.GoverningIdentityError
	var invalid config.Errors
	return !errors.As(err, &mutable) && (errors.As(err, &invalid) || errors.Is(err, config.ErrNotFound))
}
