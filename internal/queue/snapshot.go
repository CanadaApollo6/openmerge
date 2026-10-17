package queue

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"
)

// SnapshotVersion is separate from the additive event wire version. Snapshots
// contain bounded projection state, never a copy of the event history.
const SnapshotVersion = 1

// Decoder resource limits are deliberately above ordinary queue sizes. This
// codec is not wired into store persistence; PR 5 must add adaptive window state.
const maxSnapshotEntries = 100000
const maxSnapshotEffects = 100000
const maxSnapshotBytes = 64 << 20

type batchSnapshot struct {
	Batch                 Batch
	ChecksPassed          bool
	ChecksGeneration      uint64
	ChecksRefreshedAt     time.Time
	CheckRefreshes        uint64
	NeedsRefresh          bool
	CleanupRequested      bool
	CleanupPolicyRecorded bool
	UnboundChecks         []UnboundCheck
}
type groupSnapshot struct {
	PRs       []BatchPR
	Bisection bool
}
type dequeueSnapshot struct {
	Action Dequeue
	Scope  string
}
type scopeSnapshot struct {
	PR        int
	Admission uint64
	Batch     string
}
type checkSnapshot struct {
	Terminal, Success bool
	Revision          uint64
}
type buildSnapshot struct {
	Failures int
	FailedAt time.Time
	Revision uint64
	Detail   string
}
type poisonSnapshot struct {
	Key, Reason string
	Revision    uint64
}
type rulesSnapshot struct {
	Problems []string
	Revision uint64
}
type stateSnapshot struct {
	Version                                                                  int
	Lifecycle                                                                LifecycleChanged
	BaseBranch, BaseSHA, Fence, ConfigCommit, ConfigDigest                   string
	Entries                                                                  []Entry
	Chain                                                                    []batchSnapshot
	Pending                                                                  []groupSnapshot
	Invalidations                                                            []BatchesInvalidated
	NextBatch, NextAdmission, Revision                                       uint64
	Observations                                                             map[int]PRObserved
	Dequeues                                                                 map[int]dequeueSnapshot
	Effects                                                                  []json.RawMessage
	Acknowledged                                                             map[string]scopeSnapshot
	Checks                                                                   map[string]checkSnapshot
	TestedBatches                                                            map[string]string
	TestedOrder                                                              []string
	BuildRetry                                                               buildSnapshot
	ActionPoison                                                             poisonSnapshot
	Pause                                                                    PauseRequested
	BlockSources                                                             map[BlockSource]BlockSourceBlocked
	BlockRevisions                                                           map[BlockSource]uint64
	BlockPauseRevision, PauseRevision, LifecycleRevision, RulesPauseRevision uint64
	RulesSources                                                             map[RulesSource]rulesSnapshot
}

// EncodeSnapshot captures every input required to resume a pure fold and plan,
// including per-batch revocation generations, timer evidence and retry fences.
// The store currently replays journals; this is its compaction boundary.
func EncodeSnapshot(s State) ([]byte, error) {
	v := stateSnapshot{
		Version:            SnapshotVersion,
		Lifecycle:          s.Lifecycle,
		BaseBranch:         s.BaseBranch,
		BaseSHA:            s.BaseSHA,
		Fence:              s.Fence,
		ConfigCommit:       s.ConfigCommit,
		ConfigDigest:       s.ConfigDigest,
		Entries:            s.Entries,
		Invalidations:      s.Invalidations,
		NextBatch:          s.nextBatch,
		NextAdmission:      s.nextAdmission,
		Revision:           s.revision,
		Observations:       s.observations,
		Dequeues:           make(map[int]dequeueSnapshot),
		Acknowledged:       make(map[string]scopeSnapshot),
		Checks:             make(map[string]checkSnapshot),
		TestedBatches:      s.testedBatches,
		TestedOrder:        s.testedOrder,
		BuildRetry:         buildSnapshot{s.buildRetry.failures, s.buildRetry.failedAt, s.buildRetry.revision, s.buildRetry.detail},
		ActionPoison:       poisonSnapshot{s.actionPoison.key, s.actionPoison.reason, s.actionPoison.revision},
		Pause:              s.pause,
		BlockSources:       s.blockSources,
		BlockRevisions:     s.blockRevisions,
		BlockPauseRevision: s.blockPauseRevision,
		PauseRevision:      s.pauseRevision,
		LifecycleRevision:  s.lifecycleRevision,
		RulesPauseRevision: s.rulesPauseRevision,
		RulesSources:       make(map[RulesSource]rulesSnapshot),
	}
	for _, b := range s.Batches() {
		v.Chain = append(v.Chain, batchSnapshot{*b, b.checksPassed, b.checksGeneration, b.checksRefreshedAt, b.checkRefreshes, b.needsRefresh, b.cleanupRequested, b.LandingAttempt.cleanupPolicyRecorded, b.unboundChecks})
	}
	for _, g := range s.pending {
		v.Pending = append(v.Pending, groupSnapshot{g.prs, g.bisection})
	}
	for pr, d := range s.dequeues {
		v.Dequeues[pr] = dequeueSnapshot{d, d.scope}
	}
	for key, scope := range s.acknowledged {
		v.Acknowledged[key] = scopeSnapshot{scope.pr, scope.admission, scope.batch}
	}
	for sha, c := range s.checks {
		v.Checks[sha] = checkSnapshot{c.terminal, c.success, c.revision}
	}
	for source, r := range s.rulesSources {
		v.RulesSources[source] = rulesSnapshot{r.problems, r.revision}
	}
	for _, effect := range s.effects {
		_, data, err := EncodeAction(effect)
		if err != nil {
			return nil, err
		}
		v.Effects = append(v.Effects, data)
	}
	return json.Marshal(v)
}

