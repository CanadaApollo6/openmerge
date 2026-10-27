package config

import (
	"fmt"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
)

func TestProvenanceAttributesRejectedOutsideCandidate(t *testing.T) {
	for _, atom := range []string{"provenance = agent", "#agent-commits > 0", "#unresolved-checkpoints = 0", "checkpoint-present"} {
		for _, where := range []string{"enter_when", "merge_when", "select_when", "approvals"} {
			t.Run(fmt.Sprintf("%s/%s", where, atom), func(t *testing.T) {
				field := fmt.Sprintf("  %s: [{or: [draft, {not: %q}]}]\n", where, atom)
				if where == "approvals" {
					field = fmt.Sprintf("  approvals: {default: any_writer, rules: [{when: {or: [draft, {not: %q}]}, require: none}]}\n", atom)
				}
				source := "version: 1\nqueues:\n- name: main\n  merge_when: [check-success = ci]\n"
				if where == "merge_when" {
					source = "version: 1\nqueues:\n- name: main\n"
				}
				_, _, err := Load([]byte(source + field))
				if err == nil {
					t.Fatal("provenance bypass accepted")
				}
				assertSchemaAgreement(t, []byte(source+field), err)
			})
		}
		if _, err := condition.Parse(atom); err == nil {
			t.Fatal("public grammar widened", atom)
		}
	}
}

func TestProvenanceSectionRejectedByPublicLoaderAndSchema(t *testing.T) {
	source := []byte("version: 1\nqueues:\n- name: main\n  merge_when: [check-success = ci]\n  provenance: {requirements: [{when: [provenance = agent], require: {conditions: [checkpoint-present]}}]}\n")
	_, _, err := Load(source)
	if err == nil {
		t.Fatal("public loader accepted candidate section")
	}
	assertSchemaAgreement(t, source, err)
}
