package queue

import (
	"fmt"
	"slices"
	"time"
)

// BuildChainedTestRef admits one member of the fixed window. Construction still
// uses BuildTestRef's pinned inputs and its unique per-batch action key.
type BuildChainedTestRef struct {
	BuildTestRef
	ParentBatchID string `json:"parent_batch_id"`
	Position      uint64 `json:"position"`
}

func (BuildChainedTestRef) queueAction() {}

// Key pins construction to its parent and chain position.
func (a BuildChainedTestRef) Key() string {
	return a.BuildTestRef.Key() + fmt.Sprintf("/chain/%s/%d", a.ParentBatchID, a.Position)
}

// Validate requires pinned build inputs and a consistent predecessor.
func (a BuildChainedTestRef) Validate() error {
	if err := a.BuildTestRef.Validate(); err != nil {
		return err
	}
	return validateChainPosition(a.ParentBatchID, a.Position)
}

// ChainedBatchStarted is the durable admission before build I/O. The commits
// cannot be known until construction; ChainedTestRefBuilt pins both on success.
type ChainedBatchStarted struct {
	BatchID       string    `json:"batch_id"`
	BaseSHA       string    `json:"base_sha"`
	PRs           []BatchPR `json:"prs"`
	At            time.Time `json:"at"`
	ParentBatchID string    `json:"parent_batch_id"`
	Position      uint64    `json:"position"`
}

func (ChainedBatchStarted) queueEvent() {}

// Validate requires a durable admission and its chain identity.
func (e ChainedBatchStarted) Validate() error {
	if err := (BatchStarted{BatchID: e.BatchID, BaseSHA: e.BaseSHA, PRs: e.PRs, At: e.At}).Validate(); err != nil {
		return err
	}
	return validateChainPosition(e.ParentBatchID, e.Position)
}
func validateChainPosition(parent string, position uint64) error {
	if position == 0 || (position == 1) != (parent == "") {
		return fmt.Errorf("chain position requires its predecessor, except at the root")
	}
	return nil
}

// ChainedTestRefBuilt binds the tested commit and the commit to be leased onto
// the base to the admitted chain position. Fast-forward pins marker and head.
type ChainedTestRefBuilt struct {
	BatchID       string      `json:"batch_id"`
	TestedSHA     string      `json:"tested_sha"`
	LandingSHA    string      `json:"landing_sha"`
	ParentBatchID string      `json:"parent_batch_id"`
	Position      uint64      `json:"position"`
	Method        MergeMethod `json:"method"`
	At            time.Time   `json:"at"`
}

func (ChainedTestRefBuilt) queueEvent() {}

// Validate requires both commits and the actual preparation method.
func (e ChainedTestRefBuilt) Validate() error {
	if err := requireActionText("batch_id", e.BatchID, "tested_sha", e.TestedSHA, "landing_sha", e.LandingSHA); err != nil {
		return err
	}
	if e.At.IsZero() || (e.Method != Merge && e.Method != Squash && e.Method != Rebase && e.Method != FastForward) {
		return fmt.Errorf("invalid chain build method or time")
	}
	if (e.Method == FastForward) == (e.LandingSHA == e.TestedSHA) {
		return fmt.Errorf("invalid tested/landing commit pair")
	}
	return validateChainPosition(e.ParentBatchID, e.Position)
}

// InvalidateBatches makes discarding a suffix explicit and auditable. Its exact
// IDs protect rebuilt batches from delayed observations or action completion.
type InvalidateBatches struct {
	BatchIDs []string `json:"batch_ids"`
	Reason   string   `json:"reason"`
}

func (InvalidateBatches) queueAction() {}

// Key identifies the exact discarded suffix and its cause.
func (a InvalidateBatches) Key() string {
	return fmt.Sprintf("chain/invalidate/%s/%v", a.Reason, a.BatchIDs)
}

// Validate requires bounded, distinct batch identities and a reason.
func (a InvalidateBatches) Validate() error {
	if len(a.BatchIDs) == 0 || len(a.BatchIDs) > 32 {
		return fmt.Errorf("invalidation requires 1 to 32 batches")
	}
	seen := map[string]bool{}
	for _, id := range a.BatchIDs {
		if id == "" || seen[id] {
			return fmt.Errorf("invalid batch IDs")
		}
		seen[id] = true
	}
	return requireActionText("reason", a.Reason)
}

