package queue

import (
	"reflect"
	"testing"
	"time"
)

func TestStatusesPinTheObservedOrAdmittedHead(t *testing.T) {
	t.Run("blocked observation", func(t *testing.T) {
		s := applySerial(State{}, PRObserved{PR: 1, HeadSHA: "observed", Eligible: true})
		s = applySerial(s, EnqueueRequested{PR: 1})
		report := onlyAction[ReportStatus](t, Plan(testConfig, s, epoch))
		if report.HeadSHA != "observed" || report.Status != "blocked" || report.Reason != "untrusted" {
			t.Fatalf("blocked status: %+v", report)
		}
	})
	t.Run("dequeued head survives new observation", func(t *testing.T) {
		s := applySerial(queued(1), PRObserved{PR: 1, HeadSHA: "new-head", Eligible: true, Trusted: true})
		dequeue := onlyAction[Dequeue](t, Plan(testConfig, s, epoch))
		if dequeue.HeadSHA != "head1" {
			t.Fatalf("dequeue changed commit: %+v", dequeue)
		}
		s = applySerial(s, Dequeued{PR: 1, AdmissionID: 1, Reason: dequeue.Reason})
		report := onlyAction[ReportStatus](t, Plan(testConfig, s, epoch))
		if report.HeadSHA != "head1" || report.Reason != "head-changed" || report.Status != "dequeued" {
			t.Fatalf("dequeued status: %+v", report)
		}
	})
	t.Run("landed head survives new observation", func(t *testing.T) {
		s := applySerial(landingState(), PRObserved{PR: 1, HeadSHA: "new-head", Eligible: true, Trusted: true})
		s = applySerial(s, Landed{BatchID: "b1", NewBaseSHA: "tested1", NewFence: "f1"})
		report := onlyAction[ReportStatus](t, Plan(testConfig, s, epoch))
		if report.HeadSHA != "head1" || report.Status != "merged" || report.Reason != "" {
			t.Fatalf("merged status: %+v", report)
		}
	})
	t.Run("paused build", func(t *testing.T) {
		s := queued(1)
		cfg := testConfig
		cfg.BuildRetries = 1
		s = applySerial(s, TestRefBuildFailed{BatchID: s.Active.ID, At: epoch})
		report := onlyAction[ReportStatus](t, Plan(cfg, s, epoch))
		if report.HeadSHA != "head1" || report.Status != "paused" || report.Reason != "build-failed" {
			t.Fatalf("paused status: %+v", report)
		}
	})
}

func TestLandingRecoveryOnlyUsesRecordedAttempt(t *testing.T) {
	s := readyState()
	land := onlyAction[Land](t, Plan(testConfig, s, epoch))
	if !reflect.DeepEqual(land.PRs, s.Active.PRs) {
		t.Fatal("Land lost the pinned PR heads")
	}
	land.PRs[0].HeadSHA = "mutated"
	if s.Active.PRs[0].HeadSHA != "head1" {
		t.Fatal("Land aliases queue state")
	}
	s = applySerial(s, LandingStarted{BatchID: "b1", AttemptID: "random-attempt", FenceToken: "f0", At: epoch})
	s = applySerial(s, LandingStarted{BatchID: "b1", AttemptID: "replacement", FenceToken: "wrong", At: epoch})
	if len(actionsOf[Land](Plan(testConfig, s, epoch.Add(time.Hour)))) != 0 {
		t.Fatal("Landing batch emitted a fresh Land")
	}
	resolve := onlyAction[ResolveLanding](t, Plan(testConfig, s, epoch.Add(time.Hour)))
	if resolve.AttemptID != "random-attempt" || resolve.TestedSHA != "tested1" || resolve.FenceToken != "f0" {
		t.Fatalf("recovery lost original attempt: %+v", resolve)
	}
}

func TestMarkLandedEffectsArePinnedAndAcknowledged(t *testing.T) {
	for _, method := range []MergeMethod{Merge, Squash, Rebase} {
		t.Run(string(method), func(t *testing.T) {
			s := queued(1, 2)
			// Simulate a future multi-PR batch to pin one closing effect per head.
			s.Active.PRs = append(s.Active.PRs, BatchPR{PR: 2, HeadSHA: "head2"})
			s = applySerial(s, TestRefBuilt{BatchID: "b1", TestedSHA: "tested", Method: method, At: epoch})
			s = applySerial(s, ChecksCompleted{TestedSHA: "tested", Success: true, At: epoch})
			s = applySerial(s, LandingStarted{BatchID: "b1", AttemptID: "attempt", FenceToken: "f0", At: epoch})
			s = applySerial(s, Landed{BatchID: "b1", NewBaseSHA: "tested", NewFence: "f1"})
			before := Plan(testConfig, s, epoch)
			closing := actionsOf[MarkLanded](before)
			if method == Merge {
				if len(closing) != 0 {
					t.Fatal("merge emitted close effects")
				}
				return
			}
			want := []MarkLanded{{PR: 1, HeadSHA: "head1", LandedSHA: "tested"}, {PR: 2, HeadSHA: "head2", LandedSHA: "tested"}}
			if !reflect.DeepEqual(closing, want) {
				t.Fatalf("closing effects: %+v", closing)
			}
			if !reflect.DeepEqual(before, Plan(testConfig, s, epoch.Add(time.Hour))) {
				t.Fatal("pending effects changed across plans")
			}
			for _, effect := range closing {
				s = applySerial(s, ActionCompleted{Key: effect.Key()})
			}
			if len(actionsOf[MarkLanded](Plan(testConfig, s, epoch))) != 0 {
				t.Fatal("acknowledged close effects reappeared")
			}
			s = applySerial(s, Landed{BatchID: "b1", NewBaseSHA: "tested", NewFence: "f1"})
			if len(actionsOf[MarkLanded](Plan(testConfig, s, epoch))) != 0 {
				t.Fatal("replayed landing reintroduced close effects")
			}
		})
	}
}
