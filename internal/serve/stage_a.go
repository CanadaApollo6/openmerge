package serve

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/classification"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
)

// stageAPolicy has no production constructor or public configuration path in
// 11a. Isolated tests install a candidate; 11b owns activation and config digest
// composition. A nil policy preserves every legacy read and write path.
type stageAPolicy struct {
	repository cp.Repository
	candidate  cp.CandidateQueue
	policy     cp.Policy
	mappings   map[string][]cp.Identity
	identities map[string]cp.ResolvedIdentity
	governing  *stageAGoverningCache
}

func policyIdentity(u forge.UserIdentity) (cp.Identity, error) {
	kind := cp.AgentAccount
	if u.Kind == forge.UserAccount {
		kind = cp.HumanAccount
	}
	if u.ID <= 0 || u.Login == "" || u.Kind == "" || u.Kind == forge.UnknownAccount {
		return cp.Identity{}, forge.ErrIdentityUnavailable
	}
	return cp.Identity{ID: classification.GitHubUserID(u.ID), Login: u.Login, Kind: kind}, nil
}

func (w *worker) stageReaders() (forge.IdentityReader, forge.NativeReviewReader, error) {
	ids, ok := w.Driver.(forge.IdentityReader)
	reviews, rok := w.Driver.(forge.NativeReviewReader)
	if !ok || !rok {
		return nil, nil, errors.New("stage A requires identity-bound grants and native reviews")
	}
	return ids, reviews, nil
}

// resolvePool does not reuse membership across decisions. A failed or incomplete
// read remains unknown, including a team with some readable members.
func resolvePool(ctx context.Context, ids forge.IdentityReader, name string, pins map[string]cp.ResolvedIdentity) (cp.IdentitySet, []forge.UserIdentity) {
	unknown := func(err error) (cp.IdentitySet, []forge.UserIdentity) {
		return cp.IdentitySet{IDs: []string{}, Reason: err.Error()}, nil
	}
	pin, pinned := pins[name]
	if !pinned || pin.ID == "" {
		return unknown(fmt.Errorf("governing owner identity unresolved: %s", name))
	}
	var users []forge.UserIdentity
	if strings.Contains(name, "/") {
		team, err := ids.ResolveTeam(ctx, name)
		if err != nil {
			return unknown(err)
		}
		if pin.ID != teamPolicyID(team) {
			return unknown(forge.ErrIdentityMismatch)
		}
		var errRead error
		users, errRead = ids.TeamMembers(ctx, team)
		if errRead != nil {
			return unknown(errRead)
		}
	} else {
		user, err := ids.ResolveUser(ctx, name)
		if err != nil {
			return unknown(err)
		}
		if pin.ID != classification.GitHubUserID(user.ID) {
			return unknown(forge.ErrIdentityMismatch)
		}
		users = []forge.UserIdentity{user}
	}
	pool := cp.IdentitySet{IDs: []string{}, Resolved: true, Fresh: true}
	for _, u := range users {
		p, err := policyIdentity(u)
		if err != nil {
			return unknown(err)
		}
		pool.IDs = append(pool.IDs, p.ID)
	}
	slices.Sort(pool.IDs)
	pool.IDs = slices.Compact(pool.IDs)
	return pool, users
}

