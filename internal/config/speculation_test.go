package config

import (
	"testing"
)

func TestSpeculationBoundsDefaultsAndDigest(t *testing.T) {
	prefix := "version: 1\nqueues:\n  - name: main\n    merge_when: [check-success = ci]\n"
	base, _, err := Load([]byte(prefix))
	if err != nil {
		t.Fatal(err)
	}
	if base.Queues[0].Speculation != (Speculation{1, 1}) {
		t.Fatal("wrong default")
	}
	var digest string
	for _, tc := range []struct {
		yaml  string
		valid bool
	}{
		{"{max_depth: 4}", true}, {"{max_depth: 32, min_depth: 32}", true}, {"{max_depth: 0}", false}, {"{min_depth: 2}", false}, {"{max_depth: 33}", false}, {"{max_depth: 2, min_depth: 3}", false}, {"{max_depth: 4, min_depth: -1}", false}, {"{max_depth: '4'}", false}, {"{max_depth: 4, unknown: 1}", false},
	} {
		cfg, _, err := Load([]byte(prefix + "    speculation: " + tc.yaml + "\n"))
		if (err == nil) != tc.valid {
			t.Fatalf("%s: %v", tc.yaml, err)
		}
		if tc.valid {
			q := cfg.Queues[0]
			if q.QueueConfig().RetryLimit() != 1 || q.QueueConfig().ChecksRetryFailed != 1 {
				t.Fatal("speculative queue did not enable retries")
			}
			if q.Digest(true) == base.Queues[0].Digest(true) || q.Digest(true) == digest {
				t.Fatal("speculation policy missing from digest")
			}
			digest = q.Digest(true)
		}
	}
}
