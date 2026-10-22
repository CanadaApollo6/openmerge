package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

type importBranch struct {
	Name   string `json:"name"`
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

type importTree map[string]string

func (c *setupAPI) importGet(ctx context.Context, endpoint, permission string, result any) error {
	if err := c.request(ctx, http.MethodGet, endpoint, nil, result); err != nil {
		return c.importError(err, permission)
	}
	return nil
}
func (c *setupAPI) importError(err error, permission string) error {
	if permissionDenied(err) {
		return fmt.Errorf("%w: cannot read complete protection; requires %s (inherited rulesets may require an organization owner): %w", forge.ErrRulesUnavailable, permission, err)
	}
	return c.wrapError(err, "reading protection requires %s", permission)
}

func (c *setupAPI) importTree(ctx context.Context, repo, sha string) (importTree, error) {
	var result struct {
		Truncated bool `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			Mode string `json:"mode"`
			SHA  string `json:"sha"`
		} `json:"tree"`
	}
	if err := c.importGet(ctx, "repos/"+repo+"/git/trees/"+url.PathEscape(sha)+"?recursive=1", "Contents read", &result); err != nil {
		return nil, err
	}
	if result.Truncated || result.Tree == nil {
		return nil, errors.New("incomplete Git tree: cannot inspect configuration, CODEOWNERS or workflows")
	}
	tree := make(importTree)
	for _, entry := range result.Tree {
		if entry.Type == "blob" {
			if entry.Mode == "120000" {
				tree[entry.Path] = "symlink"
			} else {
				tree[entry.Path] = entry.SHA
			}
		}
	}
	return tree, nil
}
func (c *setupAPI) importBlob(ctx context.Context, repo, sha string) ([]byte, error) {
	if sha == "symlink" {
		return nil, errors.New("configuration, CODEOWNERS and workflows must be regular files, not symlinks")
	}
	var blob struct {
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	if err := c.importGet(ctx, "repos/"+repo+"/git/blobs/"+url.PathEscape(sha), "Contents read", &blob); err != nil {
		return nil, err
	}
	if blob.Encoding != "base64" {
		return nil, errors.New("unexpected Git blob encoding")
	}
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(blob.Content, "\n", ""))
	if err != nil {
		return nil, errors.New("invalid Git blob encoding")
	}
	return data, nil
}

func (c *setupAPI) readProtection(ctx context.Context, opts ImportOptions) (ProtectionReport, error) {
	snap, err := c.rules(ctx, opts.Repo)
	if err != nil {
		return ProtectionReport{}, c.importError(err, "repository admin access and Administration read, including visibility of inherited bypass actors")
	}
	r := ProtectionReport{Version: 1, Repository: opts.Repo, DefaultBranch: snap.DefaultBranch, ContributingGuide: importGuide}
	var branches []importBranch
	for page := 1; ; page++ {
		var entries []importBranch
		next, err := c.page(ctx, http.MethodGet, fmt.Sprintf("repos/%s/branches?per_page=100&page=%d", opts.Repo, page), nil, &entries)
		if err != nil {
			return r, c.importError(err, "Contents read")
		}
		branches = append(branches, entries...)
		if !next {
			break
		}
	}
	tips := make(map[string]string)
	for _, branch := range branches {
		if branch.Name == "" || branch.Commit.SHA == "" || tips[branch.Name] != "" {
			return r, errors.New("incomplete or duplicate branch listing")
		}
		tips[branch.Name] = branch.Commit.SHA
	}
	r.GoverningCommit = tips[r.DefaultBranch]
	if r.GoverningCommit == "" {
		return r, errors.New("default branch is absent from the branch listing")
	}
	tree, err := c.importTree(ctx, opts.Repo, r.GoverningCommit)
	if err != nil {
		return r, err
	}
	_, data, err := config.Find(func(path string) ([]byte, bool, error) {
		sha, exists := tree[path]
		if !exists {
			return nil, false, nil
		}
		contents, e := c.importBlob(ctx, opts.Repo, sha)
		return contents, true, e
	})
	var cfg config.Config
	if err == nil {
		var candidate config.AuthorityCandidate
		candidate, _, _ = config.LoadAuthorityCandidate(data)
		if candidate.Authority.Mode == authority.OpenMerge {
			g, acquireErr := c.operatorGoverning(ctx, opts.Repo, r.GoverningCommit)
			if acquireErr == nil {
				acquireErr = c.validateLandedAuthority(ctx, opts.Repo, r.DefaultBranch, r.GoverningCommit, candidate, g)
			}
			if acquireErr != nil {
				r.add("", "governing-evidence", "governing-acquisition", "unsupported", true, acquireErr.Error(), "# Repair landed config, CODEOWNERS and forge visibility; --accept cannot waive governing evidence.\n")
			}

			r.add("", "activation", "activation-prerequisites", "equivalent", false, config.AuthorityActivationPrerequisites, "# Run setup --verify, enable the reviewed runtime plan, and require repository-wide completion before removing redundant protection.\n")
			cfg = candidate.Config
		} else {
			cfg, _, err = config.Load(data)
		}
	}
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		return r, fmt.Errorf("default-branch configuration: %w", err)
	}
	servedDefault := false
	selected := map[string]bool{r.DefaultBranch: true}
	for _, branch := range branches {
		if strings.HasPrefix(branch.Name, "openmerge/") {
			continue
		}
		count := 0
		for _, q := range cfg.Queues {
			if q.MatchesBase(branch.Name, r.DefaultBranch) {
				count++
			}
		}
		if count > 1 {
			return r, fmt.Errorf("overlapping queue base selectors on %s", branch.Name)
		}
		if count == 1 {
			selected[branch.Name] = true
			if branch.Name == r.DefaultBranch {
				servedDefault = true
			}
		}
	}
	for _, base := range opts.Bases {
		if tips[base] == "" || strings.HasPrefix(base, "openmerge/") {
			return r, fmt.Errorf("--base %q must name an existing non-openmerge branch", base)
		}
		selected[base] = true
	}
	for base := range selected {
		r.Bases = append(r.Bases, base)
	}
	slices.Sort(r.Bases)
	if candidate, _, parseErr := config.LoadAuthorityCandidate(data); parseErr == nil && candidate.Authority.Mode == authority.OpenMerge {
		refs := []string{}
		for _, base := range r.Bases {
			refs = append(refs, "refs/heads/"+base)
		}
		for base := range tips {
			for _, family := range []string{"fence", "queue", "green", "verify"} {
				if strings.HasPrefix(base, "openmerge/"+family+"/") {
					refs = append(refs, "refs/heads/"+base)
					break
				}
			}
		}
		slices.Sort(refs)
		if _, protectionErr := c.operatorProtection(ctx, opts.Repo, refs); protectionErr != nil {
			r.add("", "governing-evidence", "governing-protection", "unsupported", true, protectionErr.Error(), "# Obtain complete governing protection evidence; --accept cannot waive this.\n")
		}
	}
	r.add("", "installation", "served-default", "equivalent", !servedDefault, "The default branch must be served by a configured queue; --base only adds audit targets.", "# Serve the default branch in queues before switching policy authority.")
	r.add("", "installation", "bypass-verification", "equivalent", true, "To verify at switch: GitHub must accept the installed OpenMerge App and exactly one team or repository-admin break-glass actor. Read-only inspection cannot prove a future ruleset write will be accepted; bots cannot be break-glass.", "# Install and verify the App and break-glass bypass before switching.")
	r.add("", "installation", "policy-approval", "equivalent", true, "Mandatory owner approval for policy-class changes cannot be waived. Install the release implementing ADR 0007 Stage A and verify this gate at switch.", "# policy_authority: openmerge (only after Stage A and all installation prerequisites)\n# Mandatory policy owner approval applies even without approvals rules.")
	owners, err := c.importOwners(ctx, opts.Repo, tree)
	if err != nil {
		return r, err
	}
	r.coverage(owners)
	writers, err := c.importWriters(ctx, opts.Repo, &r)
	if err != nil {
		return r, err
	}
	for _, rs := range snap.Rulesets {
		r.inventoryBypass("", rs)
	}
	for _, base := range r.Bases {
		baseOwners := owners
		baseTree := tree
		if base != r.DefaultBranch {
			bt, e := c.importTree(ctx, opts.Repo, tips[base])
			if e != nil {
				return r, e
			}
			baseTree = bt
			baseOwners, e = c.importOwners(ctx, opts.Repo, bt)
			if e != nil {
				return r, e
			}
		}
		workflows, err := c.importWorkflows(ctx, opts.Repo, baseTree)
		if err != nil {
			return r, err
		}
		r.compareOwners(base, owners, baseOwners)
		evidence, err := c.importChecks(ctx, opts.Repo, tips[base])
		if err != nil {
			return r, err
		}
		var applicable []Ruleset
		for _, rs := range snap.Rulesets {
			if rs.Enforcement != "active" || rs.Target == "tag" {
				continue
			}
			var cond refConditions
			var raw map[string]json.RawMessage
			known := json.Unmarshal(rs.Conditions, &raw) == nil
			if rs.Target == "push" {
				known = len(rs.Conditions) == 0 || known && len(raw) == 0
			} else {
				known = known && rs.Target == "branch" && json.Unmarshal(rs.Conditions, &cond) == nil && importRefConditionsKnown(raw) && len(cond.RefName.Include) > 0 && validPatterns(cond)
			}
			if !known {
				rules, err := json.Marshal(rs.Rules)
				if err != nil {
					return r, err
				}
				r.add(base, fmt.Sprintf("ruleset:%d", rs.ID), "conditions", "unsupported", true, fmt.Sprintf("Cannot determine ruleset applicability; untranslated requirements cannot be waived. Target=%s; raw conditions=%s; rules=%s.", rs.Target, rs.Conditions, rules), "# Resolve unknown ruleset conditions and translate every applicable rule before switching.")
				continue
			}
			if rs.Target == "branch" && !applies(cond, "refs/heads/"+base, "refs/heads/"+r.DefaultBranch) {
				continue
			}
			applicable = append(applicable, rs)
			for _, rule := range rs.Rules {
				if err := r.translateRule(base, fmt.Sprintf("ruleset:%d", rs.ID), rule, writers, evidence, workflows); err != nil {
					return r, err
				}
			}
		}
		protection, err := c.importClassic(ctx, opts.Repo, base)
		if err != nil {
			return r, err
		}
		if err := r.translateClassic(base, protection, writers, evidence, workflows); err != nil {
			return r, err
		}
		c.importPrincipals(ctx, &r, applicable, protection)
		r.effectiveWriters(base, applicable, protection)
	}
	return r, nil
}

// GitHub bypass actor IDs are identifiers, not privilege ranks.
// https://registry.terraform.io/providers/integrations/github/latest/docs/resources/repository_ruleset#bypass_actors
var importRepositoryRoles = map[int64]string{2: "maintain", 4: "write", 5: "admin"}

var importRoleRank = map[string]int{"read": 1, "triage": 2, "write": 3, "maintain": 4, "admin": 5}

type importWriter struct {
	ID          int64                                `json:"id"`
	RoleName    string                               `json:"role_name"`
	Login       string                               `json:"login"`
	Permissions struct{ Admin, Maintain, Push bool } `json:"permissions"`
}

func (c *setupAPI) importWriters(ctx context.Context, repo string, r *ProtectionReport) ([]string, error) {
	var writers []string
	roles := map[string][]string{}
	for id := range importRepositoryRoles {
		roles[fmt.Sprintf("RepositoryRole:%d", id)] = []string{}
	}
	completeRoles := true
	for page := 1; ; page++ {
		var entries []importWriter
		next, err := c.page(ctx, http.MethodGet, fmt.Sprintf("repos/%s/collaborators?affiliation=all&per_page=100&page=%d", repo, page), nil, &entries)
		if err != nil {
			return nil, c.importError(err, "repository admin access to collaborators")
		}
		if entries == nil {
			return nil, errors.New("incomplete collaborator listing")
		}
		for _, entry := range entries {
			level := importRoleRank[entry.RoleName]
			if entry.ID <= 0 || level == 0 {
				completeRoles = false
			}
			// Built-in role bypasses cover that role and higher roles.
			for id, role := range importRepositoryRoles {
				if level >= importRoleRank[role] {
					key := fmt.Sprintf("RepositoryRole:%d", id)
					roles[key] = append(roles[key], fmt.Sprintf("User:%d", entry.ID))
				}
			}
			if entry.Permissions.Admin || entry.Permissions.Maintain || entry.Permissions.Push {
				writers = append(writers, entry.Login)
			}
		}
		if !next {
			break
		}
	}
	if r.principals == nil {
		r.principals = map[string][]string{}
	}
	if completeRoles {
		for key, members := range roles {
			r.principals[key] = members
		}
	}
	slices.Sort(writers)
	return slices.Compact(writers), nil
}

// Unknown nested ref conditions must not disappear through typed decoding either.
func importRefConditionsKnown(raw map[string]json.RawMessage) bool {
	if len(raw) != 1 || raw["ref_name"] == nil {
		return false
	}
	fields, err := importUnknownFields(raw["ref_name"], struct {
		Include []string `json:"include"`
		Exclude []string `json:"exclude"`
	}{})
	return err == nil && len(fields) == 0
}
