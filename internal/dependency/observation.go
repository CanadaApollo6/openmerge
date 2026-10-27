package dependency

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// ReadStage identifies three independent fresh reads. None grants durable start
// authority; slice 4 must fence recorded observations in the start transaction.
type ReadStage string

// Declaration binding, work preparation and landing-start never share a grant.
const (
	BindingRead      ReadStage = "binding"
	WorkRead         ReadStage = "work"
	LandingStartRead ReadStage = "landing-start"
)

// QueueAdmission contains trusted, supplied queue-command evidence, not PR body
// text. Graph discovery is allowed only for an enqueued change after a writer's
// command in B. Sources are never automatically enqueued by this API.
type QueueAdmission struct {
	Enqueued  bool
	CommandID uint64
	ChangeID  string
	Queue     QueueKey
	Writer    Grant
}

// SourceLanding contains supplied settled journal evidence and a fresh ancestry
// observation at exactly BaseSHA. Forge closed/merged flags cannot manufacture it.
// Ancestry acquisition and durable decision/start fencing belong to later slices.
type SourceLanding struct {
	Revision          Baseline
	Commit            string
	SettlementEventID uint64
	BaseSHA           string
	OnBase            bool
}

// SourceRecord is the journal's current source binding and revision. It is not a
// lookup result inferred from a PR URL. A retarget may name a different change;
// the observer diagnoses it rather than following it. No graph is expanded here.
type SourceRecord struct {
	Alias     SourceAlias
	Reference Reference
	Queue     QueueKey
	Current   Baseline
	BaseSHA   string
	Landing   *SourceLanding
}

// ObservationRequest pins one edge, consumer declaration and read ordering basis.
// ReadID identifies this fresh acquisition; Sequence is allocated before I/O.
// The caller supplies authenticated declarer identity, never the queue writer
// in its place, and must persist the returned read evidence in a NEW companion
// kind when wiring. Existing dependency and queue events remain unchanged.
type ObservationRequest struct {
	Stage       ReadStage
	Admission   QueueAdmission
	Registry    Registry
	Consumer    DependenciesDeclared
	Declaration Basis
	Edge        DeclaredEdge
	Source      SourceRecord
	Declarer    forge.UserIdentity
	ReadID      string
	Sequence    uint64
	Prior       uint64
}

// ReadEvidence is authorized-only acquisition evidence. It carries the exact
// actor, source repository grant, stage and declaration/observation basis. It is
// not a deployed event and must never be put into an existing event payload.
type ReadEvidence struct {
	Stage       ReadStage
	Declaration Basis
	Grant       Grant
	Observation Observed
	Code        string
}

// ScopedObservation keeps private diagnostics out of accidental JSON/log output.
// Use View for audience output. Evidence is for trusted journal/planner adapters.
type ScopedObservation struct {
	evidence     ReadEvidence
	repositoryID string
}

// String fails closed for default diagnostics/log formatting.
func (o ScopedObservation) String() string { return "Dependency cannot be verified" }

// GoString also redacts Go-syntax formatting.
func (o ScopedObservation) GoString() string { return o.String() }

// MarshalJSON deliberately renders the generic view. Explicit authorized views
// require separately supplied recorded audience authorization, never App access.
func (o ScopedObservation) MarshalJSON() ([]byte, error) { return json.Marshal(o.View(nil)) }

// Evidence returns immutable authorized-only side facts, never an admission or
// landing decision. Partial/failed reads replace success with unavailable facts.
func (o ScopedObservation) Evidence() ReadEvidence { return o.evidence }

// ObservationView is a slice-3 redaction contract, not the full status renderer.
// Unauthorized audiences receive no code, source evidence or success signal.
type ObservationView struct {
	Reason      string    `json:"reason"`
	Code        string    `json:"code,omitempty"`
	Observation *Observed `json:"observation,omitempty"`
}

// View requires a complete nonempty audience grant set. If any reader is unknown
// or lacks identity-bound source access, every source state has identical output.
// These grants are recorded audience facts; rendering performs no forge reads.
func (o ScopedObservation) View(audience []Grant) ObservationView {
	generic := ObservationView{Reason: "Dependency cannot be verified"}
	if len(audience) == 0 || o.repositoryID == "" {
		return generic
	}
	for _, grant := range audience {
		if !grant.valid(o.repositoryID) {
			return generic
		}
	}
	e := o.evidence
	return ObservationView{Reason: e.Code, Code: e.Code, Observation: &e.Observation}
}

// validateConsumerScope checks the consumer binding independently of source
// reference parsing, which also rejects a foreign namespace later in validate.
func (r ObservationRequest) validateConsumerScope() error {
	if r.Consumer.Namespace != r.Registry.Scope.JournalNamespace || r.Consumer.Alias.validate(r.Registry.Scope) != nil || r.Registry.index().validateRepo(r.Consumer.Queue.Repository) != nil {
		return ErrOutOfScope
	}
	return nil
}

