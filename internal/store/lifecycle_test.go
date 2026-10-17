package store

import (
	"errors"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestLifecycleInvalidatesClaimedAndInFlightLandingStarts(t *testing.T) {
	for _, window := range []string{"claimed", "in-flight"} {
		t.Run(window, func(t *testing.T) {
			s := newStore(t)
			q, claim := readyLand(t, s)
			if window == "in-flight" {
				proceed, err := s.BeginConsequential(t.Context(), claim, config, epoch)
				must(t, err)
				if !proceed {
					t.Fatal("start gate rejected ready landing")
				}
			}
			appendEvents(t, s, q, queue.LifecycleChanged{State: "retired", Detail: "selector narrowed"})
			if window == "claimed" {
				proceed, err := s.BeginConsequential(t.Context(), claim, config, epoch)
				if err != nil || proceed {
					t.Fatalf("retired claim proceeded: %v %v", proceed, err)
				}
			} else {
				land := claim.Action.(queue.Land)
				err := s.Emit(t.Context(), claim, config)(queue.LandingStarted{BatchID: land.BatchID, AttemptID: "late-start", FenceToken: land.FenceToken, At: epoch.Add(time.Second)})
				if !errors.Is(err, ErrStale) {
					t.Fatalf("retired in-flight start emitted: %v", err)
				}
			}
			state, _, err := s.Load(t.Context(), q)
			must(t, err)
			if state.Active == nil || state.Active.Phase != queue.Testing {
				t.Fatal("retirement fabricated a landing attempt")
			}
		})
	}
}
