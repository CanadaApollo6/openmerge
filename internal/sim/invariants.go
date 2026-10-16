package sim

import (
	"context"
	"fmt"
	"slices"
)

func (s *simulation) checkInvariants() error {
	if s.violation != nil {
		return s.violation
	}
	tip, err := s.forge.ReadRef(context.Background(), s.repo, "main")
	if err != nil {
		return err
	}
	if tip == s.mainSHA {
		return nil
	}
	// Only inspect newly reachable first-parent commits. Re-copying the entire
	// fake history and all its file snapshots after every action is cubic work.
	var added []string
	for sha := tip; sha != s.mainSHA; {
		commit, err := s.forge.Commit(s.repo, sha)
		if err != nil {
			return err
		}
		added = append(added, sha)
		if len(commit.Parents) == 0 {
			return fmt.Errorf("main history no longer descends from previously verified tip %s", s.mainSHA)
		}
		sha = commit.Parents[0]
	}
	slices.Reverse(added)
	for _, sha := range added {
		run := s.tests[sha]
		if run == nil || !run.done || !run.success {
			return fmt.Errorf("main is not green: first-parent commit %s has no passing completed CI run", sha)
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
		for _, pinned := range run.heads {
			if previous := s.landed[pinned.PR]; previous != "" {
				return fmt.Errorf("PR %d landed twice: %s and %s", pinned.PR, previous, sha)
			}
			pr, err := s.forge.PullRequest(context.Background(), s.repo, pinned.PR)
			if err != nil {
				return err
			}
			if pr.HeadSHA != pinned.HeadSHA {
				return fmt.Errorf("PR %d landed head %s differs from tested head %s on %s", pinned.PR, pr.HeadSHA, pinned.HeadSHA, sha)
			}
			if !s.forge.ContainsPR(s.repo, sha, pinned.PR) {
				return fmt.Errorf("landed commit %s lacks tested PR %d content", sha, pinned.PR)
			}
			s.landed[pinned.PR] = sha
		}
	}
	s.mainSHA = tip
	return nil
}
