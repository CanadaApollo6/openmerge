package github

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
)

const authorityPermissionGuidance = "Administration: read lets the App read classic branch-protection settings; it neither grants ruleset writes nor guarantees bypass-list visibility. Administration: write belongs only on the separate operator token."

func setupAuthorityRules(ctx context.Context, client *setupAPI, opts RulesOptions, out io.Writer) error {
	if err := authority.Mode(opts.Authority).Validate(); err != nil {
		return err
	}
	if opts.Restore != "" {
		if opts.Config != "" || opts.Backup != "" || opts.BreakGlass != "" || opts.RotateBreakGlass || opts.RollbackPreflight {
			return errors.New("--restore cannot be combined with a proposed plan")
		}
		return client.restoreRules(ctx, opts, out)
	}
	if opts.Config == "" {
		return errors.New("authority setup requires --config with the reviewed proposed configuration")
	}
	data, err := os.ReadFile(opts.Config)
	if err != nil {
		return err
	}
	candidate, _, err := config.LoadAuthorityCandidate(data)
	if err != nil {
		return err
	}
	mode := candidate.Authority.Mode.Normalized()
	if opts.Authority != "" && authority.Mode(opts.Authority) != mode {
		return errors.New("--authority must agree with proposed policy_authority")
	}
	if opts.RollbackPreflight {
		if mode != authority.GitHub || opts.Apply || opts.Backup != "" || opts.BreakGlass != "" || opts.RotateBreakGlass {
			return errors.New("rollback preflight requires github configuration and is read-only")
		}
		return client.rollbackPreflight(ctx, opts, candidate, out)
	}
	if mode != authority.OpenMerge {
		return errors.New("proposed github configuration is supported only with --rollback-preflight")
	}
	if opts.BreakGlass != "" {
		var actor authority.Actor
		if err := json.Unmarshal([]byte(opts.BreakGlass), &actor); err != nil {
			return fmt.Errorf("--break-glass uses the candidate JSON actor grammar: %w", err)
		}
		if actor != *candidate.Authority.BreakGlass {
			return errors.New("--break-glass must match the proposed configuration")
		}
	}
	actor, err := client.setupBreakGlass(ctx, opts.Repo, *candidate.Authority.BreakGlass)
	if err != nil {
		return err
	}
	// Bind recovery retries to the reviewed file and resolved actor, not a newly
	// computed plan that would lose the first operation after a process crash.
	bindingData, _ := json.Marshal(struct {
		Config string
		Actor  BypassActor
		Rotate bool
	}{string(data), actor, opts.RotateBreakGlass})
	binding := fmt.Sprintf("%x", sha256.Sum256(bindingData))
	snapshot, err := client.rules(ctx, opts.Repo)
	if err != nil {
		return err
	}
	capture, err := planAuthorityRules(snapshot, opts, candidate, actor)
	if err != nil {
		return err
	}
	capture.Version, capture.Repository, capture.AppID, capture.Binding = 1, opts.Repo, opts.AppID, binding
	if opts.Backup != "" {
		previous, readErr := readRulesBackup(opts.Backup)
		if readErr == nil {
			if previous.Repository != opts.Repo || previous.AppID != opts.AppID || previous.Binding != binding {
				return errors.New("backup belongs to a different repository, App or proposed plan; use a new backup file")
			}
			capture = previous
		} else if !os.IsNotExist(readErr) {
			return readErr
		}
	}
	if _, err := fmt.Fprintf(out, "Repository %s; authority openmerge; App %d\n%s\n", opts.Repo, opts.AppID, authorityPermissionGuidance); err != nil {
		return err
	}
	for _, edit := range capture.Edits {
		if _, err := fmt.Fprintf(out, "~ %s (%d): guarded write restrictions, exact bypasses; retain existing coverage and content\n%s\n", edit.Name, edit.ID, diagnostic.NewSanitizer(client.secrets...).SanitizeText(string(mustJSONIndent(edit.After)))); err != nil {
			return err
		}
	}
	if !opts.Apply {
		_, err := fmt.Fprintln(out, "Preview only. Serialize repository policy edits; apply with --backup <new-file> --apply. Rotation additionally requires landed policy digests, settled attempts and hidden installation bypass lists; otherwise use a reviewed admission pause.")
		return err
	}
	if len(capture.Edits) == 0 {
		_, err := fmt.Fprintln(out, "No ruleset changes needed.")
		return err
	}
	if opts.Backup == "" {
		return errors.New("authority --apply requires --backup <file> before the first mutation")
	}
	encoded, _ := json.Marshal(capture)
	if _, redacted := diagnostic.NewSanitizer(client.secrets...).SanitizeTextRedacted(string(encoded)); redacted {
		return errors.New("managed settings contain credential-like data; refusing recovery capture")
	}
	if err := writeRulesBackup(opts.Backup, capture); err != nil {
		return err
	}
	return client.applyRulesBackup(ctx, opts, capture, false, out)
}

