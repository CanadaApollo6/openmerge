package queue

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"
)

// The golden hashes were captured from the deployed #28 fold (62ef3a8).
// Snapshot only pre-existing state and planner outputs, including wire payloads
// and idempotency keys; additive source metadata is intentionally excluded.
func TestLegacyBlockReplayGolden(t *testing.T) {
	type checkpoint struct{ Event, SHA256 string }
	var got []checkpoint
	state := State{BaseBranch: "main", BaseSHA: "base", Fence: "fence"}
	events := []string{
		"pr_observed", "enqueue_requested", "pause_requested", "action_poisoned",
		"action_poisoned", "pause_requested", "rules_blocked", "rules_source_blocked",
		"resume_requested", "rules_cleared", "rules_source_cleared", "rules_cleared",
		"rules_source_cleared", "pause_requested", "resume_requested", "resume_requested",
		"test_ref_build_failed", "test_ref_build_failed", "test_ref_build_failed", "resume_requested",
		"test_ref_built", "checks_completed", "pause_requested", "action_poisoned",
		"resume_requested", "landing_started", "rules_source_blocked", "pause_requested",
		"action_poisoned", "resume_requested", "rules_cleared", "landing_abandoned",
	}
	for i, name := range events {
		if i == 16 {
			state = Apply(state, BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: []BatchPR{{PR: 2, HeadSHA: "head"}}, At: epoch})
		}
		data, err := os.ReadFile("testdata/wire/v1/event_" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		event, err := DecodeEvent(data)
		if err != nil {
			t.Fatal(err)
		}
		state = Apply(state, event)
		type legacyBlock struct{ Reason, Detail string }
		var blocks []legacyBlock
		for _, b := range state.Blocks(testConfig) {
			blocks = append(blocks, legacyBlock{b.Reason, b.Detail})
		}
		var actions []json.RawMessage
		var keys []string
		for _, a := range Plan(testConfig, state, epoch.Add(24*time.Hour)) {
			_, payload, err := EncodeAction(a)
			if err != nil {
				t.Fatal(err)
			}
			actions, keys = append(actions, payload), append(keys, a.Key())
		}
		snapshot := struct {
			State                                       State
			Revision, PauseRevision, RulesPauseRevision uint64
			Pause                                       PauseRequested
			Poison                                      []any
			Retry                                       []any
			Rules                                       []RulesSourceStatus
			Blocks                                      []legacyBlock
			Actions                                     []json.RawMessage
			Keys                                        []string
		}{state, state.revision, state.pauseRevision, state.rulesPauseRevision, state.pause,
			[]any{state.actionPoison.key, state.actionPoison.reason, state.actionPoison.revision},
			[]any{state.buildRetry.failures, state.buildRetry.failedAt, state.buildRetry.revision, state.buildRetry.detail},
			state.RulesSources(), blocks, actions, keys}
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, checkpoint{name, fmt.Sprintf("%x", sha256.Sum256(encoded))})
	}
	data, err := os.ReadFile("testdata/legacy-block-replay.json")
	if err != nil {
		t.Fatal(err)
	}
	var want []checkpoint
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy replay changed:\n got %+v\nwant %+v", got, want)
	}
}
