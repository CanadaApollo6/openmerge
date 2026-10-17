package sim

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

type forgeLanding struct {
	base, tip string
	verified  bool
}

// Observing each write preserves even tips overwritten within one executor
// action. By transitivity, retaining the last observed tip retains every older
// tip too; keep the full sequence as evidence of the observed main history.
func (s *simulation) observeMain() error {
	tip, err := s.forge.ReadRef(context.Background(), s.repo, "main")
	if err != nil {
		return err
	}
	previous := s.mainTips[len(s.mainTips)-1]
	if tip == previous {
		return nil
	}
	s.mainTips = append(s.mainTips, tip)
	retained, err := s.forge.IsAncestor(context.Background(), s.repo, previous, tip)
	if err != nil {
		return err
	}
	if !retained {
		return fmt.Errorf("main history lost observed tip %s after write to %s", previous, tip)
	}
	return nil
}

func (s *simulation) observeWrite() {
	if s.violation == nil {
		s.violation = s.checkInvariants()
	}
}

// firstParentRange returns exactly the commits introduced on main by a landing.
// A tested rebase tip covers its intermediate commits as well as the tip itself.
func (s *simulation) firstParentRange(base, tip string) ([]string, error) {
	var added []string
	for sha := tip; sha != base; {
		commit, err := s.forge.Commit(s.repo, sha)
		if err != nil {
			return nil, err
		}
		added = append(added, sha)
		if len(commit.Parents) == 0 {
			return nil, fmt.Errorf("main history no longer descends from previously verified tip %s", base)
		}
		sha = commit.Parents[0]
	}
	slices.Reverse(added)
	return added, nil
}

func (s *simulation) observeAbandonments() error {
	fence, _, _, err := s.forge.ReadFence(context.Background(), s.repo, "openmerge/fence/main")
	if err != nil {
		return err
	}
	added, err := s.firstParentRange(s.lastFence, fence)
	if err != nil {
		return fmt.Errorf("fence history: %w", err)
	}
	for _, sha := range added {
		commit, err := s.forge.Commit(s.repo, sha)
		if err != nil {
			return err
		}
		if attempt, ok := strings.CutPrefix(commit.Message, "openmerge fence abandon/"); ok {
			attempt, _, _ = strings.Cut(attempt, " ")
			if tested := s.attempts[attempt]; tested != "" {
				s.abandoned[tested] = attempt
				s.abandonedAttempts[attempt] = true
			}
		}
	}
	s.lastFence = fence
	return nil
}

func (s *simulation) checkInvariants() error {
	if s.violation != nil {
		return s.violation
	}
	if err := s.observeMain(); err != nil {
		return err
	}
	tip, err := s.forge.ReadRef(context.Background(), s.repo, "main")
	if err != nil {
		return err
	}
	if err := s.checkAbandonedLanding(tip); err != nil {
		return err
	}
	if err := s.observeAbandonments(); err != nil {
		return err
	}
	// An independent actor can push the tested SHA without settling our
	// attempt. Only queue pushes count as landings of an abandoned attempt.
	if err := s.checkAbandonedLanding(tip); err != nil {
		return err
	}
	if tip == s.mainSHA {
		return nil
	}
	added, err := s.firstParentRange(s.mainSHA, tip)
	if err != nil {
		return err
	}
	run := s.tests[tip]
	if s.cfg.MergeMethod == queue.FastForward {
		_, _, tested, err := s.forge.ReadFence(context.Background(), s.repo, "openmerge/fence/main")
		if err != nil {
			return err
		}
		run = s.tests[tested]
		if run != nil {
			if err := s.forge.VerifyFastForward(context.Background(), s.repo, run.base, tested, tip); err != nil {
				return err
			}
		}
	}
	if run == nil || !run.done || !run.success {
		return fmt.Errorf("main is not green: first-parent commit %s has no passing completed CI run covering its landing", tip)
	}
	if run.base != s.mainSHA || !slices.Equal(added, run.covered) {
		return fmt.Errorf("main landing %s from %s differs from CI coverage based on %s", tip, s.mainSHA, run.base)
	}
	commit, err := s.forge.Commit(s.repo, tip)
	if err != nil {
		return err
	}
	if s.interactionFails(commit.PRs) {
		return fmt.Errorf("main contains an interacting failure on %s", tip)
	}
	for _, sha := range added {
		if attempt := s.abandoned[sha]; attempt != "" && s.cfg.MergeMethod != queue.FastForward {
			return fmt.Errorf("abandoned attempt %s became main tip %s", attempt, sha)
		}
		commit, err := s.forge.Commit(s.repo, sha)
		if err != nil {
			return err
		}
		for _, pr := range commit.PRs {
			if s.prs[pr].broken {
				return fmt.Errorf("main is not green: commit %s contains broken PR %d", sha, pr)
			}
		}
	}
	for _, pinned := range run.heads {
		if previous := s.landed[pinned.PR]; previous != "" {
			return fmt.Errorf("PR %d landed twice: %s and %s", pinned.PR, previous, tip)
		}
		pr, err := s.forge.PullRequest(context.Background(), s.repo, pinned.PR)
		if err != nil {
			return err
		}
		if pr.HeadSHA != pinned.HeadSHA {
			return fmt.Errorf("PR %d landed head %s differs from tested head %s on %s", pinned.PR, pr.HeadSHA, pinned.HeadSHA, tip)
		}
		if err := s.verifyPRContent(tip, pinned, run.method); err != nil {
			return err
		}
		s.landed[pinned.PR] = tip
	}
	s.observed[tip] = &forgeLanding{base: s.mainSHA, tip: tip}
	s.mainSHA = tip
	return nil
}