func (c *setupAPI) setupBreakGlass(ctx context.Context, repo string, actor authority.Actor) (BypassActor, error) {
	if err := actor.Validate(); err != nil {
		return BypassActor{}, err
	}
	if actor.RepositoryAdmin {
		// GitHub's built-in repository-admin ruleset bypass role, also validated by
		// the authority reader. Never confuse this role with a custom role ID.
		return BypassActor{ActorType: "RepositoryRole", ActorID: new(int64(5)), BypassMode: "always"}, nil
	}
	owner, _, _ := splitRepoPath(repo)
	org, slug, _ := strings.Cut(actor.Team, "/")
	if !strings.EqualFold(owner, org) {
		return BypassActor{}, errors.New("break-glass team must belong to the repository organization")
	}
	var team struct {
		ID           int64  `json:"id"`
		Slug         string `json:"slug"`
		Organization struct {
			Login string `json:"login"`
			ID    int64  `json:"id"`
		} `json:"organization"`
	}
	if err := c.request(ctx, http.MethodGet, "orgs/"+org+"/teams/"+slug, nil, &team); err != nil {
		return BypassActor{}, err
	}
	if team.ID <= 0 || team.Organization.ID <= 0 || !strings.EqualFold(team.Organization.Login, org) || !strings.EqualFold(team.Slug, slug) {
		return BypassActor{}, errors.New("unresolved or mismatched break-glass team")
	}
	return BypassActor{ActorType: "Team", ActorID: new(team.ID), BypassMode: "always"}, nil
}

func mustJSONIndent(v any) []byte { data, _ := json.MarshalIndent(v, "", "  "); return data }

func managedFenceShape(r Ruleset, appID int64, defaultBranch string) bool {
	var raw map[string]json.RawMessage
	var ref map[string]json.RawMessage
	if json.Unmarshal(r.Conditions, &raw) != nil || len(raw) != 1 || json.Unmarshal(raw["ref_name"], &ref) != nil || len(ref) != 2 || ref["include"] == nil || ref["exclude"] == nil {
		return false
	}
	var cond refConditions
	if r.Target != "branch" || r.Enforcement != "active" || !appBypass(r, appID) || len(r.BypassActors) != 1 || !hasFenceRules(r) || json.Unmarshal(r.Conditions, &cond) != nil || !validPatterns(cond) || len(cond.RefName.Exclude) != 0 || !covers(cond, fencePrefix, "refs/heads/"+defaultBranch) {
		return false
	}
	return true
}

func managedRefsShape(r Ruleset, appID int64, defaultBranch string) bool {
	if !managedFenceShape(r, appID, defaultBranch) {
		return false
	}
	var cond refConditions
	_ = json.Unmarshal(r.Conditions, &cond)
	for _, pattern := range cond.RefName.Include {
		allowed := false
		for _, family := range []string{"fence", "queue", "green", "verify"} {
			allowed = allowed || strings.HasPrefix(pattern, "refs/heads/openmerge/"+family+"/")
		}
		if !allowed {
			return false
		}
	}
	return true
}

