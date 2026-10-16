package github

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	gh "github.com/google/go-github/v93/github"
)

// LinearHistoryRequired reads classic protection and the effective active
// rules for the exact base, including inherited rulesets. App bypass does not
// change the requested history shape. Unknown policy preserves build behavior.
func (d *Driver) LinearHistoryRequired(ctx context.Context, repo forge.RepoRef, base string) (bool, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return false, err
	}
	base = strings.TrimPrefix(base, "refs/heads/")
	protection, response, classicErr := d.client.Repositories.GetBranchProtection(ctx, owner, name, base)
	if classicErr == nil && protection != nil && protection.GetRequireLinearHistory().GetEnabled() {
		return true, nil
	}
	if errors.Is(classicErr, gh.ErrBranchNotProtected) && response != nil && response.StatusCode == 404 {
		classicErr = nil
	} else if classicErr == nil && protection == nil {
		classicErr = errors.New("classic protection returned no details")
	}
	for page := 1; page != 0; {
		var rules []RepositoryRule
		response, err := d.readRules(ctx, fmt.Sprintf("repos/%s/rules/branches/%s?per_page=100&page=%d", repo.Path, url.PathEscape(base), page), &rules)
		if err != nil {
			return false, errors.Join(safeError(classicErr), err)
		}
		if slices.ContainsFunc(rules, func(rule RepositoryRule) bool { return rule.Type == "required_linear_history" }) {
			return true, nil
		}
		if response.NextPage != 0 && response.NextPage <= page {
			return false, errors.New("branch rules pagination did not advance")
		}
		page = response.NextPage
	}
	return false, safeError(classicErr)
}