func (r ObservationRequest) validate() (Reference, error) {
	if r.Stage != BindingRead && r.Stage != WorkRead && r.Stage != LandingStartRead || !cleanText(r.ReadID) || r.Sequence == 0 || r.Declaration.EventID == 0 || r.Declaration.Digest != r.Consumer.Digest || r.Consumer.Validate() != nil {
		return Reference{}, ErrMalformed
	}
	if !r.Admission.Enqueued || r.Admission.CommandID == 0 || r.Admission.ChangeID != r.Consumer.ChangeID || r.Admission.Queue != r.Consumer.Queue || !r.Admission.Writer.valid(r.Consumer.Alias.RepositoryID) {
		return Reference{}, ErrHeld
	}
	if err := r.validateConsumerScope(); err != nil {
		return Reference{}, err
	}
	if r.Consumer.Attribution.Unknown || r.Declarer.ID <= 0 || strconv.FormatInt(r.Declarer.ID, 10) != r.Consumer.ReadGrant.SubjectID || !forge.ValidIdentityLogin(r.Declarer.Login) {
		return Reference{}, ErrHeld
	}
	found := false
	for _, edge := range r.Consumer.Edges {
		found = found || edge == r.Edge
	}
	if !found {
		return Reference{}, ErrStale
	}
	if !objectID(r.Source.BaseSHA) || r.Source.Alias.Number > uint64(^uint(0)>>1) {
		return Reference{}, ErrMalformed
	}
	return ParseReference(r.Edge.Reference, r.Registry.Scope)
}

// ObserveEdge checks scope and command admission before I/O, then rechecks the
// declarer's ID-bound source permission BEFORE reading any lifecycle or body.
// All failures are blocking facts; raw forge errors never become audience output.
// It never emits events, applies state, discovers arbitrary URLs or enqueues work.
func ObserveEdge(ctx context.Context, reader forge.DependencyReader, r ObservationRequest) (ScopedObservation, error) {
	ref, err := r.validate()
	if err != nil {
		return ScopedObservation{}, err
	}
	// Unavailable observations retain only the declaration's last known baseline.
	// They never invent fresh source revision/base evidence after a failed read.
	o := ScopedObservation{evidence: ReadEvidence{Stage: r.Stage, Declaration: r.Declaration, Code: "dependency-unavailable", Observation: Observed{
		Namespace: r.Consumer.Namespace, Alias: r.Consumer.Alias, ChangeID: r.Consumer.ChangeID, Revision: r.Consumer.Revision,
		Declaration: r.Declaration, Source: r.Edge.Reference, ExpectedPrior: r.Prior, ReadSequence: r.Sequence,
		SourceRevision: r.Edge.Baseline, SourceBase: r.Source.BaseSHA, Result: "unavailable",
	}}}
	set := func(code, result string) ScopedObservation {
		o.evidence.Code = code
		o.evidence.Observation.Result = result
		return o
	}
	if r.Registry.index().validateRepo(r.Edge.Queue.Repository) != nil || r.Source.Alias.validate(r.Registry.Scope) != nil || r.Source.Reference.Scope != r.Registry.Scope {
		return set("dependency-out-of-scope", "unavailable"), nil
	}
	u, _ := repositoryURL(r.Edge.Queue.Repository)
	repo := forge.RepoRef{Path: strings.TrimPrefix(u.Path, "/")}
	// Neither permission nor graph reads may consume settlement's reserved 10%.
	if forge.ClassOfRequest(ctx) == forge.SettlementRequest {
		ctx = forge.WithRequestClass(ctx, forge.ActiveRequest)
	}
	grant, err := reader.WriteGrant(ctx, repo, r.Declarer)
	o.evidence.Grant = Grant{SubjectID: strconv.FormatInt(r.Declarer.ID, 10), ReturnedID: strconv.FormatInt(grant.User.ID, 10), RepositoryID: r.Source.Alias.RepositoryID, ReadID: r.ReadID, Fresh: err == nil, Allowed: err == nil && grant.CanRead(r.Declarer.ID)}
	if err != nil || !o.evidence.Grant.valid(r.Source.Alias.RepositoryID) {
		return o, nil
	}
	o.repositoryID = r.Source.Alias.RepositoryID
	source, err := reader.DependencySource(ctx, repo, int(r.Source.Alias.Number))
	if err != nil || !source.Valid() {
		return o, nil
	}
	if source.RepositoryID != r.Source.Alias.RepositoryID || uint64(source.Number) != r.Source.Alias.Number {
		o.evidence.Grant.Allowed = false
		return set("dependency-unresolved", "unavailable"), nil
	}
	if _, err := r.Source.Reference.Canonical(); err != nil {
		return set("dependency-unresolved", "unavailable"), nil
	}
	if r.Source.Reference.Scope != ref.Scope || r.Source.Reference.ChangeID != ref.ChangeID || r.Source.Reference.Revision != 0 || r.Source.Queue != r.Edge.Queue || source.Base != r.Edge.Queue.Base {
		return set("dependency-source-retargeted", "unavailable"), nil
	}
	if !r.Source.Current.valid() || source.BaseSHA != r.Source.BaseSHA || source.HeadSHA != r.Source.Current.Head {
		return o, nil // repair the journal observation before treating this as fresh
	}
	o.evidence.Observation.SourceRevision = r.Source.Current
	o.evidence.Observation.SourceBase = source.BaseSHA
	if landing := r.Source.Landing; landing != nil {
		if !landing.Revision.valid() || !objectID(landing.Commit) || landing.SettlementEventID == 0 || landing.BaseSHA != source.BaseSHA {
			return o, nil
		}
		if ref.Revision == 0 || landing.Revision.Revision == ref.Revision && landing.Revision.Head == r.Edge.PinnedHead {
			if !landing.OnBase {
				return set("dependency-not-on-base", "unavailable"), nil
			}
			o.evidence.Observation.SourceRevision = landing.Revision
			return set("dependency-landed", "landed"), nil
		}
	}
	if ref.Revision != 0 && (ref.Revision != r.Source.Current.Revision || r.Edge.PinnedHead != r.Source.Current.Head) {
		return set("dependency-stale", "stale"), nil
	}
	if source.Merged {
		return set("dependency-unverified", "unavailable"), nil
	}
	if source.Closed {
		return set("dependency-closed", "unavailable"), nil
	}
	return set("dependency-waiting", "waiting"), nil
}
