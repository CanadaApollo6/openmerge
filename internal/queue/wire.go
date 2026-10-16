package queue

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"time"
)

// WireVersion identifies the persisted event and action schema. Existing versions
// must remain decodable when planner wording, refs, or key generation change.
const WireVersion = 1

type wireEnvelope struct {
	Version int             `json:"version"`
	Type    string          `json:"type"`
	Data    json.RawMessage `json:"data"`
}

// Wire structs deliberately spell out every field, including private key inputs.
// Changing runtime structs must not silently change the persistence contract.
type wireBatchPR struct {
	PR      int    `json:"pr"`
	HeadSHA string `json:"head_sha"`
}

type wirePRObserved struct {
	PR       int    `json:"pr"`
	HeadSHA  string `json:"head_sha"`
	Eligible bool   `json:"eligible"`
	Trusted  bool   `json:"trusted"`
}

type wireBaseMoved struct {
	SHA string `json:"sha"`
}

type wireChecksCompleted struct {
	TestedSHA string `json:"tested_sha"`
	Success   bool   `json:"success"`
}

type wireChecksReopened struct {
	TestedSHA string    `json:"tested_sha"`
	At        time.Time `json:"at"`
}

type wireChecksRefreshed struct {
	BatchID   string    `json:"batch_id"`
	TestedSHA string    `json:"tested_sha"`
	At        time.Time `json:"at"`
}

type wirePRClosed struct {
	PR int `json:"pr"`
}

type wireEnqueueRequested struct {
	PR int       `json:"pr"`
	At time.Time `json:"at"`
}

type wireDequeueRequested struct {
	PR int `json:"pr"`
}

type wirePauseRequested struct {
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

type wireResumeRequested struct {
}

type wireRulesBlocked struct {
	Problems []string `json:"problems"`
	Basis    uint64   `json:"basis"`
}

type wireRulesCleared struct {
	Basis uint64 `json:"basis"`
}

type wireBatchStarted struct {
	BatchID string        `json:"batch_id"`
	BaseSHA string        `json:"base_sha"`
	PRs     []wireBatchPR `json:"prs"`
	At      time.Time     `json:"at"`
}
type wireBatchFailed struct {
	BatchID string `json:"batch_id"`
	Reason  string `json:"reason"`
}

type wireTestRefBuilt struct {
	BatchID   string    `json:"batch_id"`
	TestedSHA string    `json:"tested_sha"`
	At        time.Time `json:"at"`
	Method    string    `json:"method"`
}

type wireTestRefBuildFailed struct {
	BatchID  string    `json:"batch_id"`
	Conflict bool      `json:"conflict"`
	At       time.Time `json:"at"`
}

type wireLandingStarted struct {
	BatchID    string    `json:"batch_id"`
	AttemptID  string    `json:"attempt_id"`
	FenceToken string    `json:"fence_token"`
	At         time.Time `json:"at"`
}

type wireLanded struct {
	BatchID    string `json:"batch_id"`
	NewBaseSHA string `json:"new_base_sha"`
	NewFence   string `json:"new_fence"`
}

type wireHeadCleanupRequested struct {
	BatchID   string `json:"batch_id"`
	LandedSHA string `json:"landed_sha"`
}

type wireLandingAbandoned struct {
	BatchID  string `json:"batch_id"`
	NewFence string `json:"new_fence"`
}

type wireDequeued struct {
	PR          int    `json:"pr"`
	AdmissionID uint64 `json:"admission_id"`
	Reason      string `json:"reason"`
	BatchID     string `json:"batch_id"`
}

type wireActionCompleted struct {
	Key string `json:"key"`
}

type wireActionPoisoned struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

type wireBuildTestRef struct {
	BatchID string        `json:"batch_id"`
	BaseSHA string        `json:"base_sha"`
	PRs     []wireBatchPR `json:"prs"`
	Ref     string        `json:"ref"`
	Method  string        `json:"method"`
}

type wireRefreshChecks struct {
	BatchID   string `json:"batch_id"`
	TestedSHA string `json:"tested_sha"`
	Epoch     uint64 `json:"epoch"`
}

type wireLand struct {
	BatchID          string        `json:"batch_id"`
	ExpectedBaseSHA  string        `json:"expected_base_sha"`
	TestedSHA        string        `json:"tested_sha"`
	Ref              string        `json:"ref"`
	FenceToken       string        `json:"fence_token"`
	PRs              []wireBatchPR `json:"prs"`
	Method           string        `json:"method"`
	ChecksGeneration uint64        `json:"checks_generation"`
}

type wireResolveLanding struct {
	BatchID    string `json:"batch_id"`
	AttemptID  string `json:"attempt_id"`
	TestedSHA  string `json:"tested_sha"`
	FenceToken string `json:"fence_token"`
}

type wireDeleteRef struct {
	Ref string `json:"ref"`
}

type wireDequeue struct {
	HeadSHA     string `json:"head_sha"`
	PR          int    `json:"pr"`
	AdmissionID uint64 `json:"admission_id"`
	Reason      string `json:"reason"`
	BatchID     string `json:"batch_id"`
	Scope       string `json:"scope"`
}

type wireReportStatus struct {
	PR        int    `json:"pr"`
	HeadSHA   string `json:"head_sha"`
	Status    string `json:"status"`
	Reason    string `json:"reason"`
	Detail    string `json:"detail"`
	Revision  uint64 `json:"revision"`
	Admission uint64 `json:"admission"`
}

type wireMarkLanded struct {
	PR        int    `json:"pr"`
	HeadSHA   string `json:"head_sha"`
	LandedSHA string `json:"landed_sha"`
}

type wireDeleteHeadBranch struct {
	PR        int    `json:"pr"`
	HeadSHA   string `json:"head_sha"`
	LandedSHA string `json:"landed_sha"`
}

func encodeWire(name string, data any) (string, []byte, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return "", nil, err
	}
	payload, err := json.Marshal(wireEnvelope{Version: WireVersion, Type: name, Data: raw})
	return name, payload, err
}

