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
			if tested := s.attempts[attempt]; tested != "" {
				s.abandoned[tested] = attempt
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
	tip, err := s.forge.ReadRef(context.Background(), s.repo, "main")
	if err != nil {
		return err
	}
	if attempt := s.abandoned[tip]; attempt != "" {
		return fmt.Errorf("abandoned attempt %s became main tip %s", attempt, tip)
	}
	if err := s.observeAbandonments(); err != nil {
		return err
	}
	if attempt := s.abandoned[tip]; attempt != "" {
		return fmt.Errorf("abandoned attempt %s became main tip %s", attempt, tip)
	}
	if tip == s.mainSHA {
		return nil
	}
	added, err := s.firstParentRange(s.mainSHA, tip)
	if err != nil {
		return err
	}
	run := s.tests[tip]
	if run == nil || !run.done || !run.success {
		return fmt.Errorf("main is not green: first-parent commit %s has no passing completed CI run covering its landing", tip)
	}
	if run.base != s.mainSHA || !slices.Equal(added, run.covered) {
		return fmt.Errorf("main landing %s from %s differs from CI coverage based on %s", tip, s.mainSHA, run.base)
	}
	for _, sha := range added {
		if attempt := s.abandoned[sha]; attempt != "" {
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
		if !s.forge.ContainsPR(s.repo, tip, pinned.PR) {
			return fmt.Errorf("landed commit %s lacks tested PR %d content", tip, pinned.PR)
		}
		s.landed[pinned.PR] = tip
	}
	s.observed[tip] = &forgeLanding{base: s.mainSHA, tip: tip}
	s.mainSHA = tip
	return nil
}

func (s *simulation) verifyLanded(batch *queue.Batch, event queue.Landed) error {
	if err := s.checkInvariants(); err != nil {
		return err
	}
	landing := s.observed[batch.TestedSHA]
	tip, err := s.forge.ReadRef(context.Background(), s.repo, "main")
	if err != nil {
		return err
	}
	if landing == nil || landing.verified || landing.base != batch.BaseSHA || tip != batch.TestedSHA || event.NewBaseSHA != batch.TestedSHA {
		return fmt.Errorf("landed event for batch %s has no matching forge landing: expected %s -> tested %s, event tip %s, actual main tip %s", batch.ID, batch.BaseSHA, batch.TestedSHA, event.NewBaseSHA, tip)
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
	s.abandoned[batch.TestedSHA] = batch.LandingAttempt.ID
	return s.checkInvariants()
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
	return nil
}