// refreshStageAPolicy re-resolves names even at an unchanged governing commit.
// The public configuration loader remains the first boundary and rejects keys.
func (s *Server) refreshStageAPolicy(ctx context.Context, w *worker, tip string) error {
	ids, _, err := w.stageReaders()
	if err != nil {
		return err
	}
	a := w.stageA
	if a.governing == nil {
		a.governing = newStageAGoverningCache()
	}
	names := map[string]bool{}
	for _, members := range a.repository.Groups {
		for _, name := range members {
			names[name] = true
		}
	}
	for _, agent := range a.repository.Agents {
		names[agent.Identity] = true
		for _, name := range agent.Accountable {
			names[name] = true
		}
	}
	ordered := []string{}
	for name := range names {
		ordered = append(ordered, name)
	}
	slices.Sort(ordered)
	resolved := []cp.ResolvedIdentity{}
	identities := map[string]cp.Identity{}
	for _, name := range ordered {
		r := cp.ResolvedIdentity{Name: name}
		if strings.Contains(name, "/") {
			team, err := ids.ResolveTeam(ctx, name)
			if err != nil {
				return err
			}
			if team.ID <= 0 || team.OrganizationID <= 0 {
				return forge.ErrIdentityUnavailable
			}
			// Membership is not part of the policy pin, but a complete read proves
			// the referenced team remains a usable identity. An unavailable member
			// source is definite; incomplete/transient reads abort without a durable
			// fallback and retry at this exact governing commit.
			if _, err := ids.TeamMembers(ctx, team); err != nil {
				return err
			}
			r.ID = teamPolicyID(team)
		} else {
			u, err := ids.ResolveUser(ctx, name)
			if err != nil {
				return err
			}
			p, err := policyIdentity(u)
			if err != nil {
				return err
			}
			identities[name] = p
			r.ID, r.Kind = p.ID, p.Kind
		}
		resolved = append(resolved, r)
	}
	if err := a.repository.ValidateIdentities(resolved); err != nil {
		return err
	}
	// Policy identity covers the governing tree, independent of the open-change
	// set. Per-revision classification still reads only its pinned path evidence.
	governingReader, ok := w.Git.(forge.GoverningFilesReader)
	if !ok {
		return errors.New("missing governing files reader")
	}
	g, err := a.governing.read(ctx, governingReader, w.repo(), forge.GoverningFilesRequest{CommitSHA: tip, PathsComplete: true, AllAttributes: true})
	if err != nil {
		return err
	}
	if g.Coverage.Completeness != forge.DiffComplete {
		return errors.New("governing policy content unavailable")
	}
	content := func(f forge.GoverningFile) cp.GoverningContent {
		return cp.GoverningContent{Path: f.Path, Known: f.Coverage.Completeness == forge.DiffComplete, Present: f.Present, Content: f.Content}
	}
	owners := cp.GoverningContent{Path: ".github/CODEOWNERS", Known: true}
	for _, f := range g.Codeowners {
		if f.Path == g.SelectedCodeowners {
			owners = content(f)
		}
	}
	attrs := []cp.GoverningContent{}
	for _, f := range g.Attributes {
		if f.Present || f.Path == ".gitattributes" {
			attrs = append(attrs, content(f))
		}
	}
	// Static owner identities belong in the policy pin; membership does not.
	// Every configured or governing owner is resolved before the policy is
	// usable. The caller distinguishes a proven unavailable identity from a
	// transient forge read; neither may be represented by an unresolved pin.
	pins := map[string]cp.ResolvedIdentity{}
	for _, r := range resolved {
		pins[r.Name] = r
	}
	tokens, _ := a.governing.owners(g)
	for _, owner := range tokens {
		if owner.Kind == "email" {
			continue
		}
		if _, exists := pins[owner.Name]; exists {
			continue
		}
		r := cp.ResolvedIdentity{Name: owner.Name}
		if owner.Kind == "team" {
			team, err := ids.ResolveTeam(ctx, owner.Name)
			if err != nil {
				return err
			}
			if team.ID <= 0 || team.OrganizationID <= 0 {
				return forge.ErrIdentityUnavailable
			}
			if _, err := ids.TeamMembers(ctx, team); err != nil {
				return err
			}
			r.ID = teamPolicyID(team)
		} else {
			user, err := ids.ResolveUser(ctx, owner.Name)
			if err != nil {
				return err
			}
			p, err := policyIdentity(user)
			if err != nil {
				return err
			}
			r.ID, r.Kind = p.ID, p.Kind
		}
		pins[owner.Name] = r
		resolved = append(resolved, r)
	}
	digest, err := cp.PolicyDigest(cp.DigestInputs{Repository: a.repository, Queue: a.candidate, Resolved: resolved, Owners: owners, Attributes: attrs, ClassifierVersion: classification.Version})
	if err != nil {
		return err
	}
	mappings := map[string][]cp.Identity{}
	for _, agent := range a.repository.Agents {
		for _, human := range agent.Accountable {
			mappings[identities[agent.Identity].ID] = append(mappings[identities[agent.Identity].ID], identities[human])
		}
	}
	if digest != a.policy.Digest {
		for name, expression := range map[string]condition.Expression{"enter_when": w.cfg.EnterWhen, "merge_when": w.cfg.MergeWhen} {
			if slices.Contains(expression.Attributes(), reviewAttribute) {
				s.options.Logger.Warn("candidate approval configuration", "path", name, "diagnostic", cp.ReviewTermsDiagnostic)
			}
		}
	}
	a.policy, a.mappings, a.identities = cp.Policy{Digest: digest, Approvals: a.candidate.Approvals}, mappings, pins
	// Test-only effective pin. 11b defines the public ConfigApplied composition.
	w.configDigest = fmt.Sprintf("%x", sha256.Sum256([]byte("openmerge-11a-shadow\x00"+w.cfg.Digest(!s.options.DisableDeleteHeadBranch)+"\x00"+digest)))
	return nil
}

