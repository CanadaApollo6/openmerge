package authority

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// RefCoverage is exact GitHub ref_name coverage, including future bases. The
// OpenMerge namespace is always excluded, independently of queue selectors.
type RefCoverage struct {
	Includes []string
	Excludes []string
}

// CompileRefCoverage translates the supported slash-aware selector grammar.
// A terminal ** crosses directories in config, but not in GitHub fnmatch, so
// it becomes **/*. Embedded globstars are rejected rather than approximated.
// Brackets are literals in config and cannot be Git ref characters; reject
// them, escapes, and extended fnmatch syntax instead of changing their meaning.
func CompileRefCoverage(defaultBranch string, selectors []BaseSelector) (RefCoverage, error) {
	if defaultBranch == "" || strings.HasPrefix(defaultBranch, "openmerge/") || strings.ContainsAny(defaultBranch, "*?[]\\{}\x00\r\n") {
		return RefCoverage{}, fmt.Errorf("invalid default branch")
	}
	canonical, err := canonicalSelectors(selectors)
	if err != nil {
		return RefCoverage{}, err
	}
	result := RefCoverage{Includes: []string{"refs/heads/" + defaultBranch}, Excludes: []string{"refs/heads/openmerge/**/*"}}
	for _, s := range canonical {
		if s.DefaultBranch {
			continue
		}
		pattern, err := selectorPattern(s.Pattern)
		if err != nil {
			return RefCoverage{}, err
		}
		result.Includes = append(result.Includes, "refs/heads/"+pattern)
	}
	slices.Sort(result.Includes)
	result.Includes = slices.Compact(result.Includes)
	return result, nil
}

func selectorPattern(pattern string) (string, error) {
	if pattern == "" || strings.HasPrefix(pattern, "refs/") || strings.ContainsAny(pattern, "[]\\{}\x00\r\n") {
		return "", fmt.Errorf("unsupported base selector %q", pattern)
	}
	parts := strings.Split(pattern, "/")
	for i, part := range parts {
		if part == "" || strings.Contains(part, "**") && part != "**" {
			return "", fmt.Errorf("unsupported base selector %q", pattern)
		}
		if part == "**" && i == len(parts)-1 {
			parts[i] = "**/*"
		}
	}
	return strings.Join(parts, "/"), nil
}

// CompileProtectedRefCoverage uses the same selector compiler for the managed
// namespaces. Keep paired segment/recursive patterns even where they overlap.
func CompileProtectedRefCoverage() RefCoverage {
	result := RefCoverage{Includes: []string{}, Excludes: []string{}}
	for _, family := range []string{"fence", "queue", "green", "verify"} {
		for _, suffix := range []string{"*", "**"} {
			pattern, err := selectorPattern("openmerge/" + family + "/" + suffix)
			if err != nil {
				panic(err)
			}
			result.Includes = append(result.Includes, "refs/heads/"+pattern)
		}
	}
	return result
}

// MatchRefPattern implements only the fnmatch subset used by the compiler and
// understood by the local validator. Unsupported external patterns fail closed.
// **/ spans zero or more directories; a terminal ** is segment-local.
func MatchRefPattern(pattern, ref, defaultRef string) (bool, error) {
	switch pattern {
	case "~ALL":
		return true, nil
	case "~DEFAULT_BRANCH":
		return ref == defaultRef, nil
	}
	return MatchPathPattern(pattern, ref)
}

// MatchPathPattern matches pathname-aware fnmatch without ref-only sentinels.
func MatchPathPattern(pattern, path string) (bool, error) {
	if strings.ContainsAny(pattern, "[]\\{}\x00\r\n") {
		return false, fmt.Errorf("unsupported GitHub pattern %q", pattern)
	}
	var out strings.Builder
	out.WriteString(`\A(?s:`)
	parts := strings.Split(pattern, "/")
	for i, part := range parts {
		if part == "**" && i < len(parts)-1 {
			out.WriteString(`(?:.*/)?`)
			continue
		}
		if strings.Contains(part, "**") && part != "**" {
			return false, fmt.Errorf("unsupported GitHub pattern %q", pattern)
		}
		for _, c := range part {
			switch c {
			case '*':
				out.WriteString(`[^/]*`)
			case '?':
				out.WriteString(`[^/]`)
			default:
				out.WriteString(regexp.QuoteMeta(string(c)))
			}
		}
		if i < len(parts)-1 {
			out.WriteByte('/')
		}
	}
	out.WriteString(`)\z`)
	re, err := regexp.Compile(out.String())
	if err != nil {
		return false, err
	}
	return re.MatchString(path), nil
}

