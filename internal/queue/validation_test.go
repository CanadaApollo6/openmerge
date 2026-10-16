package queue

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"
)

type wireMutation struct {
	path  string
	value any
}

// Mutate every envelope, data and nested field independently so each rejection
// proves that specific field is required, including bools and private key inputs.
func missingOrNullFields(value any) []wireMutation {
	var mutations []wireMutation
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			missing := maps.Clone(value)
			delete(missing, key)
			null := maps.Clone(value)
			null[key] = nil
			mutations = append(mutations, wireMutation{key + "/missing", missing}, wireMutation{key + "/null", null})
			for _, nested := range missingOrNullFields(child) {
				mutated := maps.Clone(value)
				mutated[key] = nested.value
				mutations = append(mutations, wireMutation{key + "/" + nested.path, mutated})
			}
		}
	case []any:
		for i, child := range value {
			null := slices.Clone(value)
			null[i] = nil
			mutations = append(mutations, wireMutation{"item/null", null})
			for _, nested := range missingOrNullFields(child) {
				mutated := slices.Clone(value)
				mutated[i] = nested.value
				mutations = append(mutations, wireMutation{"item/" + nested.path, mutated})
			}
		}
	}
	return mutations
}

func TestWireV1RequiresEveryField(t *testing.T) {
	for _, action := range []bool{false, true} {
		for _, path := range wireFixtures(t, action) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var value any
			if err := json.Unmarshal(raw, &value); err != nil {
				t.Fatal(err)
			}
			for _, mutation := range missingOrNullFields(value) {
				t.Run(filepath.Base(path)+"/"+mutation.path, func(t *testing.T) {
					payload, err := json.Marshal(mutation.value)
					if err != nil {
						t.Fatal(err)
					}
					if action {
						_, err = DecodeAction(payload)
					} else {
						_, err = DecodeEvent(payload)
					}
					if err == nil {
						t.Fatalf("accepted incomplete wire value: %s", payload)
					}
				})
			}
		}
	}
}

func TestActionSemanticValidation(t *testing.T) {
	for _, path := range wireFixtures(t, true) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		data := value["data"].(map[string]any)
		for field := range data {
			// These zero values are meaningful in the v1 contract.
			if field == "epoch" || field == "admission" || field == "batch_id" && value["type"] == "dequeue" {
				continue
			}
			var invalid any = ""
			if _, number := data[field].(float64); number {
				invalid = 0
			}
			if field == "prs" {
				invalid = []any{}
			}
			t.Run(filepath.Base(path)+"/"+field, func(t *testing.T) {
				mutated := maps.Clone(value)
				fields := maps.Clone(data)
				fields[field] = invalid
				mutated["data"] = fields
				payload, err := json.Marshal(mutated)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := DecodeAction(payload); err == nil {
					t.Fatalf("accepted semantically invalid action: %s", payload)
				}
			})
		}
	}
	for _, action := range []Action{
		BuildTestRef{BatchID: "b1", BaseSHA: "base", Ref: "ref", Method: Merge, PRs: []BatchPR{{PR: 1}}},
		Land{BatchID: "b1", ExpectedBaseSHA: "base", TestedSHA: "tested", Ref: "ref", FenceToken: "fence", Method: Merge, PRs: []BatchPR{{HeadSHA: "head"}}},
		BuildTestRef{BatchID: "b1", BaseSHA: "base", Ref: "ref", Method: Merge, PRs: []BatchPR{{PR: 1, HeadSHA: "head"}, {PR: 1, HeadSHA: "head"}}},
		BuildTestRef{BatchID: "b1", BaseSHA: "base", Ref: "ref", Method: "invalid", PRs: []BatchPR{{PR: 1, HeadSHA: "head"}}},
		Land{BatchID: "b1", ExpectedBaseSHA: "base", TestedSHA: "tested", Ref: "ref", FenceToken: "fence", Method: "invalid", PRs: []BatchPR{{PR: 1, HeadSHA: "head"}}},
		Dequeue{PR: 1, AdmissionID: 1, HeadSHA: "head", scope: "event/1", Reason: "invalid"},
		Dequeue{PR: 1, AdmissionID: 1, HeadSHA: "head", scope: "batch/b1", Reason: ChecksTimeout},
		ReportStatus{PR: 1, HeadSHA: "head", Status: "invalid", Detail: "detail", revision: 1},
		DeleteRef{Ref: " \t"},
	} {
		if err := action.Validate(); err == nil {
			t.Errorf("accepted invalid %T: %+v", action, action)
		}
	}
}

