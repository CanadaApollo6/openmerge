package queue

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

func TestHeldRequestJournalAndSnapshot(t *testing.T) {
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	events := []Event{PRObserved{PR: 1, HeadSHA: "head", Trusted: true, PendingConditions: "sign-off"}, EnqueueRequested{PR: 1, At: at}}
	var state State
	for _, event := range events {
		_, data, err := EncodeEvent(event)
		if err != nil {
			t.Fatal(err)
		}
		restored, err := DecodeEvent(data)
		if err != nil {
			t.Fatal(err)
		}
		state = Apply(state, restored)
	}
	before := state
	state = snapshotRoundTrip(t, Config{}, state, at)
	state = Apply(state, PRObserved{PR: 1, HeadSHA: "head", Trusted: true, Eligible: true})
	if len(state.Entries) != 1 || state.Entries[0].HeadSHA != "head" || !state.Entries[0].EnqueuedAt.Equal(at) {
		t.Fatal("request did not survive journal/snapshot replay", state.Entries)
	}
	if _, held := state.HeldRequest(1); held {
		t.Fatal("admitted request still held")
	}
	if _, held := before.HeldRequest(1); !held || len(before.Entries) != 0 {
		t.Fatal("fold mutated earlier state")
	}
}

func TestHeldRequestCancelledByAuthorityRevision(t *testing.T) {
	state := Apply(State{}, PRObserved{PR: 12, HeadSHA: "H1", Trusted: true, PendingConditions: "sign-off"})
	state = Apply(state, EnqueueRequested{PR: 12})
	state = Apply(state, changeObservation(t, 2, "H2"))
	if _, held := state.HeldRequest(12); held {
		t.Fatal("new authority revision retained old-head request")
	}
	state = Apply(state, PRObserved{PR: 12, HeadSHA: "H2", Trusted: true, Eligible: true})
	if len(state.Entries) != 0 {
		t.Fatal("new authority head reused old request")
	}
}

func TestHeldRequestCancelledByBlockedLifecycle(t *testing.T) {
	for _, lifecycle := range []string{"dormant", "retired", "overlapping"} {
		for _, pending := range []string{"", "sign-off"} {
			t.Run(lifecycle+"/pending="+pending, func(t *testing.T) {
				state := Apply(State{}, PRObserved{PR: 1, HeadSHA: "head", Trusted: true, PendingConditions: "sign-off"})
				state = Apply(state, EnqueueRequested{PR: 1, At: epoch})
				before := state
				state = Apply(state, LifecycleChanged{State: lifecycle, Detail: "base unavailable"})
				state = snapshotRoundTrip(t, Config{}, state, epoch)
				state = Apply(state, PRObserved{PR: 1, HeadSHA: "head", Trusted: true, Eligible: pending == "", PendingConditions: pending})
				if len(state.Entries) != 0 {
					t.Fatal("held request admitted into inactive queue")
				}
				if _, held := state.HeldRequest(1); held {
					t.Fatal("inactive queue retained held request")
				}
				if _, held := before.HeldRequest(1); !held {
					t.Fatal("cancellation mutated earlier state")
				}
				state = Apply(state, LifecycleChanged{State: "active"})
				state = Apply(state, PRObserved{PR: 1, HeadSHA: "head", Trusted: true, Eligible: true})
				if len(state.Entries) != 0 {
					t.Fatal("reactivation reused cancelled request")
				}
				state = Apply(state, EnqueueRequested{PR: 1, At: epoch})
				if len(state.Entries) != 1 {
					t.Fatal("fresh request did not admit after reactivation")
				}
			})
		}
	}
}

func TestHeldRequestLifecycleReactivation(t *testing.T) {
	for _, authorityMode := range []bool{false, true} {
		for _, lifecycle := range []string{"dormant", "retired", "overlapping"} {
			t.Run(fmt.Sprintf("authority=%v/%s", authorityMode, lifecycle), func(t *testing.T) {
				var events []Event
				if authorityMode {
					pin := authorityFixture[PolicyAuthorityApplied](t, "policy_authority_applied")
					events = append(events, ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest}, pin)
				}
				for _, pr := range []int{1, 2} {
					events = append(events, PRObserved{PR: pr, HeadSHA: "head", Trusted: true, PendingConditions: "sign-off"}, EnqueueRequested{PR: pr, At: epoch})
				}
				events = append(events, LifecycleChanged{State: lifecycle, Detail: "base unavailable"}, LifecycleChanged{State: "active"}, PRObserved{PR: 1, HeadSHA: "head", Trusted: true, Eligible: true}, PRObserved{PR: 2, HeadSHA: "head", Trusted: true, Eligible: true})
				// Every cut includes journal decoding and a snapshot before replaying
				// the tail, including snapshots on both sides of the lifecycle change.
				var want []byte
				for cut := 0; cut <= len(events); cut++ {
					var state State
					for i, event := range events {
						if i == cut {
							state = snapshotRoundTrip(t, Config{}, state, epoch)
						}
						_, data, err := EncodeEvent(event)
						if err != nil {
							t.Fatal(err)
						}
						decoded, err := DecodeEvent(data)
						if err != nil {
							t.Fatal(err)
						}
						before := state
						state = Apply(state, decoded)
						if changed, ok := event.(LifecycleChanged); ok && changed.State != "active" {
							if len(before.HeldRequests()) != 2 {
								t.Fatal("lifecycle change mutated earlier holds")
							}
							if len(state.HeldRequests()) != 0 {
								t.Error("lifecycle change retained held requests")
							}
						}
					}
					if len(state.Entries) != 0 || len(state.HeldRequests()) != 0 {
						t.Fatalf("snapshot cut %d: reactivation reused old commands: entries=%+v held=%+v", cut, state.Entries, state.HeldRequests())
					}
					state = snapshotRoundTrip(t, Config{}, state, epoch)
					got, err := EncodeSnapshot(state)
					if err != nil {
						t.Fatal(err)
					}
					if cut == 0 {
						want = got
					} else if !bytes.Equal(want, got) {
						t.Fatalf("snapshot cut %d changed replay", cut)
					}
					state = Apply(state, EnqueueRequested{PR: 1, At: epoch})
					if len(state.Entries) != 1 {
						t.Fatal("fresh command did not admit after reactivation")
					}
				}
			})
		}
	}
}