func approvalBasis(r queue.RevisionRecord) queue.ApprovalBasis {
	now := time.Now().UTC()
	return queue.ApprovalBasis{ChangeID: r.ChangeID, Revision: r.Revision, Head: r.HeadSHA, At: now, ReadID: strconv.FormatInt(now.UnixNano(), 10)}
}

// recordStageA dedupes semantic observations while validating against the latest
// locked state. Reject the candidate on ANY encoding/admission failure; never
// delete another change named by a queue-wide budget diagnostic.
func (s *Server) recordStageA(ctx context.Context, w *worker, key string, value any, events ...queue.Event) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.store.RecordEvidence(ctx, w.id, key, raw, false, func(state queue.State) error { return preflightStageA(state, events) }, events...)
}

func preflightStageA(state queue.State, events []queue.Event) error {
	for _, event := range events {
		var err error
		switch e := event.(type) {
		case queue.BaseMoved:
			// The emitter checks the ancestry observation before the locked write.
		case queue.ChangeRevisionObserved:
			if !state.CanObserveChangeRevision(e) {
				return errors.New("stale revision observation")
			}
		case queue.ChangeClassified:
			i := e.Evidence.Classification.Inputs
			err = state.CheckApprovalBoundary(queue.ApprovalBasis{ChangeID: queue.ChangeID(i.ChangeID), Revision: queue.ChangeRevision(i.Revision), Head: i.Head, ReadID: "classification-preflight", At: time.Now().UTC()})
			if err == nil {
				err = state.CheckClassificationAdmission(e)
			}
		case queue.ApprovalEligibilityObserved:
			err = state.CheckApprovalAdmission(e)
		case queue.ApprovalInputsChanged:
			err = state.CheckApprovalAdmission(e.Eligibility)
		case queue.ApprovalGateRejected:
			err = e.Validate()
		default:
			err = state.CheckApprovalAdmission(e)
		}
		if err != nil {
			return err
		}
		if _, _, err := queue.EncodeEvent(event); err != nil {
			return err
		}
		state = queue.Apply(state, event)
	}
	_, err := queue.EncodeSnapshot(state)
	return err
}

func (s *Server) observeStageA(ctx context.Context, w *worker, pr forge.PullRequest, refreshClassification bool, observedAt time.Time) error {
	ids, reviews, err := w.stageReaders()
	if err != nil {
		return err
	}
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	id, err := w.Key.ChangeID(pr.Number)
	if err != nil {
		return err
	}
	r, _ := state.PRRevision(pr.Number)
	if r.HeadSHA != pr.HeadSHA || r.ChangeID == "" || r.Basis.At.IsZero() {
		revision := r.Revision + 1
		if r.HeadSHA == pr.HeadSHA {
			revision = r.Revision
		}
		if revision == 0 {
			revision = 1
		}
		e := queue.ChangeRevisionObserved{PR: pr.Number, ChangeID: id, Revision: revision, Predecessor: revision - 1, HeadSHA: pr.HeadSHA, Basis: queue.RevisionObservationBasis{BaseSHA: state.BaseSHA, At: observedAt}}
		if err := s.recordStageA(ctx, w, fmt.Sprintf("stage-a/revision/%d", pr.Number), e, e); err != nil {
			return err
		}
		state, _, err = s.store.Load(ctx, w.id)
		if err != nil {
			return err
		}
		r, _ = state.PRRevision(pr.Number)
	}
	acquisition, err := s.store.BeginApprovalRead(ctx, w.id, []queue.ChangeID{r.ChangeID})
	if err != nil {
		return err
	}
	ctx = acquisition.Context(ctx)
	b := approvalBasis(r)
	b.At = observedAt
	b.ReadID = strconv.FormatInt(observedAt.UnixNano(), 10)
	author, err := ids.ResolveUser(ctx, pr.Author)
	if err != nil {
		return err
	}
	if author.ID != pr.AuthorID {
		return forge.ErrIdentityMismatch
	}
	identity, err := policyIdentity(author)
	if err != nil {
		return err
	}
	account := queue.Accountability(b, identity, identity, w.stageA.mappings, w.stageA.policy.Digest)
	intent := queue.Intent(b, pr.Title, pr.Body)
	value := struct {
		Revision            queue.ChangeRevision
		Account             cp.Identity
		Humans              []cp.Identity
		Policy, Title, Body string
	}{r.Revision, account.Author, account.Humans, account.PolicyDigest, intent.Title, intent.Body}
	if err := s.recordStageA(ctx, w, fmt.Sprintf("stage-a/intent/%d", pr.Number), value, account, intent); err != nil {
		return err
	}
	state, _, err = s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	if refreshClassification {
		if err := s.classifyStageA(ctx, w, state, r); err != nil {
			return err
		}
	}
	native, err := reviews.NativeReviews(ctx, w.repo(), pr.Number)
	if err != nil {
		return err
	}
	return s.observeStageANative(ctx, w, pr, b, native)
}

