package queue

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
)

// AuthorityPin is present only in the openmerge variant. Activation is optional:
// a hidden or incomplete fresh read cannot adopt a draft verification.
type AuthorityPin struct {
	Binding           authority.PolicyBinding     `json:"binding"`
	Activation        *authority.Activation       `json:"activation,omitempty"`
	VerificationInput *AuthorityVerificationInput `json:"verification_input,omitempty"`
}

// PolicyAuthorityApplied binds the actual governing commit to policy identity.
// GitHub pins record rollback without inventing a break-glass actor. Ordinary
// github-only configuration changes do not emit this event.
type PolicyAuthorityApplied struct {
	GoverningCommit string         `json:"governing_commit"`
	EffectiveDigest string         `json:"effective_digest"`
	Authority       authority.Mode `json:"authority"`
	DefaultBranch   string         `json:"default_branch"`
	CoverageDigest  string         `json:"coverage_digest"`
	OpenMerge       *AuthorityPin  `json:"openmerge,omitempty"`
}

func (PolicyAuthorityApplied) queueEvent() {}

// Validate checks the mode-specific pin and optional draft adoption.
func (e PolicyAuthorityApplied) Validate() error {
	if e.GoverningCommit == "" || !authority.IsDigest(e.EffectiveDigest) || !authority.IsDigest(e.CoverageDigest) || e.DefaultBranch == "" {
		return fmt.Errorf("invalid authority pin")
	}
	switch e.Authority {
	case authority.GitHub:
		if e.OpenMerge != nil {
			return fmt.Errorf("github pin cannot declare an authority actor")
		}
	case authority.OpenMerge:
		if e.OpenMerge == nil {
			return fmt.Errorf("missing openmerge pin")
		}
		b := e.OpenMerge.Binding
		if err := b.Validate(); err != nil {
			return err
		}
		if b.EffectiveDigest != e.EffectiveDigest || b.CoverageDigest != e.CoverageDigest || b.DefaultBranch != e.DefaultBranch {
			return fmt.Errorf("authority pin binding mismatch")
		}
		if input := e.OpenMerge.VerificationInput; input != nil {
			if e.OpenMerge.Activation == nil {
				return fmt.Errorf("authority verification input requires activation")
			}
			if err := input.validateSize(); err != nil {
				return err
			}
		}
		if e.OpenMerge.Activation != nil {
			if err := e.OpenMerge.Activation.Validate(); err != nil {
				return err
			}
			if !reflect.DeepEqual(e.OpenMerge.Activation.Proposed.Binding, b) {
				return fmt.Errorf("activation policy binding mismatch")
			}
			return nil
		}
	default:
		return fmt.Errorf("invalid authority mode")
	}
	return nil
}

// WriteRestrictionObserved is an applied-scope read, not an operator verify.
// Basis is the write-restriction source revision read before network I/O.
type WriteRestrictionObserved struct {
	Binding    authority.PolicyBinding `json:"binding"`
	Basis      uint64                  `json:"basis"`
	ObservedAt time.Time               `json:"observed_at"`
	Evidence   authority.Observation   `json:"evidence"`
}

func (WriteRestrictionObserved) queueEvent() {}

// Validate checks policy binding, source basis and read visibility.
func (e WriteRestrictionObserved) Validate() error {
	if err := e.Binding.Validate(); err != nil {
		return err
	}
	if e.Basis == math.MaxUint64 || e.ObservedAt.IsZero() {
		return fmt.Errorf("invalid authority observation basis/time")
	}
	return e.Evidence.Validate()
}

// WriteRestrictionVerified requires a complete seeing read, including when that
// read proves a violation. A proposed record is historical evidence only. Each
// fresh read uses a new nonce; retries of the same read reuse it.
type WriteRestrictionVerified struct {
	Binding               authority.PolicyBinding `json:"binding"`
	Basis                 uint64                  `json:"basis"`
	Scope                 string                  `json:"scope"`
	Nonce                 string                  `json:"nonce"`
	Operator              string                  `json:"operator"`
	VerifiedAt            time.Time               `json:"verified_at"`
	ProtectionFingerprint string                  `json:"protection_fingerprint"`
	Evidence              authority.Observation   `json:"evidence"`
}

