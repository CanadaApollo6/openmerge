package github

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
)

type importOwnerRule struct {
	Pattern string
	Owners  []string
}
type importOwners struct {
	Path    string
	Rules   []importOwnerRule
	Invalid []string
}

func (c *setupAPI) importOwners(ctx context.Context, repo string, tree importTree) (importOwners, error) {
	for _, path := range []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"} {
		sha, exists := tree[path]
		if !exists {
			continue
		}
		data, err := c.importBlob(ctx, repo, sha)
		if err != nil {
			return importOwners{}, err
		}
		return parseImportOwners(path, string(data)), nil
	}
	return importOwners{}, nil
}
func parseImportOwners(path, data string) importOwners {
	result := importOwners{Path: path}
	if len(data) > 3<<20 {
		result.Invalid = append(result.Invalid, "CODEOWNERS exceeds GitHub's 3 MB limit")
		return result
	}
	for index, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		pattern := fields[0]
		if strings.ContainsAny(pattern, "\\[]!") {
			result.Invalid = append(result.Invalid, fmt.Sprintf("line %d: unsupported/invalid pattern %q", index+1, pattern))
			continue
		}
		owners := []string{}
		for _, owner := range fields[1:] {
			if strings.HasPrefix(owner, "#") {
				break
			}
			owners = append(owners, owner)
		}
		slices.Sort(owners)
		owners = slices.Compact(owners)
		result.Rules = append(result.Rules, importOwnerRule{pattern, owners})
	}
	return result
}

// CODEOWNERS keeps gitignore-style anchoring and directory inheritance, unlike
// whole-path queue globs. A later empty owner list explicitly removes ownership.
func ownerPatternMatches(pattern, path string) bool {
	anchored := strings.HasPrefix(pattern, "/") || strings.Contains(strings.TrimSuffix(pattern, "/"), "/")
	pattern = strings.Trim(pattern, "/")
	glob, err := condition.CompileGlob(pattern)
	if err != nil {
		return false
	}
	parts := strings.Split(path, "/")
	for n := len(parts); n > 0; n-- {
		candidate := strings.Join(parts[:n], "/")
		if !anchored {
			candidate = parts[n-1]
		}
		if glob.MatchString(candidate) {
			return true
		}
	}
	return false
}
func (o importOwners) effective(path string) []string {
	var owners []string
	for _, rule := range o.Rules {
		if ownerPatternMatches(rule.Pattern, path) {
			owners = rule.Owners
		}
	}
	return owners
}
func (r *ProtectionReport) coverage(o importOwners) {
	paths := []string{".openmerge.yml", ".openmerge.yaml", ".github/openmerge.yml", ".github/openmerge.yaml", ".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"}
	for _, path := range paths {
		owners := o.effective(path)
		resolvable := len(o.Invalid) == 0 && slices.ContainsFunc(owners, func(owner string) bool { return strings.HasPrefix(owner, "@") && len(owner) > 1 })
		r.add("", "governing-CODEOWNERS", "coverage-"+path, "equivalent", !resolvable, fmt.Sprintf("%s at %s: effective owners for %s = %v. OpenMerge config paths and CODEOWNERS must have resolvable owners; emails alone do not resolve Stage A owner requirements. Team visibility and eligibility must be verified at switch.", o.Path, r.GoverningCommit, path, owners), "# CODEOWNERS must cover "+path+" with eligible user/team owners.")
	}
	if len(o.Invalid) > 0 {
		r.add("", "governing-CODEOWNERS", "owners-syntax", "unsupported", true, strings.Join(o.Invalid, "; "), "# Repair CODEOWNERS syntax; incomplete ownership cannot satisfy installation coverage.")
	}
}
func (r *ProtectionReport) compareOwners(base string, governing, target importOwners) {
	if len(target.Invalid) > 0 {
		r.unsupported(base, "CODEOWNERS", "base-owners-syntax", strings.Join(target.Invalid, "; "))
	}
	// Compare the complete ordered rule sets, not just entries for config files.
	// Identical effective rules in a different file location are still equivalent.
	lines := func(o importOwners) []string {
		result := []string{}
		for _, rule := range o.Rules {
			result = append(result, rule.Pattern+" "+strings.Join(rule.Owners, " "))
		}
		return result
	}
	a, b := lines(governing), lines(target)
	if slices.Equal(a, b) {
		r.add(base, "CODEOWNERS", "owners-comparison", "equivalent", false, fmt.Sprintf("Effective CODEOWNERS rules on %s (%s) match the governing default branch (%s).", base, target.Path, governing.Path), "# Owner requirements use governing CODEOWNERS.")
		return
	}
	var diffs strings.Builder
	fmt.Fprintf(&diffs, "CODEOWNERS differs: governing %s vs %s on %s. Every differing ordered entry follows; equivalence cannot be established.\n", governing.Path, target.Path, base)
	for i := 0; i < max(len(a), len(b)); i++ {
		left, right := "<absent>", "<absent>"
		if i < len(a) {
			left = a[i]
		}
		if i < len(b) {
			right = b[i]
		}
		if left != right {
			fmt.Fprintf(&diffs, "entry %d: default=%q; base=%q\n", i+1, left, right)
		}
	}
	r.add(base, "CODEOWNERS", "owners-comparison", "unsupported", false, diffs.String(), "# Reconcile base CODEOWNERS with governing owners, or explicitly accept the owner-policy difference.")
}