// observeStageANative folds a single immutable native-review response. Callers
// that already acquired the response (for example a landing gate) reuse it so
// noise filtering, stance journaling and eligibility observe the same view.
func (s *Server) observeStageANative(ctx context.Context, w *worker, pr forge.PullRequest, b queue.ApprovalBasis, native forge.NativeReviews) error {
	ids, _, err := w.stageReaders()
	if err != nil {
		return err
	}
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	grantFor := stageAGrantPass(ctx, ids, w.repo())
	var fresh *queue.ApprovalEligibilityObserved
	relevant := func(state queue.State, e queue.ReviewStanceObserved, grant forge.WriteGrant) (bool, error) {
		if !pr.HeadInBaseRepo && e.Stance.Kind == cp.Approve {
			return true, nil
		}
		if fresh == nil {
			read, err := s.readStageAInputsWithGrants(ctx, w, state, b, native, grantFor, nil)
			if err != nil {
				return false, err
			}
			fresh = &read
		}
		return s.stageAActorRelevantWithInputs(state, w, b, e.Reviewer, grant, *fresh)
	}
	// Reject noise BEFORE durable attribution. A currently unauthorized account
	// cannot consume permanent command/request source pins. Retained sources are
	// still read at every gate so revocation never lends an old grant.
	for _, review := range native.Reviews {
		e, ok := queue.NativeStance(b, review)
		if !ok || e.Stance.Kind == cp.Approve && review.CommitID != b.Head {
			continue
		}
		facts, _ := state.ApprovalFacts(b.ChangeID)
		retainedNegative := e.Stance.Kind != cp.Approve && hasRetainedNativeActor(facts, e.Reviewer.ID)
		if !retainedNegative {
			grant, err := grantFor(review.Author)
			if err != nil {
				if errors.Is(err, forge.ErrIdentityUnavailable) || errors.Is(err, forge.ErrIdentityMismatch) {
					continue
				}
				return err
			}
			if !grant.CanWrite(review.Author.ID) {
				continue
			}
			allowed, err := relevant(state, e, grant)
			if err != nil {
				return err
			}
			if !allowed {
				continue
			}
		}
		// A dismissal fallback is pinned at first observation, including its
		// uncertainty bound. A later poll cannot renew it or create duplicate facts.
		sourceKey := stageASourceKey(e)
		if first, ok := facts.Sources[sourceKey]; ok {
			e = first.Event
			e.Basis = b
			e.Stance.Revision = uint64(b.Revision)
		}
		v := e
		v.Observation = forge.NativeObservation{}
		v.Basis.ReadID = ""
		v.Basis.At = time.Time{}
		if err := s.recordStageA(ctx, w, fmt.Sprintf("stage-a/review/%d/%d/%s", pr.Number, review.ID, review.State), v, e); err != nil {
			return err
		}
		state, _, err = s.store.Load(ctx, w.id)
		if err != nil {
			return err
		}
	}
	// Persist the complete eligibility view even when no new stance survived
	// admission: absence, revoked grants and membership changes are decision inputs.
	freshInputs, err := s.readStageAInputsWithGrants(ctx, w, state, b, native, grantFor, fresh)
	if err != nil {
		return err
	}
	value := freshInputs
	value.Basis.ReadID = ""
	value.Basis.At = time.Time{}
	return s.recordStageA(ctx, w, "stage-a/eligibility/"+string(b.ChangeID), value, freshInputs)
}

