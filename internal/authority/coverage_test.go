package authority_test

import (
	"slices"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/config"
)

func TestCompiledCoverageMatchesQueueGrammar(t *testing.T) {
	selectors := []string{"", "main", "release/*", "release/**", "**/stable", "team/**/release/?", "**"}
	branches := []string{"main", "other", "release/one", "release/one/two", "release/one/two/three", "stable", "x/stable", "team/release/a", "team/x/release/a", "openmerge/queue/x", "openmerge/fence/main"}
	for _, selector := range selectors {
		c, err := authority.CompileRefCoverage("main", []authority.BaseSelector{{DefaultBranch: selector == "", Pattern: selector}})
		if err != nil {
			t.Fatal(err)
		}
		for _, branch := range branches {
			got, err := c.ContainsRef("refs/heads/"+branch, "refs/heads/main")
			if err != nil {
				t.Fatal(err)
			}
			want := (branch == "main" || (config.Queue{Base: selector}).MatchesBase(branch, "main")) && !slices.Contains([]string{"openmerge/queue/x", "openmerge/fence/main"}, branch)
			if got != want {
				t.Errorf("%q branch %q: got %v want %v", selector, branch, got, want)
			}
		}
	}
}

func TestCoverageRejectsUnprovenSelectors(t *testing.T) {
	for _, selector := range []string{"qa**", "a/**b/c", "x***", "x/[ab]", `x/\*`, "x/{a,b}", "refs/heads/main", "x//y"} {
		if _, err := authority.CompileRefCoverage("main", []authority.BaseSelector{{Pattern: selector}}); err == nil {
			t.Errorf("accepted %q", selector)
		}
	}
}

func TestCoverageTransitionPreservesCurrentAndReportsUnion(t *testing.T) {
	current, _ := authority.CompileRefCoverage("main", []authority.BaseSelector{{Pattern: "release/*"}})
	proposed, _ := authority.CompileRefCoverage("main", []authority.BaseSelector{{Pattern: "next/**"}})
	union := authority.RefCoverage{Includes: append(slices.Clone(current.Includes), "refs/heads/next/**/*"), Excludes: slices.Clone(current.Excludes)}
	extra, err := authority.ValidateCoverageTransition(current, proposed, union)
	if err != nil || !slices.Equal(extra, []string{"refs/heads/next/**/*"}) {
		t.Fatalf("%v %v", extra, err)
	}
	if _, err := authority.ValidateCoverageTransition(current, proposed, proposed); err == nil {
		t.Fatal("accepted loss of current coverage")
	}
	union.Excludes = nil
	if _, err := authority.ValidateCoverageTransition(current, proposed, union); err == nil {
		t.Fatal("accepted namespace relaxation")
	}
}

func TestTransitionExclusionsAreSets(t *testing.T) {
	current := authority.RefCoverage{Includes: []string{"a"}, Excludes: []string{"x", "y"}}
	proposed := authority.RefCoverage{Includes: []string{"a", "b"}, Excludes: []string{"y", "x"}}
	installed := authority.RefCoverage{Includes: []string{"a", "b"}, Excludes: []string{"y", "x"}}
	if _, err := authority.ValidateCoverageTransition(current, proposed, installed); err != nil {
		t.Fatal(err)
	}
}
