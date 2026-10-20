package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

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
		// Run the legacy validator on every current protected ref, plus the complete
		// future families via its conservative namespace-overlap check. Remapping a
		// family to queue keeps the actual validator as the compatibility authority.
		for _, ref := range refs {
			for _, p := range rollbackRules(rulesComplete, snapshot.Rulesets, ref, snapshot.DefaultBranch, opts.AppID) {
				if p.Code == BaseBypassMissing {
					block("rulesets", p.Ref, string(p.Code), rollbackRuleMessage(p))
				}
			}
		}
		for _, family := range []string{"green", "verify"} {
			projected := slices.Clone(snapshot.Rulesets)
			for i := range projected {
				projected[i].Conditions = rollbackFamilyConditions(projected[i].Conditions, family)
			}
			for _, p := range rollbackRules(rulesComplete, projected, snapshot.DefaultBranch, snapshot.DefaultBranch, opts.AppID) {
				if p.Code == RefBlocked && strings.HasPrefix(p.Ref, queuePrefix) {
					p.Ref = strings.Replace(p.Ref, queuePrefix, "refs/heads/openmerge/"+family+"/", 1)
					p.Message = strings.ReplaceAll(p.Message, queuePrefix, "refs/heads/openmerge/"+family+"/")
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
	endpoint := "repos/" + repo + "/branches/" + url.PathEscape(ref) + "/protection"
	var raw json.RawMessage
	if err := c.request(ctx, http.MethodGet, endpoint, nil, &raw); err != nil {
		var api *setupAPIError
		if errors.As(err, &api) && api.status.StatusCode == 404 && api.message == "Branch not protected" {
			return nil, nil
		}
		return nil, err
	}
	var protection *gh.Protection
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if err := json.Unmarshal(raw, &protection); err != nil || protection == nil || len(fields) == 0 {
		return nil, errors.New("incomplete classic protection response")
	}
	if protection.RequiredSignatures == nil {
		var signatures *gh.SignaturesProtectedBranch
		if err := c.request(ctx, http.MethodGet, endpoint+"/required_signatures", nil, &signatures); err != nil {
			return nil, err
		}
		if signatures == nil || signatures.Enabled == nil {
			return nil, errors.New("incomplete required-signatures response")
		}
		protection.RequiredSignatures = signatures
	}
	if protection.RequiredSignatures.Enabled == nil {
		return nil, errors.New("incomplete required-signatures response")
	}
	return protection, nil
}

// Never infer missing protection from a partial ruleset inventory.
func rollbackRules(complete bool, rules []Ruleset, base, defaultBranch string, appID int64) []RuleProblem {
	if !complete {
		return nil
	}
	return ValidateRules(rules, base, defaultBranch, appID)
}

// Swap namespaces rather than collapsing two families. In particular a queue
// exclusion must not accidentally waive a broad rule on green/verify. Decode
// strings first so escaped JSON slashes have exactly the same semantics.
func rollbackFamilyConditions(raw json.RawMessage, family string) json.RawMessage {
	var conditions, ref map[string]json.RawMessage
	if json.Unmarshal(raw, &conditions) != nil || json.Unmarshal(conditions["ref_name"], &ref) != nil {
		return raw
	}
	swap := strings.NewReplacer(queuePrefix, "refs/heads/openmerge/"+family+"/", "refs/heads/openmerge/"+family+"/", queuePrefix)
	for _, key := range []string{"include", "exclude"} {
		var patterns []string
		if json.Unmarshal(ref[key], &patterns) != nil {
			return raw
		}
		for i := range patterns {
			patterns[i] = swap.Replace(patterns[i])
		}
		setJSON(ref, key, patterns)
	}
	setJSON(conditions, "ref_name", ref)
	result, err := json.Marshal(conditions)
	if err != nil {
		return raw
	}
	return result
}
