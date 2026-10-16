package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func (s *Server) pause(ctx context.Context, w *worker, reason, detail string) error {
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	if state.PauseReason(w.plannerConfig(s.options.PollInterval)) == reason && state.PauseDetail(w.plannerConfig(s.options.PollInterval)) == detail {
		return nil
	}
	_, err = s.store.Append(ctx, w.id, []queue.Event{queue.PauseRequested{Reason: reason, Detail: detail}}, "")
	if err == nil {
		s.options.Logger.Warn("queue paused", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "reason", reason, "detail", detail)
	}
	return err
}

var errTrustGate = errors.New("PR no longer passes the exact-head trust gate")

func (s *Server) trustGate(ctx context.Context, w *worker, prs []queue.BatchPR) error {
	if err := s.configuration(ctx, w); err != nil {
		return err
	}
	if w.configError != nil {
		return errTrustGate
	}
	for _, pin := range prs {
		pr, err := s.observe(ctx, w, pin.PR)
		if err != nil {
			return err
		}
		if pr.HeadSHA != pin.HeadSHA || !trusted(pr) || pr.Closed || pr.Base != w.Key.BaseBranch {
			return errTrustGate
		}
	}
	return nil
}

func (s *Server) execute(ctx context.Context, w *worker, claim store.ClaimedAction) (result error) {
	// Completion survives shutdown cancellation. Uncertain writes are retried via
	// their durable idempotency keys or resolved through the landing fence.
	defer func() {
		completion := store.Done
		if result != nil {
			completion = store.Failed
		}
		persist, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.store.Complete(persist, claim, completion); err != nil && !errors.Is(err, store.ErrClaimLost) {
			result = errors.Join(result, err)
		}
	}()
	actionCtx, cancel := context.WithTimeout(ctx, s.options.ActionTimeout)
	defer cancel()
	switch action := claim.Action.(type) {
	case queue.BuildTestRef:
		if err := s.trustGate(actionCtx, w, action.PRs); err != nil && !errors.Is(err, errTrustGate) {
			return err
		}
	case queue.Land:
		if err := s.trustGate(actionCtx, w, action.PRs); err != nil && !errors.Is(err, errTrustGate) {
			return err
		}
	}
	cfg := w.plannerConfig(s.options.PollInterval)
	if store.Consequential(claim.Action) {
		proceed, err := s.store.BeginConsequential(actionCtx, claim, cfg, time.Now())
		if err != nil || !proceed {
			return err
		}
	}
	state, _, err := s.store.Load(actionCtx, w.id)
	if err != nil {
		return err
	}
	if !store.Consequential(claim.Action) {
		current := false
		for _, action := range queue.Plan(cfg, state, time.Now()) {
			if action.Key() == claim.Key {
				current = true
				break
			}
		}
		if !current {
			return nil
		}
	}
	x := executor.Executor{Driver: w.Driver, Git: w.Git, Repo: w.repo(), Base: w.Key.BaseBranch, AllowNoRequiredChecks: true}
	var finalChecks []forge.CheckRun
	x.CheckPolicy = func(checks []forge.CheckRun) (bool, bool, error) {
		finalChecks = checks
		if w.configError != nil {
			return false, false, errors.New("invalid repository configuration")
		}
		if state.Active == nil {
			return false, false, errors.New("no active batch for check policy")
		}
		for _, pin := range state.Active.PRs {
			met, err := evaluate(w.cfg.MergeWhen, w.prs[pin.PR], checks)
			if err != nil {
				return false, false, err
			}
			if !met {
				if failedCheck(w.cfg.MergeWhen, checks) {
					return true, false, nil
				}
				return false, false, nil
			}
		}
		return true, true, nil
	}
	x.BeforeLand = func(ctx context.Context, land queue.Land) error {
		if err := s.trustGate(ctx, w, land.PRs); err != nil {
			return err
		}
		for _, pin := range land.PRs {
			met, err := evaluate(w.cfg.MergeWhen, w.prs[pin.PR], finalChecks)
			if err != nil {
				return err
			}
			if !met {
				return executor.ErrChecksNotSuccessful
			}
		}
		return nil
	}
	err = x.Execute(actionCtx, claim.Action, s.store.Emit(actionCtx, claim, cfg))
	var absent *executor.ErrLandingNotOnBase
	var ambiguity *executor.CheckSourceAmbiguityError
	if errors.As(err, &absent) {
		if pauseErr := s.pause(actionCtx, w, "landing-not-on-base", absent.Error()); pauseErr != nil {
			return errors.Join(err, pauseErr)
		}
	} else if errors.As(err, &ambiguity) {
		w.blockedSHA, w.blockedDetail = state.Active.TestedSHA, ambiguity.Error()
		// Keep check refreshes running: ambiguity is a visible block, not a pause
		// that would prevent discovering a corrected CI producer configuration.
		if reportErr := s.reportBatch(actionCtx, w, state, forge.Status{State: forge.Blocked, Reason: "ambiguous-check-sources", Summary: ambiguity.Error()}); reportErr != nil {
			return errors.Join(err, reportErr)
		}
		return nil // refresh events committed; the next epoch re-reads CI
	}
	if err == nil {
		if _, refreshing := claim.Action.(queue.RefreshChecks); refreshing {
			w.blockedSHA, w.blockedDetail = "", ""
		}
	}
	if errors.Is(err, store.ErrStale) {
		return nil
	}
	if err != nil {
		_ = s.report(actionCtx, w)
	}
	return err
}