func (WriteRestrictionVerified) queueEvent() {}

// Validate requires a complete seeing read with the recorded fingerprint.
func (e WriteRestrictionVerified) Validate() error {
	if err := e.Binding.Validate(); err != nil {
		return err
	}
	if e.Basis == math.MaxUint64 || e.VerifiedAt.IsZero() || e.Operator == "" || len(e.Operator) > 256 || e.Nonce == "" || len(e.Nonce) > 256 || e.Scope != "proposed" && e.Scope != "applied" {
		return fmt.Errorf("invalid authority verification")
	}
	fingerprint, err := e.Evidence.Fingerprint()
	if err != nil {
		return err
	}
	if e.ProtectionFingerprint != fingerprint {
		return fmt.Errorf("protection fingerprint mismatch")
	}
	return nil
}

// Authority evidence is isolated from legacy sources and proposed history.
type authorityProjection struct {
	Pin            *PolicyAuthorityApplied      `json:"pin,omitempty"`
	Adopted        *PolicyAuthorityApplied      `json:"adopted,omitempty"`
	Observed       *WriteRestrictionObserved    `json:"observed,omitempty"`
	Verified       *WriteRestrictionVerified    `json:"verified,omitempty"`
	Proposed       *WriteRestrictionVerified    `json:"proposed,omitempty"`
	Findings       map[string]authority.Finding `json:"findings,omitempty"`
	LastObservedAt time.Time                    `json:"last_observed_at,omitempty"`
	LastVerifiedAt time.Time                    `json:"last_verified_at,omitempty"`
	Complete       bool                         `json:"complete,omitempty"`
}

func cloneAuthority[T any](v *T) *T {
	if v == nil {
		return nil
	}
	raw, _ := json.Marshal(v)
	var result T
	_ = json.Unmarshal(raw, &result)
	return &result
}

// AuthorityPin returns the durable mode pin without sharing mutable evidence.
func (s State) AuthorityPin() *PolicyAuthorityApplied {
	if s.authority == nil {
		return nil
	}
	return cloneAuthority(s.authority.Pin)
}

