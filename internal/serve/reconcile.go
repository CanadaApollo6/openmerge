package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

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
	return s.store.Record(ctx, w.id, key, data, events...)
}

func (s *Server) configuration(ctx context.Context, w *worker) error {
	basis, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	branch, loadErr := w.Driver.DefaultBranch(ctx, w.repo())
	if loadErr == nil && branch == "" {
		loadErr = errors.New("repository default branch is missing")
	}
	tip, baseErr := w.Git.ReadRef(ctx, w.repo(), w.Key.BaseBranch)
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
		}
	}
	if loadErr == nil && (configTip != w.configTip || branch != w.configBranch) {
		loadErr = s.loadConfiguration(ctx, w, branch, configTip)
	}
	// Read failures stop this cycle in memory, without writing block/clear or
	// base/config observations. A parsed invalid file or proven deletion is a
	// definite condition and can be recorded independently.
	var invalid config.Errors
	if loadErr != nil && !errors.Is(loadErr, config.ErrNotFound) && !errors.As(loadErr, &invalid) && (w.stageA == nil || !stageADefiniteIdentityFailure(loadErr)) {
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
	}
	// Identity absence/mismatch is a proved policy failure and must be durable.
	// Transport, rate-limit and incomplete-membership errors abort entirely: a
	// stale in-memory policy is never used, but neither is an uncertain result
	// written as a durable configuration failure.
	if loadErr != nil && stageARefresh && !errors.As(loadErr, &invalid) && !stageADefiniteIdentityFailure(loadErr) {
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
	if loadErr == nil && (state.ConfigDigest != w.configDigest || w.cfg.QueueConfig().MaxDepth() > 1 && !state.WindowInitialized()) {
		// Publish the policy before using it. Replay invalidates work built under
		// another digest, and start gates reject workers holding an older policy.
		events := []queue.Event{}
		if state.ConfigDigest != "" && state.ConfigDigest != w.configDigest && !state.WindowInitialized() && w.cfg.QueueConfig().MaxDepth() > 1 {
			// Old journals omit bounds. Recover the fixed window from the policy
			// that actually ran before clamping it under the replacement policy.
			previous, readErr := s.repositoryConfig(ctx, w.Target, state.ConfigCommit)
			if readErr != nil {
				return fmt.Errorf("read historical window policy: %w", readErr)
			}
			selected, diagnostics := previous.ResolveBranch(w.Key.BaseBranch, branch)
			if len(diagnostics) != 0 {
				return diagnostics
			}
			if selected.Approvals != nil {
				w.configError = fmt.Errorf("historical approval policy has a folded digest without recorded window bounds; drain this queue and restore a verified pre-activation backup, or recover at verified legacy depth 1 before changing speculation depth")
				return s.block(ctx, w, queue.ConfigSource, "Default-branch configuration: "+w.configError.Error(), "")
			}
			// The serve cleanup default may have changed since the old policy.
			if selected.Digest(true) != state.ConfigDigest && selected.Digest(false) != state.ConfigDigest {
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
		events = append(events, applied)
		_, err = s.store.Append(ctx, w.id, events, "")
		if err != nil {
			return err
		}
	}
	// Retry failures even at the same tip. Failed metadata/ref/file reads must
	// pause admission and must never leave a previously valid policy usable.
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

func (s *Server) repositoryConfig(ctx context.Context, target Target, tip string) (config.Config, error) {
	_, data, err := config.Find(func(path string) ([]byte, bool, error) {
		data, err := target.Driver.ReadFile(ctx, forge.RepoRef{Path: target.Key.Repository}, tip, path)
		if errors.Is(err, forge.ErrFileNotFound) {
			return nil, false, nil
		}
		return data, err == nil, err
	})
	if err != nil {
		return config.Config{}, err
	}
	cfg, warnings, err := config.Load(data)
	if err != nil {
		return config.Config{}, err
	}
	for _, warning := range warnings {
		s.options.Logger.Warn("repository configuration", "repository", target.Key.Repository, "diagnostic", warning.Error())
	}
	return cfg, nil
}

func (s *Server) loadConfiguration(ctx context.Context, w *worker, branch, tip string) error {
	cfg, err := s.repositoryConfig(ctx, w.Target, tip)
	if err != nil {
		return err
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
	if selected.Approvals != nil {
		for _, candidate := range cfg.Policy.Queues {
			if candidate.Name == selected.Name {
				w.stageA = &stageAPolicy{repository: cfg.Policy, candidate: candidate, defaultBranch: branch}
				break
			}
		}
		if w.stageA == nil {
			return config.Errors{{Path: "queues", Message: "approval queue has no parsed policy candidate"}}
		}
	}
	w.cfg = selected
	w.deleteHeadBranch = cfg.DeleteHeadBranch
	deleteHeadBranch := !s.options.DisableDeleteHeadBranch
	if cfg.DeleteHeadBranch != nil {
		deleteHeadBranch = *cfg.DeleteHeadBranch
	}
	digest := selected.Digest(deleteHeadBranch)
	if digest != w.configDigest {
		// Invalidate at the digest's only write site, including refreshes and
		// policies awaiting activation before the next reconciliation.
		w.summaries = make(map[int]forge.PullRequestSummary)
		if w.managed {
			w.ready = false
			select {
			case s.refresh <- struct{}{}:
			default:
			}
		}
		w.configDigest = digest
	}
	w.publishApprovalStatus(s.options.PollInterval)
	return nil
}

func (s *Server) reconcile(ctx context.Context, w *worker, force bool) error {
	if err := s.configuration(ctx, w); err != nil {
		return err
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
		if w.stageA == nil && !force && known && old.HeadSHA == summary.HeadSHA && old.UpdatedAt.Equal(summary.UpdatedAt) && (!needsConflict || w.prs[summary.Number].ConflictKnown) {
			continue
		}
		if _, err := s.observe(ctx, w, summary.Number); err != nil {
			return err
		}
		if err := s.commands(ctx, w, summary.Number); err != nil {
			return err
		}
		w.summaries[summary.Number] = summary
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
	for n := range closed {
		if err := s.record(ctx, w, fmt.Sprintf("pr/%d", n), "closed", queue.PRClosed{PR: n}); err != nil {
			return err
		}
		delete(w.prs, n)
		delete(w.summaries, n)
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
	if err != nil {
		return pr, err
	}
	if w.stageA == nil && !pr.HeadInBaseRepo {
		for i, approval := range pr.Approvals {
			if approval.SHA != pr.HeadSHA {
				continue
			}
			allowed, err := w.Driver.CanWrite(ctx, w.repo(), approval.User)
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
	eligible, err := s.eligible(ctx, w, pr)
	if err != nil {
		return pr, err
	}
	observation := queue.PRObserved{PR: number, HeadSHA: pr.HeadSHA, Eligible: eligible, Trusted: trusted(pr)}
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
