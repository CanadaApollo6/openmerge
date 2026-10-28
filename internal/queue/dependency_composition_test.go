package queue

import (
	"reflect"
	"slices"
	"testing"
)

func TestDependencyCompositionDiscardExactSuffixAndLandingBoundary(t *testing.T) {
	cfg, s, now := chainState(t, 4, 4)
	for range 4 {
		s, _ = admitChain(t, cfg, s, now)
	}
	before := s
	s = s.WithDependencyInvalidation("b2")
	if before.Batch("b2").InvalidationReason != "" {
		t.Fatal("planning view mutated original")
	}
	actions := Plan(cfg, s, now)
	invalid, ok := actions[0].(InvalidateBatches)
	if !ok || !slices.Equal(invalid.BatchIDs, []string{"b2", "b3", "b4"}) {
		t.Fatal("wrong planned suffix", actions)
	}
	event := DependencyCompositionsInvalidated{BatchIDs: invalid.BatchIDs, CompositionIDs: []uint64{12, 13, 14}, Reason: invalid.Reason, CauseEventIDs: []uint64{30, 30, 30}}
	kind, raw, err := EncodeEvent(event)
	if err != nil || kind != "dependency_compositions_invalidated" {
		t.Fatal(kind, err)
	}
	decoded, err := DecodeEvent(raw)
	if err != nil || !reflect.DeepEqual(decoded, event) {
		t.Fatal("codec", err)
	}
	after := Apply(s, decoded)
	if len(after.Batches()) != 1 || after.Active.ID != "b1" || !reflect.DeepEqual(after.Entries, s.Entries) {
		t.Fatal("discard changed queue positions")
	}
	for _, bad := range []DependencyCompositionsInvalidated{
		{BatchIDs: []string{"b2"}, CompositionIDs: []uint64{12}, CauseEventIDs: []uint64{30}, Reason: "dependency-changed"},
		{BatchIDs: []string{"b4", "b3", "b2"}, CompositionIDs: []uint64{14, 13, 12}, CauseEventIDs: []uint64{30, 30, 30}, Reason: "dependency-changed"},
	} {
		if got := Apply(s, bad); len(got.Batches()) != 4 {
			t.Fatal("accepted inexact suffix", bad)
		}
	}
	landing := Apply(before, ChecksCompleted{TestedSHA: before.Active.TestedSHA, Success: true})
	landing = Apply(landing, LandingStarted{BatchID: "b1", AttemptID: "uncertain", FenceToken: landing.Fence, At: now})
	landing = landing.WithDependencyInvalidation("b1")
	if landing.Active.InvalidationReason != "" || landing.Active.Phase != Landing {
		t.Fatal("cascade abandoned a started push")
	}
	if got := Apply(landing, DependencyCompositionsInvalidated{BatchIDs: []string{"b1", "b2", "b3", "b4"}, CompositionIDs: []uint64{11, 12, 13, 14}, CauseEventIDs: []uint64{30, 30, 30, 30}, Reason: "dependency-changed"}); len(got.Batches()) != 4 {
		t.Fatal("discard crossed settlement boundary")
	}
}

func TestDependencyDiscardBeforeAlreadyInvalidatedSuffix(t *testing.T) {
 cfg,s,now:=chainState(t,4,4)
 for range 4 { s,_=admitChain(t,cfg,s,now) }
 s=cloneState(s); s.invalidateFrom(s.Batch("b3"),"config-changed")
 plain:=s
 s=s.WithDependencyInvalidation("b2")
 action:=Plan(cfg,s,now)[0].(InvalidateBatches)
 if !slices.Equal(action.BatchIDs,[]string{"b2"}) { t.Fatal("wrong next reason segment",action) }
 event:=DependencyCompositionsInvalidated{BatchIDs:action.BatchIDs,CompositionIDs:[]uint64{12},CauseEventIDs:[]uint64{30},Reason:action.Reason}
 for _,before:=range []State{plain,s} {
  after:=Apply(before,event)
  if after.Batch("b2")!=nil || after.Batch("b3")==nil || after.Batch("b3").InvalidationReason!="config-changed" { t.Fatal("fold refused planner segment") }
  next:=Plan(cfg,after,now)[0].(InvalidateBatches)
  if next.Reason!="config-changed" || !slices.Equal(next.BatchIDs,[]string{"b3","b4"}) { t.Fatal("later reason was lost",next) }
 }
}