func planAuthorityRules(snapshot ruleSnapshot, opts RulesOptions, candidate config.AuthorityCandidate, actor BypassActor) (rulesBackup, error) {
	var result rulesBackup
	baseCoverage, err := authority.CompileRefCoverage(snapshot.DefaultBranch, candidate.BaseSelectors())
	if err != nil {
		return result, err
	}
	refsCoverage := authority.CompileProtectedRefCoverage()
	managed := map[string]*Ruleset{}
	for i := range snapshot.Rulesets {
		r := &snapshot.Rulesets[i]
		if r.Name != "openmerge-base" && r.Name != "openmerge-fence" && r.Name != "openmerge-refs" {
			continue
		}
		if r.SourceType != "Repository" || !strings.EqualFold(r.Source, opts.Repo) {
			return result, fmt.Errorf("managed name %s collides with inherited policy; its owner must act", r.Name)
		}
		key := r.Name
		if key == "openmerge-fence" {
			key = "openmerge-refs"
		}
		if managed[key] != nil {
			return result, errors.New("duplicate or ambiguous managed rulesets (including fence/refs pair)")
		}
		managed[key] = r
	}
	if opts.RotateBreakGlass && (managed["openmerge-base"] == nil || managed["openmerge-refs"] == nil || managed["openmerge-refs"].Name != "openmerge-refs") {
		return result, errors.New("rotation requires both installed authority rulesets")
	}
	for _, name := range []string{"openmerge-base", "openmerge-refs"} {
		r := managed[name]
		coverage := baseCoverage
		actors := []BypassActor{{ActorID: new(opts.AppID), ActorType: "Integration", BypassMode: "always"}, actor}
		if name == "openmerge-refs" {
			coverage, actors = refsCoverage, actors[:1]
		}
		var before map[string]json.RawMessage
		after := map[string]json.RawMessage{}
		if r != nil {
			before = ruleSettings(snapshot.Raw[r.ID])
			after = cloneSettings(before)
			if r.Target != "branch" || !hasFenceRules(*r) {
				return result, fmt.Errorf("%s has incompatible managed target/restrictions; inspect manually", r.Name)
			}
			var conditions refConditions
			if json.Unmarshal(r.Conditions, &conditions) != nil || !validPatterns(conditions) || len(conditions.RefName.Include) == 0 {
				return result, fmt.Errorf("%s has invalid coverage", r.Name)
			}
			// Reject unknown conditions rather than removing a narrowing predicate.
			var rawCond map[string]json.RawMessage
			_ = json.Unmarshal(r.Conditions, &rawCond)
			var rawRef map[string]json.RawMessage
			_ = json.Unmarshal(rawCond["ref_name"], &rawRef)
			if len(rawCond) != 1 || len(rawRef) != 2 || rawRef["include"] == nil || rawRef["exclude"] == nil {
				return result, fmt.Errorf("%s has unknown conditions; inspect manually", r.Name)
			}
			if name == "openmerge-refs" {
				if !managedRefsShape(*r, opts.AppID, snapshot.DefaultBranch) {
					return result, errors.New("fence/refs is not an active App-only managed shape")
				}
			} else {
				if !appBypass(*r, opts.AppID) || len(r.BypassActors) != 2 {
					return result, errors.New("openmerge-base must have exactly the App and one break-glass actor")
				}
				nonApp := 0
				for _, a := range r.BypassActors {
					if a.ActorType == "Integration" {
						continue
					}
					nonApp++
					if a.ActorID == nil || *a.ActorID <= 0 || a.BypassMode != "always" || a.ActorType != "Team" && (a.ActorType != "RepositoryRole" || *a.ActorID != 5) {
						return result, errors.New("openmerge-base has an unsupported actor")
					}
				}
				if nonApp != 1 {
					return result, errors.New("openmerge-base requires exactly one break-glass actor")
				}
				oldActors, newActors := map[string]json.RawMessage{}, map[string]json.RawMessage{}
				setJSON(oldActors, "bypass_actors", r.BypassActors)
				setJSON(newActors, "bypass_actors", actors)
				if !settingsEqual(oldActors, newActors) && !opts.RotateBreakGlass {
					return result, errors.New("break-glass actor changed: use explicit --rotate-break-glass after the coordinated policy transition; never union actors")
				}
				current := authority.RefCoverage{Includes: conditions.RefName.Include, Excludes: conditions.RefName.Exclude}
				installed := authority.RefCoverage{Includes: slices.Clone(coverage.Includes), Excludes: coverage.Excludes}
				installed.Includes = unionPatterns(current.Includes, installed.Includes)
				if _, err := authority.ValidateCoverageTransition(current, coverage, installed); err != nil {
					return result, err
				}
				oldConditions, newConditions := map[string]json.RawMessage{}, map[string]json.RawMessage{}
				setJSON(oldConditions, "conditions", r.Conditions)
				proposedConditions := refConditions{}
				proposedConditions.RefName.Include, proposedConditions.RefName.Exclude = coverage.Includes, coverage.Excludes
				setJSON(newConditions, "conditions", proposedConditions)
				if opts.RotateBreakGlass && !settingsEqual(oldConditions, newConditions) {
					return result, errors.New("rotation cannot change selector coverage; complete selector transitions separately")
				}
				coverage = installed
			}
			coverage.Includes = unionPatterns(conditions.RefName.Include, coverage.Includes)
		} else {
			setJSON(after, "target", "branch")
			rules := []RepositoryRule{}
			for _, kind := range fenceRuleTypes {
				rules = append(rules, fenceRule(kind))
			}
			setJSON(after, "rules", rules)
		}
		setJSON(after, "name", name)
		setJSON(after, "enforcement", "active")
		if r != nil {
			var rawActors []map[string]json.RawMessage
			if json.Unmarshal(before["bypass_actors"], &rawActors) != nil {
				return result, errors.New("invalid managed actors")
			}
			for _, rawActor := range rawActors {
				if len(rawActor) != 3 {
					return result, errors.New("unknown managed bypass fields; inspect manually")
				}
			}
		}
		setJSON(after, "bypass_actors", actors)
		conditions := refConditions{}
		conditions.RefName.Include, conditions.RefName.Exclude = coverage.Includes, coverage.Excludes
		setJSON(after, "conditions", conditions)
		if opts.RotateBreakGlass && r != nil {
			oldShape, newShape := cloneSettings(before), cloneSettings(after)
			delete(oldShape, "bypass_actors")
			delete(newShape, "bypass_actors")
			if !settingsEqual(oldShape, newShape) {
				return result, errors.New("rotation may change only the base bypass list; repair other settings separately")
			}
		}
		if r != nil && settingsEqual(before, after) {
			continue
		}
		if opts.RotateBreakGlass && name == "openmerge-refs" {
			return result, errors.New("rotation may update only the base bypass list; repair refs separately")
		}
		edit := rulesEdit{Name: name, Before: before, After: after}
		if r != nil {
			edit.ID = r.ID
		}
		result.Edits = append(result.Edits, edit)
	}
	return result, nil
}

func unionPatterns(a, b []string) []string {
	result := append(slices.Clone(a), b...)
	slices.Sort(result)
	return slices.Compact(result)
}
