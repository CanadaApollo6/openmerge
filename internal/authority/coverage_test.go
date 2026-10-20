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

func TestCoverageFutureFamilyOverlap(t *testing.T) {
	for _, tt := range []struct {
		name, include string
		excludes      []string
		want          bool
	}{
		{"star family", "refs/heads/openmerge/*/*", nil, true},
		{"recursive family", "refs/heads/openmerge/**/*", nil, true},
		{"recursive middle", "refs/heads/**/gr*/*", nil, true},
		{"question", "refs/heads/openmerge/gr?en/*", nil, true},
		{"middle star", "refs/heads/openmerge/g*en/*", nil, true},
		{"prefix star", "refs/heads/openmerge/gr*/*", nil, true},
		{"suffix star", "refs/heads/openmerge/*een/*", nil, true},
		{"terminal globstar", "refs/heads/openmerge/gr*/**", nil, true},
		{"nested only", "refs/heads/openmerge/gr*/*/*", nil, true},
		{"all", "~ALL", nil, true},
		{"default", "~DEFAULT_BRANCH", nil, false},
		{"different family", "refs/heads/openmerge/qu*/*", nil, false},
		{"wrong depth", "refs/heads/openmerge/*", nil, false},
		{"terminal globstar wrong depth", "refs/heads/openmerge/**", nil, false},
		{"root only", "refs/heads/openmerge/green", nil, false},
		{"other namespace", "refs/heads/other/**/*", nil, false},
		{"other exclusion", "refs/heads/openmerge/gr*/*", []string{"refs/heads/openmerge/queue/**/*"}, true},
		{"partial exclusion", "~ALL", []string{"refs/heads/openmerge/green/*"}, true},
		{"recursive exclusion", "~ALL", []string{"refs/heads/openmerge/green/**/*"}, false},
		{"wildcard exclusion", "~ALL", []string{"refs/heads/openmerge/gr?en/**/*"}, false},
		{"middle exclusion", "~ALL", []string{"refs/heads/openmerge/g*en/**/*"}, false},
		{"ancestor exclusion", "~ALL", []string{"refs/heads/openmerge/**/*"}, false},
		{"all exclusion", "~ALL", []string{"~ALL"}, false},
		{"identical cancellation", "refs/heads/openmerge/gr*/*", []string{"refs/heads/openmerge/gr*/*"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			coverage := authority.RefCoverage{Includes: []string{tt.include}, Excludes: tt.excludes}
			got, err := coverage.MayContainRefUnder("refs/heads/openmerge/green/", "refs/heads/main")
			if err != nil || got != tt.want {
				t.Fatalf("overlap = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
	for _, pattern := range []string{"refs/heads/openmerge/[fg]*/*", "refs/heads/openmerge/gr**/*", `refs/heads/openmerge/g\*/*`, "refs/heads/{green,queue}/*"} {
		for _, coverage := range []authority.RefCoverage{
			{Includes: []string{pattern}},
			{Includes: []string{"~ALL"}, Excludes: []string{pattern}},
			{Includes: []string{pattern}, Excludes: []string{"~ALL"}},
		} {
			if _, err := coverage.MayContainRefUnder("refs/heads/openmerge/green/", "refs/heads/main"); err == nil {
				t.Errorf("accepted undecidable pattern: %+v", coverage)
			}
		}
	}
}
