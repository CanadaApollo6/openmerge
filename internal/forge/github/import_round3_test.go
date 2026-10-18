package github

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestImportGeneralOwnerReviewUnsupported(t *testing.T) {
	for _, source := range []string{"classic", "ruleset:1"} {
		for _, stale := range []bool{false, true} {
			for _, count := range []int{0, 2} {
				t.Run(fmt.Sprintf("%s/stale=%t/count=%d", source, stale, count), func(t *testing.T) {
					r := ProtectionReport{}
					if source == "classic" {
						r.reviews("main", source, importReviews{Owners: true, Stale: stale, Count: count}, []string{"alice", "bob"})
					} else if err := r.translateRule("main", source, RepositoryRule{Type: "pull_request", Parameters: json.RawMessage(fmt.Sprintf(`{"require_code_owner_review":true,"dismiss_stale_reviews_on_push":%t,"required_approving_review_count":%d}`, stale, count))}, []string{"alice", "bob"}, nil, nil); err != nil {
						t.Fatal(err)
					}
					item := findImportItem(t, r, "main", source, "owner-review")
					if item.Classification != "unsupported" {
						t.Fatalf("unenforceable general owner review: %+v", item)
					}
					for _, gap := range []string{"ADR 0007's `{code_owners: true}` rule requirement exists", "not activated until PR 11b publishes the loader and enables enforcement", "only policy files get mandatory owner approval"} {
						if !strings.Contains(item.Message, gap) {
							t.Fatalf("missing adopter guidance %q: %+v", gap, item)
						}
					}
					if err := r.accept(nil); err != nil {
						t.Fatal(err)
					}
					if r.Ready || !strings.Contains(item.Fragment, "no equivalent configuration") {
						t.Fatalf("owner gap silently cleared: %+v", r)
					}
					if count == 0 && strings.Contains(r.ProposedConfig, "approvals:") {
						t.Fatalf("owner-only protection drafted a substitute approval requirement:\n%s", r.ProposedConfig)
					}
					if strings.Contains(r.ProposedConfig, "Stage A owner requirement:") {
						t.Fatal("draft implies an enforceable general owner requirement")
					}
				})
			}
		}
	}
}

// The evaluator lives in PR 55; this pins the fragment needed when a human
// author rule definitely matches an empty diff and suppresses the default.
func TestImportEmptyDiffWithAuthorNoneKeepsIndependentCount(t *testing.T) {
	r := ProtectionReport{}
	r.reviews("main", "test", importReviews{Count: 3}, []string{"alice", "bob", "carol"})
	if err := r.accept(nil); err != nil {
		t.Fatal(err)
	}
	var draft struct {
		Approvals struct {
			Default map[string]any `yaml:"default"`
			Rules   []struct {
				When    map[string][]string `yaml:"when"`
				Require any                 `yaml:"require"`
			} `yaml:"rules"`
		} `yaml:"approvals"`
	}
	// Model an operator adding a rule that suppresses the default on human
	// changes, including a complete empty diff. The imported rule must remain
	// paths **: it is unknown on that diff and enforced as if matched.
	fragment := strings.Replace(r.ProposedConfig, "    rules:\n", "    rules:\n        - when: {authors: [human]}\n          require: none\n", 1)
	if err := yaml.Unmarshal([]byte(fragment), &draft); err != nil {
		t.Fatal(err)
	}
	if len(draft.Approvals.Rules) != 2 {
		t.Fatalf("lost independent count rule: %+v", draft)
	}
	author, count := draft.Approvals.Rules[0], draft.Approvals.Rules[1]
	want, err := yaml.Marshal(map[string]any{"group": "imported_reviewers", "count": 3})
	if err != nil {
		t.Fatal(err)
	}
	for _, requirement := range []any{draft.Approvals.Default, count.Require} {
		got, err := yaml.Marshal(requirement)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("lost count: %s", got)
		}
	}
	if len(author.When["authors"]) != 1 || author.When["authors"][0] != "human" || author.Require != "none" {
		t.Fatalf("missing author none counterexample: %+v", author)
	}
	if len(count.When) != 1 || len(count.When["paths"]) != 1 || count.When["paths"][0] != "**" {
		t.Fatalf("empty diff requires an independently enforced unknown paths rule: %+v", count)
	}
	if strings.Contains(fragment, "code_owners:") {
		t.Fatal("owner rule must wait for PR 11b")
	}
}
