package queue

import (
	"bytes"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestCheckRetryProgressGoldenAndProjectionCopies(t *testing.T) {
	cfg, s := retryTesting(t, 1)
	s = foldCheckEvents(t, s, retryObservation(cfg, s, 7, "failure"))
	s = requestRetry(t, cfg, s, epoch)
	for _, phase := range []string{"intent", "accepted"} {
		data, err := os.ReadFile("testdata/retry_dispatch_" + phase + ".json")
		if err != nil {
			t.Fatal(err)
		}
		event, err := DecodeEvent(data)
		if err != nil {
			t.Fatal(err)
		}
		_, encoded, err := EncodeEvent(event)
		if err != nil || !bytes.Equal(data, encoded) {
			t.Fatalf("progress wire changed: %s %v", encoded, err)
		}
		progress := event.(CheckRerunDispatchProgress)
		progress.BatchID = s.Active.ID
		progress.Generation = s.Active.CheckRetry.Generation
		progress.Target.At = epoch.Add(time.Second)
		action := onlyAction[RerunChecks](t, Plan(cfg, s, epoch))
		if !CanStart(cfg, s, action, progress) {
			t.Fatalf("current %s declined", phase)
		}
		before := cloneState(s)
		s = foldCheckEvents(t, s, progress)
		if len(s.Active.CheckRetry.Dispatches) != 1 || s.Active.CheckRetry.Dispatches[0].Accepted != (phase == "accepted") {
			t.Fatalf("progress=%+v", s.Active.CheckRetry)
		}
		if phase == "accepted" && before.Active.CheckRetry.Dispatches[0].Accepted {
			t.Fatal("acceptance mutated prior projection")
		}
		if CanStart(cfg, s, action, progress) {
			t.Fatal("duplicate progress authorized another POST")
		}
		if s.Active.CheckRetry.Dispatched {
			t.Fatal("per-target progress completed whole request")
		}
	}
	// A stale lease/process with no projected progress cannot append a second
	// intent under the queue lock, even while the original API read is unchanged.
	action := onlyAction[RerunChecks](t, Plan(cfg, s, epoch))
	intent := CheckRerunDispatchProgress{BatchID: s.Active.ID, TestedSHA: "tested", Generation: s.Active.CheckRetry.Generation, Target: CheckRerunTarget{WorkflowRunID: 700, Attempt: 1, At: epoch.Add(time.Second)}}
	if CanStart(cfg, s, action, intent) {
		t.Fatal("accepted target authorized a second intent")
	}
	before := cloneState(s)
	s = foldCheckEvents(t, s, intent)
	if !reflect.DeepEqual(before.Active.CheckRetry, s.Active.CheckRetry) {
		t.Fatal("duplicate intent changed accepted state")
	}
}

func TestCheckRetryRefusedDispatchReplaysAndRequiresAnotherIntent(t *testing.T) {
	cfg, s := retryTesting(t, 1)
	s = foldCheckEvents(t, s, retryObservation(cfg, s, 7, "failure"))
	s = requestRetry(t, cfg, s, epoch)
	for _, phase := range []string{"intent", "refused", "intent", "accepted"} {
		data, err := os.ReadFile("testdata/retry_dispatch_" + phase + ".json")
		if err != nil {
			t.Fatal(err)
		}
		event, err := DecodeEvent(data)
		if err != nil {
			t.Fatal(err)
		}
		_, encoded, err := EncodeEvent(event)
		if err != nil || !bytes.Equal(data, encoded) {
			t.Fatalf("wire=%s %v", encoded, err)
		}
		progress := event.(CheckRerunDispatchProgress)
		progress.BatchID = s.Active.ID
		progress.Generation = s.Active.CheckRetry.Generation
		progress.Target.At = epoch.Add(time.Second)
		action := onlyAction[RerunChecks](t, Plan(cfg, s, epoch))
		if !CanStart(cfg, s, action, progress) {
			t.Fatalf("%s declined", phase)
		}
		before := cloneState(s)
		s = foldCheckEvents(t, s, progress)
		if reflect.DeepEqual(before.Active.CheckRetry.Dispatches, s.Active.CheckRetry.Dispatches) {
			t.Fatalf("%s did not persist", phase)
		}
		if CanStart(cfg, s, action, progress) {
			t.Fatalf("duplicate %s accepted", phase)
		}
		wrong := progress
		wrong.Target.Attempt++
		if CanStart(cfg, s, action, wrong) {
			t.Fatal("different original attempt accepted")
		}
	}
	if !s.Active.CheckRetry.Dispatches[0].Accepted || s.Active.CheckRetry.Dispatches[0].NotDispatched {
		t.Fatalf("final dispatch=%+v", s.Active.CheckRetry)
	}
}
