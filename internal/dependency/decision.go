package dependency

import (
	"errors"
	"net/url"
	"reflect"
	"slices"
	"strings"
)

// ErrNotAdmitted wraps a typed blocking code from Admission. It is never a
// deployed event field; status rendering and redaction belong to slice 5.
var ErrNotAdmitted = errors.New("dependency admission blocked")

// AdmissionBasis pins one consumer revision's authorization to exact journal
// records: the alias declaration (possibly empty), the queue's policy companion,
// any acknowledgement clearing a hold, and one current observation per edge.
type AdmissionBasis struct {
	Declaration            Basis    `json:"declaration"`
	PolicyEventID          uint64   `json:"policy_event_id"`
	PolicyDigest           string   `json:"policy_digest"`
	AcknowledgementEventID uint64   `json:"acknowledgement_event_id"`
	AcknowledgementKind    string   `json:"acknowledgement_kind"`
	ObservationIDs         []uint64 `json:"observation_ids"`
}

func (b AdmissionBasis) valid() bool {
	if b.Declaration.EventID == 0 || !hexDigest(b.Declaration.Digest) || b.PolicyEventID == 0 || !hexDigest(b.PolicyDigest) || b.ObservationIDs == nil || len(b.ObservationIDs) > MaxDirectEdges {
		return false
	}
	if (b.AcknowledgementEventID == 0) != (b.AcknowledgementKind == "") || b.AcknowledgementKind != "" && b.AcknowledgementKind != "dependency_declaration_acknowledged" && b.AcknowledgementKind != "dependency_declaration_self_acknowledged" {
		return false
	}
	for i, id := range b.ObservationIDs {
		if id == 0 || slices.Contains(b.ObservationIDs[:i], id) {
			return false
		}
	}
	return true
}

// Admission is derived only from recorded side facts; it performs no forge or
// database reads. An empty Code is NOT landing authority: the start transaction
// still revalidates source journals under their queue locks. Events lists the
// records consumed, so callers can derive the dependency wait clock.
type Admission struct {
	Code   string
	Alias  SourceAlias
	Basis  AdmissionBasis
	Events []uint64
}

func (s State) declarationFor(changeID string) (declarationProjection, bool) {
	for _, d := range s.declarations {
		if d.Event.ChangeID == changeID {
			return d, true
		}
	}
	return declarationProjection{}, false
}

// Admission returns the planner/start gate for one consumer revision. Missing
// discovery is unknown, never an empty graph: a change without a recorded
// declaration at this exact revision waits. Holds, revoked self-acknowledgement
// and disabled policy block before any edge is considered.
func (s State) Admission(changeID string, revision uint64) Admission {
	d, ok := s.declarationFor(changeID)
	if !ok || d.Event.Revision != revision || s.initialization == nil {
		return Admission{Code: "dependency-unresolved"}
	}
	a := Admission{Alias: d.Event.Alias}
	if d.Held || d.Unknown {
		a.Code = "dependency-declaration-changed"
		return a
	}
	p := s.policy(d.Event)
	if p.EventID == 0 || !p.Event.Enabled {
		a.Code = "dependency-out-of-scope"
		return a
	}
	a.Basis = AdmissionBasis{Declaration: Basis{d.EventID, d.Event.Digest}, PolicyEventID: p.EventID, PolicyDigest: p.Event.Digest, ObservationIDs: []uint64{}}
	a.Events = []uint64{d.EventID, p.EventID}
	if d.Ack != nil {
		// The fold re-holds on revocation; check again so a forged projection
		// cannot let a revoked self-acknowledgement authorize work.
		if d.Ack.Revoked || d.Ack.Kind == "dependency_declaration_self_acknowledged" && !p.Event.selfAckEnabled() {
			a.Code = "dependency-declaration-changed"
			return a
		}
		a.Basis.AcknowledgementEventID, a.Basis.AcknowledgementKind = d.Ack.EventID, d.Ack.Kind
		a.Events = append(a.Events, d.Ack.EventID)
	}
	for _, edge := range d.Event.Edges {
		if !slices.Contains(p.Event.Policy.AllowFrom, edge.Queue.Repository) {
			a.Code = "dependency-out-of-scope"
			return a
		}
		o, ok := s.observations[(Observed{Alias: d.Event.Alias, ChangeID: changeID, Revision: revision, Source: edge.Reference}).key()]
		if !ok || o.Event.Declaration != a.Basis.Declaration {
			a.Code = "dependency-waiting"
			return a
		}
		switch o.Event.Result {
		case "landed":
		case "stale":
			a.Code = "dependency-stale"
			return a
		case "unavailable":
			a.Code = "dependency-unavailable"
			return a
		default:
			a.Code = "dependency-waiting"
			return a
		}
		a.Basis.ObservationIDs = append(a.Basis.ObservationIDs, o.EventID)
		a.Events = append(a.Events, o.EventID)
	}
	return a
}

// Edges returns the current declared edges for a consumer revision, in canonical
// order, so the start transaction can derive its direct-source lock set.
func (s State) Edges(changeID string, revision uint64) ([]DeclaredEdge, SourceAlias, bool) {
	d, ok := s.declarationFor(changeID)
	if !ok || d.Event.Revision != revision {
		return nil, SourceAlias{}, false
	}
	return slices.Clone(d.Event.Edges), d.Event.Alias, true
}