// stageAGrantPass memoizes by immutable user ID for one acquisition only. A new
// poll always creates a new pass, including after denied or failed reads.
func stageAGrantPass(ctx context.Context, ids forge.IdentityReader, repo forge.RepoRef) func(forge.UserIdentity) (forge.WriteGrant, error) {
	type result struct {
		grant forge.WriteGrant
		err   error
	}
	results := map[int64]result{}
	return func(user forge.UserIdentity) (forge.WriteGrant, error) {
		if r, ok := results[user.ID]; ok {
			return r.grant, r.err
		}
		grant, err := ids.WriteGrant(ctx, repo, user)
		results[user.ID] = result{grant, err}
		return grant, err
	}
}

func stageASourceKey(e queue.ReviewStanceObserved) string {
	source := e.Stance.Source
	if source.DismissedReviewID != "" {
		return string(source.Channel) + "/dismissal/" + source.DismissedReviewID
	}
	return string(source.Channel) + "/" + source.ID
}

func hasRetainedNativeActor(facts queue.ApprovalFacts, actorID string) bool {
	for _, source := range facts.Sources {
		if source.Event.Stance.Source.Channel == cp.NativeReview && source.Event.Reviewer.ID == actorID {
			return true
		}
	}
	return false
}

func (s *Server) classifyStageA(ctx context.Context, w *worker, state queue.State, r queue.RevisionRecord) error {
	e, err := s.prepareStageAClassification(ctx, w, state, r, w.configTip, false)
	if err != nil || e == nil {
		return err
	}
	return s.recordStageA(ctx, w, "stage-a/classification/"+string(r.ChangeID), *e, *e)
}

func (s *Server) prepareStageAClassification(ctx context.Context, w *worker, state queue.State, r queue.RevisionRecord, governingTip string, rewrite bool) (*queue.ChangeClassified, error) {
	previous, exists := state.Classification(r.ChangeID, r.Revision)
	base := r.Basis.BaseSHA
	generation := uint64(1)
	if exists {
		old := previous.Evidence.Classification.Inputs
		base, generation = old.Base, old.Generation
		forward := old.Base == state.BaseSHA
		if !forward {
			var err error
			forward, err = w.Git.IsAncestor(ctx, w.repo(), base, state.BaseSHA)
			if err != nil {
				return nil, err
			}
		}
		// A recorded revision is immutable. With the same pinned base, head,
		// governing commit and semantic policy, neither the revision diff nor
		// governing probes can affect its durable classification. Owner pools are
		// deliberately refreshed later at the eligibility boundary.
		if !rewrite && forward && stageAClassificationComplete(previous.Evidence) && old.Head == r.HeadSHA && old.GoverningCommit == governingTip && old.PolicyDigest == w.stageA.policy.Digest && old.ClassifierVersion == classification.Version {
			return nil, nil
		}
		if !forward || rewrite {
			base = state.BaseSHA
		}
	}
	diffReader, ok := w.Git.(forge.RevisionDiffReader)
	if !ok {
		return nil, errors.New("missing diff capability")
	}
	d, err := diffReader.RevisionDiff(ctx, w.repo(), forge.RevisionDiffRequest{BaseSHA: base, HeadSHA: r.HeadSHA})
	if err != nil {
		return nil, err
	}
	paths := []string{}
	for _, f := range d.Files {
		if f.OldPath != "" {
			paths = append(paths, f.OldPath)
		}
		if f.NewPath != "" {
			paths = append(paths, f.NewPath)
		}
	}
	greader, ok := w.Git.(forge.GoverningFilesReader)
	if !ok {
		return nil, errors.New("missing governing capability")
	}
	if w.stageA.governing == nil {
		w.stageA.governing = newStageAGoverningCache()
	}
	g, err := w.stageA.governing.read(ctx, greader, w.repo(), forge.GoverningFilesRequest{CommitSHA: governingTip, Paths: paths, PathsComplete: d.Inventory.Completeness == forge.DiffComplete})
	if err != nil {
		return nil, err
	}
	ids, _, err := w.stageReaders()
	if err != nil {
		return nil, err
	}
	resolver := func(o classification.Owner) cp.IdentitySet {
		pool, _ := resolvePool(ctx, ids, o.Name, w.stageA.identities)
		return pool
	}
	settings := w.stageA.repository.Classification
	derive := func(generation uint64) (classification.Evidence, error) {
		return classification.Classify(cp.ClassificationInputs{ChangeID: string(r.ChangeID), Revision: uint64(r.Revision), Generation: generation, Base: base, Head: r.HeadSHA, MergeBase: d.MergeBaseSHA, GoverningCommit: governingTip, PolicyDigest: w.stageA.policy.Digest}, d, g, classification.Settings{Docs: settings.Docs, Generated: settings.Generated, Sensitive: settings.Sensitive}, w.stageA.governing.deriver(g, resolver))
	}
	evidence, err := derive(generation)
	if err != nil {
		return nil, err
	}
	if exists {
		reasons := classification.RefreshReasons(classification.SemanticInputs(previous.Evidence), classification.SemanticInputs(evidence), previous.Evidence.Classification.Inputs.Base != base || rewrite, false)
		if len(reasons) == 0 {
			// Preserve a new immutable governing provenance at the same semantic
			// generation. The projection explicitly permits this update and the
			// next unchanged poll can safely take the no-I/O fast path above.
			return &queue.ChangeClassified{Evidence: evidence}, nil
		}
		generation, err = classification.NextGeneration(generation, reasons)
		if err != nil {
			return nil, err
		}
		evidence, err = derive(generation)
		if err != nil {
			return nil, err
		}
	}
	e := queue.ChangeClassified{Evidence: evidence}
	return &e, nil
}