func (s *Server) status(ctx context.Context, w *worker, pr int, head string, status forge.Status) error {
	key := fmt.Sprintf("status/%d", pr)
	value := struct {
		Head   string
		Status forge.Status
	}{head, status}
	// The durable cache is updated only after the idempotent forge write succeeds.
	previous, err := s.store.Observation(ctx, w.id, key)
	if err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if string(previous) == string(data) {
		return nil
	}
	if err := w.Driver.ReportStatus(ctx, w.repo(), pr, head, status); err != nil {
		return err
	}
	return s.store.Record(ctx, w.id, key, data)
}

func (s *Server) reportBatch(ctx context.Context, w *worker, state queue.State, status forge.Status) error {
	if state.Active == nil {
		return nil
	}
	for _, pr := range state.Active.PRs {
		if err := s.status(ctx, w, pr.PR, pr.HeadSHA, status); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) report(ctx context.Context, w *worker) error {
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	reason := state.PauseReason(w.plannerConfig(s.options.PollInterval))
	if w.configError != nil {
		for n, pr := range w.prs {
			if err := s.status(ctx, w, n, pr.HeadSHA, forge.Status{State: forge.Paused, Reason: "invalid-config", Summary: w.configError.Error()}); err != nil {
				return err
			}
		}
		return nil
	}
	if reason != "" {
		status := forge.Status{State: forge.Paused, Reason: reason, Summary: state.PauseDetail(w.plannerConfig(s.options.PollInterval)) + "; a writer can @openmerge refresh after fixing the cause"}
		for n, pr := range w.prs {
			if pr.Closed {
				continue
			}
			if err := s.status(ctx, w, n, pr.HeadSHA, status); err != nil {
				return err
			}
		}
		return nil
	}
	for _, entry := range state.Entries {
		status := forge.Status{State: forge.Queued, Summary: "Waiting for a test batch"}
		if state.Active != nil {
			for _, pr := range state.Active.PRs {
				if pr.PR == entry.PR {
					status = forge.Status{State: forge.StatusState(state.Active.Phase), Summary: "Batch " + state.Active.ID}
				}
			}
			if status.State == "building" {
				status.State = forge.Testing
			}
		}
		if state.Active != nil && state.Active.TestedSHA == w.blockedSHA && w.blockedDetail != "" {
			status = forge.Status{State: forge.Blocked, Reason: "ambiguous-check-sources", Summary: w.blockedDetail}
		}
		if err := s.status(ctx, w, entry.PR, entry.HeadSHA, status); err != nil {
			return err
		}
	}
	return nil
}
