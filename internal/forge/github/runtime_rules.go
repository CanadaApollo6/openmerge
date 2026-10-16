package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// ValidateRepositoryRules uses only the installation client (Metadata: read).
// Hidden bypass lists cannot prove ownership or missing permission. Validate
// their visible structure assuming an App-only bypass and WARN about that
// assumption; setup's separate admin-token validation remains strict.
func (d *Driver) ValidateRepositoryRules(ctx context.Context, repo forge.RepoRef, base string) (problems, warnings []string, err error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, nil, err
	}
	info, _, err := d.client.Repositories.Get(ctx, owner, name)
	if err != nil {
		return nil, nil, safeError(err)
	}
	if info.GetDefaultBranch() == "" {
		return nil, nil, errors.New("repository default branch is missing")
	}
	var rules []Ruleset
	seen := make(map[int64]bool)
	for page := 1; page != 0; {
		var list []Ruleset
		response, err := d.readRules(ctx, fmt.Sprintf("repos/%s/rulesets?includes_parents=true&per_page=100&page=%d", repo.Path, page), &list)
		if err != nil {
			return nil, nil, err
		}
		for _, entry := range list {
			if entry.ID <= 0 || seen[entry.ID] {
				return nil, nil, errors.New("invalid or repeated ruleset in GitHub pagination")
			}
			seen[entry.ID] = true
			var detail Ruleset
			if _, err := d.readRules(ctx, fmt.Sprintf("repos/%s/rulesets/%d?includes_parents=true", repo.Path, entry.ID), &detail); err != nil {
				return nil, nil, err
			}
			if detail.ID != entry.ID {
				return nil, nil, errors.New("ruleset detail ID does not match list")
			}
			rules = append(rules, detail)
		}
		if response.NextPage != 0 && response.NextPage <= page {
			return nil, nil, errors.New("ruleset pagination did not advance")
		}
		page = response.NextPage
	}
	slices.SortFunc(rules, func(a, b Ruleset) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	for i := range rules {
		r := &rules[i]
		if r.BypassActors == nil && r.Enforcement != "disabled" && r.Enforcement != "evaluate" && r.Target != "tag" {
			warnings = append(warnings, fmt.Sprintf("ruleset %q (%d): bypass actors hidden from installation; App bypass and exclusive fence ownership are unverified; run openmerge setup rulesets with a separate admin token", r.Name, r.ID))
			r.BypassActors = []BypassActor{{ActorID: new(d.app.id), ActorType: "Integration", BypassMode: "always"}}
		}
	}
	for _, problem := range ValidateRules(rules, base, info.GetDefaultBranch(), d.app.id) {
		problems = append(problems, fmt.Sprintf("[%s] %s", problem.Code, problem.Message))
	}
	return problems, warnings, nil
}

// Decode our own lossless rules schema through the normal authenticated,
// conditional and quota-aware driver transport rather than an admin client.
func (d *Driver) readRules(ctx context.Context, path string, result any) (*rulePage, error) {
	request, err := d.client.NewRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, safeError(err)
	}
	response, err := d.client.Do(request, result)
	if err != nil {
		return nil, safeError(err)
	}
	return &rulePage{NextPage: response.NextPage}, nil
}

type rulePage struct{ NextPage int }
