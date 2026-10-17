package serve

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func (s *Server) block(ctx context.Context, w *worker, source queue.BlockSource, detail, subject string) error {
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	// Every failure advances this source, invalidating overlapping clear reads.
	_, err = s.store.Append(ctx, w.id, []queue.Event{queue.BlockSourceBlocked{Source: source, Detail: detail, Subject: subject, Basis: state.BlockSourceRevision(source)}}, "")
	if err == nil {
		s.logCommitted(ctx, w)
	}
	return err
}

func (s *Server) operatorClears(ctx context.Context, w *worker) ([]queue.Event, error) {
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return nil, err
	}
	var events []queue.Event
	for _, source := range []queue.BlockSource{queue.Operator, queue.ActionPoison, queue.LandingNotOnBase} {
		if !state.SourceBlocked(source) {
			continue
		}
		if source == queue.LandingNotOnBase {
			// The failure's durable subject is the landed commit, including the
			// pinned head for fast-forward. Never clear on an unverified refresh.
			tip, err := w.Git.ReadRef(ctx, w.repo(), w.Key.BaseBranch)
			if err != nil {
				return nil, err
			}
			contains, err := w.Git.IsAncestor(ctx, w.repo(), state.BlockSourceSubject(source), tip)
			if err != nil {
				return nil, err
			}
			if !contains {
				continue
			}
		}
		events = append(events, queue.BlockSourceCleared{Source: source, Basis: state.BlockSourceRevision(source)})
	}
	return events, nil
}

// Re-read the entire source before clearing: one PR's unambiguous checks cannot
// erase another PR's ambiguity, nor can admission checks erase a batch block.
// These are observations, so they continue while the planner stops new work.
func (s *Server) recheckCheckSources(ctx context.Context, w *worker) error {
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil || !state.SourceBlocked(queue.CheckSources) || w.configError != nil {
		return err
	}
	read := func(sha string, batch bool) error {
		checks, err := w.Driver.CheckRuns(ctx, w.repo(), sha)
		if err != nil {
			return err
		}
		expression := w.cfg.EnterWhen
		if batch {
			expression = w.cfg.MergeWhen
			checks = slices.DeleteFunc(slices.Clone(checks), func(check forge.CheckRun) bool {
				return !strings.HasPrefix(check.Source, "status:") && check.HeadBranch != state.Active.TestRef
			})
		}
		_, err = checkSnapshot(expression, checks)
		return err
	}
	if hasChecks(w.cfg.EnterWhen) {
		for _, pr := range w.prs {
			if !pr.Closed && pr.Base == w.Key.BaseBranch {
				if err := read(pr.HeadSHA, false); err != nil {
					return s.checkSourceFailure(ctx, w, err)
				}
			}
		}
	}
	if state.Active != nil && state.Active.Phase == queue.Testing && hasChecks(w.cfg.MergeWhen) {
		if err := read(state.Active.TestedSHA, true); err != nil {
			return s.checkSourceFailure(ctx, w, err)
		}
	}
	_, err = s.store.Append(ctx, w.id, []queue.Event{queue.BlockSourceCleared{Source: queue.CheckSources, Basis: state.BlockSourceRevision(queue.CheckSources)}}, "")
	return err
}

func (s *Server) checkSourceFailure(ctx context.Context, w *worker, err error) error {
	var ambiguity *executor.CheckSourceAmbiguityError
	if errors.As(err, &ambiguity) {
		return s.block(ctx, w, queue.CheckSources, ambiguity.Error(), "")
	}
	return err
}