// DecodeSnapshot rejects incomplete/unknown schemas rather than manufacturing
// missing timer or fence evidence. Subsequent events use the ordinary Apply.
func DecodeSnapshot(data []byte) (State, error) {
	if len(data) > maxSnapshotBytes {
		return State{}, fmt.Errorf("snapshot exceeds byte bound")
	}
	if err := requireSnapshotFields(data, reflect.TypeFor[stateSnapshot](), "snapshot"); err != nil {
		return State{}, err
	}
	var v stateSnapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&v); err != nil {
		return State{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return State{}, fmt.Errorf("trailing snapshot data")
	}
	if v.Version != SnapshotVersion || len(v.Chain) > 32 || len(v.Invalidations) > 32 || len(v.TestedOrder) > testedHistory || len(v.Checks) > maxRetainedChecks || len(v.Entries) > maxSnapshotEntries || len(v.Effects) > maxSnapshotEffects {
		return State{}, fmt.Errorf("invalid snapshot version or bounds")
	}
	s := State{
		Lifecycle:          v.Lifecycle,
		BaseBranch:         v.BaseBranch,
		BaseSHA:            v.BaseSHA,
		Fence:              v.Fence,
		ConfigCommit:       v.ConfigCommit,
		ConfigDigest:       v.ConfigDigest,
		Entries:            v.Entries,
		Invalidations:      v.Invalidations,
		nextBatch:          v.NextBatch,
		nextAdmission:      v.NextAdmission,
		revision:           v.Revision,
		observations:       v.Observations,
		dequeues:           make(map[int]Dequeue),
		acknowledged:       make(map[string]effectScope),
		checks:             make(map[string]retainedCheck),
		testedBatches:      v.TestedBatches,
		testedOrder:        v.TestedOrder,
		buildRetry:         buildRetryState{v.BuildRetry.Failures, v.BuildRetry.FailedAt, v.BuildRetry.Revision, v.BuildRetry.Detail},
		actionPoison:       actionPoisonState{v.ActionPoison.Key, v.ActionPoison.Reason, v.ActionPoison.Revision},
		pause:              v.Pause,
		blockSources:       v.BlockSources,
		blockRevisions:     v.BlockRevisions,
		blockPauseRevision: v.BlockPauseRevision,
		pauseRevision:      v.PauseRevision,
		lifecycleRevision:  v.LifecycleRevision,
		rulesPauseRevision: v.RulesPauseRevision,
		rulesSources:       make(map[RulesSource]rulesSourceState),
	}
	seen := make(map[string]bool)
	seenPRs := make(map[int]bool)
	invalidating := false
	for i, item := range v.Chain {
		b := item.Batch
		if b.ID == "" || seen[b.ID] || b.BaseSHA == "" || b.ParentBatchID == b.ID || validateBatchPRs(b.PRs) != nil {
			return State{}, fmt.Errorf("invalid snapshot batch")
		}
		if len(b.ObservedRuns) > 0 {
			if err := validateCheckRuns(b.ObservedRuns); err != nil {
				return State{}, err
			}
			if err := validateHistoricalRuns(b.ObservedRuns); err != nil {
				return State{}, err
			}
		}
		if len(b.LastObservedRuns) > 0 {
			if err := validateHistoricalRuns(b.LastObservedRuns); err != nil {
				return State{}, err
			}
		}
		for _, pr := range b.PRs {
			if seenPRs[pr.PR] {
				return State{}, fmt.Errorf("duplicate PR in snapshot chain")
			}
			seenPRs[pr.PR] = true
		}
		seen[b.ID] = true
		if b.Speculative {
			if validateChainPosition(b.ParentBatchID, b.ChainPosition) != nil {
				return State{}, fmt.Errorf("invalid snapshot position")
			}
		} else if i > 0 || b.ParentBatchID != "" || b.ChainPosition != 0 {
			return State{}, fmt.Errorf("invalid legacy snapshot position")
		}
		if invalidating && b.InvalidationReason == "" {
			return State{}, fmt.Errorf("invalid snapshot suffix")
		}
		invalidating = invalidating || b.InvalidationReason != ""
		if i == 0 && b.InvalidationReason == "" && b.Phase != Landing && b.BaseSHA != v.BaseSHA {
			return State{}, fmt.Errorf("invalid snapshot root base")
		}
		if b.Phase != Building && b.Phase != Testing && b.Phase != Landing || i > 0 && b.Phase == Landing {
			return State{}, fmt.Errorf("invalid snapshot phase")
		}
		if i > 0 {
			parent := v.Chain[i-1].Batch
			if b.ParentBatchID == parent.ID {
				if b.BaseSHA != parent.LandingCommit() || b.ChainPosition != parent.ChainPosition+1 {
					return State{}, fmt.Errorf("invalid snapshot chain")
				}
			} else if b.InvalidationReason == "" || b.ChainPosition <= parent.ChainPosition+1 || seen[b.ParentBatchID] {
				return State{}, fmt.Errorf("invalid snapshot chain")
			}
		}
		b.checksPassed, b.checksGeneration, b.checksRefreshedAt, b.checkRefreshes, b.needsRefresh, b.cleanupRequested, b.unboundChecks = item.ChecksPassed, item.ChecksGeneration, item.ChecksRefreshedAt, item.CheckRefreshes, item.NeedsRefresh, item.CleanupRequested, item.UnboundChecks
		b.LandingAttempt.cleanupPolicyRecorded = item.CleanupPolicyRecorded
		if i == 0 {
			s.Active = &b
		} else {
			s.Following = append(s.Following, &b)
		}
	}
	for _, g := range v.Pending {
		if validateBatchPRs(g.PRs) != nil {
			return State{}, fmt.Errorf("invalid pending group")
		}
		for _, pr := range g.PRs {
			if seenPRs[pr.PR] {
				return State{}, fmt.Errorf("duplicate PR in snapshot chain or pending groups")
			}
			seenPRs[pr.PR] = true
		}
		s.pending = append(s.pending, pendingGroup{g.PRs, g.Bisection})
	}
	entryPRs := make(map[int]bool)
	for _, entry := range v.Entries {
		if entry.PR <= 0 || entry.HeadSHA == "" || entryPRs[entry.PR] {
			return State{}, fmt.Errorf("invalid or duplicate snapshot entry")
		}
		entryPRs[entry.PR] = true
	}
	for pr, d := range v.Dequeues {
		d.Action.scope = d.Scope
		if err := d.Action.Validate(); err != nil {
			return State{}, err
		}
		s.dequeues[pr] = d.Action
	}
	for key, scope := range v.Acknowledged {
		s.acknowledged[key] = effectScope{scope.PR, scope.Admission, scope.Batch}
	}
	for sha, c := range v.Checks {
		s.checks[sha] = retainedCheck{c.Terminal, c.Success, c.Revision}
	}
	for source, r := range v.RulesSources {
		s.rulesSources[source] = rulesSourceState{r.Problems, r.Revision}
	}
	effectKeys := make(map[string]bool)
	for _, raw := range v.Effects {
		a, err := DecodeAction(raw)
		if err != nil {
			return State{}, err
		}
		if effectKeys[a.Key()] {
			return State{}, fmt.Errorf("duplicate snapshot effect key %q", a.Key())
		}
		effectKeys[a.Key()] = true
		s.effects = append(s.effects, a)
	}
	return cloneState(s), nil
}

