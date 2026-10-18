package queue

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"reflect"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/classification"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// ApprovalFacts is a side projection. Sources retain first-observation revision
// attribution and barriers across head reuse, duplicate polling and snapshots.
// Revision zero means historical/ambiguous, never authorization.
type ApprovalFacts struct {
	Accountability *ChangeAccountabilityObserved
	Intent         *ChangeIntentObserved
	Eligibility    *ApprovalEligibilityObserved
	Sources        map[string]AttributedStance
	Latest         map[string]string
	Frontiers      map[string]string
	Blockers       map[string]string
	Saturated      bool
}

// AttributedStance pins the first source observation; LastReadID alone may refresh.
type AttributedStance struct {
	Event      ReviewStanceObserved
	Revision   ChangeRevision
	LastReadID string
}

// At capacity, fail closed without dropping attribution or reviving old sources.
const maxApprovalSources = 4096

type approvalProjection map[ChangeID]ApprovalFacts

func detached[T any](v T) T {
	raw, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}
func newApprovalFacts() ApprovalFacts {
	return ApprovalFacts{Sources: map[string]AttributedStance{}, Latest: map[string]string{}, Frontiers: map[string]string{}, Blockers: map[string]string{}}
}
func sourceKey(e ReviewStanceObserved) string {
	s := e.Stance.Source
	// A later timeline event joins the same dismissal, including a previously
	// observed fallback. Native review and timeline IDs are different namespaces.
	if s.DismissedReviewID != "" {
		return string(s.Channel) + "/dismissal/" + s.DismissedReviewID
	}
	return string(s.Channel) + "/" + s.ID
}
func frontierKey(e ReviewStanceObserved) string {
	return e.Stance.UserID + "/" + string(e.Stance.Source.Channel)
}
func sourceBound(s cp.StanceSource) time.Time {
	if !s.UpperTimeBound.IsZero() {
		return s.UpperTimeBound
	}
	if s.TimeKnown {
		return s.Time
	}
	return time.Time{}
}

// newerInChannel never compares review IDs with timeline IDs. Comparable
// timestamps decide between those namespaces; blockers win ambiguity.
func orderedSourceChannel(a, b cp.Stance) bool {
	return a.Source.Channel == b.Source.Channel && a.Source.DismissedReviewID == "" && b.Source.DismissedReviewID == ""
}

