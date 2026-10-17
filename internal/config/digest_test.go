package config

import (
	"strings"
	"testing"
)

func TestEffectiveQueuePolicyDigest(t *testing.T) {
	const source = `version: 1
queues:
  - name: default
    merge_when: [check-success = ci]
`
	digest := func(data string, deleteHeadBranch bool) string {
		t.Helper()
		cfg, _, err := Load([]byte(data))
		if err != nil {
			t.Fatal(err)
		}
		return cfg.Queues[0].Digest(deleteHeadBranch)
	}
	want := digest(source, true)
	for _, same := range []string{
		"# comment\n" + source,
		strings.ReplaceAll(source, "check-success = ci", "check-success=ci"),
		source + "    merge_method: squash\n    check_timeout: 60m\n    batch: {max_size: 1, max_wait: 0}\n",
	} {
		if got := digest(same, true); got != want {
			t.Fatal("formatting or explicit defaults changed the effective policy digest")
		}
	}
	for _, different := range []string{
		strings.ReplaceAll(source, "check-success = ci", "check-success = new-ci"),
		source + "    enter_when: [label = ready]\n",
		source + "    merge_method: merge\n",
		source + "    check_timeout: 2h\n",
		source + "    batch: {max_size: 2}\n",
		source + "    batch: {max_wait: 1m}\n",
	} {
		if got := digest(different, true); got == want {
			t.Fatalf("policy change did not change digest: %s", different)
		}
	}
	if digest(source, false) == want {
		t.Fatal("effective head cleanup policy was not included in digest")
	}
}
