package queue

import (
	"fmt"
	"slices"
)

// BlockSource owns one independently observed reason to stop new work.
type BlockSource string

// Independently managed runtime block sources. Rules and lifecycle keep their existing events.
const (
	ConfigSource     BlockSource = "config"
	CheckSources     BlockSource = "check-sources"
	LandingNotOnBase BlockSource = "landing-not-on-base"
	ActionPoison     BlockSource = "action-poisoned"
	Operator         BlockSource = "operator"
)

var blockSourceOrder = [...]BlockSource{ConfigSource, CheckSources, LandingNotOnBase, ActionPoison, Operator}

// BlockSourceBlocked always applies a definite failure. Subject pins the SHA
// requiring landing verification, or identifies the quarantined action key.
type BlockSourceBlocked struct {
	Source  BlockSource
	Detail  string
	Subject string
	Basis   uint64
}

// BlockSourceCleared clears only Source, at its current observation revision.
type BlockSourceCleared struct {
	Source BlockSource
	Basis  uint64
}

func (BlockSourceBlocked) queueEvent() {}
func (BlockSourceCleared) queueEvent() {}

func (source BlockSource) validate() error {
	if !slices.Contains(blockSourceOrder[:], source) {
		return fmt.Errorf("unknown block source %q", source)
	}
	return nil
}

// Validate requires a diagnostic and a durable subject for landing/poison blocks.
func (e BlockSourceBlocked) Validate() error {
	if err := e.Source.validate(); err != nil {
		return err
	}
	if err := requireActionText("detail", e.Detail); err != nil {
		return err
	}
	if e.Source == LandingNotOnBase || e.Source == ActionPoison {
		if err := requireActionText("subject", e.Subject); err != nil {
			return err
		}
	} else if e.Subject != "" {
		return fmt.Errorf("block source %q does not accept a subject", e.Source)
	}
	return (RulesCleared{Basis: e.Basis}).Validate()
}

// Validate rejects unknown sources and non-incrementable revisions.
func (e BlockSourceCleared) Validate() error {
	if err := e.Source.validate(); err != nil {
		return err
	}
	return (RulesCleared{Basis: e.Basis}).Validate()
}

// OperatorClearable excludes automatically observed conditions and lifecycle.
func (source BlockSource) OperatorClearable() bool {
	return source == Operator || source == ActionPoison || source == LandingNotOnBase
}

// BlockSourceRevision is independent of every other source's observations.
func (s State) BlockSourceRevision(source BlockSource) uint64 {
	return s.blockRevisions[source]
}

// BlockSourceSubject returns the durable evidence to verify before clearing.
func (s State) BlockSourceSubject(source BlockSource) string { return s.blockSources[source].Subject }

// BlockSourceDetail includes legacy blocks under their compatible source.
func (s State) BlockSourceDetail(source BlockSource) string {
	if detail := s.blockSources[source].Detail; detail != "" {
		return detail
	}
	if source == Operator && s.pause.Reason != "" {
		return s.pause.Detail
	}
	if source == ActionPoison && s.actionPoison.key != "" {
		return s.poisonDetail()
	}
	return ""
}

// SourceBlocked also recognizes legacy pauses with an empty detail.
func (s State) SourceBlocked(source BlockSource) bool {
	return s.blockSources[source].Detail != "" || source == Operator && s.pause.Reason != "" || source == ActionPoison && s.actionPoison.key != ""
}

func (s *State) clearLegacySource(source BlockSource) {
	switch source {
	case Operator:
		s.pause = PauseRequested{}
	case ActionPoison:
		s.actionPoison = actionPoisonState{}
	}
}

func (s State) hasBlockSources() bool {
	for _, block := range s.blockSources {
		if block.Detail != "" {
			return true
		}
	}
	return false
}

func (source BlockSource) reason() string {
	switch source {
	case ConfigSource:
		return "invalid-config"
	case CheckSources:
		return "ambiguous-check-sources"
	default:
		return string(source)
	}
}