func newerInChannel(next, old ReviewStanceObserved) bool {
	a, b := next.Stance, old.Stance
	if a.Source.DismissedReviewID == "" && b.Source.DismissedReviewID == "" {
		if a.Source.Sequence != b.Source.Sequence {
			return a.Source.Sequence > b.Source.Sequence
		}
		return a.Kind != cp.Approve && b.Kind == cp.Approve
	}
	if a.Kind == cp.Approve && b.Kind != cp.Approve {
		bound := sourceBound(b.Source)
		return a.Source.TimeKnown && !bound.IsZero() && a.Source.Time.After(bound)
	}
	if a.Source.TimeKnown && b.Source.TimeKnown && !a.Source.Time.Equal(b.Source.Time) {
		return a.Source.Time.After(b.Source.Time)
	}
	return a.Kind != cp.Approve && b.Kind == cp.Approve
}
func (s State) approvalBinding(b ApprovalBasis) (RevisionRecord, bool) {
	for _, r := range s.changeRevisions.snapshot() {
		if r.ChangeID == b.ChangeID && r.Revision == b.Revision && r.HeadSHA == b.Head {
			return r, true
		}
	}
	return RevisionRecord{}, false
}
func (s State) observeApproval(e Event) (State, bool) {
	var b ApprovalBasis
	switch e := e.(type) {
	case ChangeAccountabilityObserved:
		if e.Validate() != nil {
			return s, true
		}
		b = e.Basis
	case ChangeIntentObserved:
		if e.Validate() != nil {
			return s, true
		}
		b = e.Basis
	case ApprovalEligibilityObserved:
		if e.Validate() != nil {
			return s, true
		}
		b = e.Basis
	case ReviewStanceObserved:
		if e.Validate() != nil {
			return s, true
		}
		b = e.Basis
	default:
		return s, false
	}
	boundary, ok := s.approvalBinding(b)
	if !ok {
		return s, true
	}
	f, exists := s.approvalFacts[b.ChangeID]
	if !exists {
		f = newApprovalFacts()
	} else {
		f = detached(f)
	}
	switch e := e.(type) {
	case ChangeAccountabilityObserved:
		f.Accountability = &e
	case ChangeIntentObserved:
		f.Intent = &e
	case ApprovalEligibilityObserved:
		f.Eligibility = &e
	case ReviewStanceObserved:
		key := sourceKey(e)
		if first, exists := f.Sources[key]; exists {
			// Re-observation can attest that the blocker is still present, but cannot
			// renew its barrier, revision attribution or ordering (including restart).
			if first.Event.Stance.UserID == e.Stance.UserID {
				first.LastReadID = b.ReadID
				f.Sources[key] = first
			}
			break
		}
		if len(f.Sources) >= maxApprovalSources {
			f.Saturated = true
			break
		}
		revision := b.Revision
		if e.Stance.Source.Channel == cp.NativeReview {
			// On reused heads a review must prove it happened after the current
			// boundary. Unknown/older discoveries remain historical, even at equal SHA.
			if e.Stance.Head != b.Head || b.Revision > 1 && (boundary.Basis.At.IsZero() || !e.Stance.Source.TimeKnown || !e.Stance.Source.Time.After(boundary.Basis.At)) {
				revision = 0
			}
		}
		if e.Stance.Source.DismissedReviewID != "" {
			if original, known := f.Sources[string(cp.NativeReview)+"/"+e.Stance.Source.DismissedReviewID]; known {
				revision = original.Revision
			}
		}
		f.Sources[key] = AttributedStance{e, revision, b.ReadID}
		if revision == 0 || revision != b.Revision {
			break
		}
		user := e.Stance.UserID
		channel := frontierKey(e)
		if e.Stance.Kind != cp.Approve {
			retainBlocker(&f, user, key, revision)
		}
		if oldKey, ok := f.Frontiers[channel]; ok {
			old := f.Sources[oldKey]
			if old.Revision == revision && !newerInChannel(e, old.Event) {
				// Source order alone cannot prove that an approval cleared a
				// blocking action discovered late with equal/unknown times.
				if e.Stance.Kind != cp.Approve {
					latest := f.Sources[f.Latest[user]]
					bound := sourceBound(e.Stance.Source)
					if latest.Revision == revision && latest.Event.Stance.Kind == cp.Approve && (bound.IsZero() || !latest.Event.Stance.Source.TimeKnown || !latest.Event.Stance.Source.Time.After(bound)) {
						f.Latest[user] = key
					}
				}
				break
			}
		}
		f.Frontiers[channel] = key
		if e.Stance.Kind == cp.Approve {
			if latestKey, ok := f.Latest[user]; ok {
				latest := f.Sources[latestKey]
				if latest.Revision == revision && !orderedSourceChannel(e.Stance, latest.Event.Stance) && latest.Event.Stance.Source.TimeKnown && e.Stance.Source.TimeKnown && latest.Event.Stance.Source.Time.After(e.Stance.Source.Time) {
					break
				}
			}
			if blockerKey, ok := f.Blockers[user]; ok {
				blocker := f.Sources[blockerKey]
				bound := sourceBound(blocker.Event.Stance.Source)
				if blocker.Revision == revision && (bound.IsZero() || !e.Stance.Source.TimeKnown || !e.Stance.Source.Time.After(bound) || blocker.LastReadID != b.ReadID) {
					break
				}
			}
		} else {
			// An older blocker cannot displace a provably newer current stance.
			if latestKey, ok := f.Latest[user]; ok {
				latest := f.Sources[latestKey]
				bound := sourceBound(e.Stance.Source)
				if latest.Revision == revision && !orderedSourceChannel(e.Stance, latest.Event.Stance) && latest.Event.Stance.Source.TimeKnown && !bound.IsZero() && latest.Event.Stance.Source.Time.After(bound) {
					break
				}
			}

		}
		f.Latest[user] = key
	}
	s.approvalFacts = maps.Clone(s.approvalFacts)
	if s.approvalFacts == nil {
		s.approvalFacts = approvalProjection{}
	}
	s.approvalFacts[b.ChangeID] = detached(f)
	return s, true
}

