package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// RulesOptions uses the repository's default branch as the landing base. The
// separate admin token never becomes an App credential or gets written to disk.
type RulesOptions struct {
	Repo  string
	AppID int64
	Apply bool
}

type ruleSnapshot struct {
	Base     string
	Rulesets []Ruleset
}

type ruleChange struct {
	ID      int64
	Name    string
	Body    map[string]any
	Summary string
}

// SetupRules reports problems and previews changes before making any writes.
// GITHUB_TOKEN must belong to a repository admin and have Administration: write
// for --apply (read for inspection). App permissions deliberately omit this.
func SetupRules(ctx context.Context, opts RulesOptions, token string, out io.Writer) error {
	return setupRules(ctx, opts, token, out, "https://api.github.com/")
}

func setupRules(ctx context.Context, opts RulesOptions, token string, out io.Writer, api string) error {
	if _, _, err := splitRepoPath(opts.Repo); err != nil {
		return err
	}
	if opts.AppID <= 0 {
		return errors.New("provide --app-id or --config-dir containing app-id (the App ID, not the installation ID)")
	}
	if token == "" {
		return errors.New("GITHUB_TOKEN is required: use a repository admin token with Administration read (write for --apply)")
	}
	client, err := setupClient(api, token)
	if err != nil {
		return err
	}
	snapshot, err := client.rules(ctx, opts.Repo)
	if err != nil {
		return err
	}
	changes, err := planRules(snapshot, opts.Repo, opts.AppID)
	if err != nil {
		return err
	}
	problems := ValidateRules(snapshot.Rulesets, snapshot.Base, opts.AppID)
	var summary strings.Builder
	fmt.Fprintf(&summary, "Repository %s; base %s; App %d\n", opts.Repo, snapshot.Base, opts.AppID)
	for _, p := range problems {
		fmt.Fprintf(&summary, "! [%s] %s\n", p.Code, p.Message)
	}
	for _, change := range changes {
		fmt.Fprintln(&summary, change.Summary)
	}
	if len(changes) == 0 {
		fmt.Fprintln(&summary, "No ruleset changes needed.")
	} else if !opts.Apply {
		fmt.Fprintln(&summary, "Preview only. Run again with --apply to make these changes.")
	}
	if _, err := io.WriteString(out, summary.String()); err != nil {
		return err
	}
	if opts.Apply && len(changes) > 0 {
		// PUT has no compare-and-swap. Refuse an observed concurrent edit before
		// the first write; operators must serialize repository policy changes.
		fresh, err := client.rules(ctx, opts.Repo)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(snapshot, fresh) {
			return errors.New("repository rules changed during preview; rerun setup before applying")
		}
		for _, change := range changes {
			method, endpoint := http.MethodPost, "repos/"+opts.Repo+"/rulesets"
			if change.ID > 0 {
				method, endpoint = http.MethodPut, endpoint+"/"+strconv.FormatInt(change.ID, 10)
			}
			var result Ruleset
			if err := client.request(ctx, method, endpoint, change.Body, &result); err != nil {
				return fmt.Errorf("applying ruleset %q failed; rerun to inspect any completed changes: %w", change.Name, err)
			}
		}
		fresh, err = client.rules(ctx, opts.Repo)
		if err != nil {
			return err
		}
		problems = ValidateRules(fresh.Rulesets, fresh.Base, opts.AppID)
		if _, err := fmt.Fprintln(out, "Applied changes; re-read repository rules."); err != nil {
			return err
		}
		for _, p := range problems {
			if _, err := fmt.Fprintf(out, "! [%s] %s\n", p.Code, p.Message); err != nil {
				return err
			}
		}
	}
	if len(problems) > 0 {
		return errors.New("repository rules need attention; review the problems above")
	}
	_, err = fmt.Fprintln(out, "Rulesets allow base landing and queue pushes; fence ownership is exclusive to the App.")
	return err
}