func TestWireRejectsUnknownFieldsAtEveryObject(t *testing.T) {
	raw, err := os.ReadFile("testdata/wire/v1/action_land.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, location := range []string{"envelope", "data", "pr"} {
		t.Run(location, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(raw, &value); err != nil {
				t.Fatal(err)
			}
			target := value
			if location != "envelope" {
				target = value["data"].(map[string]any)
			}
			if location == "pr" {
				target = target["prs"].([]any)[0].(map[string]any)
			}
			target["unexpected"] = 1
			payload, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeAction(payload); err == nil {
				t.Fatalf("accepted unknown field: %s", payload)
			}
		})
	}
}

func TestPlanRejectsInvalidActionBeforeEmission(t *testing.T) {
	s := readyState()
	s.Fence = ""
	defer func() {
		if recover() == nil {
			t.Fatal("planner emitted an action with an empty fence")
		}
	}()
	Plan(testConfig, s, epoch)
}

func TestActionPoisonPauseAndResume(t *testing.T) {
	for _, initial := range []State{queued(1), testingState(), readyState(), landingState()} {
		s := applySerial(initial, ActionPoisoned{Key: "bad/key", Reason: "invalid payload"})
		if s.PauseReason(testConfig) != "action-poisoned" {
			t.Fatal("poison did not pause queue")
		}
		report := onlyAction[ReportStatus](t, Plan(testConfig, s, epoch.Add(time.Hour)))
		if report.Status != "paused" || report.Reason != "action-poisoned" || report.HeadSHA != "head1" {
			t.Fatalf("pause is not visible on head PR: %+v", report)
		}
		s = applySerial(s, ActionCompleted{Key: report.Key()})
		actions := Plan(testConfig, s, epoch.Add(time.Hour))
		if s.Active != nil && s.Active.Phase == Landing {
			resolve := onlyAction[ResolveLanding](t, actions)
			if len(actions) != 1 || resolve.AttemptID != s.Active.LandingAttempt.ID {
				t.Fatalf("paused landing did not retain only its resolver: %+v", actions)
			}
		} else if len(actions) != 0 {
			t.Fatalf("paused work continued: %+v", actions)
		}
		s = applySerial(s, BaseMoved{SHA: "another-base"})
		if s.PauseReason(testConfig) != "action-poisoned" {
			t.Fatal("base update cleared poison pause")
		}
		s = applySerial(s, ResumeRequested{})
		if s.PauseReason(testConfig) != "" || len(Plan(testConfig, s, epoch.Add(time.Hour))) == 0 {
			t.Fatal("resume did not retry work")
		}
	}
}

func TestActionPoisonedWhilePausedIsNoOp(t *testing.T) {
	s := applySerial(readyState(), ActionPoisoned{Key: "bad/key", Reason: "invalid payload"})
	report := onlyAction[ReportStatus](t, Plan(testConfig, s, epoch))
	for _, acknowledged := range []bool{false, true} {
		if acknowledged {
			s = applySerial(s, ActionCompleted{Key: report.Key()})
		}
		for range 5 {
			after := applySerial(s, ActionPoisoned{Key: report.Key(), Reason: "corrupt pause report"})
			if !reflect.DeepEqual(s, after) {
				t.Fatal("repeated poison changed the paused state or its revision")
			}
			s = after
		}
	}
	s = applySerial(s, ResumeRequested{})
	s = applySerial(s, ActionPoisoned{Key: "new/key", Reason: "new incident"})
	if next := onlyAction[ReportStatus](t, Plan(testConfig, s, epoch)); next.Key() == report.Key() {
		t.Fatal("resume did not permit a new pause report")
	}
}

func TestPausedLandingRespectsResolutionDeadline(t *testing.T) {
	for _, reason := range []string{"action-poisoned", "build-failed"} {
		t.Run(reason, func(t *testing.T) {
			s := landingState()
			if reason == "action-poisoned" {
				s = applySerial(s, ActionPoisoned{Key: "unrelated", Reason: "invalid payload"})
			} else {
				// Build failures normally reset on success; retain a pause here
				// to assert that resolution is independent of the pause reason.
				s.buildRetry = buildRetryState{failures: 3, revision: s.revision}
			}
			if s.PauseReason(testConfig) != reason {
				t.Fatal("missing pause")
			}
			deadline := epoch.Add(testConfig.ResolveAfter)
			if actions := Plan(testConfig, s, deadline); len(actionsOf[ResolveLanding](actions)) != 0 {
				t.Fatal("pause bypassed resolution deadline")
			}
			actions := Plan(testConfig, s, deadline.Add(time.Nanosecond))
			resolve := onlyAction[ResolveLanding](t, actions)
			if resolve.AttemptID != "attempt1" || resolve.FenceToken != "f0" || resolve.TestedSHA != "tested1" {
				t.Fatalf("pause changed resolution inputs: %+v", resolve)
			}
			for _, action := range actions {
				switch action.(type) {
				case ResolveLanding, ReportStatus:
				default:
					t.Fatalf("pause started new work: %+v", action)
				}
			}
		})
	}
}