// QueuePolicy returns the current companion for an exact canonical queue key.
func (s State) QueuePolicy(q QueueKey) (PolicyApplied, uint64, bool) {
	for _, p := range s.policies {
		if p.Event.Queue == q {
			return clonePolicy(p.Event), p.EventID, true
		}
	}
	return PolicyApplied{}, 0, false
}

// RepositoryPath maps a canonical repository URL to its driver path, requiring
// the binding's own forge origin. Names are never accepted from other hosts.
func RepositoryPath(binding JournalInitialized, repository string) (string, error) {
	u, err := repositoryURL(repository)
	if err != nil {
		return "", err
	}
	_, origin, _ := strings.Cut(binding.ForgeInstance, ":")
	if (&url.URL{Scheme: u.Scheme, Host: u.Host}).String() != origin {
		return "", ErrOutOfScope
	}
	return strings.TrimPrefix(u.Path, "/"), nil
}

// SourceSettlement is the landing-start evidence for one direct source edge: the
// declarer's fresh identity-bound read grant, the source's settled OpenMerge
// landing, and the source queue journal watermark/base checked under its lock.
type SourceSettlement struct {
	Reference         string      `json:"reference"`
	Queue             QueueKey    `json:"queue"`
	Source            SourceAlias `json:"source"`
	Grant             Grant       `json:"grant"`
	Revision          Baseline    `json:"revision"`
	Commit            string      `json:"commit"`
	SettlementEventID uint64      `json:"settlement_event_id"`
	Watermark         uint64      `json:"watermark"`
	BaseSHA           string      `json:"base_sha"`
}

func (s SourceSettlement) valid() bool {
	return validQueue(s.Queue) && s.Source.valid() && s.Grant.valid(s.Source.RepositoryID) && s.Revision.valid() && objectID(s.Commit) && s.SettlementEventID > 0 && s.Watermark >= s.SettlementEventID && objectID(s.BaseSHA)
}

// DecisionRecorded is one batch member's dependency decision companion. The
// start transaction commits one per member beside the unchanged policy decisions
// and LandingStarted; each stays under the 1 MiB event cap even with 32 edges.
// DecisionID is zero only when the queue records no approval decision.
type DecisionRecorded struct {
	Namespace    string             `json:"journal_namespace"`
	BatchID      string             `json:"batch_id"`
	AttemptID    string             `json:"attempt_id"`
	TestedSHA    string             `json:"tested_sha"`
	LandedSHA    string             `json:"landed_sha"`
	StartEventID uint64             `json:"start_event_id"`
	DecisionID   uint64             `json:"decision_id"`
	Member       uint64             `json:"member"`
	Members      uint64             `json:"members"`
	Alias        SourceAlias        `json:"alias"`
	ChangeID     string             `json:"change_id"`
	Revision     uint64             `json:"revision"`
	Head         string             `json:"head"`
	Admission    AdmissionBasis     `json:"admission"`
	Sources      []SourceSettlement `json:"sources"`
}

func (DecisionRecorded) dependencyEvent() {}

// Validate checks framing only. Apply checks the current admission, and the
// shared reader resolves the start/decision/batch journal targets.
func (e DecisionRecorded) Validate() error {
	if !validDigestID(e.Namespace, "omj1_") || !cleanText(e.BatchID) || !cleanText(e.AttemptID) || !objectID(e.TestedSHA) || !objectID(e.LandedSHA) || e.StartEventID == 0 || e.DecisionID >= e.StartEventID || e.Members == 0 || e.Members > 128 || e.Member >= e.Members {
		return ErrMalformed
	}
	if !e.Alias.valid() || !validDigestID(e.ChangeID, "chg1_") || e.Revision == 0 || !objectID(e.Head) || !e.Admission.valid() || e.Sources == nil || len(e.Sources) != len(e.Admission.ObservationIDs) {
		return ErrMalformed
	}
	for i, source := range e.Sources {
		if !source.valid() || i > 0 && e.Sources[i-1].Reference >= source.Reference {
			return ErrMalformed
		}
	}
	return nil
}

// validateDecision requires the companion to equal the CURRENT admission. The
// landing-start observations are appended earlier in the same bundle, so an
// older observation or a since-revoked acknowledgement cannot be re-certified.
func (s State) validateDecision(e DecisionRecorded) error {
	a := s.Admission(e.ChangeID, e.Revision)
	if a.Code != "" {
		return errors.Join(ErrNotAdmitted, errors.New(a.Code))
	}
	if a.Alias != e.Alias || !reflect.DeepEqual(a.Basis, e.Admission) {
		return ErrStale
	}
	d, _ := s.declarationFor(e.ChangeID)
	if len(d.Event.Edges) != len(e.Sources) {
		return ErrStale
	}
	scope := s.initialization.scope()
	for i, edge := range d.Event.Edges {
		source := e.Sources[i]
		o := s.observations[(Observed{Alias: e.Alias, ChangeID: e.ChangeID, Revision: e.Revision, Source: edge.Reference}).key()].Event
		ref, err := ParseReference(edge.Reference, scope)
		if err != nil {
			return err
		}
		if source.Source.validate(scope) != nil || source.Reference != edge.Reference || source.Queue != edge.Queue || source.Grant.SubjectID != d.Event.ReadGrant.SubjectID || o.SourceRevision != source.Revision || o.SourceBase != source.BaseSHA {
			return ErrStale
		}
		if ref.Revision > 0 && (source.Revision.Revision != ref.Revision || source.Revision.Head != edge.PinnedHead) {
			return ErrStale
		}
	}
	return nil
}
