package treededupe

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

// LedgerVersion is the persisted ledger format. A reader refuses any other
// version rather than overwriting it, so a rolled-back binary leaves a newer
// ledger untouched.
const LedgerVersion = 1

// MaxBuilds bounds the retained ledger. Older builds are dropped and counted.
const MaxBuilds = 512

// Build kinds. A rebuild after invalidation is an ordinary batch or chained
// build with a new composition; its key, not its kind, finds the reuse.
const (
	KindBatch       = "batch"
	KindChained     = "chained"
	KindBisection   = "bisection"
	KindFastForward = "fast_forward"
)

// Outcomes and shadow hits.
const (
	OutcomeSuccess     = "success"
	OutcomeFailure     = "failure"
	WouldReuseSuccess  = "would-reuse-success"
	WouldReuseFailure  = "would-reuse-failure"
	VerdictAgree       = "agree"
	VerdictDisagree    = "disagree"
	defaultSummaryMode = "shadow"
)

// Build is one queue test build: a test ref the queue itself pushed and whose
// required checks the queue observed through its existing trust gates. PR CI
// and fork runs never reach this ledger.
type Build struct {
	At      time.Time `json:"at"`
	BatchID string    `json:"batch"`
	SHA     string    `json:"sha"`
	Kind    string    `json:"kind"`
	Key     string    `json:"key,omitempty"`
	Unknown []string  `json:"unknown,omitempty"`
	// Singleton and RetryConfigured decide whether a failure is confirmed:
	// a singleton failure in a queue without the same-commit retry is final.
	Singleton       bool `json:"singleton,omitempty"`
	RetryConfigured bool `json:"retry,omitempty"`

	Outcome   string `json:"outcome,omitempty"`
	Confirmed bool   `json:"confirmed,omitempty"`
	// TerminalAt is the first terminal result and fixes the run duration used
	// for avoidable minutes; a later replacement result never moves it.
	TerminalAt time.Time `json:"terminal_at,omitzero"`
	// ResultSeq is the ledger sequence of this build's newest result or
	// revocation. Reuse follows result order, not build order: among builds of
	// one key, the highest ResultSeq is the result a real cache would hold.
	ResultSeq uint64 `json:"result_seq,omitempty"`

	Hit              string  `json:"hit,omitempty"`
	SourceBatchID    string  `json:"source_batch,omitempty"`
	SourceSHA        string  `json:"source_sha,omitempty"`
	AvoidableSeconds float64 `json:"avoidable_seconds,omitempty"`
	// Verdict compares a hit with this build's own result once the queue acts
	// on it. A disagreement is a false reuse the shadow would have caused.
	Verdict string `json:"verdict,omitempty"`
}

// Ledger is the bounded, persisted shadow record for one queue.
type Ledger struct {
	Version int     `json:"version"`
	Builds  []Build `json:"builds"`
	// Dropped counts builds evicted by MaxBuilds.
	Dropped int `json:"dropped"`
	// Seq is the last assigned result sequence.
	Seq uint64 `json:"seq,omitempty"`
	// Floor maps a key to the newest ResultSeq among its evicted builds, so a
	// retained older result never outranks newer evidence that was dropped.
	// Only keys that still have retained builds are kept.
	Floor map[string]uint64 `json:"floor,omitempty"`
}

// ErrLedgerVersion refuses a ledger written by another format version.
var ErrLedgerVersion = errors.New("tree dedupe shadow ledger version unsupported")

// Decode reads a persisted ledger. Empty input is a new ledger.
func Decode(raw []byte) (Ledger, error) {
	if len(raw) == 0 {
		return Ledger{Version: LedgerVersion}, nil
	}
	var l Ledger
	if err := json.Unmarshal(raw, &l); err != nil {
		return Ledger{}, fmt.Errorf("decode tree dedupe shadow ledger: %w", err)
	}
	if l.Version != LedgerVersion {
		return Ledger{}, ErrLedgerVersion
	}
	return l, nil
}

// Encode writes the ledger.
func (l Ledger) Encode() ([]byte, error) {
	l.Version = LedgerVersion
	if l.Builds == nil {
		l.Builds = []Build{}
	}
	return json.Marshal(l)
}

