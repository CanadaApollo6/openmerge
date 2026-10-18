package queue

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"strconv"
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
	PriorHeads     []string
	HistoryKnown   bool
	Saturated      bool
	// ReceiptBoundaries retains old durable revisions only when receipts need them.
	ReceiptBoundaries map[ChangeRevision]RevisionRecord `json:",omitempty"`
}

// AttributedStance pins the first source observation; LastReadID alone may refresh.
type AttributedStance struct {
	Event      ReviewStanceObserved
	Revision   ChangeRevision
	LastReadID string
}

// At capacity, fail closed for this change without dropping first-source pins.
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

// orderedSourceChannel compares only sources in the same sequence namespace.
func orderedSourceChannel(a, b cp.Stance) bool {
	return a.Source.Channel == b.Source.Channel && a.Source.DismissedReviewID == "" && b.Source.DismissedReviewID == ""
}

// supersedes requires source ordering AND time proof for barriers. Read grouping
// is acquisition metadata, never ordering evidence. Unknown/equal times lose.
func supersedes(a, b cp.Stance) bool {
	bound := sourceBound(b.Source)
	return (!orderedSourceChannel(a, b) || a.Source.Sequence > b.Source.Sequence) &&
		a.Source.TimeKnown && !bound.IsZero() && a.Source.Time.After(bound)
}

// preferred is a total, deterministic presentation order, not barrier clearance.
func preferred(a, b ReviewStanceObserved) bool {
	x, y := a.Stance, b.Stance
	if !x.Source.Time.Equal(y.Source.Time) {
		return x.Source.Time.After(y.Source.Time)
	}
	return sourceKey(a) > sourceKey(b)
}

// recomputeUser folds the SET for one user on the current revision. Every
// positive is reconsidered when a source arrives; no rejected-positive cache or
// incremental frontier can hide a request for changes.
func recomputeUser(f *ApprovalFacts, user string, revision ChangeRevision) {
	delete(f.Latest, user)
	delete(f.Blockers, user)
	for _, channel := range []cp.SourceChannel{cp.NativeReview, cp.CommentCommand} {
		delete(f.Frontiers, user+"/"+string(channel))
	}
	positives := map[cp.SourceChannel]string{}
	var requests, withdrawals []string
	for key, v := range f.Sources {
		e := v.Event
		if v.Revision != revision || e.Stance.UserID != user {
			continue
		}
		channel := frontierKey(e)
		if old, ok := f.Frontiers[channel]; !ok || preferred(e, f.Sources[old].Event) {
			f.Frontiers[channel] = key
		}
		switch e.Stance.Kind {
		case cp.Approve:
			ch := e.Stance.Source.Channel
			if old, ok := positives[ch]; !ok || e.Stance.Source.Sequence > f.Sources[old].Event.Stance.Source.Sequence {
				positives[ch] = key
			}
		case cp.RequestChanges:
			requests = append(requests, key)
		case cp.Withdraw:
			withdrawals = append(withdrawals, key)
		}
	}
	choose := func(keys []string) string {
		best := ""
		for _, key := range keys {
			if best == "" || preferred(f.Sources[key].Event, f.Sources[best].Event) {
				best = key
			}
		}
		return best
	}
	// At most two positive candidates: latest in each channel. Both must clear
	// every pinned barrier, including older lower-ID blockers.
	var valid []string
	for _, key := range positives {
		a := f.Sources[key].Event.Stance
		barriersCleared := true
		for _, blocks := range [][]string{requests, withdrawals} {
			for _, k := range blocks {
				barrier := f.Sources[k].Event.Stance
				if barrier.Source.DismissedReviewID != "" && (a.Source.Channel != cp.NativeReview || a.Source.Sequence > dismissalReviewSequence(barrier)) {
					continue
				}
				if !supersedes(a, barrier) {
					barriersCleared = false
				}
			}
		}
		if barriersCleared {
			valid = append(valid, key)
		}
	}
	if best := choose(valid); best != "" {
		f.Latest[user] = best
		return
	}
	// A withdrawal can clear a request only with strict time proof. In
	// particular a fallback dismissal cannot mask an ambiguous/newer request.
	// Prefix maxima over descending command IDs answer every same-namespace
	// query in O(n log n), even for thousands of alternating blockers.
	var commands []cp.Stance
	var dismissals []cp.Stance
	for _, key := range withdrawals {
		w := f.Sources[key].Event.Stance
		if !w.Source.TimeKnown {
			continue
		}
		if w.Source.DismissedReviewID != "" {
			dismissals = append(dismissals, w)
		} else {
			commands = append(commands, w)
		}
	}
	slices.SortFunc(commands, func(a, b cp.Stance) int { return cmp.Compare(b.Source.Sequence, a.Source.Sequence) })
	times := make([]time.Time, len(commands))
	for i, w := range commands {
		times[i] = w.Source.Time
		if i > 0 && times[i-1].After(times[i]) {
			times[i] = times[i-1]
		}
	}
	var outstanding []string
	for _, key := range requests {
		r := f.Sources[key].Event.Stance
		bound := sourceBound(r.Source)
		var proof time.Time
		for _, w := range dismissals {
			if r.Source.Channel == cp.NativeReview && r.Source.Sequence <= dismissalReviewSequence(w) && w.Source.Time.After(proof) {
				proof = w.Source.Time
			}
		}
		n := len(commands)
		if r.Source.Channel == cp.CommentCommand {
			n, _ = slices.BinarySearchFunc(commands, r.Source.Sequence, func(w cp.Stance, seq uint64) int { return cmp.Compare(seq, w.Source.Sequence) })
		}
		if n > 0 && times[n-1].After(proof) {
			proof = times[n-1]
		}
		if bound.IsZero() || !proof.After(bound) {
			outstanding = append(outstanding, key)
		}
	}
	best := choose(outstanding)
	if best == "" {
		best = choose(withdrawals)
	}
	if best != "" {
		f.Latest[user] = best
		f.Blockers[user] = best
	}
}

