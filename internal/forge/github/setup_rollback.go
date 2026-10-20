package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	gh "github.com/google/go-github/v93/github"
)

func (c *setupAPI) rollbackPreflight(ctx context.Context, opts RulesOptions, candidate config.AuthorityCandidate, out io.Writer) error {
	var blockers []string
	block := func(source, ref, code, message string) {
		blockers = append(blockers, fmt.Sprintf("BLOCKER source=%s ref=%q code=%s: %s", source, ref, code, c.sanitize(message)))
	}
	snapshot, err := c.rules(ctx, opts.Repo)
	rulesComplete := err == nil
	if err != nil {
		block("rulesets", "", "read_failed", err.Error())
		// An incomplete ruleset read cannot hide independent classic or config
		// blockers. Recover just the repository default for those remaining reads.
		var info struct {
			DefaultBranch string               `json:"default_branch"`
			Permissions   struct{ Admin bool } `json:"permissions"`
		}
		if readErr := c.request(ctx, http.MethodGet, "repos/"+opts.Repo, nil, &info); readErr != nil {
			block("inventory", "", "read_failed", readErr.Error())
		} else if info.DefaultBranch != "" && info.Permissions.Admin {
			snapshot.DefaultBranch = info.DefaultBranch
		}
	}
	// Runtime checks use only the installation's client and actual cached token
	// grant. Admin credentials never substitute for installation permissions.
	if opts.RuntimeDriver == nil {
		block("permissions", "", "installation_required", "provide --installation-id and --private-key for actual installation permission checks")
	} else if opts.RuntimeDriver.app.id != opts.AppID {
		block("permissions", "", "app_mismatch", "installation client does not belong to --app-id")
	} else {
		problems, warnings, readErr := opts.RuntimeDriver.ValidateInstallationPermissions(ctx)
		if readErr != nil {
			block("permissions", "", "read_failed", readErr.Error())
		}
		for _, problem := range problems {
			block("permissions", "", "runtime_grant", problem)
		}
		for _, warning := range warnings {
			if _, err := fmt.Fprintln(out, "WARN source=permissions:", warning); err != nil {
				return err
			}
		}
	}
	if snapshot.DefaultBranch != "" {
		// Successor recognition must not be inferred from a familiar name alone.
		if _, err := planRules(snapshot, opts.Repo, opts.AppID); rulesComplete && err != nil {
			block("rulesets", "", "managed_shape", err.Error())
		}
		bases := map[string]bool{snapshot.DefaultBranch: true}
		refs := []string{}
		seen := map[string]bool{}
		for page := 1; ; page++ {
			var branches []struct {
				Name string `json:"name"`
			}
			next, err := c.page(ctx, http.MethodGet, fmt.Sprintf("repos/%s/branches?per_page=100&page=%d", opts.Repo, page), nil, &branches)
			if err != nil || branches == nil {
				message := "incomplete branch inventory"
				if err != nil {
					message = err.Error()
				}
				block("inventory", "", "read_failed", message)
				break
			}
			for _, branch := range branches {
				if _, err := forge.BranchRef(branch.Name); err != nil || seen[branch.Name] {
					block("inventory", branch.Name, "invalid_branch", "invalid or repeated branch")
					continue
				}
				seen[branch.Name] = true
				if strings.HasPrefix(branch.Name, "openmerge/") {
					refs = append(refs, branch.Name)
					continue
				}
				for _, queue := range candidate.Config.Queues {
					if queue.MatchesBase(branch.Name, snapshot.DefaultBranch) {
						bases[branch.Name] = true
					}
				}
			}
			if !next {
				break
			}
		}
		if !seen[snapshot.DefaultBranch] {
			block("inventory", snapshot.DefaultBranch, "incomplete", "default branch missing from branch inventory")
		}
		ordered := make([]string, 0, len(bases))
		for base := range bases {
			ordered = append(ordered, base)
		}
		slices.Sort(ordered)
		for _, base := range ordered {
			queue, diagnostics := candidate.Config.ResolveBranch(base, snapshot.DefaultBranch)
			if len(diagnostics) > 0 {
				block("config", base, "queue_resolution", diagnostics.Error())
			}
			for _, p := range rollbackRules(rulesComplete, snapshot.Rulesets, base, snapshot.DefaultBranch, opts.AppID) {
				block("rulesets", p.Ref, string(p.Code), rollbackRuleMessage(p))
			}
			for _, ref := range []string{base, "openmerge/fence/" + base} {
				protection, readErr := c.setupClassicProtection(ctx, opts.Repo, ref)
				if readErr != nil {
					block("classic", ref, "read_failed", readErr.Error())
					continue
				}
				if protection != nil {
					for _, problem := range classicProtectionProblems(protection, ref, opts.AppID, ref == base && forge.MergeMethod(queue.MergeMethod) == forge.Merge, false) {
						block("classic", ref, "legacy_incompatible", problem)
					}
				}
			}
		}
		// Current refs and all future family descendants are checked directly;
		// textual namespace substitution cannot preserve wildcard semantics.
		for _, ref := range refs {
			for _, p := range rollbackRules(rulesComplete, snapshot.Rulesets, ref, snapshot.DefaultBranch, opts.AppID) {
				if p.Code == BaseBypassMissing {
					block("rulesets", p.Ref, string(p.Code), rollbackRuleMessage(p))
				}
			}
		}
	}
	slices.Sort(blockers)
	blockers = slices.Compact(blockers)
	for _, blocker := range blockers {
		if _, err := fmt.Fprintln(out, blocker); err != nil {
			return err
		}
	}
	if len(blockers) > 0 {
		return fmt.Errorf("rollback preflight failed: %d blocker(s); stay in authority mode until explicitly resolved", len(blockers))
	}
	_, err = fmt.Fprintln(out, "Rollback preflight passed; read-only evidence, no policies changed.")
	return err
}