// stageAClassificationComplete rejects cached transport uncertainty. A retry at
// the same immutable commits can recover shallow history, missing blobs and
// bounded evidence reads, so only a fully acquired snapshot may skip I/O.
func stageAClassificationComplete(e classification.Evidence) bool {
	if e.Inventory.Completeness != forge.DiffComplete || e.Hunks.Completeness != forge.DiffComplete || e.Governing.Coverage.Completeness != forge.DiffComplete {
		return false
	}
	for _, files := range [][]forge.GoverningFile{e.Governing.Config, e.Governing.Codeowners, e.Governing.Attributes} {
		for _, file := range files {
			if file.Coverage.Completeness != forge.DiffComplete {
				return false
			}
		}
	}
	return true
}

func (s *Server) readStageAInputs(ctx context.Context, w *worker, state queue.State, b queue.ApprovalBasis) (queue.ApprovalEligibilityObserved, error) {
	_, nativeReader, err := w.stageReaders()
	if err != nil {
		return queue.ApprovalEligibilityObserved{}, err
	}
	var number int
	for pr, r := range state.ChangeRevisions() {
		if r.ChangeID == b.ChangeID {
			number = pr
			break
		}
	}
	native, err := nativeReader.NativeReviews(ctx, w.repo(), number)
	if err != nil {
		return queue.ApprovalEligibilityObserved{}, err
	}
	return s.readStageAInputsFromNative(ctx, w, state, b, native)
}

// readStageAInputsFromNative derives fresh decision inputs from one already
// acquired native response. It deliberately does not retain membership or grant
// results beyond the returned observation.
func (s *Server) readStageAInputsFromNative(ctx context.Context, w *worker, state queue.State, b queue.ApprovalBasis, native forge.NativeReviews) (queue.ApprovalEligibilityObserved, error) {
	if err := state.CheckApprovalBoundary(b); err != nil {
		return queue.ApprovalEligibilityObserved{}, err
	}
	ids, _, err := w.stageReaders()
	if err != nil {
		return queue.ApprovalEligibilityObserved{}, err
	}
	return s.readStageAInputsWithGrants(ctx, w, state, b, native, stageAGrantPass(ctx, ids, w.repo()), nil)
}