// BatchesInvalidated records which batches were discarded, and why. The state
// retains only a bounded recent set for status; the event log owns history.
type BatchesInvalidated InvalidateBatches

func (BatchesInvalidated) queueEvent() {}

// Validate applies the same bounds as the invalidation action.
func (e BatchesInvalidated) Validate() error { return InvalidateBatches(e).Validate() }

// Batches returns admitted, unlanded batches in landing order. Callers must not
// mutate the projection. Active remains the oldest for legacy integrations.
func (s State) Batches() []*Batch {
	if s.Active == nil {
		return nil
	}
	return append([]*Batch{s.Active}, s.Following...)
}

// Batch finds an admitted batch, including a suffix awaiting invalidation.
func (s State) Batch(id string) *Batch {
	for _, b := range s.Batches() {
		if b.ID == id {
			return b
		}
	}
	return nil
}

// BatchForPR finds a change's current batch in the chain.
func (s State) BatchForPR(pr int) *Batch {
	for _, b := range s.Batches() {
		for _, p := range b.PRs {
			if p.PR == pr {
				return b
			}
		}
	}
	return nil
}
func (s State) batchForSHA(sha string) *Batch {
	for _, b := range s.Batches() {
		if b.TestedSHA == sha {
			return b
		}
	}
	return nil
}

// LandingCommit is the exact tested tip, or the verified head below an FF marker.
func (b Batch) LandingCommit() string {
	if b.LandingSHA != "" {
		return b.LandingSHA
	}
	if b.Method == FastForward && len(b.PRs) > 0 {
		return b.PRs[len(b.PRs)-1].HeadSHA
	}
	return b.TestedSHA
}
func cloneBatch(b *Batch) *Batch {
	if b == nil {
		return nil
	}
	c := *b
	c.PRs = slices.Clone(b.PRs)
	c.ObservedRuns = slices.Clone(b.ObservedRuns)
	c.LastObservedRuns = slices.Clone(b.LastObservedRuns)
	c.unboundChecks = slices.Clone(b.unboundChecks)
	if b.CheckRetry != nil {
		r := *b.CheckRetry
		r.Dispatches = slices.Clone(r.Dispatches)
		r.FailedRuns = slices.Clone(r.FailedRuns)
		r.ObservedRuns = slices.Clone(r.ObservedRuns)
		c.CheckRetry = &r
	}
	return &c
}

// MaxDepth gives the fixed admission window; zero is the legacy default of one.
func (cfg Config) MaxDepth() int {
	if cfg.SpeculationMaxDepth == 0 {
		return 1
	}
	return cfg.SpeculationMaxDepth
}

// RetryLimit is shared by planner and executor for speculative queues.
func (cfg Config) RetryLimit() int {
	if cfg.MaxDepth() > 1 {
		return 1
	}
	return cfg.ChecksRetryFailed
}

