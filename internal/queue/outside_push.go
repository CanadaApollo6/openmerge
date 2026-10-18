package queue

import (
	"fmt"
	"slices"
	"time"
)

// OutsidePushCommitLimit bounds the newly reachable commits retained per observation.
const OutsidePushCommitLimit = 100

// OutsidePushLimit bounds the status/snapshot projection; the journal keeps every event.
const OutsidePushLimit = 20

// OutsidePushRecorded is an observation supported by fence evidence, never an inference
// from commit authorship. Commits are newly reachable from NewSHA but not PreviousSHA,
// newest first in topological order, including side histories. A rollback can have
// a known empty list. Unknown ancestry must not be represented as a proven fast-forward.
// Later verification is separate evidence and must never replace this record.
type OutsidePushRecorded struct {
	PreviousSHA               string    `json:"previous_sha"`
	NewSHA                    string    `json:"new_sha"`
	AncestryKnown             bool      `json:"ancestry_known"`
	Rewrite                   bool      `json:"rewrite"`
	HistoryKnown              bool      `json:"history_known"`
	Commits                   []string  `json:"commits"`
	Truncated                 bool      `json:"truncated"`
	VerificationBeforeLanding string    `json:"verification_before_landing"`
	At                        time.Time `json:"at"`
}

func (OutsidePushRecorded) queueEvent() {}

// Validate rejects ambiguous combinations rather than manufacturing evidence.
func (e OutsidePushRecorded) Validate() error {
	if e.PreviousSHA == "" || e.NewSHA == "" || e.PreviousSHA == e.NewSHA || e.At.IsZero() || e.VerificationBeforeLanding != "none" {
		return fmt.Errorf("invalid outside push observation")
	}
	if !e.AncestryKnown && e.Rewrite || !e.HistoryKnown && (len(e.Commits) != 0 || e.Truncated) || len(e.Commits) > OutsidePushCommitLimit || e.Truncated && len(e.Commits) != OutsidePushCommitLimit {
		return fmt.Errorf("invalid outside push history")
	}
	if e.HistoryKnown && ((e.AncestryKnown && !e.Rewrite && len(e.Commits) == 0) || (len(e.Commits) > 0 && !slices.Contains(e.Commits, e.NewSHA))) {
		return fmt.Errorf("outside push history must include the new tip")
	}
	seen := make(map[string]bool)
	for _, sha := range e.Commits {
		if sha == "" || sha == e.PreviousSHA || seen[sha] {
			return fmt.Errorf("invalid outside push commit")
		}
		seen[sha] = true
	}
	return nil
}

func cloneOutsidePushes(pushes []OutsidePushRecorded) []OutsidePushRecorded {
	result := slices.Clone(pushes)
	for i := range result {
		result[i].Commits = append([]string{}, result[i].Commits...)
	}
	return result
}

// RecentOutsidePushes returns oldest first, without sharing mutable projection data.
func (s State) RecentOutsidePushes() []OutsidePushRecorded {
	return cloneOutsidePushes(s.outsidePushes)
}