func (s *simulation) verifyPRContent(tip string, pr queue.BatchPR, method queue.MergeMethod) error {
	if !s.forge.ContainsPR(s.repo, tip, pr.PR) {
		return fmt.Errorf("landed commit %s lacks intended PR %d content", tip, pr.PR)
	}
	if method == queue.Merge || method == queue.FastForward {
		ancestor, err := s.forge.IsAncestor(context.Background(), s.repo, pr.HeadSHA, tip)
		if err != nil {
			return err
		}
		if !ancestor {
			return fmt.Errorf("landed merge %s lacks intended PR %d head %s ancestry", tip, pr.PR, pr.HeadSHA)
		}
	}
	return nil
}

func (s *simulation) verifyLanded(batch *queue.Batch, event queue.Landed) error {
	if err := s.checkInvariants(); err != nil {
		return err
	}
	landedSHA := batch.TestedSHA
	if head := batch.LandingAttempt.FastForwardHeadSHA; head != "" {
		landedSHA = head
	}
	landing := s.observed[landedSHA]
	tip, err := s.forge.ReadRef(context.Background(), s.repo, "main")
	if err != nil {
		return err
	}
	if landing == nil || landing.verified || landing.base != batch.BaseSHA || tip != landedSHA || event.NewBaseSHA != landedSHA {
		return fmt.Errorf("landed event for batch %s has no matching forge landing: expected %s -> tested %s, event tip %s, actual main tip %s", batch.ID, batch.BaseSHA, batch.TestedSHA, event.NewBaseSHA, tip)
	}
	run := s.tests[batch.TestedSHA]
	if run == nil || !slices.Equal(run.heads, batch.PRs) {
		return fmt.Errorf("CI membership for %s differs from intended batch PRs %v", batch.TestedSHA, batch.PRs)
	}
	for _, pr := range batch.PRs {
		if err := s.verifyPRContent(tip, pr, batch.Method); err != nil {
			return err
		}
	}
	fence, id, tested, err := s.forge.ReadFence(context.Background(), s.repo, "openmerge/fence/main")
	if err != nil {
		return err
	}
	if event.NewFence != fence || id != "land/"+batch.LandingAttempt.ID || tested != batch.TestedSHA {
		return fmt.Errorf("landed event for batch %s does not match forge fence: event %s, actual %s (%s tested=%s)", batch.ID, event.NewFence, fence, id, tested)
	}
	landing.verified = true
	return nil
}

func (s *simulation) verifyAbandoned(batch *queue.Batch, event queue.LandingAbandoned) error {
	fence, id, _, err := s.forge.ReadFence(context.Background(), s.repo, "openmerge/fence/main")
	if err != nil {
		return err
	}
	if event.NewFence != fence || id != "abandon/"+batch.LandingAttempt.ID {
		return fmt.Errorf("landing-abandoned event for batch %s without successful AdvanceFence: expected abandon/%s, event fence %s, actual %s (%s)", batch.ID, batch.LandingAttempt.ID, event.NewFence, fence, id)
	}
	if started, ok := s.disturbance[batch.LandingAttempt.ID]; ok && s.disturbances() == started && len(s.pendingFaults) == 0 {
		return fmt.Errorf("landing attempt %s for batch %s was abandoned with no injected fault or external base push since it started", batch.LandingAttempt.ID, batch.ID)
	}
	s.abandoned[batch.TestedSHA] = batch.LandingAttempt.ID
	s.abandonedAttempts[batch.LandingAttempt.ID] = true
	return s.checkInvariants()
}

// disturbances counts everything that can legitimately make a landing attempt
// fail: injected faults, delayed or duplicated pushes, and external base pushes.
// A landing abandoned with no new disturbance and no fault still pending
// recovery is a bug in the simulator's world, where main only moves forward.
func (s *simulation) disturbances() int {
	r := s.result
	return r.FaultsInjected + r.DelayedPushes + r.DuplicateExecutions + r.ExternalPushes
}

func (s *simulation) checkSettled() error {
	if err := s.checkInvariants(); err != nil {
		return err
	}
	for _, landing := range s.observed {
		if !landing.verified {
			return fmt.Errorf("forge landing %s -> %s has no matching Landed after recovery settled", landing.base, landing.tip)
		}
	}
	for pr := 1; pr < len(s.prs); pr++ {
		p := s.prs[pr]
		if !p.untrusted && !p.enqueued.IsZero() && p.landed.IsZero() && p.dequeued == "" {
			return fmt.Errorf("admitted PR %d silently disappeared without landing or dequeue", pr)
		}
		if !p.landed.IsZero() {
			status := s.forge.Statuses(s.repo, pr)[p.head]
			if status.State != "merged" {
				return fmt.Errorf("landed PR %d lost its merged report", pr)
			}
		}
	}

	return nil
}

// Generated test commits identify one batch. Fast-forward lands the marker's
// parent, so its fence binds that head to the unique tested marker and attempt.
func (s *simulation) checkAbandonedLanding(tip string) error {
	if s.cfg.MergeMethod == queue.FastForward {
		_, id, tested, err := s.forge.ReadFence(context.Background(), s.repo, "openmerge/fence/main")
		if err != nil {
			return err
		}
		marker, err := s.forge.Commit(s.repo, tested)
		if err != nil && tested != "" {
			return err
		}
		if attempt, ok := strings.CutPrefix(id, "land/"); ok && len(marker.Parents) == 1 && marker.Parents[0] == tip && s.abandonedAttempts[attempt] {
			return fmt.Errorf("abandoned attempt %s became main tip %s", attempt, tip)
		}
		return nil
	}
	if attempt := s.abandoned[tip]; attempt != "" && (tip != s.mainSHA || s.observed[tip] != nil) {
		return fmt.Errorf("abandoned attempt %s became main tip %s", attempt, tip)
	}
	return nil
}