func cloneApprovalFacts(f ApprovalFacts) ApprovalFacts {
	f.ReceiptBoundaries = maps.Clone(f.ReceiptBoundaries)
	f.PriorHeads = slices.Clone(f.PriorHeads)
	f.Sources = maps.Clone(f.Sources)
	f.Latest = maps.Clone(f.Latest)
	f.Frontiers = maps.Clone(f.Frontiers)
	f.Blockers = maps.Clone(f.Blockers)
	return f
}

func (s State) approvalBinding(b ApprovalBasis) (RevisionRecord, bool) {
	for _, r := range s.changeRevisions.snapshot() {
		if r.ChangeID == b.ChangeID && r.Revision == b.Revision && r.HeadSHA == b.Head && !r.Basis.At.IsZero() {
			return r, true
		}
	}
	return RevisionRecord{}, false
}

// ApprovalBoundaryError rejects acquisition before its durable revision boundary.
type ApprovalBoundaryError struct{ Basis ApprovalBasis }

func (e ApprovalBoundaryError) Error() string {
	return "approval observation has no current revision boundary"
}

// CheckApprovalBoundary is the typed preflight for the 11a journal producer.
func (s State) CheckApprovalBoundary(b ApprovalBasis) error {
	if _, ok := s.approvalBinding(b); !ok {
		return ApprovalBoundaryError{b}
	}
	return b.Validate()
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
		f.HistoryKnown = b.Revision == 1
	} else {
		f = cloneApprovalFacts(f)
	}
	switch e := e.(type) {
	case ChangeAccountabilityObserved:
		e = detached(e)
		f.Accountability = &e
	case ChangeIntentObserved:
		f.Intent = &e
	case ApprovalEligibilityObserved:
		e = detached(e)
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
			clear(f.Latest)
			clear(f.Blockers)
			clear(f.Frontiers)
			// Preserve a new barrier in preference to positive noise. Saturated
			// remains a fail-closed barrier for the entire epoch.
			evict := ""
			if e.Stance.Kind != cp.Approve || e.Stance.Receipt != nil {
				for k, v := range f.Sources {
					if v.Event.Stance.Kind == cp.Approve && v.Event.Stance.Receipt == nil && (evict == "" || k < evict) {
						evict = k
					}
				}
			}
			if evict == "" {
				break
			}
			delete(f.Sources, evict)
		}
		revision := b.Revision
		if e.Stance.Source.Channel == cp.NativeReview {
			submitted := e.SourceTime
			submissionKnown := e.Stance.Source.TimeKnown
			if e.Stance.Source.DismissedReviewID != "" {
				submitted = e.OriginalSubmission
				submissionKnown = !submitted.Time.IsZero()
			}
			// Only reused heads need epoch proof. Allow for the same 2s clock
			// uncertainty and 1s timestamp resolution used by the Q4 fallback bound.
			reused := b.Revision > 1 && !f.HistoryKnown
			for _, head := range f.PriorHeads {
				if head == b.Head {
					reused = true
				}
			}
			if e.Stance.Head != b.Head || e.Stance.Kind != cp.RequestChanges && reused && (!submissionKnown || submitted.Time.IsZero() || !submitted.Time.After(boundary.Basis.At.Add(3*time.Second))) {
				revision = 0
			}
		}
		if e.Stance.Receipt != nil {
			receipt := *e.Stance.Receipt
			e.Stance.Receipt = &receipt
		}
		f.Sources[key] = AttributedStance{e, revision, b.ReadID}
		// Dismissals always derive attribution from their original, irrespective
		// of which of the two sources was discovered first.
		for k, v := range f.Sources {
			if v.Event.Stance.UserID != e.Stance.UserID || v.Event.Stance.Source.DismissedReviewID == "" {
				continue
			}
			if original, known := f.Sources[string(cp.NativeReview)+"/"+v.Event.Stance.Source.DismissedReviewID]; known {
				v.Revision = original.Revision
				// A join changes attribution evidence, not the pinned action time.
				if v.Revision > v.Event.Basis.Revision {
					v.Event.Basis = original.Event.Basis
					v.Event.Stance.Revision = uint64(original.Event.Basis.Revision)
				}
				f.Sources[k] = v
			}
		}
		if !f.Saturated {
			recomputeUser(&f, e.Stance.UserID, b.Revision)
		}
	}

	s.approvalFacts = maps.Clone(s.approvalFacts)
	if s.approvalFacts == nil {
		s.approvalFacts = approvalProjection{}
	}
	s.approvalFacts[b.ChangeID] = f
	return s, true
}

