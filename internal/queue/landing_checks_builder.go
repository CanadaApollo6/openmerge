package queue

import (
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

type landingCheckKey struct {
	Name, Source, Kind, Identity, Result, Branch string
	Run, Attempt, Workflow                       uint64
}

func (c LandingCheck) key() landingCheckKey {
	k := landingCheckKey{Name: c.Name, Source: c.Source, Kind: c.ProducerKind, Result: string(c.Result)}
	if c.ProducerIdentity != nil {
		k.Identity = *c.ProducerIdentity
	}
	if c.HeadBranch != nil {
		k.Branch = *c.HeadBranch
	}
	if c.RunID != nil {
		k.Run = *c.RunID
	}
	if c.Attempt != nil {
		k.Attempt = *c.Attempt
	}
	if c.WorkflowRunID != nil {
		k.Workflow = *c.WorkflowRunID
	}
	return k
}

// LandingChecksBuilder interns only bounded inputs. Its cumulative budget counts
// the complete encoded envelope before any input list is copied or serialized.
// Discard the builder after an error; no partial evidence may authorize a start.
type LandingChecksBuilder struct {
	event  LandingChecksRecorded
	bytes  int
	checks map[landingCheckKey]int
}

// NewLandingChecksBuilder starts empty shared tables for one exact read.
func NewLandingChecksBuilder(header LandingChecksRecorded) (_ *LandingChecksBuilder, err error) {
	defer func() { err = landingChecksBuildError(err) }()
	if err := header.validateHeader(); err != nil {
		return nil, err
	}
	header.RequiredCheckSource.SourceRevisions = slices.Clone(header.RequiredCheckSource.SourceRevisions)
	header.Members = []LandingCheckMember{}
	header.Checks = []LandingCheck{}
	header.CheckOrder = []int{}
	header.Expressions = []string{}
	header.Inputs = []condition.ObservedInput{}
	size, err := landingJSONBytes(reflect.ValueOf(header), MaxLandingChecksBytes-landingChecksEnvelopeBytes)
	if err != nil {
		return nil, err
	}
	return &LandingChecksBuilder{event: header, bytes: size + landingChecksEnvelopeBytes, checks: map[landingCheckKey]int{}}, nil
}

func (b *LandingChecksBuilder) reserve(value any, prior int) error {
	size, err := landingJSONBytes(reflect.ValueOf(value), MaxLandingChecksBytes-b.bytes)
	if err != nil {
		return err
	}
	if prior > 0 {
		size++
	}
	if size > MaxLandingChecksBytes-b.bytes {
		return LandingChecksTooLargeError{"wire event", MaxLandingChecksBytes}
	}
	b.bytes += size
	return nil
}

// AddCheck retains each observation once, with an ordered reference for every
// occurrence in the executor's policyChecks slice, including identical runs.
func (b *LandingChecksBuilder) AddCheck(c LandingCheck) (err error) {
	defer func() { err = landingChecksBuildError(err) }()
	if len(b.event.CheckOrder) >= maxLandingCheckRuns {
		return LandingChecksTooLargeError{"checks", maxLandingCheckRuns}
	}
	if err := c.validate(); err != nil {
		return err
	}
	key := c.key()
	index, found := b.checks[key]
	if !found {
		if len(b.event.Checks) >= maxLandingCheckRuns {
			return LandingChecksTooLargeError{"checks", maxLandingCheckRuns}
		}
		index = len(b.event.Checks)
		if err := b.reserve(c, index); err != nil {
			return err
		}
		// Own nullable provenance even if the caller later reuses its struct.
		c.ProducerIdentity = cloneCheckPointer(c.ProducerIdentity)
		c.HeadBranch = cloneCheckPointer(c.HeadBranch)
		c.RunID = cloneCheckPointer(c.RunID)
		c.Attempt = cloneCheckPointer(c.Attempt)
		c.WorkflowRunID = cloneCheckPointer(c.WorkflowRunID)
		b.event.Checks = append(b.event.Checks, c)
		b.checks[key] = index
	}
	if err := b.reserve(index, len(b.event.CheckOrder)); err != nil {
		return err
	}
	b.event.CheckOrder = append(b.event.CheckOrder, index)
	return nil
}
func cloneCheckPointer[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// RecordMember borrows the evaluator's values and copies only newly interned
// inputs, after charging their JSON size. Shared CI lists never expand per PR.
func (b *LandingChecksBuilder) RecordMember(pr int, head string, expression condition.Expression, values condition.PR) (err error) {
	defer func() { err = landingChecksBuildError(err) }()
	if len(b.event.Members) >= maxLandingCheckMembers {
		return LandingChecksTooLargeError{"members", maxLandingCheckMembers}
	}
	if len(head) > MaxLandingCheckStringBytes {
		return LandingChecksTooLargeError{"member head", MaxLandingCheckStringBytes}
	}
	if pr < 1 || !forge.ValidDiffObjectID(head) {
		return fmt.Errorf("invalid landing check member")
	}
	for _, prior := range b.event.Members {
		if prior.PR == pr {
			return fmt.Errorf("duplicate landing check member")
		}
	}
	text := expression.String()
	if len(text) > condition.MaxEvidenceExpressionBytes {
		return LandingChecksTooLargeError{"expression", condition.MaxEvidenceExpressionBytes}
	}
	index := slices.Index(b.event.Expressions, text)
	if index < 0 {
		index = len(b.event.Expressions)
		if index >= maxLandingCheckMembers {
			return LandingChecksTooLargeError{"expressions", maxLandingCheckMembers}
		}
		if err := b.reserve(text, index); err != nil {
			return err
		}
		b.event.Expressions = append(b.event.Expressions, text)
	}
	member := LandingCheckMember{PR: pr, HeadSHA: head, Conditions: LandingCheckCondition{Expression: index, Inputs: []int{}}}
	err = expression.VisitInputs(values, func(v condition.ObservedInput) error {
		if err := v.Validate(); err != nil {
			if errors.Is(err, condition.ErrEvidenceTooLarge) {
				return LandingChecksTooLargeError{"condition input", condition.MaxEvidenceStringBytes}
			}
			return err
		}
		input := -1
		for i, prior := range b.event.Inputs {
			if prior.Equal(v) {
				input = i
				break
			}
		}
		if len(member.Conditions.Inputs) >= maxLandingCheckMemberInputs {
			return LandingChecksTooLargeError{"member inputs", maxLandingCheckMemberInputs}
		}
		if input < 0 {
			if len(b.event.Inputs) >= maxLandingCheckInputs {
				return LandingChecksTooLargeError{"inputs", maxLandingCheckInputs}
			}
			// Borrow explicit empty slices for accurate sizing; copying follows reserve.
			if v.List == nil {
				v.List = []string{}
			}
			if v.Observed == nil {
				v.Observed = []string{}
			}
			if v.Unsettled == nil {
				v.Unsettled = []string{}
			}
			input = len(b.event.Inputs)
			if err := b.reserve(v, input); err != nil {
				return err
			}
			b.event.Inputs = append(b.event.Inputs, v.Clone())
		}
		member.Conditions.Inputs = append(member.Conditions.Inputs, input)
		return nil
	})
	if err != nil {
		return err
	}
	if err := b.reserve(member, len(b.event.Members)); err != nil {
		return err
	}
	b.event.Members = append(b.event.Members, member)
	return nil
}

// Event finishes the read. The attempt ID was included in the initial budget;
// the store validates its final bindings before admission.
func (b *LandingChecksBuilder) Event() LandingChecksRecorded { return b.event }