func (l *Ledger) find(sha string) *Build {
	for i := len(l.Builds) - 1; i >= 0; i-- {
		if l.Builds[i].SHA == sha {
			return &l.Builds[i]
		}
	}
	return nil
}

// ObserveBuild records a new test build and, when its key is known, compares
// it with the newest result already recorded for that key. "Newest" is result
// order (ResultSeq), not build order: a build that started earlier but
// finished or was revoked later is newer evidence. Only a trusted success or a
// confirmed failure is a reuse source; an unconfirmed failure or a revoked
// result is never a candidate and, being newest, also shadows older results
// of that key, because a real cache would hold the newest result. Repeated
// observations of the same batch and SHA are ignored.
func (l *Ledger) ObserveBuild(b Build) {
	for _, old := range l.Builds {
		if old.BatchID == b.BatchID && old.SHA == b.SHA {
			return
		}
	}
	b.Outcome, b.Confirmed, b.TerminalAt, b.ResultSeq, b.Hit, b.SourceBatchID, b.SourceSHA, b.AvoidableSeconds, b.Verdict = "", false, time.Time{}, 0, "", "", "", 0, ""
	if s := l.newestResult(b.Key); s != nil {
		switch {
		case s.Outcome == OutcomeSuccess:
			b.Hit = WouldReuseSuccess
		case s.Outcome == OutcomeFailure && s.Confirmed:
			b.Hit = WouldReuseFailure
		}
		if b.Hit != "" {
			b.SourceBatchID, b.SourceSHA = s.BatchID, s.SHA
			b.AvoidableSeconds = max(0, s.TerminalAt.Sub(s.At).Seconds())
		}
	}
	l.Builds = append(l.Builds, b)
	if extra := len(l.Builds) - MaxBuilds; extra > 0 {
		l.evict(extra)
	}
}

// newestResult returns the retained build of key with the newest result or
// revocation, or nil when the key is unknown, every build is still in flight,
// or the newest evidence was evicted.
func (l *Ledger) newestResult(key string) *Build {
	if key == "" {
		return nil
	}
	var newest *Build
	for i := range l.Builds {
		s := &l.Builds[i]
		if s.Key == key && s.ResultSeq > 0 && (newest == nil || s.ResultSeq > newest.ResultSeq) {
			newest = s
		}
	}
	if newest == nil || newest.ResultSeq <= l.Floor[key] {
		return nil
	}
	return newest
}

func (l *Ledger) evict(n int) {
	for _, gone := range l.Builds[:n] {
		if gone.Key != "" && gone.ResultSeq > l.Floor[gone.Key] {
			if l.Floor == nil {
				l.Floor = map[string]uint64{}
			}
			l.Floor[gone.Key] = gone.ResultSeq
		}
	}
	l.Builds = slices.Delete(l.Builds, 0, n)
	l.Dropped += n
	for key := range l.Floor {
		if !slices.ContainsFunc(l.Builds, func(b Build) bool { return b.Key == key }) {
			delete(l.Floor, key)
		}
	}
	if len(l.Floor) == 0 {
		l.Floor = nil
	}
}

// settle applies a result or revocation to b. Only a change is new evidence:
// it takes the next sequence and recomputes the verdict, so a repeated poll
// of the same terminal result neither reorders results nor rewrites the row.
func (l *Ledger) settle(b *Build, outcome string, confirmed bool, at time.Time) {
	if outcome != "" && b.TerminalAt.IsZero() {
		b.TerminalAt = at
	}
	if b.Outcome == outcome && b.Confirmed == confirmed {
		return
	}
	b.Outcome, b.Confirmed = outcome, confirmed
	l.Seq++
	b.ResultSeq = l.Seq
	b.judge()
}

// ObserveOutcome records a terminal required-check result for sha. It may
// replace an earlier result; the first terminal time still fixes duration.
func (l *Ledger) ObserveOutcome(sha string, at time.Time, success bool) {
	b := l.find(sha)
	if b == nil {
		return
	}
	if success {
		l.settle(b, OutcomeSuccess, false, at)
		return
	}
	// Confirmed implies a failure outcome, so a repeated failure keeps it.
	confirmed := b.Confirmed || b.Singleton && !b.RetryConfigured
	l.settle(b, OutcomeFailure, confirmed, at)
}

