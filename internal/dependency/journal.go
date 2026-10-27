package dependency

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// These contracts are intentionally separate from queue.Event. No production
// journal reader, emitter, startup, or cursor calls this codec or fold.
const WireVersion = 1

var (
	ErrStale = errors.New("stale dependency basis")
	ErrHeld  = errors.New("dependency-declaration-changed")
)

func digest(domain string, value any) string {
	raw, _ := json.Marshal(struct {
		Domain  string `json:"domain"`
		Version int    `json:"version"`
		Value   any    `json:"value"`
	}{domain, 1, value})
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
func hexDigest(s string) bool { return validDigestID(s, "") }
func objectID(s string) bool {
	return hexDigest(s) || len(s) == 40 && hexDigest(s+strings.Repeat("0", 24))
}

// JournalInitialized consumes the receipt database identity from ADR 0008 and
// an externally generated random 256-bit token. Token generation, the events
// lock, stopped/drained recovery and remote reconciliation are NOT activated.
// Physical copies retaining the identity AND token cannot be detected here.
type JournalInitialized struct {
	DatabaseIdentity string `json:"database_identity"`
	Incarnation      string `json:"incarnation"`
	Namespace        string `json:"namespace"`
	ForgeInstance    string `json:"forge_instance"`
	Installation     string `json:"installation"`
}

// NewJournalIdentity frames namespace inputs with a version and domain separator.
// A future initializer must generate token with crypto/rand once under the lock.
func NewJournalIdentity(databaseIdentity string, token [32]byte, forge, installation string) (JournalInitialized, error) {
	e := JournalInitialized{DatabaseIdentity: databaseIdentity, Incarnation: hex.EncodeToString(token[:]), ForgeInstance: forge, Installation: installation}
	e.Namespace = "omj1_" + digest("openmerge.dependency.journal-namespace", []string{e.DatabaseIdentity, e.Incarnation})
	return e, e.Validate()
}
func (e JournalInitialized) scope() Scope { return Scope{e.ForgeInstance, e.Installation, e.Namespace} }
func (e JournalInitialized) Validate() error {
	if !validDigestID(e.DatabaseIdentity, "openmerge-db-v2:sha256:") || !hexDigest(e.Incarnation) || e.Incarnation == strings.Repeat("0", 64) || e.Namespace != "omj1_"+digest("openmerge.dependency.journal-namespace", []string{e.DatabaseIdentity, e.Incarnation}) {
		return ErrMalformed
	}
	return e.scope().Validate()
}

// VerifyBinding must be given freshly verified operator/startup identity facts.
// Missing identity and a rotated incarnation fail closed; never rewrite old refs.
func (e JournalInitialized) VerifyBinding(expected JournalInitialized) error {
	if e.Validate() != nil || expected.Validate() != nil || e != expected {
		return ErrOutOfScope
	}
	return nil
}

// SourceAlias survives queue retargeting, repository rename and transfer. Names,
// bases, lanes and local change IDs deliberately do not participate in this key.
type SourceAlias struct {
	ForgeInstance string `json:"forge_instance"`
	Installation  string `json:"installation"`
	RepositoryID  string `json:"repository_id"`
	Number        uint64 `json:"number"`
}

func (a SourceAlias) validate(scope Scope) error {
	if a.ForgeInstance != scope.ForgeInstance || a.Installation != scope.Installation {
		return ErrOutOfScope
	}
	if !cleanText(a.RepositoryID) || a.Number == 0 {
		return ErrMalformed
	}
	return nil
}
func (a SourceAlias) valid() bool {
	return (Scope{a.ForgeInstance, a.Installation, "omj1_" + strings.Repeat("0", 64)}).Validate() == nil && cleanText(a.RepositoryID) && a.Number > 0
}
func (a SourceAlias) key() string { raw, _ := json.Marshal(a); return string(raw) }

// Basis is a compare-and-swap fence on the alias's declaration, including empty
// declarations. Equal digests at different events are different authorization.
type Basis struct {
	EventID uint64 `json:"event_id"`
	Digest  string `json:"digest"`
}

// Grant records an identity-bound fresh permission observation. The resource is
// the stable forge repository ID; identity mismatch is never a positive grant.
type Grant struct {
	SubjectID    string `json:"subject_id"`
	ReturnedID   string `json:"returned_id"`
	RepositoryID string `json:"repository_id"`
	ReadID       string `json:"read_id"`
	Fresh        bool   `json:"fresh"`
	Allowed      bool   `json:"allowed"`
}

func (g Grant) valid(repo string) bool {
	return cleanText(g.SubjectID) && g.SubjectID == g.ReturnedID && g.RepositoryID == repo && cleanText(g.ReadID) && g.Fresh && g.Allowed
}

type Accountability struct {
	Author    string   `json:"author"`
	Submitter string   `json:"submitter"`
	Humans    []string `json:"humans"`
}

func (a Accountability) valid() bool {
	return cleanText(a.Author) && (a.Submitter == "" || cleanText(a.Submitter)) && sortedIDs(a.Humans)
}
func sortedIDs(ids []string) bool {
	if ids == nil {
		return false
	}
	for i, id := range ids {
		if !cleanText(id) || i > 0 && ids[i-1] >= id {
			return false
		}
	}
	return true
}
func unionIDs(lists ...[]string) []string {
	out := []string{}
	for _, list := range lists {
		for _, id := range list {
			if id != "" {
				out = append(out, id)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Attribution includes every identified editor in a coalesced poll window.
// Unknown includes unidentified OR unauthenticated editing, even with opt-in.
type Attribution struct {
	Editors []string `json:"editors"`
	Unknown bool     `json:"unknown"`
}

// CoalesceEditors retains all senders; a polled body without authenticated
// history must contribute Unknown. No timestamp/deadline ordering is used.
func CoalesceEditors(windows ...Attribution) Attribution {
	out := Attribution{Editors: []string{}}
	for _, w := range windows {
		out.Editors = unionIDs(out.Editors, w.Editors)
		out.Unknown = out.Unknown || w.Unknown || len(w.Editors) == 0
	}
	return out
}

// Baseline is informational and stays fixed for a retained revision-less edge.
// Neither it nor the observed source head participates in declaration digests.
type Baseline struct {
	Revision     uint64 `json:"revision"`
	IntentDigest string `json:"intent_digest"`
	Head         string `json:"head"`
}

func (b Baseline) valid() bool {
	return b.Revision > 0 && hexDigest(b.IntentDigest) && objectID(b.Head)
}

type DeclaredEdge struct {
	Reference  string   `json:"reference"`
	Queue      QueueKey `json:"queue"`
	PinnedHead string   `json:"pinned_head"`
	Baseline   Baseline `json:"baseline"`
}

// AliasBinding is frozen while its textual input survives successive observed
// declarations. Removing/readding in distinct polls permits a fresh binding.
type AliasBinding struct {
	Input     string `json:"input"`
	Reference string `json:"reference"`
}

type DeclarationHold struct {
	Held    bool     `json:"held"`
	Unknown bool     `json:"unknown"`
	Editors []string `json:"editors"`
}

type DependenciesDeclared struct {
	Hold           DeclarationHold `json:"hold"`
	Namespace      string          `json:"journal_namespace"`
	Alias          SourceAlias     `json:"alias"`
	ChangeID       string          `json:"change_id"`
	Revision       uint64          `json:"revision"`
	Queue          QueueKey        `json:"queue"`
	Prior          Basis           `json:"prior"`
	Poll           uint64          `json:"poll"`
	Digest         string          `json:"digest"`
	Edges          []DeclaredEdge  `json:"edges"`
	Bindings       []AliasBinding  `json:"bindings"`
	Attribution    Attribution     `json:"attribution"`
	ReadGrant      Grant           `json:"read_grant"`
	Accountability Accountability  `json:"accountability"`
	Redeclaration  bool            `json:"redeclaration"`
}

// DeclarationDigest excludes baselines, alias spelling, observations and heads
// of revision-less sources. Pins include the exact head supplied at binding.
func DeclarationDigest(edges []DeclaredEdge) string {
	type entry struct {
		Reference  string `json:"reference"`
		PinnedHead string `json:"pinned_head"`
	}
	values := make([]entry, len(edges))
	for i, e := range edges {
		values[i] = entry{e.Reference, e.PinnedHead}
	}
	slices.SortFunc(values, func(a, b entry) int { return strings.Compare(a.Reference, b.Reference) })
	return digest("openmerge.dependency.declaration", values)
}
func validQueue(q QueueKey) bool {
	_, err := repositoryURL(q.Repository)
	return err == nil && cleanText(q.Base) && (q.Lane == "" || cleanText(q.Lane))
}
func (e DependenciesDeclared) Validate() error {
	if !validDigestID(e.Namespace, "omj1_") || !e.Alias.valid() || !sortedIDs(e.Hold.Editors) || e.Hold.Unknown && !e.Hold.Held {
		return ErrMalformed
	}
	scope := Scope{e.Alias.ForgeInstance, e.Alias.Installation, e.Namespace}
	if len(e.Edges) > MaxDirectEdges || e.Edges == nil || e.Bindings == nil || len(e.Bindings) > MaxInputs || !validDigestID(e.ChangeID, "chg1_") || e.Revision == 0 || e.Poll == 0 || !validQueue(e.Queue) || !e.Accountability.valid() || !sortedIDs(e.Attribution.Editors) || !e.ReadGrant.valid(e.Alias.RepositoryID) || e.Digest != DeclarationDigest(e.Edges) || (e.Prior.EventID == 0) != (e.Prior.Digest == "") || e.Prior.EventID > 0 && !hexDigest(e.Prior.Digest) {
		return ErrMalformed
	}
	refs := map[string]bool{}
	for i, edge := range e.Edges {
		ref, err := ParseReference(edge.Reference, scope)
		if err != nil || !validQueue(edge.Queue) || edge.Queue.Repository == e.Queue.Repository || !edge.Baseline.valid() || (ref.Revision == 0 && edge.PinnedHead != "") || ref.Revision > 0 && (!objectID(edge.PinnedHead) || edge.Baseline.Revision != ref.Revision || edge.PinnedHead != edge.Baseline.Head) {
			return ErrMalformed
		}
		if i > 0 && e.Edges[i-1].Reference >= edge.Reference {
			return ErrMalformed
		}
		if refs[ref.ChangeID] {
			return ErrConflict
		}
		refs[ref.ChangeID] = true
	}
	edgeRefs := map[string]bool{}
	for _, edge := range e.Edges {
		edgeRefs[edge.Reference] = true
	}
	for i, b := range e.Bindings {
		if i > 0 && e.Bindings[i-1].Input >= b.Input || !edgeRefs[b.Reference] {
			return ErrMalformed
		}
		repo, err := parseAlias(b.Input, scope)
		if err != nil {
			return err
		}
		for _, edge := range e.Edges {
			if edge.Reference == b.Reference && edge.Queue.Repository != repo {
				return ErrUnresolved
			}
		}
	}
	return nil
}

// PolicyApplied is a companion, never an extension of ConfigApplied. Supplied
// governing-read facts must name B's default branch, never the change head.
type PolicyApplied struct {
	Enabled       bool        `json:"enabled"`
	Namespace     string      `json:"journal_namespace"`
	Alias         SourceAlias `json:"alias"`
	Queue         QueueKey    `json:"queue"`
	PriorEventID  uint64      `json:"prior_event_id"`
	DefaultBranch string      `json:"default_branch"`
	ReadRef       string      `json:"read_ref"`
	ReadID        string      `json:"read_id"`
	Commit        string      `json:"commit"`
	Digest        string      `json:"digest"`
	Policy        Policy      `json:"policy"`
	Writers       []Grant     `json:"writers"`
}

func PolicyDigest(p Policy, enabled bool) string {
	p.AllowFrom = unionIDs(p.AllowFrom)
	p.AllowTo = unionIDs(p.AllowTo)
	return digest("openmerge.dependency.policy", struct {
		Enabled bool   `json:"enabled"`
		Policy  Policy `json:"policy"`
	}{enabled, p})
}
func (e PolicyApplied) Validate() error {
	if !validDigestID(e.Namespace, "omj1_") || !e.Alias.valid() {
		return ErrMalformed
	}
	if !validQueue(e.Queue) || !cleanText(e.DefaultBranch) || e.ReadRef != "refs/heads/"+e.DefaultBranch || !cleanText(e.ReadID) || !objectID(e.Commit) || e.Policy.Validate() != nil || e.Digest != PolicyDigest(e.Policy, e.Enabled) {
		return ErrMalformed
	}
	seen := map[string]bool{}
	for _, g := range e.Writers {
		if !g.valid(e.Alias.RepositoryID) || seen[g.SubjectID] {
			return ErrMalformed
		}
		seen[g.SubjectID] = true
	}
	return nil
}
func (e PolicyApplied) key() string {
	return digest("openmerge.dependency.queue-policy", []string{e.Alias.ForgeInstance, e.Alias.Installation, e.Alias.RepositoryID, e.Queue.Base, e.Queue.Lane})
}

// Acknowledged is independent authorization. SelfAcknowledged is a distinct
// wire kind with explicit overridden exclusions and governing opt-in evidence.
type Acknowledged struct {
	Namespace       string      `json:"journal_namespace"`
	Alias           SourceAlias `json:"alias"`
	Declaration     Basis       `json:"declaration"`
	Writer          Grant       `json:"writer"`
	Exclusions      []string    `json:"exclusions"`
	PolicyEventID   uint64      `json:"policy_event_id"`
	GoverningCommit string      `json:"governing_commit"`
	PolicyDigest    string      `json:"policy_digest"`
}

func (e Acknowledged) Validate() error {
	if !validDigestID(e.Namespace, "omj1_") || !e.Alias.valid() {
		return ErrMalformed
	}
	if e.Declaration.EventID == 0 || !hexDigest(e.Declaration.Digest) || !e.Writer.valid(e.Alias.RepositoryID) || !sortedIDs(e.Exclusions) || e.PolicyEventID == 0 || !objectID(e.GoverningCommit) || !hexDigest(e.PolicyDigest) {
		return ErrMalformed
	}
	return nil
}

type SelfAcknowledged struct {
	Acknowledged Acknowledged `json:"acknowledged"`
	Overridden   []string     `json:"overridden"`
}

func (e SelfAcknowledged) Validate() error {
	if err := e.Acknowledged.Validate(); err != nil {
		return err
	}
	if !sortedIDs(e.Overridden) || !slices.Equal(e.Overridden, e.Acknowledged.Exclusions) || !slices.Contains(e.Overridden, e.Acknowledged.Writer.SubjectID) {
		return ErrMalformed
	}
	return nil
}

// Observed is a side fact keyed by consumer change/revision and source edge.
// ExpectedPrior and ReadSequence prevent delayed success overwriting newer
// unavailable/stale evidence. Admission/landing interpretation belongs to later slices.
type Observed struct {
	Namespace      string      `json:"journal_namespace"`
	Alias          SourceAlias `json:"alias"`
	ChangeID       string      `json:"change_id"`
	Revision       uint64      `json:"revision"`
	Declaration    Basis       `json:"declaration"`
	Source         string      `json:"source"`
	ExpectedPrior  uint64      `json:"expected_prior"`
	ReadSequence   uint64      `json:"read_sequence"`
	SourceRevision Baseline    `json:"source_revision"`
	SourceBase     string      `json:"source_base"`
	Result         string      `json:"result"`
}

func (e Observed) Validate() error {
	if !validDigestID(e.Namespace, "omj1_") || !e.Alias.valid() {
		return ErrMalformed
	}
	if !validDigestID(e.ChangeID, "chg1_") || e.Revision == 0 || e.Declaration.EventID == 0 || !hexDigest(e.Declaration.Digest) || e.ReadSequence == 0 || !e.SourceRevision.valid() || !objectID(e.SourceBase) {
		return ErrMalformed
	}
	switch e.Result {
	case "waiting", "stale", "unavailable", "landed":
		return nil
	default:
		return ErrMalformed
	}
}
func (e Observed) key() string {
	return digest("openmerge.dependency.observation-key", []any{e.Alias, e.ChangeID, e.Revision, e.Source})
}

// Event cannot be implemented outside this package; no queue event implements it.
type Event interface {
	Validate() error
	dependencyEvent()
}

func (JournalInitialized) dependencyEvent()   {}
func (DependenciesDeclared) dependencyEvent() {}
func (PolicyApplied) dependencyEvent()        {}
func (Acknowledged) dependencyEvent()         {}
func (SelfAcknowledged) dependencyEvent()     {}
func (Observed) dependencyEvent()             {}

func (e PolicyApplied) selfAckEnabled() bool { return e.Enabled && e.Policy.SingleMaintainer }