// Source frontiers and time barriers are independent: even a lower-ID blocker
// needs positive time proof before an approval can clear it.
func retainBlocker(f *ApprovalFacts, user, key string, revision ChangeRevision) {
	oldKey, blocked := f.Blockers[user]
	old, next := f.Sources[oldKey], f.Sources[key]
	oldBound, newBound := sourceBound(old.Event.Stance.Source), sourceBound(next.Event.Stance.Source)
	if !blocked || old.Revision != revision || !oldBound.IsZero() && (newBound.IsZero() || newBound.After(oldBound)) {
		f.Blockers[user] = key
	}
}

// ApprovalFacts returns detached evidence. Absence remains nil for legacy queues.
func (s State) ApprovalFacts(id ChangeID) (ApprovalFacts, bool) {
	f, ok := s.approvalFacts[id]
	return detached(f), ok
}

// ApprovalFingerprint covers decision inputs, not polling read times. It does
// not participate in CI keys/timers, classification or landing until PR 8/11.
func (s State) ApprovalFingerprint(id ChangeID) string {
	f, ok := s.ApprovalFacts(id)
	if !ok {
		return ""
	}
	for key, v := range f.Sources {
		v.LastReadID = ""
		v.Event.Basis.ReadID = ""
		v.Event.Basis.At = time.Time{}
		v.Event.Observation = forge.NativeObservation{}
		f.Sources[key] = v
	}
	if f.Accountability != nil {
		f.Accountability.Basis.ReadID = ""
		f.Accountability.Basis.At = time.Time{}
	}
	if f.Intent != nil {
		f.Intent.Basis.ReadID = ""
		f.Intent.Basis.At = time.Time{}
	}
	if f.Eligibility != nil {
		f.Eligibility.Basis.ReadID = ""
		f.Eligibility.Basis.At = time.Time{}
	}
	var current RevisionRecord
	for _, r := range s.changeRevisions.snapshot() {
		if r.ChangeID == id {
			current = r
			break
		}
	}
	raw, _ := json.Marshal(struct {
		Facts    ApprovalFacts
		Revision RevisionRecord
	}{f, current})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// EvaluateApprovals is the dormant decision boundary. The caller supplies a
// freshly read eligibility event for THIS read, never cached
// classification pools or stored eligibility. No forge I/O occurs here.
func (s State) EvaluateApprovals(policy cp.Policy, current cp.ReceiptIdentity, fresh ApprovalEligibilityObserved) cp.Evaluation {
	if policy.Approvals == nil {
		return cp.Evaluate(policy, cp.Input{})
	}
	in := cp.Input{Current: current, Actors: map[string]cp.Actor{}, Stances: map[string]cp.Stance{}}
	id := ChangeID(current.ChangeID)
	stored, hasClassification := s.Classification(id, ChangeRevision(current.Revision))
	if hasClassification {
		in.Classification = stored.Evidence.Classification
		owners := classification.ResolveOwnerEligibility(stored.Evidence, freshOwnerResolver(fresh))
		in.Classification.CodeOwners, in.Classification.PolicyOwners = owners.CodeOwners, owners.PolicyOwners
		in.Classification.CodeOwnersKnown, in.Classification.PolicyOwnersKnown = owners.CodeOwnersKnown, owners.PolicyOwnersKnown
	}
	f, ok := s.ApprovalFacts(id)
	_, currentBinding := s.approvalBinding(fresh.Basis)
	if f.Saturated || !currentBinding || fresh.Validate() != nil || !fresh.Known || fresh.Basis.ChangeID != id || uint64(fresh.Basis.Revision) != current.Revision || fresh.Basis.Head != current.Head {
		return cp.Evaluation{OptedIn: policy.Approvals != nil, ReviewsKnown: false, Reasons: []string{"fresh eligibility unresolved"}}
	}
	// Stored group snapshots and Grants.Fresh never flow into this decision.
	policy.Groups = detached(fresh.Groups)
	in.Actors = detached(fresh.Actors)
	if ok && f.Accountability != nil && uint64(f.Accountability.Basis.Revision) == current.Revision && f.Accountability.Basis.Head == current.Head {
		a := f.Accountability
		in.AuthorKind = a.Author.Kind
		in.ExcludedIDs = append(in.ExcludedIDs, a.Author.ID, a.SubmitterID)
		for _, h := range a.Humans {
			in.ExcludedIDs = append(in.ExcludedIDs, h.ID)
		}
	} else if policy.Approvals != nil {
		return cp.Evaluation{OptedIn: true, Reasons: []string{"accountability not recorded"}}
	}
	for user, key := range f.Latest {
		attributed := f.Sources[key]
		stance := attributed.Event.Stance
		if uint64(attributed.Revision) == current.Revision && stance.Head == current.Head {
			stance.Revision = uint64(attributed.Revision)
			in.Stances[user] = stance
		}
	}
	return cp.Evaluate(policy, in)
}

// Resolve only THIS read's recorded owner-token pools. Stored classification
// and prior eligibility pools are never candidates, even with Fresh=true.
func freshOwnerResolver(fresh ApprovalEligibilityObserved) classification.OwnerResolver {
	return func(o classification.Owner) cp.IdentitySet {
		pool, ok := fresh.OwnerPools[o.Label()]
		if !ok {
			return cp.IdentitySet{Reason: "fresh owner resolution unavailable"}
		}
		return detached(pool)
	}
}

func upgradeApprovalSnapshot(data []byte, version int) ([]byte, error) {
	data, err := upgradeClassificationSnapshot(data, version)
	if err != nil || version >= 5 {
		return data, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if _, ok := fields["ApprovalFacts"]; ok {
		return nil, fmt.Errorf("old snapshot cannot contain approval facts")
	}
	fields["ApprovalFacts"] = json.RawMessage(`null`)
	return json.Marshal(fields)
}
func validateApprovalSnapshot(p approvalProjection) error {
	count := 0
	for id, f := range p {
		if id.Validate() != nil || len(f.Sources) > maxApprovalSources || f.Sources == nil || f.Frontiers == nil || f.Latest == nil || f.Blockers == nil {
			return fmt.Errorf("invalid approval projection")
		}
		for _, event := range []Event{valueEvent(f.Accountability), valueEvent(f.Intent), valueEvent(f.Eligibility)} {
			if event != nil {
				var basis ApprovalBasis
				switch e := event.(type) {
				case ChangeAccountabilityObserved:
					if e.Validate() != nil {
						return fmt.Errorf("invalid accountability snapshot")
					}
					basis = e.Basis
				case ChangeIntentObserved:
					if e.Validate() != nil {
						return fmt.Errorf("invalid intent snapshot")
					}
					basis = e.Basis
				case ApprovalEligibilityObserved:
					if e.Validate() != nil {
						return fmt.Errorf("invalid eligibility snapshot")
					}
					basis = e.Basis
				}
				if basis.ChangeID != id {
					return fmt.Errorf("cross-change approval snapshot")
				}
			}
		}
		for key, v := range f.Sources {
			count++
			if count > maxSnapshotEntries || v.Event.Validate() != nil || key != sourceKey(v.Event) || v.Event.Basis.ChangeID != id || v.Revision > v.Event.Basis.Revision || v.Revision != 0 && v.Event.Stance.Source.Channel == cp.CommentCommand && v.Revision != v.Event.Basis.Revision || v.LastReadID == "" {
				return fmt.Errorf("invalid attributed source")
			}
		}
		for name, index := range map[string]map[string]string{"latest": f.Latest, "blockers": f.Blockers, "frontiers": f.Frontiers} {
			for user, key := range index {
				v, ok := f.Sources[key]
				expected := v.Event.Stance.UserID
				if name == "frontiers" {
					expected = frontierKey(v.Event)
				}
				if !ok || v.Revision == 0 || user != expected || name == "blockers" && v.Event.Stance.Kind == cp.Approve {
					return fmt.Errorf("invalid stance index")
				}
			}
		}
	}
	return nil
}
func valueEvent[T Event](p *T) Event {
	if p == nil {
		return nil
	}
	return *p
}

// Shared contracts are explicit structs. Strict recursive presence and unknown
// field checks apply to these new types without altering older wire codecs.
func readApprovalWire[T interface{ Validate() error }](data json.RawMessage) (T, error) {
	var v T
	if err := requireSnapshotFields(data, reflect.TypeFor[T](), "approval"); err != nil {
		return v, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&v); err != nil {
		return v, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return v, fmt.Errorf("trailing approval data")
	}
	return v, v.Validate()
}