// ObserveReopened revokes a recorded result when the queue reopens checks. A
// revoked build is a miss and shadows older results of its key until a
// replacement terminal result arrives; its verdict is cleared and recomputed
// from that replacement.
func (l *Ledger) ObserveReopened(sha string) {
	if b := l.find(sha); b != nil && b.Outcome != "" {
		l.settle(b, "", false, time.Time{})
	}
}

// ObserveConfirmedFailure records the queue's retry-confirmed check failure.
// Timeout confirmations are not test results and are ignored.
func (l *Ledger) ObserveConfirmedFailure(batchID, sha string, at time.Time) {
	b := l.find(sha)
	if b == nil || b.BatchID != batchID {
		return
	}
	l.settle(b, OutcomeFailure, true, at)
}

// judge derives the verdict from the current result alone, once the queue
// acts on it: a success, or a failure that is confirmed or that no retry will
// revisit. Anything else, including a revoked result, has no verdict yet.
func (b *Build) judge() {
	b.Verdict = ""
	if b.Hit == "" {
		return
	}
	switch {
	case b.Outcome == OutcomeSuccess:
		b.Verdict = verdict(b.Hit == WouldReuseSuccess)
	case b.Outcome == OutcomeFailure && (b.Confirmed || !b.RetryConfigured):
		b.Verdict = verdict(b.Hit == WouldReuseFailure)
	}
}

func verdict(agree bool) string {
	if agree {
		return VerdictAgree
	}
	return VerdictDisagree
}

// Summary aggregates shadow observations. Nothing in it was applied.
type Summary struct {
	Mode       string `json:"mode"`
	KeyVersion string `json:"key_version"`
	Builds     int    `json:"builds"`
	Keyed      int    `json:"keyed"`
	// KeyUnknown counts builds per unknown input; one build may count under
	// several inputs.
	KeyUnknown        map[string]int `json:"key_unknown,omitempty"`
	WouldReuseSuccess int            `json:"would_reuse_success"`
	WouldReuseFailure int            `json:"would_reuse_failure"`
	HitsByKind        map[string]int `json:"hits_by_kind,omitempty"`
	// AvoidableMinutes sums the source runs' wall-clock durations, from test
	// ref push to first terminal result, for every hit.
	AvoidableMinutes float64 `json:"avoidable_minutes"`
	Agreed           int     `json:"agreed"`
	Disagreed        int     `json:"disagreed"`
	// Retained and Dropped describe ledger coverage; Since is the oldest
	// retained build. Counts before Since are no longer available.
	Retained int       `json:"retained"`
	Dropped  int       `json:"dropped"`
	Since    time.Time `json:"since,omitzero"`
}

// Summarize aggregates builds recorded in [since, until). Zero bounds are open.
func (l Ledger) Summarize(since, until time.Time) Summary {
	s := Summary{Mode: defaultSummaryMode, KeyVersion: KeyVersion, Retained: len(l.Builds), Dropped: l.Dropped}
	if len(l.Builds) > 0 {
		s.Since = l.Builds[0].At
	}
	unknown, kinds := map[string]int{}, map[string]int{}
	for _, b := range l.Builds {
		if !since.IsZero() && b.At.Before(since) || !until.IsZero() && !b.At.Before(until) {
			continue
		}
		s.Builds++
		if b.Key != "" {
			s.Keyed++
		}
		for _, input := range b.Unknown {
			unknown[input]++
		}
		switch b.Hit {
		case WouldReuseSuccess:
			s.WouldReuseSuccess++
		case WouldReuseFailure:
			s.WouldReuseFailure++
		default:
			continue
		}
		kinds[b.Kind]++
		s.AvoidableMinutes += b.AvoidableSeconds / 60
		switch b.Verdict {
		case VerdictAgree:
			s.Agreed++
		case VerdictDisagree:
			s.Disagreed++
		}
	}
	if len(unknown) > 0 {
		s.KeyUnknown = unknown
	}
	if len(kinds) > 0 {
		s.HitsByKind = kinds
	}
	return s
}

// Unknowns converts key inputs to their persisted names.
func Unknowns(inputs []UnknownInput) []string {
	if len(inputs) == 0 {
		return nil
	}
	out := make([]string, len(inputs))
	for i, input := range inputs {
		out[i] = string(input)
	}
	return out
}
