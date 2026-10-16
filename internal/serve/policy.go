package serve

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func supported(cfg config.Queue) error {
	for _, expression := range []condition.Expression{cfg.EnterWhen, cfg.MergeWhen} {
		for _, name := range expression.Attributes() {
			switch name {
			case "base", "draft", "author", "approved-reviews-by", "check-success", "check-failure", "check-pending":
			default:
				return fmt.Errorf("serve cannot yet observe attribute %q; remove it or wait for the metadata driver extension", name)
			}
		}
	}
	return nil
}

func snapshot(pr forge.PullRequest) condition.Snapshot {
	var approvals []string
	for _, a := range pr.Approvals {
		if a.SHA == pr.HeadSHA {
			approvals = append(approvals, a.User)
		}
	}
	return condition.Snapshot{"base": condition.String(pr.Base), "draft": condition.Bool(pr.Draft), "author": condition.String(pr.Author), "approved-reviews-by": condition.Strings(approvals...)}
}

func hasChecks(expression condition.Expression) bool {
	return slices.ContainsFunc(expression.Attributes(), func(a string) bool { return strings.HasPrefix(a, "check-") })
}

// Check lists are built only after producer identity is checked. In particular,
// NOT must never turn an ambiguous producer's missing success into permission.
func checkSnapshot(expression condition.Expression, checks []forge.CheckRun) (condition.Snapshot, error) {
	sources := make(map[string]map[string]bool)
	latest := make(map[string]forge.CheckRun)
	for _, c := range checks {
		if sources[c.Name] == nil {
			sources[c.Name] = make(map[string]bool)
		}
		sources[c.Name][c.Source] = true
		if old, exists := latest[c.Name]; !exists || c.ID >= old.ID {
			latest[c.Name] = c
		}
	}
	names := make([]string, 0, len(latest))
	for name := range latest {
		names = append(names, name)
	}
	slices.Sort(names)
	lists := map[string][]string{"check-success": {}, "check-failure": {}, "check-pending": {}}
	for _, name := range names {
		if len(sources[name]) > 1 || sources[name][""] {
			if expression.ReferencesCheck(name) {
				var producers []string
				for source := range sources[name] {
					producers = append(producers, source)
				}
				slices.Sort(producers)
				return nil, &executor.CheckSourceAmbiguityError{Name: name, Sources: producers}
			}
			continue
		}
		attribute := "check-pending"
		switch latest[name].State {
		case forge.Success:
			attribute = "check-success"
		case forge.Failure, forge.Cancelled, forge.Neutral, forge.Skipped:
			attribute = "check-failure"
		}
		lists[attribute] = append(lists[attribute], name)
	}
	return condition.CheckResults(lists["check-success"], lists["check-failure"], lists["check-pending"]), nil
}

func policyValues(expression condition.Expression, pr forge.PullRequest, checks []forge.CheckRun) (condition.Snapshot, error) {
	values := snapshot(pr)
	if hasChecks(expression) {
		ci, err := checkSnapshot(expression, checks)
		if err != nil {
			return nil, err
		}
		for name, value := range ci {
			values[name] = value
		}
	}
	return values, nil
}

func evaluate(expression condition.Expression, pr forge.PullRequest, checks []forge.CheckRun) (bool, error) {
	_, success, err := mergeResult(expression, pr, checks)
	return success, err
}

func mergeResult(expression condition.Expression, pr forge.PullRequest, checks []forge.CheckRun) (terminal, success bool, err error) {
	values, err := policyValues(expression, pr, checks)
	if err != nil {
		return false, false, err
	}
	success, _ = expression.Evaluate(values)
	definitelyFalse, _ := condition.Not(expression).Evaluate(values)
	return success || definitelyFalse, success, nil
}

func (s *Server) eligible(ctx context.Context, w *worker, pr forge.PullRequest) (bool, error) {
	if w.configError != nil || pr.Closed || pr.Base != w.Key.BaseBranch {
		return false, nil
	}
	var checks []forge.CheckRun
	if hasChecks(w.cfg.EnterWhen) {
		var err error
		checks, err = w.Driver.CheckRuns(ctx, w.repo(), pr.HeadSHA)
		if err != nil {
			return false, err
		}
	}
	met, err := evaluate(w.cfg.EnterWhen, pr, checks)
	if err != nil {
		return false, s.pause(ctx, w, "ambiguous-check-sources", err.Error())
	}
	return met, nil
}