// ApprovalFacts returns detached evidence. Absence remains nil for legacy queues.
func (s State) ApprovalFacts(id ChangeID) (ApprovalFacts, bool) {
	f, ok := s.approvalFacts[id]
	return detached(f), ok
}

// ApprovalFingerprint hashes only current decision inputs. Acquisition IDs/times,
// revision-zero sources and non-effective historical barriers are not inputs.
func (s State) ApprovalFingerprint(id ChangeID) string {
	f, ok := s.ApprovalFacts(id)
	if !ok {
		return ""
	}
	var current RevisionRecord
	for _, r := range s.changeRevisions.snapshot() {
		if r.ChangeID == id {
			current = r
			break
		}
	}
	if current.Revision == 0 {
		return ""
	}
	effective := map[string]cp.Stance{}
	for user, key := range f.Latest {
		if v, ok := f.Sources[key]; ok && v.Revision != 0 && v.Revision == current.Revision {
			effective[user] = v.Event.Stance
		}
	}
	if f.Accountability != nil {
		f.Accountability.Basis.ReadID = ""
		f.Accountability.Basis.At = time.Time{}
		f.Accountability.Reason = ""
	}
	if f.Intent != nil {
		f.Intent.Basis.ReadID = ""
		f.Intent.Basis.At = time.Time{}
	}
	if f.Eligibility != nil {
		f.Eligibility.Basis.ReadID = ""
		f.Eligibility.Basis.At = time.Time{}
		f.Eligibility.Reasons = nil
	}
	// Membership and accountability are sets; acquisition order is not a change.
	if f.Eligibility != nil {
		for _, pools := range []map[string]cp.IdentitySet{f.Eligibility.Groups, f.Eligibility.OwnerPools} {
			for key, pool := range pools {
				pool.Reason = ""
				pool.IDs = append([]string{}, pool.IDs...)
				slices.Sort(pool.IDs)
				pool.IDs = slices.Compact(pool.IDs)
				pools[key] = pool
			}
		}
	}
	if f.Accountability != nil {
		slices.SortFunc(f.Accountability.Humans, func(a, b cp.Identity) int { return cmp.Compare(a.ID, b.ID) })
		f.Accountability.Humans = slices.CompactFunc(f.Accountability.Humans, func(a, b cp.Identity) bool { return a == b })
	}
	var inputs cp.ClassificationInputs
	if c, ok := s.classifications[id][current.Revision]; ok {
		inputs = c.Evidence.Classification.Inputs
		inputs.ConfigBlob = ""
		inputs.GoverningCommit = ""
	}
	raw, _ := json.Marshal(struct {
		Accountability *ChangeAccountabilityObserved
		Intent         *ChangeIntentObserved
		Eligibility    *ApprovalEligibilityObserved
		Stances        map[string]cp.Stance
		Saturated      bool
		ChangeID       ChangeID
		Revision       ChangeRevision
		Head           string
		Classification cp.ClassificationInputs
		ConfigDigest   string
	}{f.Accountability, f.Intent, f.Eligibility, effective, f.Saturated, id, current.Revision, current.HeadSHA, inputs, s.ConfigDigest})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// EvaluateApprovals is the dormant decision boundary. The caller supplies a
// freshly read eligibility event for THIS read, never cached
// classification pools or stored eligibility. No forge I/O occurs here.
func (s State) EvaluateApprovals(policy cp.Policy, current cp.ReceiptIdentity, fresh ApprovalEligibilityObserved) cp.Evaluation {
	return s.evaluateApprovals(policy, current, fresh, "")
}

// ApprovalActorRelevant is an admission filter, never landing authority. A
// hypothetical native stance asks the same evaluator which pools the actor can
// affect. Unknown pools retain evidence conservatively; definite exclusions do
// not consume permanent source pins. Receipt requirements still expose native
// eligibility without counting the hypothetical approval as a receipt.
func (s State) ApprovalActorRelevant(policy cp.Policy, current cp.ReceiptIdentity, fresh ApprovalEligibilityObserved, actor string) bool {
	result := s.evaluateApprovals(policy, current, fresh, actor)
	if !result.ReviewsKnown {
		return true
	}
	for _, r := range result.Requirements {
		if slices.Contains(r.EligibleIDs, actor) {
			return true
		}
	}
	return false
}

func (s State) evaluateApprovals(policy cp.Policy, current cp.ReceiptIdentity, fresh ApprovalEligibilityObserved, probeActor string) cp.Evaluation {
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
	if f.Intent == nil || f.Intent.Basis.Revision != fresh.Basis.Revision || f.Intent.Basis.Head != current.Head || f.Intent.Digest != current.IntentDigest {
		return cp.Evaluation{OptedIn: true, Reasons: []string{"current intent not recorded"}}
	}
	// Stored group snapshots and Grants.Fresh never flow into this decision.
	policy.Groups = detached(fresh.Groups)
	in.Actors = detached(fresh.Actors)
	if ok && f.Accountability != nil && uint64(f.Accountability.Basis.Revision) == current.Revision && f.Accountability.Basis.Head == current.Head && f.Accountability.PolicyDigest == current.PolicyDigest {
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
	if probeActor != "" {
		in.Stances = map[string]cp.Stance{probeActor: {UserID: probeActor, Kind: cp.Approve, Revision: current.Revision, Head: current.Head}}
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
func validateApprovalSnapshot(p approvalProjection, revisions map[int]RevisionRecord) error {
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
		var revision ChangeRevision
		for _, r := range revisions {
			if r.ChangeID == id {
				revision = r.Revision
			}
		}
		for key, boundary := range f.ReceiptBoundaries {
			if len(f.ReceiptBoundaries) > maxSnapshotEntries || key == 0 || key >= revision || key != boundary.Revision || boundary.ChangeID != id || !forge.ValidDiffObjectID(boundary.HeadSHA) || boundary.Basis.At.IsZero() {
				return fmt.Errorf("invalid receipt revision history")
			}
		}
		if revision == 0 || len(f.PriorHeads) > maxSnapshotEntries || uint64(len(f.PriorHeads)) >= uint64(revision) {
			return fmt.Errorf("invalid approval history")
		}
		for _, v := range f.Sources {
			if v.Revision > revision || v.Event.Basis.Revision > revision {
				return fmt.Errorf("approval source exceeds current revision")
			}
		}
		seen := map[string]bool{}
		for _, head := range f.PriorHeads {
			if head == "" || seen[head] {
				return fmt.Errorf("invalid approval prior heads")
			}
			seen[head] = true
		}
		derived := cloneApprovalFacts(f)
		clear(derived.Latest)
		clear(derived.Blockers)
		clear(derived.Frontiers)
		if !f.Saturated {
			users := map[string]map[string]AttributedStance{}
			for key, v := range f.Sources {
				user := v.Event.Stance.UserID
				if users[user] == nil {
					users[user] = map[string]AttributedStance{}
				}
				users[user][key] = v
			}
			// Fold each user's sources once; scanning the full set once per user made
			// snapshot admission quadratic in the number of distinct noise accounts.
			for user, sources := range users {
				part := newApprovalFacts()
				part.Sources = sources
				recomputeUser(&part, user, revision)
				maps.Copy(derived.Latest, part.Latest)
				maps.Copy(derived.Blockers, part.Blockers)
				maps.Copy(derived.Frontiers, part.Frontiers)
			}
		}
		if !maps.Equal(f.Latest, derived.Latest) || !maps.Equal(f.Blockers, derived.Blockers) || !maps.Equal(f.Frontiers, derived.Frontiers) {
			return fmt.Errorf("approval cache contradicts retained sources")
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

// ApprovalDecision keeps the evaluated inputs together for Stage A 9's atomic
// eligibility-journal/snapshot transaction. Snapshot includes precisely Fresh.
type ApprovalDecision struct {
	Snapshot       State
	Fresh          ApprovalEligibilityObserved
	Accountability *ChangeAccountabilityObserved
	Intent         *ChangeIntentObserved
	// ReliedOn contains all effective current-revision stances. The counted
	// subset is Evaluation.Requirements[].ReliedOn.
	ReliedOn   []AttributedStance
	Current    cp.ReceiptIdentity
	Evaluation cp.Evaluation
}

// PrepareApprovalDecision never acquires permission itself. Stage A 9 must
// journal Fresh atomically with Snapshot; journal failure rejects the decision.
func (s State) PrepareApprovalDecision(policy cp.Policy, current cp.ReceiptIdentity, fresh ApprovalEligibilityObserved) (ApprovalDecision, error) {
	if err := s.CheckApprovalAdmission(fresh); err != nil {
		return ApprovalDecision{}, err
	}
	fresh = detached(fresh)
	next := Apply(s, fresh)
	f, _ := next.ApprovalFacts(ChangeID(current.ChangeID))
	d := ApprovalDecision{Snapshot: next, Fresh: fresh, Accountability: f.Accountability, Intent: f.Intent, Current: current, Evaluation: next.EvaluateApprovals(policy, current, fresh)}
	for _, key := range f.Latest {
		v := f.Sources[key]
		if uint64(v.Revision) == current.Revision {
			d.ReliedOn = append(d.ReliedOn, v)
		}
	}
	slices.SortFunc(d.ReliedOn, func(a, b AttributedStance) int { return cmp.Compare(a.Event.Stance.UserID, b.Event.Stance.UserID) })
	return d, nil
}

// DismissedReviewID is the native sequence, never the timeline event ID.
func dismissalReviewSequence(w cp.Stance) uint64 {
	n, _ := strconv.ParseUint(w.Source.DismissedReviewID, 10, 64)
	return n
}

// History lives exclusively beside approval evidence. Legacy-only changes do
// not allocate it. Missing early history remains unknown until full replay.
func (s *State) advanceApprovalRevision(current RevisionRecord, revision ChangeRevision) {
	f, ok := s.approvalFacts[current.ChangeID]
	if !ok || current.Revision == revision {
		return
	}
	f = cloneApprovalFacts(f)
	for _, receipt := range s.receipts[current.ChangeID] {
		if receipt.Basis.Revision == current.Revision {
			if f.ReceiptBoundaries == nil {
				f.ReceiptBoundaries = map[ChangeRevision]RevisionRecord{}
			}
			f.ReceiptBoundaries[current.Revision] = current
			break
		}
	}
	if f.HistoryKnown && !slices.Contains(f.PriorHeads, current.HeadSHA) {
		f.PriorHeads = append(f.PriorHeads, current.HeadSHA)
	}
	// Native positive pins on older epochs cannot authorize again without head
	// and time proof. Retain command pins and every request/withdrawal barrier.
	for key, v := range f.Sources {
		if v.Revision > 0 && v.Revision < revision && v.Event.Stance.Source.Channel == cp.NativeReview && v.Event.Stance.Kind == cp.Approve {
			delete(f.Sources, key)
		}
	}
	f.Saturated = len(f.Sources) >= maxApprovalSources
	clear(f.Latest)
	clear(f.Blockers)
	clear(f.Frontiers)
	s.approvalFacts = maps.Clone(s.approvalFacts)
	s.approvalFacts[current.ChangeID] = f
}

// ApprovalSnapshotBudgetError rejects admission for ChangeID before producing
// an unrestorable queue snapshot. 11a must roll back that change's admission.
type ApprovalSnapshotBudgetError struct {
	ChangeID                ChangeID
	ChangeBytes, QueueBytes int
}

func (e ApprovalSnapshotBudgetError) Error() string {
	return fmt.Sprintf("approval snapshot admission rejected for %s: change=%d queue=%d bytes", e.ChangeID, e.ChangeBytes, e.QueueBytes)
}

const maxApprovalChangeBytes = 8 << 20
const maxApprovalQueueBytes = 48 << 20

func checkApprovalSnapshotBudget(p approvalProjection) error {
	ids := make([]ChangeID, 0, len(p))
	for id := range p {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	total := 0
	for _, id := range ids {
		raw, err := json.Marshal(p[id])
		if err != nil {
			return err
		}
		total += len(raw)
		if len(raw) > maxApprovalChangeBytes || total > maxApprovalQueueBytes {
			return ApprovalSnapshotBudgetError{id, len(raw), total}
		}
	}
	return nil
}
