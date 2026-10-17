package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

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
	if loadErr != nil && !errors.Is(loadErr, config.ErrNotFound) && !errors.As(loadErr, &invalid) {
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
		if _, err := s.store.Append(ctx, w.id, []queue.Event{queue.BaseMoved{SHA: tip}}, ""); err != nil {
			return err
		}
		// Conflict observations can change when this base moves even if the
		// default branch (and therefore the policy) stays at the same commit.
		w.summaries = make(map[int]forge.PullRequestSummary)
	}
	if loadErr == nil && (state.ConfigDigest != w.configDigest || w.cfg.QueueConfig().MaxDepth() > 1 && !state.WindowInitialized()) {
		// Publish the policy before using it. Replay invalidates work built under
		// another digest, and start gates reject workers holding an older policy.
		_, err = s.store.Append(ctx, w.id, []queue.Event{w.cfg.QueueConfig().Applied(configTip, w.configDigest)}, "")
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
		if !force && known && old.HeadSHA == summary.HeadSHA && old.UpdatedAt.Equal(summary.UpdatedAt) && (!needsConflict || w.prs[summary.Number].ConflictKnown) {
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
	read := w.Driver.PullRequest
	needsFiles := slices.Contains(w.cfg.EnterWhen.Attributes(), "files") || slices.Contains(w.cfg.MergeWhen.Attributes(), "files")
	if metadata, ok := w.Driver.(forge.PullRequestMetadataReader); ok && !needsFiles {
		read = metadata.PullRequestMetadata
	}
	pr, err := read(ctx, w.repo(), number)
	if err != nil {
		return pr, err
	}
	if !pr.HeadInBaseRepo {
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
		if err := s.quota(); err != nil {
			return err
		}
		receipt := fmt.Sprintf("comment/%d", c.ID)
		seen, err := s.store.Observation(ctx, w.id, receipt)
		if err != nil {
			return err
		}
		if len(seen) == 0 {
			var events []queue.Event
			var outcome any = true
			if cmd := command(c.Body); cmd != "" && c.Author != w.BotLogin {
				fields := strings.Fields(cmd)
				if fields[0] == "queue" && w.managed && !w.ready {
					return s.ignoreWaitingCommand(ctx, w, number, c)
				}
				allowed, err := w.Driver.CanWrite(ctx, w.repo(), c.Author)
				if err != nil {
					return err
				}
				if !allowed {
					if err := w.Driver.Comment(ctx, w.repo(), number, receipt+"/unauthorized", "Only repository writers can use @openmerge commands."); err != nil {
						return err
					}
				} else {
					switch fields[0] {
					case "queue":
						// Trust must be freshly read even if UpdatedAt did not change.
						pr, err := s.observe(ctx, w, number)
						if err != nil {
							return err
						}
						if len(fields) == 2 && !strings.EqualFold(pr.HeadSHA, fields[1]) {
							if err := w.Driver.Comment(ctx, w.repo(), number, receipt+"/head-changed", "The SHA does not match the current head "+pr.HeadSHA+". Please re-issue @openmerge queue with the intended SHA."); err != nil {
								return err
							}
						} else {
							events = append(events, queue.EnqueueRequested{PR: number, At: c.CreatedAt})
							outcome = queueReceipt{QueuedSHA: pr.HeadSHA}
						}
					case "dequeue":
						events = append(events, queue.DequeueRequested{PR: number})
					case "refresh":
						if _, err := s.observe(ctx, w, number); err != nil {
							return err
						}
						clears, err := s.operatorClears(ctx, w)
						if err != nil {
							return err
						}
						events = append(events, clears...)
						events = append(events, queue.RefreshRequested{})
					}
				}
			}
			if err := s.record(ctx, w, receipt, outcome, events...); err != nil {
				return err
			}
		}
		// A committed receipt also repairs a crash before the acceptance reply.
		// Its immutable command outcome pins the exact SHA, even after a push.
		if err := s.replyQueued(ctx, w, number, receipt); err != nil {
			return err
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
