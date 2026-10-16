package github

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	gh "github.com/google/go-github/v93/github"
)

// ValidateClassicProtection checks both refs in the atomic landing using only
// installation credentials. Unreadable protection is an error, never a pass.
func (d *Driver) ValidateClassicProtection(ctx context.Context, repo forge.RepoRef, base string, method forge.MergeMethod) (problems, warnings []string, err error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, nil, err
	}
	problems, err = d.classicProtection(ctx, owner, name, base, method)
	return problems, nil, err
}

func (d *Driver) classicProtection(ctx context.Context, owner, repo, base string, method forge.MergeMethod) ([]string, error) {
	var problems []string
	var readErrors []error
	base = strings.TrimPrefix(base, "refs/heads/")
	for _, ref := range []string{base, "openmerge/fence/" + base} {
		protection, response, err := d.client.Repositories.GetBranchProtection(ctx, owner, repo, ref)
		if errors.Is(err, gh.ErrBranchNotProtected) && response != nil && response.StatusCode == 404 {
			continue
		}
		if err != nil {
			// A generic 404 can hide a missing permission. Only GitHub's explicit
			// "Branch not protected" response establishes absence of protection.
			readErrors = append(readErrors, fmt.Errorf("cannot read classic protection for %s: %w", ref, safeError(err)))
			continue
		}
		if protection == nil {
			readErrors = append(readErrors, fmt.Errorf("classic protection for %s returned no details", ref))
			continue
		}
		if ref == base && protection.GetRequireLinearHistory().GetEnabled() && method == "" {
			readErrors = append(readErrors, errors.New("classic linear history validation awaits base-branch merge_method configuration"))
		}
		if protection.RequiredSignatures == nil {
			signatures, _, err := d.client.Repositories.GetSignaturesProtectedBranch(ctx, owner, repo, ref)
			if err != nil {
				readErrors = append(readErrors, fmt.Errorf("cannot read required signatures for %s: %w", ref, safeError(err)))
			} else if signatures == nil || signatures.Enabled == nil {
				readErrors = append(readErrors, fmt.Errorf("required signatures for %s returned no details", ref))
			} else {
				protection.RequiredSignatures = signatures
			}
		}
		// Both the base and fence advance to children of their leased tips.
		// --force-with-lease supplies CAS; it does not require a non-FF push.
		problems = append(problems, classicProtectionProblems(protection, ref, d.app.id, ref == base && method == forge.Merge, false)...)
	}
	return problems, errors.Join(readErrors...)
}

func classicProtectionProblems(p *gh.Protection, ref string, appID int64, hasMerges, needsForce bool) []string {
	var problems []string
	problem := func(code, message string) {
		problems = append(problems, fmt.Sprintf("[classic_%s] classic protection on %s: %s", code, ref, message))
	}
	includesApp := func(apps []*gh.App) bool {
		return slices.ContainsFunc(apps, func(app *gh.App) bool { return appID > 0 && app.GetID() == appID })
	}
	if reviews := p.RequiredPullRequestReviews; reviews != nil && !includesApp(reviews.GetBypassPullRequestAllowances().GetApps()) {
		problem("reviews", "required pull requests/reviews block the atomic push; add this App to bypass_pull_request_allowances.apps")
	}
	if checks := p.RequiredStatusChecks; checks != nil && (len(checks.GetContexts()) > 0 || len(checks.GetChecks()) > 0 || checks.Strict) {
		problem("status_checks", "required status checks can block the atomic push; use an App-bypass ruleset and express CI requirements in merge_when")
	}
	if p.GetRequiredSignatures().GetEnabled() {
		problem("signed_commits", "required signed commits block OpenMerge's unsigned test/fence commits; use an App-bypass ruleset")
	}
	if hasMerges && p.GetRequireLinearHistory().GetEnabled() {
		problem("linear_history", "required linear history can block merge-method test commits; use an App-bypass ruleset")
	}
	if p.Restrictions != nil && !includesApp(p.Restrictions.Apps) {
		problem("push_restrictions", "push restrictions do not include this App; add its App ID to restrictions.apps")
	}
	if p.GetLockBranch().GetEnabled() {
		problem("locked", "locked branch blocks the atomic push; unlock it or use an App-bypass ruleset")
	}
	if needsForce && !p.GetAllowForcePushes().GetEnabled() {
		problem("force_push", "this ref update requires force pushes but they are disallowed")
	}
	return problems
}
