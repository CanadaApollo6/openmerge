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

func TestConfigAppliedReducedBatchLimitReformsGroups(t *testing.T) {
	for _, window := range []string{"building", "testing", "base-before", "base-after", "pending-bisection", "landing-abandoned"} {
		t.Run(window, func(t *testing.T) {
			cfg := batchConfig(2)
			cfg.ConfigDigest = strings.Repeat("a", 64)
			s := Apply(batchQueue(4), ConfigApplied{Commit: "policy-a", Digest: cfg.ConfigDigest})
			if window == "pending-bisection" {
				cfg.BatchMaxSize = 4
			}
			s = startPlanned(t, cfg, s)
			if window != "building" {
				s = Apply(s, TestRefBuilt{BatchID: "b1", TestedSHA: "tested-b1", Method: Merge, At: epoch})
			}
			switch window {
			case "base-before":
				s = Apply(s, BaseMoved{SHA: "base2"})
			case "pending-bisection":
				s = Apply(s, ChecksCompleted{TestedSHA: "tested-b1", Success: false})
			case "landing-abandoned":
				s = Apply(s, LandingStarted{BatchID: "b1", AttemptID: "attempt", FenceToken: "f0", At: epoch})
			}
			admissions := append([]Entry(nil), s.Entries...)
			cfg.ConfigDigest, cfg.BatchMaxSize = strings.Repeat("b", 64), 1
			s = Apply(s, ConfigApplied{Commit: "policy-b", Digest: cfg.ConfigDigest})
			if window == "base-after" {
				s = Apply(s, BaseMoved{SHA: "base2"})
			}
			if window == "landing-abandoned" {
				s = Apply(s, BaseMoved{SHA: "base2"})
				s = Apply(s, LandingAbandoned{BatchID: "b1", NewFence: "f1"})
			}
			if !reflect.DeepEqual(s.Entries, admissions) {
				t.Fatal("configuration change replaced or reordered admissions")
			}
			for _, entry := range admissions {
				build := plannedBuild(t, cfg, s, epoch)
				if !reflect.DeepEqual(build.PRs, []BatchPR{{PR: entry.PR, HeadSHA: entry.HeadSHA}}) || build.BaseSHA != s.BaseSHA {
					t.Fatalf("rebuild ignored reduced size or admission order: %+v", build)
				}
				s = startPlanned(t, cfg, s)
				s = Apply(s, TestRefBuilt{BatchID: build.BatchID, TestedSHA: "tested-" + build.BatchID, Method: Merge, At: epoch})
				s = Apply(s, LandingStarted{BatchID: build.BatchID, AttemptID: "attempt-" + build.BatchID, FenceToken: s.Fence, At: epoch})
				s = Apply(s, Landed{BatchID: build.BatchID, NewBaseSHA: "tested-" + build.BatchID, NewFence: "f-" + build.BatchID})
			}
			if len(s.Entries) != 0 {
				t.Fatal("configuration change lost progress")
			}
		})
	}
}