func (c *setupAPI) rules(ctx context.Context, repo string) (ruleSnapshot, error) {
	var info struct {
		DefaultBranch string               `json:"default_branch"`
		Permissions   struct{ Admin bool } `json:"permissions"`
	}
	if err := c.request(ctx, http.MethodGet, "repos/"+repo, nil, &info); err != nil {
		return ruleSnapshot{}, err
	}
	if !info.Permissions.Admin || info.DefaultBranch == "" {
		return ruleSnapshot{}, errors.New("GITHUB_TOKEN must have repository admin access to inspect complete ruleset bypass lists")
	}
	snapshot := ruleSnapshot{Base: info.DefaultBranch}
	seen := make(map[int64]bool)
	for page := 1; ; page++ {
		var list []Ruleset
		next, err := c.page(ctx, http.MethodGet, fmt.Sprintf("repos/%s/rulesets?includes_parents=true&per_page=100&page=%d", repo, page), nil, &list)
		if err != nil {
			return ruleSnapshot{}, err
		}
		for _, entry := range list {
			if entry.ID <= 0 || seen[entry.ID] {
				return ruleSnapshot{}, errors.New("invalid or repeated ruleset in GitHub pagination")
			}
			seen[entry.ID] = true
			var detail Ruleset
			if err := c.request(ctx, http.MethodGet, fmt.Sprintf("repos/%s/rulesets/%d?includes_parents=true", repo, entry.ID), nil, &detail); err != nil {
				return ruleSnapshot{}, err
			}
			if detail.ID != entry.ID || detail.BypassActors == nil {
				return ruleSnapshot{}, errors.New("incomplete ruleset details; use an admin token that can see bypass actors, including inherited policies")
			}
			snapshot.Rulesets = append(snapshot.Rulesets, detail)
		}
		if !next {
			break
		}
	}
	slices.SortFunc(snapshot.Rulesets, func(a, b Ruleset) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return snapshot, nil
}

func planRules(snapshot ruleSnapshot, repo string, appID int64) ([]ruleChange, error) {
	var changes []ruleChange
	var fence *Ruleset
	for i := range snapshot.Rulesets {
		r := snapshot.Rulesets[i]
		local := r.SourceType == "Repository" && strings.EqualFold(r.Source, repo)
		if r.Name == "openmerge-fence" && local {
			if fence != nil {
				return nil, errors.New("multiple repository rulesets named openmerge-fence; resolve the duplicate manually")
			}
			fence = &snapshot.Rulesets[i]
			continue
		}
		var c refConditions
		if local && r.Target == "branch" && r.Enforcement == "active" && json.Unmarshal(r.Conditions, &c) == nil && validPatterns(c) && applies(c, "refs/heads/"+snapshot.Base, "refs/heads/"+snapshot.Base) && !appBypass(r, appID) {
			actors := slices.Clone(r.BypassActors)
			found := false
			for i, actor := range actors {
				if actor.ActorType == "Integration" && actor.ActorID != nil && *actor.ActorID == appID {
					actors[i].BypassMode = "always"
					found = true
				}
			}
			if !found {
				actors = append(actors, BypassActor{ActorID: new(appID), ActorType: "Integration", BypassMode: "always"})
			}
			changes = append(changes, ruleChange{ID: r.ID, Name: r.Name, Body: map[string]any{"bypass_actors": actors}, Summary: fmt.Sprintf("+ ruleset %q (%d): Integration App %d bypass_mode=always (retain other actors, conditions and rules)", r.Name, r.ID, appID)})
		}
	}
	if fence == nil {
		conditions := refConditions{}
		conditions.RefName.Include, conditions.RefName.Exclude = slices.Clone(fencePatterns), []string{}
		rules := make([]RepositoryRule, 0, len(fenceRuleTypes))
		for _, kind := range fenceRuleTypes {
			rules = append(rules, fenceRule(kind))
		}
		changes = append(changes, ruleChange{Name: "openmerge-fence", Body: map[string]any{
			"name": "openmerge-fence", "target": "branch", "enforcement": "active", "conditions": conditions, "rules": rules,
			"bypass_actors": []BypassActor{{ActorID: new(appID), ActorType: "Integration", BypassMode: "always"}},
		}, Summary: fmt.Sprintf("+ ruleset openmerge-fence: active, %s; restrict creation/update/deletion/non_fast_forward; only Integration App %d bypass_mode=always", strings.Join(fencePatterns, ", "), appID)})
		return changes, nil
	}
	var conditions refConditions
	if fence.Target != "branch" || json.Unmarshal(fence.Conditions, &conditions) != nil || !validPatterns(conditions) || len(conditions.RefName.Exclude) > 0 || len(conditions.RefName.Include) == 0 {
		return nil, errors.New("openmerge-fence has incompatible target/conditions; correct it manually before applying")
	}
	for _, pattern := range conditions.RefName.Include {
		if !strings.HasPrefix(pattern, fencePrefix) {
			return nil, errors.New("openmerge-fence also targets branches outside the fence namespace; split that policy manually before applying")
		}
	}
	body := make(map[string]any)
	var lines []string
	if fence.Enforcement != "active" {
		body["enforcement"] = "active"
		lines = append(lines, "+ enforcement=active")
	}
	if !appBypass(*fence, appID) || len(fence.BypassActors) != 1 {
		body["bypass_actors"] = []BypassActor{{ActorID: new(appID), ActorType: "Integration", BypassMode: "always"}}
		old, _ := json.Marshal(fence.BypassActors)
		lines = append(lines, "- bypass_actors="+string(old), fmt.Sprintf("+ only Integration App %d bypass_mode=always", appID))
	}
	if !covers(conditions, fencePrefix, "refs/heads/"+snapshot.Base) {
		// Only append includes; retain all existing conditions verbatim.
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(fence.Conditions, &raw); err != nil || len(raw) != 1 {
			return nil, errors.New("openmerge-fence has unknown conditions; inspect manually")
		}
		for _, pattern := range fencePatterns {
			if !slices.Contains(conditions.RefName.Include, pattern) {
				conditions.RefName.Include = append(conditions.RefName.Include, pattern)
				lines = append(lines, "+ include "+pattern)
			}
		}
		var refName map[string]json.RawMessage
		if err := json.Unmarshal(raw["ref_name"], &refName); err != nil {
			return nil, errors.New("openmerge-fence has invalid ref conditions")
		}
		refName["include"], _ = json.Marshal(conditions.RefName.Include)
		raw["ref_name"], _ = json.Marshal(refName)
		body["conditions"] = raw
	}
	if !hasFenceRules(*fence) {
		rules := slices.Clone(fence.Rules)
		for i, rule := range rules {
			if rule.Type == "update" && !strictFenceUpdate(rule) {
				params := make(map[string]json.RawMessage)
				if len(rule.Parameters) > 0 && string(rule.Parameters) != "null" {
					if err := json.Unmarshal(rule.Parameters, &params); err != nil {
						return nil, errors.New("openmerge-fence has invalid update parameters")
					}
				}
				params["update_allows_fetch_and_merge"] = json.RawMessage(`false`)
				rules[i].Parameters, _ = json.Marshal(params)
				lines = append(lines, "+ update.update_allows_fetch_and_merge=false")
			}
		}
		for _, kind := range fenceRuleTypes {
			if !slices.ContainsFunc(rules, func(r RepositoryRule) bool { return r.Type == kind }) {
				rules = append(rules, fenceRule(kind))
				lines = append(lines, "+ rule "+kind)
			}
		}
		body["rules"] = rules
	}
	if len(body) > 0 {
		changes = append(changes, ruleChange{ID: fence.ID, Name: fence.Name, Body: body, Summary: fmt.Sprintf("~ ruleset openmerge-fence (%d)\n%s", fence.ID, strings.Join(lines, "\n"))})
	}
	return changes, nil
}
