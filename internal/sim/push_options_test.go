package sim

import (
	"context"
	"errors"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestDelayedPushRetainsAuthorityOptions(t *testing.T) {
	s := onePR(t)
	land := readyLanding(t, s)
	s.cfg.Faults.DelayedPushRate = 1
	req := forge.LandRequest{Base: "main", ExpectedBaseSHA: land.ExpectedBaseSHA, TestedSHA: land.TestedSHA, Fence: "openmerge/fence/main", ExpectedFence: land.FenceToken, NextFenceID: "land/options"}
	sentinel := errors.New("retained guard rejected delayed push")
	calls := 0
	opts := forge.PushOptions{AuthorityRequired: true, Guard: func(context.Context, forge.PushValidationContext) error { calls++; return sentinel }}
	_, err := s.boundary.Land(t.Context(), s.repo, req, opts)
	if err != errDelayed {
		t.Fatalf("not scheduled: %v", err)
	}
	var delayed *scheduledEvent
	for i := range s.scheduled {
		if s.scheduled[i].kind == delayedPush {
			delayed = &s.scheduled[i]
			break
		}
	}
	if delayed == nil || !delayed.pushOptions.AuthorityRequired || delayed.pushOptions.Guard == nil {
		t.Fatal("delayed event dropped options")
	}
	if err := s.deliver(*delayed); !errors.Is(err, sentinel) || calls != 1 {
		t.Fatalf("guard calls=%d error=%v", calls, err)
	}
}