// ContainsRef tests include-minus-exclude coverage without dropping exclusions.
func (c RefCoverage) ContainsRef(ref, defaultRef string) (bool, error) {
	included := false
	for _, p := range c.Includes {
		ok, err := MatchRefPattern(p, ref, defaultRef)
		if err != nil {
			return false, err
		}
		included = included || ok
	}
	for _, p := range c.Excludes {
		ok, err := MatchRefPattern(p, ref, defaultRef)
		if err != nil {
			return false, err
		}
		if ok {
			return false, nil
		}
	}
	return included, nil
}

// MayContainRefUnder conservatively intersects include-minus-exclude coverage
// with every nonempty descendant of a literal ref prefix. It uses the same
// segment matcher as ContainsRef. Unknown grammar returns an error, never a
// disjointness proof. Exclusions prove absence only when they cover the entire
// prefix, or exactly cancel an include; partial exclusions retain the overlap.
func (c RefCoverage) MayContainRefUnder(prefix, defaultRef string) (bool, error) {
	for _, patterns := range [][]string{c.Includes, c.Excludes} {
		for _, pattern := range patterns {
			if _, err := MatchRefPattern(pattern, "", defaultRef); err != nil {
				return false, err
			}
		}
	}
	for _, exclude := range c.Excludes {
		if exclude == "~ALL" {
			return false, nil
		}
		parts, states := refPrefixStates(exclude, prefix, defaultRef)
		for state := range states {
			if strings.Join(parts[state:], "/") == "**/*" {
				return false, nil
			}
		}
	}
	for _, include := range c.Includes {
		if slices.Contains(c.Excludes, include) {
			continue
		}
		parts, states := refPrefixStates(include, prefix, defaultRef)
		for state := range states {
			if state < len(parts) {
				return true, nil
			}
		}
	}
	return false, nil
}

// States are offsets in a pathname glob after consuming the fixed prefix.
// Nonterminal ** has an epsilon transition to the next segment and a loop
// consuming any segment. A terminal ** behaves like *, as in MatchPathPattern.
func refPrefixStates(pattern, prefix, defaultRef string) ([]string, map[int]bool) {
	switch pattern {
	case "~ALL":
		pattern = "**/*"
	case "~DEFAULT_BRANCH":
		pattern = defaultRef
	}
	parts := strings.Split(pattern, "/")
	closure := func(states map[int]bool) {
		for i, part := range parts {
			if states[i] && part == "**" && i < len(parts)-1 {
				states[i+1] = true
			}
		}
	}
	states := map[int]bool{0: true}
	closure(states)
	for _, segment := range strings.Split(strings.TrimSuffix(prefix, "/"), "/") {
		next := map[int]bool{}
		for state := range states {
			if state == len(parts) {
				continue
			}
			if parts[state] == "**" && state < len(parts)-1 {
				next[state] = true
			} else if matches, _ := MatchPathPattern(parts[state], segment); matches {
				next[state+1] = true
			}
		}
		closure(next)
		states = next
	}
	return parts, states
}

// ValidateCoverageTransition proves containment constructively for setup's
// reviewed old/proposed union. Installed must retain every current selector,
// and may add only exactly compiled proposed selectors. Exclusions cannot
// change. PR 3b must separately assert proposed coverage is installed and retain
// its actor and App-only ref checks; containment alone is not installation.
// Extra returns proposed patterns added during this transition for reporting.
func ValidateCoverageTransition(current, proposed, installed RefCoverage) (extra []string, err error) {
	if !samePatterns(current.Excludes, proposed.Excludes) || !samePatterns(current.Excludes, installed.Excludes) {
		return nil, fmt.Errorf("coverage exclusions changed")
	}
	for _, p := range current.Includes {
		if !slices.Contains(installed.Includes, p) {
			return nil, fmt.Errorf("current coverage lost: %s", p)
		}
	}
	for _, p := range installed.Includes {
		if slices.Contains(current.Includes, p) {
			continue
		}
		if !slices.Contains(proposed.Includes, p) {
			return nil, fmt.Errorf("unreviewed extra coverage: %s", p)
		}
		extra = append(extra, p)
	}
	slices.Sort(extra)
	return slices.Compact(extra), nil
}

func samePatterns(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(slices.Compact(a), slices.Compact(b))
}