func (s State) observeAuthority(e Event) (State, bool) {
	switch v := e.(type) {
	case PolicyAuthorityApplied:
		if v.Validate() != nil || s.ConfigDigest != v.EffectiveDigest {
			return s, true
		}
		if s.authority != nil && reflect.DeepEqual(s.authority.Pin, &v) {
			return s, true
		}
		// A new governing commit with the same effective binding must not
		// erase a known violation (for example a formatting-only policy edit).
		if v.OpenMerge != nil && s.matchesAuthority(v.OpenMerge.Binding) {
			p := *s.authority
			p.Pin = cloneAuthority(&v)
			s.authority = &p
			return s, true
		}
		hadBlock := len(s.RulesSourceProblems(WriteRestriction)) > 0
		s = cloneState(s)
		s.authority = &authorityProjection{Pin: cloneAuthority(&v)}
		delete(s.rulesSources, WriteRestriction)
		if hadBlock {
			s.revision++
			s.rulesPauseRevision = s.revision
		}
		if v.OpenMerge != nil {
			s.rulesSources[WriteRestriction] = rulesSourceState{}
			if a := v.OpenMerge.Activation; a != nil && AuthorityDraftAdopted(v) {
				s.authority.Adopted = cloneAuthority(&v)
				s = s.foldWriteRestriction(0, a.SeeingReadAt, a.Evidence, true)
			}
		}
		return s, true
	case WriteRestrictionObserved:
		if v.Validate() != nil || !s.matchesAuthority(v.Binding) || s.ConfigDigest != v.Binding.EffectiveDigest {
			return s, true
		}
		accepted := len(v.Evidence.Findings) > 0 || v.Basis == s.RulesSourceRevision(WriteRestriction) && !v.ObservedAt.Before(s.authority.LastObservedAt)
		s = s.foldWriteRestriction(v.Basis, v.ObservedAt, v.Evidence, false)
		if accepted && (v.Evidence.Inventory != authority.ReadFailure || len(v.Evidence.Findings) > 0) && (s.authority.Observed == nil || !v.ObservedAt.Before(s.authority.Observed.ObservedAt)) {
			p := *s.authority
			p.Observed = cloneAuthority(&v)
			s.authority = &p
		}
		return s, true
	case WriteRestrictionVerified:
		if v.Validate() != nil {
			return s, true
		}
		if v.Scope == "proposed" {
			p := authorityProjection{}
			if s.authority != nil {
				p = *s.authority
			}
			if p.Proposed == nil || !v.VerifiedAt.Before(p.Proposed.VerifiedAt) {
				p.Proposed = cloneAuthority(&v)
				s.authority = &p
			}
			return s, true
		}
		if !s.matchesAuthority(v.Binding) || s.ConfigDigest != v.Binding.EffectiveDigest {
			return s, true
		}
		accepted := len(v.Evidence.Findings) > 0 || v.Basis == s.RulesSourceRevision(WriteRestriction) && !v.VerifiedAt.Before(s.authority.LastObservedAt)
		s = s.foldWriteRestriction(v.Basis, v.VerifiedAt, v.Evidence, true)
		if accepted && (s.authority.Verified == nil || !v.VerifiedAt.Before(s.authority.Verified.VerifiedAt)) {
			p := *s.authority
			p.Verified = cloneAuthority(&v)
			s.authority = &p
		}
		return s, true
	default:
		return s, false
	}
}

func (s State) matchesAuthority(b authority.PolicyBinding) bool {
	return s.authority != nil && s.authority.Pin != nil && s.authority.Pin.Authority == authority.OpenMerge && s.authority.Pin.OpenMerge != nil && reflect.DeepEqual(s.authority.Pin.OpenMerge.Binding, b)
}

func validateAuthoritySnapshot(p *authorityProjection) error {
	if p == nil {
		return nil
	}
	if p.Pin == nil && p.Observed == nil && p.Verified == nil && p.Proposed == nil {
		return fmt.Errorf("empty authority snapshot")
	}
	if p.Pin != nil {
		if err := p.Pin.Validate(); err != nil {
			return err
		}
	}
	if len(p.Findings) > 100000 {
		return fmt.Errorf("authority findings exceed snapshot budget")
	}
	if p.Pin == nil || p.Pin.OpenMerge == nil {
		if len(p.Findings) > 0 || !p.LastObservedAt.IsZero() || !p.LastVerifiedAt.IsZero() || p.Complete {
			return fmt.Errorf("active authority evidence without an openmerge pin")
		}
	}
	for element, f := range p.Findings {
		if element == "" || f.Element != element {
			return fmt.Errorf("invalid authority finding index")
		}
	}
	state := State{authority: p}
	if p.Observed != nil {
		if err := p.Observed.Validate(); err != nil {
			return err
		}
		if !state.matchesAuthority(p.Observed.Binding) {
			return fmt.Errorf("snapshot observation binding mismatch")
		}
	}
	for _, e := range []*WriteRestrictionVerified{p.Verified, p.Proposed} {
		if e != nil {
			if err := e.Validate(); err != nil {
				return err
			}
		}
	}
	if p.Verified != nil && (p.Verified.Scope != "applied" || !state.matchesAuthority(p.Verified.Binding)) || p.Proposed != nil && p.Proposed.Scope != "proposed" {
		return fmt.Errorf("snapshot verification scope/binding mismatch")
	}
	if p.Adopted != nil && (!AuthorityDraftAdopted(*p.Adopted) || !state.matchesAuthority(p.Adopted.OpenMerge.Binding)) {
		return fmt.Errorf("snapshot adoption interpretation/binding mismatch; replay journal and reverify authority")
	}
	// Materialized completeness and operator ages need a retained source. The
	// original interpreted adoption survives same-binding governing pin changes;
	// an old snapshot containing only raw activation cannot authenticate a pass.
	if !p.LastVerifiedAt.IsZero() && p.Verified == nil && p.Adopted == nil {
		return fmt.Errorf("snapshot verification lacks interpreted provenance; replay journal and reverify authority")
	}
	adoptedPass := p.Adopted != nil && len(p.Adopted.OpenMerge.Activation.Evidence.Findings) == 0
	observedPass := p.Observed != nil && p.Observed.Evidence.SeeingComplete() && len(p.Observed.Evidence.Findings) == 0
	verifiedPass := p.Verified != nil && p.Verified.Evidence.SeeingComplete() && len(p.Verified.Evidence.Findings) == 0
	if p.Complete && !adoptedPass && !observedPass && !verifiedPass {
		return fmt.Errorf("snapshot completeness lacks evidence provenance; replay journal and reverify authority")
	}
	return nil
}

