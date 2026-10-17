package queue

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestConfigAppliedRequeuesWorkAndRejectsStalePolicy(t *testing.T) {
	for _, phase := range []Phase{Building, Testing} {
		for _, legacy := range []bool{false, true} {
			t.Run(string(phase)+"/legacy="+fmt.Sprint(legacy), func(t *testing.T) {
				cfg := testConfig
				s := batchQueue(1)
				if !legacy {
					cfg.ConfigDigest = strings.Repeat("a", 64)
					s = Apply(s, ConfigApplied{Commit: "policy-a", Digest: cfg.ConfigDigest})
				}
				s = startPlanned(t, cfg, s)
				if phase == Testing {
					s = Apply(s, TestRefBuilt{BatchID: "b1", TestedSHA: "old-tested", Method: Merge, At: epoch})
					s = Apply(s, ChecksCompleted{TestedSHA: "old-tested", Success: true})
				}
				before := cloneState(s)
				changed := Apply(s, ConfigApplied{Commit: "policy-b", Digest: strings.Repeat("b", 64)})
				if !reflect.DeepEqual(s, before) || changed.Active != nil || len(changed.Entries) != 1 || changed.ConfigCommit != "policy-b" {
					t.Fatalf("policy change lost admissions, retained work, or mutated input: %+v", changed)
				}
				if actions := Plan(cfg, changed, epoch); len(actions) != 0 {
					t.Fatalf("stale worker planned under a different policy: %v", actions)
				}
				start := LandingStarted{BatchID: "b1", AttemptID: "stale", FenceToken: "f0", At: epoch}
				for _, action := range Plan(cfg, s, epoch) {
					if CanStart(cfg, changed, action, start) {
						t.Fatal("policy change permitted the old landing")
					}
				}
				cfg.ConfigDigest, cfg.MergeMethod = changed.ConfigDigest, Squash
				build := plannedBuild(t, cfg, changed, epoch)
				if build.BatchID != "b2" || build.Method != Squash || build.BaseSHA != s.BaseSHA || !reflect.DeepEqual(build.PRs, before.Active.PRs) {
					t.Fatalf("policy change did not rebuild the admitted heads: %+v", build)
				}
			})
		}
	}
}

func TestConfigAppliedIdenticalDigestPreservesBatchAndStartedLandingSettles(t *testing.T) {
	cfg := testConfig
	cfg.ConfigDigest = strings.Repeat("a", 64)
	s := Apply(batchQueue(1), ConfigApplied{Commit: "policy-a", Digest: cfg.ConfigDigest})
	s = startPlanned(t, cfg, s)
	s = Apply(s, TestRefBuilt{BatchID: "b1", TestedSHA: "tested", Method: Merge, At: epoch})
	s = Apply(s, ChecksCompleted{TestedSHA: "tested", Success: true})
	land := Plan(cfg, s, epoch)[0].(Land)
	start := LandingStarted{BatchID: "b1", AttemptID: "attempt", FenceToken: "f0", At: epoch}
	unchanged := Apply(s, ConfigApplied{Commit: "unrelated-commit", Digest: cfg.ConfigDigest})
	if !reflect.DeepEqual(s.Active, unchanged.Active) || !CanStart(cfg, unchanged, land, start) || unchanged.ConfigCommit != "policy-a" {
		t.Fatal("identical effective policy invalidated the batch")
	}
	// A worker with a different in-memory policy cannot start even when its
	// planned Land payload would otherwise be identical.
	other := cfg
	other.ConfigDigest = strings.Repeat("b", 64)
	if CanStart(other, s, land, start) {
		t.Fatal("landing start ignored the worker policy digest")
	}
	s = Apply(s, start)
	s = Apply(s, ConfigApplied{Commit: "policy-b", Digest: other.ConfigDigest})
	for _, workerConfig := range []Config{cfg, other} {
		actions := Plan(workerConfig, s, epoch.Add(2*time.Minute))
		if len(actions) != 1 || actions[0].(ResolveLanding).AttemptID != "attempt" {
			t.Fatalf("policy change interfered with landing settlement: %v", actions)
		}
	}
	s = Apply(s, Landed{BatchID: "b1", NewBaseSHA: "tested", NewFence: "f1"})
	if s.Active != nil || len(s.Entries) != 0 || s.BaseSHA != "tested" {
		t.Fatal("started landing did not settle after policy change")
	}
}
