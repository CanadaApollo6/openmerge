package queue

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
)

// WindowBounds is recorded with ConfigApplied so replay can clamp at the exact
// policy transition, including when no batch is open. Omitted bounds mean one.
type WindowBounds struct {
	MinDepth int `json:"min_depth"`
	MaxDepth int `json:"max_depth"`
}

func (b WindowBounds) validate() error {
	if b.MinDepth < 1 || b.MinDepth > b.MaxDepth || b.MaxDepth > 32 {
		return fmt.Errorf("invalid speculation window bounds")
	}
	return nil
}

// WindowFailure identifies the accepted event in the queue's fold, not a check
// observation's timestamp. Duplicate observations never replace this evidence.
type WindowFailure struct {
	Revision uint64        `json:"revision"`
	BatchID  string        `json:"batch_id"`
	Reason   DequeueReason `json:"reason"`
}

type adaptiveWindow struct {
	BoundsRecorded    bool
	LegacySpeculative bool
	Bounds            WindowBounds
	Current           int
	LastHalved        *WindowFailure
}

type changeOutcome struct {
	Revision    uint64
	AdmissionID uint64
	PR          int
	Failed      bool
}

// Window returns the admission limit. Legacy logs without recorded bounds keep
// their fixed window until reconciliation records the current policy bounds.
func (s State) Window(cfg Config) int {
	if s.window == nil || !s.window.BoundsRecorded && s.window.LegacySpeculative {
		return cfg.MaxDepth()
	}
	return min(cfg.MaxDepth(), max(max(1, cfg.SpeculationMinDepth), s.window.Current))
}

// WindowInitialized distinguishes old speculative journals during upgrade.
func (s State) WindowInitialized() bool { return s.window != nil && s.window.BoundsRecorded }

// LastWindowFailure returns a copy of the event that last halved the window.
func (s State) LastWindowFailure() *WindowFailure {
	if s.window == nil || s.window.LastHalved == nil {
		return nil
	}
	f := *s.window.LastHalved
	return &f
}

// Applied records bounds only for speculative policies. Legacy depth-1 events
// remain byte-identical; a downgrade's omitted bounds still clamp a known W to 1.
func (cfg Config) Applied(commit, digest string) ConfigApplied {
	e := ConfigApplied{Commit: commit, Digest: digest}
	if cfg.MaxDepth() > 1 {
		e.Window = &WindowBounds{MinDepth: max(1, cfg.SpeculationMinDepth), MaxDepth: cfg.MaxDepth()}
	}
	return e
}

func (s *State) applyWindow(bounds *WindowBounds) {
	recorded := bounds != nil || s.window != nil && s.window.BoundsRecorded
	if bounds == nil {
		bounds = &WindowBounds{MinDepth: 1, MaxDepth: 1}
	}
	if s.window == nil {
		s.window = &adaptiveWindow{Current: bounds.MaxDepth}
	}
	s.window.Bounds = *bounds
	s.window.BoundsRecorded = recorded
	s.window.LegacySpeculative = false
	s.window.Current = min(bounds.MaxDepth, max(bounds.MinDepth, s.window.Current))
}

func (s *State) growWindow() {
	if s.window != nil {
		s.window.Current = min(s.window.Bounds.MaxDepth, s.window.Current+1)
	}
}

func (s *State) halveWindow(b *Batch, reason DequeueReason) {
	if b.FromBisection || b.windowFailureRecorded {
		return
	}
	b.windowFailureRecorded = true
	if s.window != nil {
		s.window.Current = max(s.window.Bounds.MinDepth, s.window.Current/2)
		s.window.LastHalved = &WindowFailure{Revision: s.revision, BatchID: b.ID, Reason: reason}
	}
}

func (s *State) recordOutcome(entry *Entry, failed bool) {
	if entry == nil {
		return
	}
	s.outcomes = append(s.outcomes, changeOutcome{Revision: s.revision, AdmissionID: entry.AdmissionID, PR: entry.PR, Failed: failed})
	if len(s.outcomes) > 200 {
		s.outcomes = s.outcomes[len(s.outcomes)-200:]
	}
}

// FailureEstimate counts checks/timeout dequeues and landings only, in event-log
// order. Status may divide these integers for display; planning never does.
func (s State) FailureEstimate() (failures, samples int) {
	for _, o := range s.outcomes {
		if o.Failed {
			failures++
		}
	}
	return failures, len(s.outcomes)
}

// ValidTargetPassRate accepts zero only as the internal disabled default.
func ValidTargetPassRate(target float64) bool {
	return target == 0 || !math.IsNaN(target) && target > 0 && target < 1
}

// AdaptiveBatchSize compares integer powers against the exact rational value of
// the target's canonical shortest decimal (Go's FormatFloat), rather than pow or
// logarithms. E.g. 0.81 is exactly 81/100, so equality at (.9)^2 always passes.
// The sole float conversion is deterministic IEEE-754 parsing/decimal formatting;
// all threshold comparisons and the empirical failure fraction are exact.
func AdaptiveBatchSize(maxSize, failures, samples int, target float64) int {
	if maxSize == 0 {
		maxSize = 1
	}
	if maxSize < 1 || maxSize > 128 || failures < 0 || failures > samples || samples > 200 || !ValidTargetPassRate(target) {
		panic("invalid adaptive batch configuration")
	}
	if target == 0 || samples < 20 {
		return maxSize
	}
	rate, ok := new(big.Rat).SetString(strconv.FormatFloat(target, 'g', -1, 64))
	if !ok {
		panic("invalid pass rate")
	}
	numerator, denominator := big.NewInt(1), big.NewInt(1)
	good, count := big.NewInt(int64(samples-failures)), big.NewInt(int64(samples))
	size := 1
	for k := 1; k <= maxSize; k++ {
		numerator.Mul(numerator, good)
		denominator.Mul(denominator, count)
		left := new(big.Int).Mul(numerator, rate.Denom())
		right := new(big.Int).Mul(denominator, rate.Num())
		if left.Cmp(right) < 0 {
			break
		}
		size = k
	}
	return size
}

// BatchSize is the ceiling for fresh batches; pending groups retain their sizes.
func (s State) BatchSize(cfg Config) int {
	if cfg.BatchTargetPassRate == 0 {
		return AdaptiveBatchSize(cfg.BatchMaxSize, 0, 0, 0)
	}
	failures, samples := s.FailureEstimate()
	return AdaptiveBatchSize(cfg.BatchMaxSize, failures, samples, cfg.BatchTargetPassRate)
}