func (s *Server) readStageAInputsWithGrants(ctx context.Context, w *worker, state queue.State, b queue.ApprovalBasis, native forge.NativeReviews, grantFor func(forge.UserIdentity) (forge.WriteGrant, error), pools *queue.ApprovalEligibilityObserved) (queue.ApprovalEligibilityObserved, error) {
	ids, _, err := w.stageReaders()
	if err != nil {
		return queue.ApprovalEligibilityObserved{}, err
	}
	e := queue.ApprovalEligibilityObserved{Basis: b, Actors: map[string]cp.Actor{}, Groups: map[string]cp.IdentitySet{}, OwnerPools: map[string]cp.IdentitySet{}, Known: true, Reasons: []string{}}
	users := map[string]forge.UserIdentity{}
	f, _ := state.ApprovalFacts(b.ChangeID)
	for _, source := range f.Sources {
		p := source.Event.Reviewer
		n, _ := strconv.ParseInt(strings.TrimPrefix(p.ID, "github.com:user:"), 10, 64)
		kind := forge.BotAccount
		if p.Kind == cp.HumanAccount {
			kind = forge.UserAccount
		}
		users[p.ID] = forge.UserIdentity{ID: n, Login: p.Login, Kind: kind}
	}
	// Refresh display logins from current source identities, retaining the stable
	// author ID as the grant subject. A renamed account can recover; a reassigned
	// login still fails WriteGrant. Stored sources remain immutable provenance.
	present := map[string]bool{}
	for _, review := range native.Reviews {
		id := classification.GitHubUserID(review.Author.ID)
		if _, relevant := users[id]; relevant {
			users[id] = review.Author
		}
		if review.State == forge.ReviewApproved && review.CommitID == b.Head {
			present[id+"/"+strconv.FormatInt(review.ID, 10)] = true
		}
	}
	for _, comment := range native.Comments {
		id := classification.GitHubUserID(comment.Author.ID)
		if _, relevant := users[id]; relevant {
			users[id] = comment.Author
		}
	}
	missing := map[string]bool{}
	for id, key := range f.Latest {
		source := f.Sources[key]
		if source.Revision == b.Revision && source.Event.Stance.Kind == cp.Approve && source.Event.Stance.Source.Channel == cp.NativeReview && !present[id+"/"+source.Event.Stance.Source.ID] {
			missing[id] = true
		}
	}
	addPool := func(name string) cp.IdentitySet {
		pool, observed := resolvePool(ctx, ids, name, w.stageA.identities)
		for _, u := range observed {
			users[classification.GitHubUserID(u.ID)] = u
		}
		return pool
	}
	for group, names := range w.stageA.repository.Groups {
		if pools != nil {
			e.Groups[group] = pools.Groups[group]
			continue
		}
		pool := cp.IdentitySet{IDs: []string{}, Resolved: true, Fresh: true}
		for _, name := range names {
			p := addPool(name)
			pool.IDs = append(pool.IDs, p.IDs...)
			if !p.Resolved || !p.Fresh {
				pool.Resolved, pool.Fresh, pool.Reason = false, false, p.Reason
			}
		}
		slices.Sort(pool.IDs)
		pool.IDs = slices.Compact(pool.IDs)
		e.Groups[group] = pool
	}
	classified, ok := state.Classification(b.ChangeID, b.Revision)
	if !ok {
		return e, errors.New("classification missing")
	}
	classification.ResolveOwnerEligibility(classified.Evidence, func(o classification.Owner) cp.IdentitySet {
		if pool, ok := e.OwnerPools[o.Label()]; ok {
			return pool
		}
		if pools != nil {
			pool := pools.OwnerPools[o.Label()]
			e.OwnerPools[o.Label()] = pool
			return pool
		}
		pool := addPool(o.Name)
		e.OwnerPools[o.Label()] = pool
		return pool
	})
	if pools != nil {
		for id, actor := range pools.Actors {
			if _, exists := users[id]; exists {
				continue
			}
			n, _ := strconv.ParseInt(strings.TrimPrefix(id, "github.com:user:"), 10, 64)
			kind := forge.BotAccount
			if actor.Identity.Kind == cp.HumanAccount {
				kind = forge.UserAccount
			}
			users[id] = forge.UserIdentity{ID: n, Login: actor.Identity.Login, Kind: kind}
		}
	}
	for id, u := range users {
		p, err := policyIdentity(u)
		if err != nil {
			return e, err
		}
		if _, mapped := w.stageA.mappings[id]; mapped {
			p.Kind = cp.AgentAccount
		}
		g := cp.WriteGrant{SubjectID: id, Write: cp.Unknown}
		grant, err := grantFor(u)
		if err == nil && grant.User.ID == u.ID && !missing[id] {
			g.ReturnedID, g.Fresh, g.Write = id, true, cp.False
			if grant.CanWrite(u.ID) {
				g.Write = cp.True
			}
		}
		e.Actors[id] = cp.Actor{Identity: p, Grant: g}
	}
	return e, e.Validate()
}

