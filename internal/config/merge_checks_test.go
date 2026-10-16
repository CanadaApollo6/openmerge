package config

import (
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
)

func TestMergeRequiresPositiveNamedSuccessInConjunction(t *testing.T) {
	for _, fields := range []string{
		"",
		"    merge_when: []\n",
		"    merge_when: [base = main]\n",
		"    enter_when: [check-success = ci]\n    merge_when: [{not: draft}]\n",
		"    merge_when: [{or: [check-success = ci, base = main]}]\n",
		"    merge_when: [{not: check-success = ci}]\n",
		"    merge_when: ['#check-success >= 0']\n",
		"    merge_when: ['#check-success >= 1']\n",
		"    merge_when: [check-failure = ci]\n",
		"    merge_when: [check-pending = ci]\n",
		"    merge_when: [-check-success = ci]\n",
		"    merge_when: [check-success != ci]\n",
		"    merge_when: [check-success glob ci-*]\n",
		"    merge_when: [check-success ~= ci]\n",
		"    merge_when: [{not: {not: check-success = ci}}]\n",
		"    merge_when: [{and: [{or: [check-success = ci, check-success = fallback]}]}]\n",
	} {
		t.Run(fields, func(t *testing.T) {
			cfg, _, err := Load([]byte("version: 1\nqueues:\n  - name: release\n" + fields))
			if err == nil || len(cfg.Queues) != 0 {
				t.Fatalf("unsafe merge condition accepted: %+v", cfg)
			}
			if !strings.Contains(err.Error(), `queue "release"`) || !strings.Contains(err.Error(), "merge_when: [check-success = ci]") {
				t.Fatalf("missing actionable check validation: %v", err)
			}
		})
	}
}

func TestMergeRequiredCheckAllowsNestedConjunctionAndExtraOR(t *testing.T) {
	for _, rule := range []string{
		"check-success = ci",
		"{and: [check-success = ci]}",
		"{and: [{and: [check-success = ci]}, {or: [base = main, author = writer]}]}",
		"check-success = ci, {or: [check-success = linux, check-success = fallback]}",
	} {
		t.Run(rule, func(t *testing.T) {
			cfg, _, err := Load([]byte("version: 1\nqueues:\n  - name: release\n    merge_when: [" + rule + "]\n"))
			if err != nil {
				t.Fatal(err)
			}
			expression := cfg.Queues[0].MergeWhen
			for _, checks := range []condition.Snapshot{
				condition.CheckResults(nil, nil, nil),
				condition.CheckResults(nil, []string{"ci", "linux", "fallback"}, nil),
				condition.CheckResults(nil, nil, []string{"ci", "linux", "fallback"}),
			} {
				checks["base"], checks["author"] = condition.String("main"), condition.String("writer")
				if ok, _ := expression.Evaluate(checks); ok {
					t.Fatalf("merge authorized without successful required check: %+v", checks)
				}
			}
			passing := condition.CheckResults([]string{"ci", "fallback"}, []string{"linux"}, nil)
			passing["base"] = condition.String("main")
			if ok, failed := expression.Evaluate(passing); !ok {
				t.Fatalf("passing required check rejected: %+v", failed)
			}
		})
	}
}
