package config

import "testing"

func TestAdaptiveTargetValidationSchemaAndDigest(t *testing.T) {
	prefix := "version: 1\nqueues:\n- name: main\n  merge_when: [check-success = ci]\n"
	base, _, err := Load([]byte(prefix))
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"0.8", "0.81", "1e-3", "0", "1", "-0.1", "1.1", ".nan", ".inf", "'0.8'", "false"} {
		raw := []byte(prefix + "  batch: {target_pass_rate: " + target + "}\n")
		cfg, _, err := Load(raw)
		valid := target == "0.8" || target == "0.81" || target == "1e-3"
		if (err == nil) != valid {
			t.Fatalf("target %s: %v", target, err)
		}
		assertSchemaAgreement(t, raw, err)
		if valid && (cfg.Queues[0].QueueConfig().BatchTargetPassRate == 0 || cfg.Queues[0].Digest(true) == base.Queues[0].Digest(true)) {
			t.Fatal("target missing from policy/digest")
		}
	}
	explicit, _, err := Load([]byte(prefix + "  speculation: {min_depth: 1, max_depth: 1}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Queues[0].Digest(true) != base.Queues[0].Digest(true) {
		t.Fatal("default bounds changed digest")
	}
}

// The backlog guard is on by default: omitting it or spelling the default keeps
// the digest; only false changes policy, and it requires a target.
func TestBacklogGuardValidationSchemaAndDigest(t *testing.T) {
	prefix := "version: 1\nqueues:\n- name: main\n  merge_when: [check-success = ci]\n"
	load := func(batch string) (Config, error) {
		raw := []byte(prefix + "  batch: {" + batch + "}\n")
		cfg, _, err := Load(raw)
		assertSchemaAgreement(t, raw, err)
		return cfg, err
	}
	target, err := load("target_pass_rate: 0.8")
	if err != nil {
		t.Fatal(err)
	}
	if target.Queues[0].QueueConfig().BatchBacklogGuardOff {
		t.Fatal("guard off by default")
	}
	on, err := load("target_pass_rate: 0.8, backlog_guard: true")
	if err != nil || on.Queues[0].Digest(true) != target.Queues[0].Digest(true) {
		t.Fatalf("explicit default changed digest: %v", err)
	}
	off, err := load("target_pass_rate: 0.8, backlog_guard: false")
	if err != nil || !off.Queues[0].QueueConfig().BatchBacklogGuardOff || off.Queues[0].Digest(true) == target.Queues[0].Digest(true) {
		t.Fatalf("disabled guard missing from policy/digest: %v", err)
	}
	for _, bad := range []string{"backlog_guard: true", "backlog_guard: false", "target_pass_rate: 0.8, backlog_guard: 1", "target_pass_rate: 0.8, backlog_guard: 'false'", "target_pass_rate: 0.8, backlog_guard: null"} {
		if _, err := load(bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