// Snapshot collections may be nil, but scalar evidence and every non-optional
// field must be present, including nested checks generations and retry fences.
func requireSnapshotFields(raw []byte, schema reflect.Type, path string) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return fmt.Errorf("%s is missing", path)
	}
	if bytes.Equal(trimmed, []byte("null")) {
		switch schema.Kind() {
		case reflect.Map, reflect.Slice, reflect.Pointer:
			return nil
		default:
			return fmt.Errorf("%s must not be null", path)
		}
	}
	if schema == reflect.TypeFor[time.Time]() || schema == reflect.TypeFor[json.RawMessage]() {
		return nil
	}
	switch schema.Kind() {
	case reflect.Pointer:
		return requireSnapshotFields(raw, schema.Elem(), path)
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		for i := 0; i < schema.NumField(); i++ {
			field := schema.Field(i)
			if !field.IsExported() {
				continue
			}
			name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			value, ok := fields[name]
			if !ok && strings.Contains(options, "omitempty") {
				continue
			}
			if err := requireSnapshotFields(value, field.Type, path+"."+name); err != nil {
				return err
			}
		}
	case reflect.Slice:
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return err
		}
		for i, item := range items {
			if err := requireSnapshotFields(item, schema.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case reflect.Map:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		for key, value := range fields {
			if err := requireSnapshotFields(value, schema.Elem(), path+"."+key); err != nil {
				return err
			}
		}
	}
	return nil
}
