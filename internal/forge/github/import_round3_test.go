package github

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
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
					for _, gap := range []string{"Stage A has no requirement form for approval from the effective owners of every owned file", "only policy files get mandatory owner approval"} {
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