func rollbackRuleMessage(p RuleProblem) string {
	message := p.Message
	if p.Code == BaseBypassMissing || p.Code == RefBlocked {
		message += ". Operator choices: explicitly approve adding an App bypass (loses App content enforcement), disable/remove the owned rule, or ask its inherited-policy owner to act. Retain before/after evidence; if none is acceptable, stay in authority mode."
	}
	return message
}

func (c *setupAPI) setupClassicProtection(ctx context.Context, repo, ref string) (*gh.Protection, error) {
	fields, err := c.readClassicProtection(ctx, repo, ref)
	if err != nil || fields == nil {
		return nil, err
	}
	raw, _ := json.Marshal(fields)
	var protection *gh.Protection
	if err := json.Unmarshal(raw, &protection); err != nil || protection == nil {
		return nil, errors.New("incomplete classic protection response")
	}
	return protection, nil
}

// Never infer missing protection from a partial ruleset inventory.
func rollbackRules(complete bool, rules []Ruleset, base, defaultBranch string, appID int64) []RuleProblem {
	if !complete {
		return nil
	}
	problems := slices.DeleteFunc(ValidateRules(rules, base, defaultBranch, appID), func(p RuleProblem) bool { return p.Code == RefBlocked })
	defaultRef := "refs/heads/" + strings.TrimPrefix(defaultBranch, "refs/heads/")
	for _, r := range rules {
		if r.Enforcement != "active" || r.Target != "branch" && r.Target != "push" || len(r.Rules) == 0 || appBypass(r, appID) {
			continue
		}
		var conditions refConditions
		if r.Target == "branch" && json.Unmarshal(r.Conditions, &conditions) != nil {
			continue // ValidateRules already reports the malformed conditions.
		}
		coverage := authority.RefCoverage{Includes: conditions.RefName.Include, Excludes: conditions.RefName.Exclude}
		for _, family := range []string{"fence", "queue", "green", "verify"} {
			prefix := "refs/heads/openmerge/" + family + "/"
			overlap, err := coverage.MayContainRefUnder(prefix, defaultRef)
			if r.Target == "push" || overlap || err != nil {
				problems = append(problems, RuleProblem{Code: RefBlocked, RulesetID: r.ID, Ruleset: r.Name, Ref: prefix + "*", Message: fmt.Sprintf("ruleset %q (%d): cannot prove App pushes in %s* are unblocked; add the App bypass or exclude this namespace", r.Name, r.ID, prefix)})
			}
		}
	}
	return problems
}