func decodeAuthorityEvent[T interface{ Validate() error }](raw json.RawMessage) (T, error) {
	if err := strictAuthorityFields(raw, reflect.TypeFor[T]()); err != nil {
		var zero T
		return zero, err
	}
	v, err := readWireData[T](raw)
	if err != nil {
		return v, err
	}
	return v, v.Validate()
}

// New authority contracts reject duplicate and case-folded keys as well as
// unknown keys. encoding/json's case-insensitive matching is not wire identity.
func strictAuthorityFields(raw []byte, schema reflect.Type) error {
	if schema == reflect.TypeFor[time.Time]() {
		return nil
	}
	if schema.Kind() == reflect.Pointer {
		return strictAuthorityFields(raw, schema.Elem())
	}
	if schema.Kind() == reflect.Slice {
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for _, value := range values {
			if err := strictAuthorityFields(value, schema.Elem()); err != nil {
				return err
			}
		}
	}
	if schema.Kind() != reflect.Struct {
		return nil
	}
	fields := map[string]reflect.Type{}
	for i := 0; i < schema.NumField(); i++ {
		f := schema.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name != "-" {
			fields[name] = f.Type
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token != json.Delim('{') {
		return fmt.Errorf("authority value must be an object")
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return err
		}
		name, ok := token.(string)
		if !ok {
			return fmt.Errorf("invalid authority key")
		}
		field, ok := fields[name]
		if !ok || seen[name] {
			return fmt.Errorf("unknown or duplicate authority key %q", name)
		}
		seen[name] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		if err := strictAuthorityFields(value, field); err != nil {
			return err
		}
	}
	return nil
}

// AuthorityProtection returns the latest retained applied observation and its
// source revision, without sharing mutable evidence. Proposed verification is never
// runtime evidence. Push guards use this accessor for source drift detection;
// evaluation uses the pin activation's seeing observation. PR 4 owns revisions;
// this accessor activates
// no planning behavior and emits nothing.
func (s State) AuthorityProtection() (uint64, authority.Observation, bool) {
	if s.authority == nil {
		return 0, authority.Observation{}, false
	}
	observed, verified := s.authority.Observed, s.authority.Verified
	if verified != nil && (observed == nil || (verified.Basis > observed.Basis || verified.Basis == observed.Basis && verified.VerifiedAt.After(observed.ObservedAt))) {
		return s.RulesSourceRevision(WriteRestriction), authority.CloneObservation(verified.Evidence), true
	}
	if observed != nil {
		return s.RulesSourceRevision(WriteRestriction), authority.CloneObservation(observed.Evidence), true
	}
	return 0, authority.Observation{}, false
}