func (s *State) startChainedBatch(e ChainedBatchStarted) {
	if e.Validate() != nil {
		return
	}
	if b := s.Batch(e.BatchID); b != nil {
		return
	} // repeated pre-build emission
	if e.BatchID != s.nextBatchID() || !s.runnablePRs(e.PRs) {
		return
	}
	for _, pr := range e.PRs {
		if s.inBatch(pr.PR) {
			return
		}
	}
	batches := s.Batches()
	if len(batches) == 0 {
		if e.ParentBatchID != "" || e.Position != 1 || e.BaseSHA != s.BaseSHA {
			return
		}
	} else {
		parent := batches[len(batches)-1]
		if parent.InvalidationReason != "" || parent.ProvisionalFailure != "" || parent.LandingCommit() == "" || e.ParentBatchID != parent.ID || e.Position != parent.ChainPosition+1 || e.BaseSHA != parent.LandingCommit() {
			return
		}
	}
	fromBisection := false
	if len(s.pending) > 0 {
		if !slices.Equal(s.pending[0].prs, e.PRs) {
			return
		}
		fromBisection = s.pending[0].bisection
		s.pending = s.pending[1:]
	}
	b := &Batch{Speculative: true, ID: e.BatchID, BaseSHA: e.BaseSHA, ConfigDigest: s.ConfigDigest, PRs: slices.Clone(e.PRs), FromBisection: fromBisection, TestRef: "openmerge/queue/" + s.BaseBranch + "/" + e.BatchID, Phase: Building, ParentBatchID: e.ParentBatchID, ChainPosition: e.Position}
	s.nextBatch++
	if s.Active == nil {
		s.Active = b
	} else {
		s.Following = append(s.Following, b)
	}
}
func (s *State) applyChainedBuilt(e ChainedTestRefBuilt) {
	b := s.Batch(e.BatchID)
	if e.Validate() != nil {
		return
	}
	if b == nil {
		s.cleanupLateBuild(e.BatchID)
		return
	}
	if !b.Speculative || b.ParentBatchID != e.ParentBatchID || b.ChainPosition != e.Position {
		return
	}
	if e.Method == FastForward && e.LandingSHA != b.PRs[len(b.PRs)-1].HeadSHA {
		return
	}
	s.applyBuilt(TestRefBuilt{BatchID: e.BatchID, TestedSHA: e.TestedSHA, At: e.At, Method: e.Method})
}

// invalidateFrom is also the hook for a future approval/landing gate rejection.
// Once LandingStarted is durable that batch always settles through its fence.
func (s *State) invalidateFrom(first *Batch, reason string) {
	found := false
	for _, b := range s.Batches() {
		found = found || b.ID == first.ID
		if found && b.Phase != Landing && b.InvalidationReason == "" {
			b.InvalidationReason = reason
		}
	}
}
func (s State) pendingInvalidation() (InvalidateBatches, bool) {
	var a InvalidateBatches
	for _, b := range s.Batches() {
		if b.InvalidationReason != "" {
			if a.Reason == "" {
				a.Reason = b.InvalidationReason
			}
			if b.InvalidationReason != a.Reason {
				break
			}
			a.BatchIDs = append(a.BatchIDs, b.ID)
		}
	}
	return a, len(a.BatchIDs) > 0
}
func (s *State) applyInvalidation(e BatchesInvalidated) {
	if e.Validate() != nil {
		return
	}
	// Revalidate each named batch; a late event cannot discard rebuilt work.
	for _, id := range e.BatchIDs {
		b := s.Batch(id)
		if b == nil || b.Phase == Landing || b.InvalidationReason != e.Reason {
			return
		}
	}
	// Pending infrastructure retries must not jump ahead of earlier members
	// returned to Entries by this invalidation. Preserve those batch boundaries
	// alongside retries, then order all recovered work by original admission.
	preserveGroups := slices.ContainsFunc(s.pending, func(g pendingGroup) bool { return !g.bisection })
	var groups []pendingGroup
	for _, id := range e.BatchIDs {
		b := s.Batch(id)
		if (b.FromBisection || preserveGroups) && b.ConfigDigest == s.ConfigDigest && e.Reason != "config-changed" {
			prs := slices.DeleteFunc(slices.Clone(b.PRs), func(pr BatchPR) bool { return !s.runnablePRs([]BatchPR{pr}) })
			if len(prs) > 0 {
				groups = append(groups, pendingGroup{prs: prs, bisection: b.FromBisection})
			}
		}
		s.discardBatch(b)
	}
	s.pending = append(s.pending, groups...)
	// Recovered halves stay ahead of later halves, and behind newly split halves.
	slices.SortStableFunc(s.pending, func(a, b pendingGroup) int { return s.entryPosition(a.prs[0].PR) - s.entryPosition(b.prs[0].PR) })
	s.Invalidations = append(s.Invalidations, BatchesInvalidated{BatchIDs: slices.Clone(e.BatchIDs), Reason: e.Reason})
	if len(s.Invalidations) > 32 {
		s.Invalidations = slices.Clone(s.Invalidations[len(s.Invalidations)-32:])
	}
}
func (s State) entryPosition(pr int) int {
	for i, e := range s.Entries {
		if e.PR == pr {
			return i
		}
	}
	return len(s.Entries)
}

