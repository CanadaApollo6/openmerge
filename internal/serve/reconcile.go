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
	tip, err := w.Git.ReadRef(ctx, w.repo(), w.Key.BaseBranch)
	if err != nil {
		return err
	}
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	if state.BaseSHA != tip {
		if _, err := s.store.Append(ctx, w.id, []queue.Event{queue.BaseMoved{SHA: tip}}, ""); err != nil {
			return err
		}
	}
	if tip != w.configTip {
		_, data, loadErr := config.Find(func(path string) ([]byte, bool, error) {
			data, err := w.Driver.ReadFile(ctx, w.repo(), tip, path)
			if errors.Is(err, forge.ErrFileNotFound) {
				return nil, false, nil
			}
			return data, err == nil, err
		})
		if loadErr == nil {
			cfg, warnings, err := config.Load(data)
			loadErr = err
			if err == nil {
				if w.CheckProtection != nil && w.cfg.MergeMethod != cfg.Queues[0].MergeMethod {
					w.rulesNextCheck = time.Time{}
				}
				w.cfg = cfg.Queues[0]
				for _, warning := range warnings {
					s.options.Logger.Warn("repository configuration", "repository", w.Key.Repository, "diagnostic", warning.Error())
				}
			}
		}
		// Retry read failures too: permissions and network errors can recover at the
		// same tip. A failed read never leaves the previous policy usable.
		w.configError = loadErr
		if loadErr == nil {
			w.configTip = tip
		} else {
			w.configTip = ""
		}
	}
	state, _, err = s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	if w.configError != nil {
		return s.pause(ctx, w, "invalid-config", "Base-branch configuration: "+w.configError.Error())
	} else if state.PauseReason(queue.Config{}) == "invalid-config" {
		_, err = s.store.Append(ctx, w.id, []queue.Event{queue.ResumeRequested{}}, "")
		if err == nil {
			s.logCommitted(ctx, w)
		}
	}
	return err
}

func (s *Server) reconcile(ctx context.Context, w *worker, force bool) error {
	oldTip := w.configTip
	if err := s.configuration(ctx, w); err != nil {
		return err
	}
	force = force || oldTip != w.configTip
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
	state, _, err := s.store.Load(ctx, w.id)
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
	return s.report(ctx, w)
}

func (s *Server) observe(ctx context.Context, w *worker, number int) (forge.PullRequest, error) {
	pr, err := w.Driver.PullRequest(ctx, w.repo(), number)
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
				allowed, err := w.Driver.CanWrite(ctx, w.repo(), c.Author)
				if err != nil {
					return err
				}
				if !allowed {
					if err := w.Driver.Comment(ctx, w.repo(), number, receipt+"/unauthorized", "Only repository writers can use @openmerge commands."); err != nil {
						return err
					}
				} else {
					fields := strings.Fields(cmd)
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
						if w.configError == nil {
							events = append(events, queue.ResumeRequested{}, queue.RefreshRequested{})
						}
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
