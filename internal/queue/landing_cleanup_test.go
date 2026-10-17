package queue

import "testing"

func TestLandingCleanupPolicyIsPinnedToAttempt(t *testing.T) {
	enabled := LandingCleanupPolicyRecorded{BatchID: "b1", AttemptID: "attempt1", DeleteHeadBranch: true}
	disabled := enabled
	disabled.DeleteHeadBranch = false
	wrongBatch, wrongAttempt := enabled, enabled
	wrongBatch.BatchID, wrongAttempt.AttemptID = "other", "other"
	for _, tc := range []struct {
		name   string
		events []Event
		delete bool
	}{
		{name: "legacy"},
		{name: "enabled", events: []Event{enabled}, delete: true},
		{name: "disabled", events: []Event{disabled}},
		{name: "wrong-batch", events: []Event{wrongBatch}},
		{name: "wrong-attempt", events: []Event{wrongAttempt}},
		{name: "cannot-enable-later", events: []Event{disabled, enabled}},
		{name: "cannot-disable-later", events: []Event{enabled, disabled}, delete: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := readyState()
			// A policy observation before LandingStarted cannot authorize cleanup.
			s = Apply(s, enabled)
			s = Apply(s, LandingStarted{BatchID: "b1", AttemptID: "attempt1", FenceToken: "f0", At: epoch})
			for _, event := range tc.events {
				_, data, err := EncodeEvent(event)
				if err != nil {
					t.Fatal(err)
				}
				replayed, err := DecodeEvent(data)
				if err != nil {
					t.Fatal(err)
				}
				s = Apply(s, replayed)
			}
			s = Apply(s, HeadCleanupRequested{BatchID: "b1", LandedSHA: "tested1"})
			s = Apply(s, Landed{BatchID: "b1", NewBaseSHA: "tested1", NewFence: "f1"})
			deletions := actionsOf[DeleteHeadBranch](Plan(testConfig, s, epoch))
			if (len(deletions) == 1) != tc.delete {
				t.Fatalf("cleanup = %v, want deletion %v", deletions, tc.delete)
			}
		})
	}
}