func planChainAdmission(cfg Config, s State, now time.Time) []Action {
	if cfg.ConfigDigest != s.ConfigDigest || s.PauseReason(cfg) != "" || len(s.Batches()) >= cfg.MaxDepth() {
		return nil
	}
	if s.buildRetry.failures > 0 && now.Sub(s.buildRetry.failedAt) < cfg.buildRetryDelay(s.buildRetry.failures) {
		return nil
	}
	base, parent, position := s.BaseSHA, "", uint64(1)
	for _, b := range s.Batches() {
		if !b.Speculative || b.InvalidationReason != "" || b.ProvisionalFailure != "" || b.LandingCommit() == "" || b.CheckRetry != nil && b.CheckRetry.RerunState == "failed" {
			return nil
		}
		base, parent, position = b.LandingCommit(), b.ID, b.ChainPosition+1
	}
	prs, _ := s.batchCandidates(cfg, now)
	if len(prs) == 0 {
		return nil
	}
	id := s.nextBatchID()
	return []Action{BuildChainedTestRef{BuildTestRef: BuildTestRef{BatchID: id, BaseSHA: base, PRs: prs, Ref: "openmerge/queue/" + s.BaseBranch + "/" + id, Method: cfg.MergeMethod}, ParentBatchID: parent, Position: position}}
}

// DiagnoseBatch promotes an already recorded provisional build failure
// only when its predecessors have landed and settled.
type DiagnoseBatch struct {
	BatchID string        `json:"batch_id"`
	Reason  DequeueReason `json:"reason"`
}

func (DiagnoseBatch) queueAction() {}

// Key pins diagnosis to the oldest batch and recorded failure.
func (a DiagnoseBatch) Key() string { return "batch/" + a.BatchID + "/diagnose/" + string(a.Reason) }

// Validate limits diagnosis to build conflicts and CI failures.
func (a DiagnoseBatch) Validate() error {
	switch a.Reason {
	case ChecksFailed, ChecksTimeout, Conflict, FastForwardRequired:
	default:
		return fmt.Errorf("invalid diagnosis")
	}
	return requireActionText("batch_id", a.BatchID)
}

// BatchDiagnosed records the decision after a provisional failure reaches head.
type BatchDiagnosed DiagnoseBatch

func (BatchDiagnosed) queueEvent() {}

// Validate requires the same subject and reason as the diagnosis action.
func (e BatchDiagnosed) Validate() error { return DiagnoseBatch(e).Validate() }

// ActionBatch selects the batch whose timer, checks and policy inputs an
// executor must use. Non-batch effects return nil.
func (s State) ActionBatch(action Action) *Batch {
	var id string
	switch a := action.(type) {
	case BuildChainedTestRef:
		id = a.BatchID
	case BuildTestRef:
		id = a.BatchID
	case RefreshChecks:
		id = a.BatchID
	case Land:
		id = a.BatchID
	case RecordCheckRerun:
		id = a.BatchID
	case RerunChecks:
		id = a.BatchID
	case ConfirmCheckFailure:
		id = a.BatchID
	case ResolveLanding:
		id = a.BatchID
	case FastForwardResolveLanding:
		id = a.BatchID
	}
	return s.Batch(id)
}

// CleanupInvalidatedRef retries cleanup after a discarded builder reports late.
// The revision distinguishes it from a DeleteRef that may already have finished
// before the delayed push; done action keys remain terminal in storage.
type CleanupInvalidatedRef struct {
	Ref      string `json:"ref"`
	Revision uint64 `json:"revision"`
}

func (CleanupInvalidatedRef) queueAction() {}

// Key identifies cleanup evidence observed after the original invalidation.
func (a CleanupInvalidatedRef) Key() string {
	return fmt.Sprintf("ref/%s/invalidated/%d/delete", a.Ref, a.Revision)
}

// Validate requires a test ref and a durable event revision.
func (a CleanupInvalidatedRef) Validate() error {
	if a.Revision == 0 {
		return fmt.Errorf("cleanup requires a revision")
	}
	return requireActionText("ref", a.Ref)
}

func (s *State) cleanupLateBuild(id string) {
	s.addEffect(CleanupInvalidatedRef{Ref: "openmerge/queue/" + s.BaseBranch + "/" + id, Revision: s.revision})
}
