package store

import (
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// The registry is also checked against queue's method sets by codec_test.go.
// Names form the on-disk contract and must remain stable when Go types move.
var eventTypes = map[string]queue.Event{
	"pr_observed": queue.PRObserved{}, "base_moved": queue.BaseMoved{},
	"checks_completed": queue.ChecksCompleted{}, "checks_refreshed": queue.ChecksRefreshed{},
	"pr_closed": queue.PRClosed{}, "enqueue_requested": queue.EnqueueRequested{},
	"dequeue_requested": queue.DequeueRequested{}, "resume_requested": queue.ResumeRequested{},
	"test_ref_built": queue.TestRefBuilt{}, "test_ref_build_failed": queue.TestRefBuildFailed{},
	"landing_started": queue.LandingStarted{}, "landed": queue.Landed{},
	"landing_abandoned": queue.LandingAbandoned{}, "dequeued": queue.Dequeued{},
	"action_completed": queue.ActionCompleted{},
}

var actionTypes = map[string]queue.Action{
	"build_test_ref": queue.BuildTestRef{}, "refresh_checks": queue.RefreshChecks{},
	"land": queue.Land{}, "resolve_landing": queue.ResolveLanding{},
	"delete_ref": queue.DeleteRef{}, "dequeue": queue.Dequeue{},
	"report_status": queue.ReportStatus{}, "mark_landed": queue.MarkLanded{},
}

type envelope struct {
	Type    string          `json:"type"`
	Data    json.RawMessage `json:"data"`
	Planner *plannerInput   `json:"planner,omitempty"`
}

type plannerInput struct {
	Config queue.Config `json:"config"`
	Now    time.Time    `json:"now"`
}

func encodeVariant[T any](value T, registry map[string]T, planner *plannerInput) (string, []byte, error) {
	for name, sample := range registry {
		if reflect.TypeOf(value) != reflect.TypeOf(sample) {
			continue
		}
		data, err := json.Marshal(value)
		if err != nil {
			return "", nil, err
		}
		payload, err := json.Marshal(envelope{Type: name, Data: data, Planner: planner})
		return name, payload, err
	}
	return "", nil, fmt.Errorf("unregistered queue variant %T", value)
}

func encodeEvent(e queue.Event) (string, []byte, error) {
	return encodeVariant(e, eventTypes, nil)
}

func decodeEvent(payload []byte) (queue.Event, error) {
	var encoded envelope
	if err := json.Unmarshal(payload, &encoded); err != nil {
		return nil, err
	}
	sample, ok := eventTypes[encoded.Type]
	if !ok {
		return nil, fmt.Errorf("unknown event type %q", encoded.Type)
	}
	value := reflect.New(reflect.TypeOf(sample))
	if err := json.Unmarshal(encoded.Data, value.Interface()); err != nil {
		return nil, err
	}
	return value.Elem().Interface().(queue.Event), nil
}

func encodeAction(a queue.Action, cfg queue.Config, now time.Time) (string, []byte, error) {
	return encodeVariant(a, actionTypes, &plannerInput{Config: cfg, Now: now})
}

func decodeAction(payload []byte, key string, state queue.State) (queue.Action, error) {
	var encoded envelope
	if err := json.Unmarshal(payload, &encoded); err != nil {
		return nil, err
	}
	sample, ok := actionTypes[encoded.Type]
	if !ok || encoded.Planner == nil {
		return nil, fmt.Errorf("unknown action type or missing planner input: %q", encoded.Type)
	}
	var storedData any
	if err := json.Unmarshal(encoded.Data, &storedData); err != nil {
		return nil, err
	}
	for _, action := range queue.Plan(encoded.Planner.Config, state, encoded.Planner.Now) {
		if action.Key() != key || reflect.TypeOf(action) != reflect.TypeOf(sample) {
			continue
		}
		data, err := json.Marshal(action)
		if err != nil {
			return nil, err
		}
		var plannedData any
		if err := json.Unmarshal(data, &plannedData); err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(storedData, plannedData) {
			return nil, fmt.Errorf("action %q payload disagrees with its historical plan", key)
		}
		return action, nil
	}
	return nil, fmt.Errorf("action %q is absent from its historical plan", key)
}