func stageAReviewEvidence(state queue.State, policy cp.Policy, fresh queue.ApprovalEligibilityObserved) (*ApprovalEvidence, error) {
	fact, err := state.RecordedBrief(policy, fresh.Basis)
	if err != nil {
		return nil, err
	}
	current, err := brief.Identity(fact)
	if err != nil {
		return nil, err
	}
	evaluation := state.EvaluateApprovals(policy, current, fresh)
	return &ApprovalEvidence{Known: evaluation.ReviewsKnown, Reviewers: evaluation.ReviewLogins}, nil
}

// observeStageABase commits rewrite classifications WITH the base observation.
// A crash cannot leave the new base durable with old receipts still eligible.
// Started attempts retain frozen decisions and settle before this path runs.
func (s *Server) observeStageABase(ctx context.Context, w *worker, state queue.State, tip, governingTip string) error {
	forward := state.BaseSHA == ""
	var err error
	if !forward {
		forward, err = w.Git.IsAncestor(ctx, w.repo(), state.BaseSHA, tip)
		if err != nil {
			return err
		}
	}
	events := []queue.Event{}
	if !forward {
		next := queue.Apply(state, queue.BaseMoved{SHA: tip})
		for _, r := range state.ChangeRevisions() {
			if _, ok := state.Classification(r.ChangeID, r.Revision); !ok {
				continue
			}
			e, err := s.prepareStageAClassification(ctx, w, next, r, governingTip, true)
			if err != nil {
				return err
			}
			if e != nil {
				events = append(events, *e)
			}
		}
	}
	events = append(events, queue.BaseMoved{SHA: tip})
	data, _ := json.Marshal(tip)
	return s.store.RecordEvidence(ctx, w.id, "stage-a/base", data, false, func(current queue.State) error {
		if current.BaseSHA != state.BaseSHA {
			return fmt.Errorf("base changed during observation")
		}
		return preflightStageA(current, events)
	}, events...)
}

func (s *Server) stageACheckSummary(ctx context.Context, w *worker, pr int) (string, error) {
	if w.ledgerCache == nil {
		if _, err := s.ledgerSummary(ctx, w, pr); err != nil {
			return "", err
		}
	}
	if w.ledgerCache == nil {
		return "", nil
	}
	text, err := status.ReviewerCheckSummaryFromProjection(w.ledgerCache.journal, &w.ledgerCache.projection, pr, w.stageA.policy)
	if strings.Contains(text, "policy\\_unavailable") {
		s.options.Logger.Warn("check approval policy unavailable", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "pr", pr)
	}
	return text, err
}

func (s *Server) stageAActorRelevantWithInputs(state queue.State, w *worker, b queue.ApprovalBasis, actor cp.Identity, grant forge.WriteGrant, fresh queue.ApprovalEligibilityObserved) (bool, error) {
	if _, mapped := w.stageA.mappings[actor.ID]; mapped {
		actor.Kind = cp.AgentAccount
	}
	fresh.Actors = maps.Clone(fresh.Actors)
	fresh.Actors[actor.ID] = cp.Actor{Identity: actor, Grant: cp.WriteGrant{SubjectID: actor.ID, ReturnedID: actor.ID, Fresh: true, Write: cp.True}}
	if !grant.CanWrite(grant.User.ID) || classification.GitHubUserID(grant.User.ID) != actor.ID {
		return false, nil
	}
	fact, err := state.RecordedBrief(w.stageA.policy, b)
	if err != nil {
		return false, err
	}
	identity, err := brief.Identity(fact)
	if err != nil {
		return false, err
	}
	return state.ApprovalActorRelevant(w.stageA.policy, identity, fresh, actor.ID), nil
}

func teamPolicyID(team forge.TeamIdentity) string {
	return fmt.Sprintf("github.com:team:%d:%d", team.OrganizationID, team.ID)
}
