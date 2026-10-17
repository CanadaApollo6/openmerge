package config

import "testing"

// These digests were computed on origin/main before the speculation fields existed.
func TestLegacyDefaultPolicyDigest(t *testing.T) {
	const source = "version: 1\nqueues:\n  - name: default\n    merge_when: [check-success = ci]\n"
	for _, tc := range []struct{ extra, want string }{
		{"", "75c86e0f6a55101c55b9de4d07940a8d98c1f110df2b98aad901cf0557bfa4aa"},
		{"    speculation: {max_depth: 1, min_depth: 1}\n", "75c86e0f6a55101c55b9de4d07940a8d98c1f110df2b98aad901cf0557bfa4aa"},
		{"    batch: {max_size: 8, max_wait: 5m}\n    checks: {retry_failed: 1}\n", "936a86848e84606ba89a7ae1ee56db20d204957d800189accb6a83ea9aa505e8"},
	} {
		cfg, _, err := Load([]byte(source + tc.extra))
		if err != nil {
			t.Fatal(err)
		}
		if got := cfg.Queues[0].Digest(true); got != tc.want {
			t.Errorf("%q digest = %s, want %s", tc.extra, got, tc.want)
		}
		speculative, _, err := Load([]byte(source + "    speculation: {max_depth: 4, min_depth: 2}\n"))
		if err != nil {
			t.Fatal(err)
		}
		if speculative.Queues[0].Digest(true) == tc.want {
			t.Fatal("non-default speculation did not change digest")
		}
	}
}