func decodeJSON(payload []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON value, got trailing data: %v", err)
	}
	return requireWireFields(payload, reflect.TypeOf(value).Elem(), "wire")
}

// Every v1 field is required, including false/zero values and nested PR fields.
// Inspect only the explicit wire schema; time and RawMessage decode themselves.
func requireWireFields(raw []byte, schema reflect.Type, path string) error {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("%s is required and must not be null", path)
	}
	if schema == reflect.TypeFor[time.Time]() || schema == reflect.TypeFor[json.RawMessage]() {
		return nil
	}
	switch schema.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		for i := 0; i < schema.NumField(); i++ {
			field := schema.Field(i)
			name := field.Tag.Get("json")
			if err := requireWireFields(fields[name], field.Type, path+"."+name); err != nil {
				return err
			}
		}
	case reflect.Slice:
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return err
		}
		for i, item := range items {
			if err := requireWireFields(item, schema.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func decodeWire(payload []byte) (wireEnvelope, error) {
	var envelope wireEnvelope
	if err := decodeJSON(payload, &envelope); err != nil {
		return envelope, err
	}
	if envelope.Version != WireVersion {
		return envelope, fmt.Errorf("unsupported queue wire version %d", envelope.Version)
	}
	return envelope, nil
}

func decodeWireData[T any](data json.RawMessage, convert func(T) Event) (Event, error) {
	value, err := readWireData[T](data)
	if err != nil {
		return nil, err
	}
	return convert(value), nil
}

func decodeWireAction[T any](data json.RawMessage, convert func(T) Action) (Action, error) {
	value, err := readWireData[T](data)
	if err != nil {
		return nil, err
	}
	action := convert(value)
	if err := action.Validate(); err != nil {
		return nil, fmt.Errorf("invalid queue action %T: %w", action, err)
	}
	return action, nil
}

func readWireData[T any](data json.RawMessage) (T, error) {
	var value T
	if trimmed := bytes.TrimSpace(data); len(trimmed) == 0 || trimmed[0] != '{' {
		return value, fmt.Errorf("queue wire data must be an object")
	}
	err := decodeJSON(data, &value)
	return value, err
}

func encodeBatchPRs(prs []BatchPR) []wireBatchPR {
	if prs == nil {
		return nil
	}
	result := make([]wireBatchPR, len(prs))
	for i, pr := range prs {
		result[i] = wireBatchPR(pr)
	}
	return result
}

func decodeBatchPRs(prs []wireBatchPR) []BatchPR {
	if prs == nil {
		return nil
	}
	result := make([]BatchPR, len(prs))
	for i, pr := range prs {
		result[i] = BatchPR(pr)
	}
	return result
}

// EncodeEvent returns the stable type discriminator and versioned JSON.
func EncodeEvent(value Event) (string, []byte, error) {
	switch v := value.(type) {
	case PRObserved:
		return encodeWire("pr_observed", wirePRObserved(v))
	case BaseMoved:
		return encodeWire("base_moved", wireBaseMoved(v))
	case ChecksCompleted:
		if err := v.Validate(); err != nil {
			return "", nil, err
		}
		return encodeWire("checks_completed", wireChecksCompleted(v))
	case ChecksReopened:
		if err := v.Validate(); err != nil {
			return "", nil, err
		}
		return encodeWire("checks_reopened", wireChecksReopened(v))
	case ChecksRefreshed:
		return encodeWire("checks_refreshed", wireChecksRefreshed(v))
	case PRClosed:
		return encodeWire("pr_closed", wirePRClosed(v))
	case EnqueueRequested:
		return encodeWire("enqueue_requested", wireEnqueueRequested(v))
	case DequeueRequested:
		return encodeWire("dequeue_requested", wireDequeueRequested(v))
	case RefreshRequested:
		return encodeWire("refresh_requested", wireResumeRequested{})
	case PauseRequested:
		if err := v.Validate(); err != nil {
			return "", nil, err
		}
		return encodeWire("pause_requested", wirePauseRequested(v))
	case ResumeRequested:
		return encodeWire("resume_requested", wireResumeRequested{})
	case RulesBlocked:
		if err := v.Validate(); err != nil {
			return "", nil, err
		}
		return encodeWire("rules_blocked", wireRulesBlocked(v))
	case RulesCleared:
		if err := v.Validate(); err != nil {
			return "", nil, err
		}
		return encodeWire("rules_cleared", wireRulesCleared(v))
	case BatchStarted:
		if err := v.Validate(); err != nil {
			return "", nil, err
		}
		return encodeWire("batch_started", wireBatchStarted{BatchID: v.BatchID, BaseSHA: v.BaseSHA, PRs: encodeBatchPRs(v.PRs), At: v.At})
	case BatchFailed:
		if err := (FailBatch(v)).Validate(); err != nil {
			return "", nil, err
		}
		return encodeWire("batch_failed", wireBatchFailed{BatchID: v.BatchID, Reason: string(v.Reason)})
	case TestRefBuilt:
		return encodeWire("test_ref_built", wireTestRefBuilt{BatchID: v.BatchID, TestedSHA: v.TestedSHA, At: v.At, Method: string(v.Method)})
	case TestRefBuildFailed:
		return encodeWire("test_ref_build_failed", wireTestRefBuildFailed(v))
	case LandingStarted:
		return encodeWire("landing_started", wireLandingStarted(v))
	case Landed:
		return encodeWire("landed", wireLanded(v))
	case HeadCleanupRequested:
		if err := v.Validate(); err != nil {
			return "", nil, err
		}
		return encodeWire("head_cleanup_requested", wireHeadCleanupRequested(v))
	case LandingAbandoned:
		return encodeWire("landing_abandoned", wireLandingAbandoned(v))
	case Dequeued:
		return encodeWire("dequeued", wireDequeued{PR: v.PR, AdmissionID: v.AdmissionID, Reason: string(v.Reason), BatchID: v.BatchID})
	case ActionCompleted:
		return encodeWire("action_completed", wireActionCompleted(v))
	case ActionPoisoned:
		return encodeWire("action_poisoned", wireActionPoisoned(v))
	default:
		return "", nil, fmt.Errorf("unregistered queue event %T", value)
	}
}

// DecodeEvent restores persisted fields without consulting the planner.
func DecodeEvent(payload []byte) (Event, error) {
	envelope, err := decodeWire(payload)
	if err != nil {
		return nil, err
	}
	switch envelope.Type {
	case "pr_observed":
		return decodeWireData(envelope.Data, func(v wirePRObserved) Event {
			return PRObserved(v)
		})
	case "base_moved":
		return decodeWireData(envelope.Data, func(v wireBaseMoved) Event {
			return BaseMoved(v)
		})
	case "checks_completed":
		v, err := readWireData[wireChecksCompleted](envelope.Data)
		if err != nil {
			return nil, err
		}
		event := ChecksCompleted(v)
		if err := event.Validate(); err != nil {
			return nil, err
		}
		return event, nil
	case "checks_reopened":
		v, err := readWireData[wireChecksReopened](envelope.Data)
		if err != nil {
			return nil, err
		}
		event := ChecksReopened(v)
		if err := event.Validate(); err != nil {
			return nil, err
		}
		return event, nil
	case "checks_refreshed":
		return decodeWireData(envelope.Data, func(v wireChecksRefreshed) Event {
			return ChecksRefreshed(v)
		})
	case "pr_closed":
		return decodeWireData(envelope.Data, func(v wirePRClosed) Event {
			return PRClosed(v)
		})
	case "enqueue_requested":
		return decodeWireData(envelope.Data, func(v wireEnqueueRequested) Event {
			return EnqueueRequested(v)
		})
	case "dequeue_requested":
		return decodeWireData(envelope.Data, func(v wireDequeueRequested) Event {
			return DequeueRequested(v)
		})
	case "refresh_requested":
		return decodeWireData(envelope.Data, func(wireResumeRequested) Event { return RefreshRequested{} })
	case "pause_requested":
		v, err := readWireData[wirePauseRequested](envelope.Data)
		if err != nil {
			return nil, err
		}
		event := PauseRequested(v)
		if err := event.Validate(); err != nil {
			return nil, err
		}
		return event, nil
	case "resume_requested":
		return decodeWireData(envelope.Data, func(_ wireResumeRequested) Event {
			return ResumeRequested{}
		})
	case "rules_blocked":
		v, err := readWireData[wireRulesBlocked](envelope.Data)
		if err != nil {
			return nil, err
		}
		event := RulesBlocked(v)
		if err := event.Validate(); err != nil {
			return nil, err
		}
		return event, nil
	case "rules_cleared":
		v, err := readWireData[wireRulesCleared](envelope.Data)
		if err != nil {
			return nil, err
		}
		event := RulesCleared(v)
		if err := event.Validate(); err != nil {
			return nil, err
		}
		return event, nil
	case "batch_started":
		v, err := readWireData[wireBatchStarted](envelope.Data)
		if err != nil {
			return nil, err
		}
		event := BatchStarted{BatchID: v.BatchID, BaseSHA: v.BaseSHA, PRs: decodeBatchPRs(v.PRs), At: v.At}
		if err := event.Validate(); err != nil {
			return nil, err
		}
		return event, nil
	case "batch_failed":
		v, err := readWireData[wireBatchFailed](envelope.Data)
		if err != nil {
			return nil, err
		}
		event := BatchFailed{BatchID: v.BatchID, Reason: DequeueReason(v.Reason)}
		if err := FailBatch(event).Validate(); err != nil {
			return nil, err
		}
		return event, nil
	case "test_ref_built":
		return decodeWireData(envelope.Data, func(v wireTestRefBuilt) Event {
			return TestRefBuilt{BatchID: v.BatchID, TestedSHA: v.TestedSHA, At: v.At, Method: MergeMethod(v.Method)}
		})
	case "test_ref_build_failed":
		return decodeWireData(envelope.Data, func(v wireTestRefBuildFailed) Event {
			return TestRefBuildFailed(v)
		})
	case "landing_started":
		return decodeWireData(envelope.Data, func(v wireLandingStarted) Event {
			return LandingStarted(v)
		})
	case "landed":
		return decodeWireData(envelope.Data, func(v wireLanded) Event {
			return Landed(v)
		})
	case "head_cleanup_requested":
		v, err := readWireData[wireHeadCleanupRequested](envelope.Data)
		if err != nil {
			return nil, err
		}
		event := HeadCleanupRequested(v)
		if err := event.Validate(); err != nil {
			return nil, err
		}
		return event, nil
	case "landing_abandoned":
		return decodeWireData(envelope.Data, func(v wireLandingAbandoned) Event {
			return LandingAbandoned(v)
		})
	case "dequeued":
		return decodeWireData(envelope.Data, func(v wireDequeued) Event {
			return Dequeued{PR: v.PR, AdmissionID: v.AdmissionID, Reason: DequeueReason(v.Reason), BatchID: v.BatchID}
		})
	case "action_completed":
		return decodeWireData(envelope.Data, func(v wireActionCompleted) Event {
			return ActionCompleted(v)
		})
	case "action_poisoned":
		return decodeWireData(envelope.Data, func(v wireActionPoisoned) Event {
			return ActionPoisoned(v)
		})
	default:
		return nil, fmt.Errorf("unknown queue event type %q", envelope.Type)
	}
}

// EncodeAction returns the stable type discriminator and versioned JSON.
func EncodeAction(value Action) (string, []byte, error) {
	switch v := value.(type) {
	case FailBatch:
		return encodeWire("fail_batch", wireBatchFailed{BatchID: v.BatchID, Reason: string(v.Reason)})
	case BuildTestRef:
		return encodeWire("build_test_ref", wireBuildTestRef{BatchID: v.BatchID, BaseSHA: v.BaseSHA, PRs: encodeBatchPRs(v.PRs), Ref: v.Ref, Method: string(v.Method)})
	case RefreshChecks:
		return encodeWire("refresh_checks", wireRefreshChecks{BatchID: v.BatchID, TestedSHA: v.TestedSHA, Epoch: v.epoch})
	case Land:
		return encodeWire("land", wireLand{BatchID: v.BatchID, ExpectedBaseSHA: v.ExpectedBaseSHA, TestedSHA: v.TestedSHA, Ref: v.Ref, FenceToken: v.FenceToken, PRs: encodeBatchPRs(v.PRs), Method: string(v.Method), ChecksGeneration: v.ChecksGeneration})
	case ResolveLanding:
		return encodeWire("resolve_landing", wireResolveLanding(v))
	case DeleteRef:
		return encodeWire("delete_ref", wireDeleteRef(v))
	case Dequeue:
		return encodeWire("dequeue", wireDequeue{HeadSHA: v.HeadSHA, PR: v.PR, AdmissionID: v.AdmissionID, Reason: string(v.Reason), BatchID: v.BatchID, Scope: v.scope})
	case ReportStatus:
		return encodeWire("report_status", wireReportStatus{PR: v.PR, HeadSHA: v.HeadSHA, Status: v.Status, Reason: v.Reason, Detail: v.Detail, Revision: v.revision, Admission: v.admission})
	case MarkLanded:
		return encodeWire("mark_landed", wireMarkLanded(v))
	case DeleteHeadBranch:
		return encodeWire("delete_head_branch", wireDeleteHeadBranch(v))
	default:
		return "", nil, fmt.Errorf("unregistered queue action %T", value)
	}
}

// DecodeAction restores persisted fields without consulting the planner.
func DecodeAction(payload []byte) (Action, error) {
	envelope, err := decodeWire(payload)
	if err != nil {
		return nil, err
	}
	switch envelope.Type {
	case "fail_batch":
		return decodeWireAction(envelope.Data, func(v wireBatchFailed) Action { return FailBatch{BatchID: v.BatchID, Reason: DequeueReason(v.Reason)} })
	case "build_test_ref":
		return decodeWireAction(envelope.Data, func(v wireBuildTestRef) Action {
			return BuildTestRef{BatchID: v.BatchID, BaseSHA: v.BaseSHA, PRs: decodeBatchPRs(v.PRs), Ref: v.Ref, Method: MergeMethod(v.Method)}
		})
	case "refresh_checks":
		return decodeWireAction(envelope.Data, func(v wireRefreshChecks) Action {
			return RefreshChecks{BatchID: v.BatchID, TestedSHA: v.TestedSHA, epoch: v.Epoch}
		})
	case "land":
		return decodeWireAction(envelope.Data, func(v wireLand) Action {
			return Land{BatchID: v.BatchID, ExpectedBaseSHA: v.ExpectedBaseSHA, TestedSHA: v.TestedSHA, Ref: v.Ref, FenceToken: v.FenceToken, PRs: decodeBatchPRs(v.PRs), Method: MergeMethod(v.Method), ChecksGeneration: v.ChecksGeneration}
		})
	case "resolve_landing":
		return decodeWireAction(envelope.Data, func(v wireResolveLanding) Action {
			return ResolveLanding(v)
		})
	case "delete_ref":
		return decodeWireAction(envelope.Data, func(v wireDeleteRef) Action {
			return DeleteRef(v)
		})
	case "dequeue":
		return decodeWireAction(envelope.Data, func(v wireDequeue) Action {
			return Dequeue{HeadSHA: v.HeadSHA, PR: v.PR, AdmissionID: v.AdmissionID, Reason: DequeueReason(v.Reason), BatchID: v.BatchID, scope: v.Scope}
		})
	case "report_status":
		return decodeWireAction(envelope.Data, func(v wireReportStatus) Action {
			return ReportStatus{PR: v.PR, HeadSHA: v.HeadSHA, Status: v.Status, Reason: v.Reason, Detail: v.Detail, revision: v.Revision, admission: v.Admission}
		})
	case "mark_landed":
		return decodeWireAction(envelope.Data, func(v wireMarkLanded) Action {
			return MarkLanded(v)
		})
	case "delete_head_branch":
		return decodeWireAction(envelope.Data, func(v wireDeleteHeadBranch) Action {
			return DeleteHeadBranch(v)
		})
	default:
		return nil, fmt.Errorf("unknown queue action type %q", envelope.Type)
	}
}
