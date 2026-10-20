package queue

import (
	"bytes"
	"fmt"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func waitQueue(t *testing.T, depth, size, n int) (State, Config, []ApprovalBasis) {
	t.Helper()
	now := approvalBasis().At
	cfg := Config{BatchMaxSize: size, SpeculationMaxDepth: depth, MergeMethod: Merge, CheckTimeout: time.Minute, RefreshInterval: time.Second}
	s := Apply(State{BaseBranch: "release"}, BaseMoved{SHA: strings.Repeat("a", 40)})
	var bases []ApprovalBasis
	for i := 1; i <= n; i++ {
		id, err := PRChangeID("7", "Owner/Repo", "release", "", i)
		if err != nil {
			t.Fatal(err)
		}
		b := ApprovalBasis{id, 1, fmt.Sprintf("%040x", i+100), fmt.Sprintf("read-%d", i), now}
		bases = append(bases, b)
		s = Apply(s, ChangeRevisionObserved{PR: i, ChangeID: id, Revision: 1, HeadSHA: b.Head, Basis: RevisionObservationBasis{BaseSHA: s.BaseSHA, At: now.Add(-time.Hour)}})
		s = Apply(s, PRObserved{PR: i, HeadSHA: b.Head, Eligible: true, Trusted: true})
		s = Apply(s, EnqueueRequested{PR: i, At: now.Add(time.Duration(i) * time.Second)})
		s = Apply(s, Intent(b, "title", "body"))
		s = Apply(s, eligibilityEvent(b))
	}
	return s, cfg, bases
}
func waitBuild(t *testing.T, s State, cfg Config, now time.Time) State {
	t.Helper()
	for _, a := range Plan(cfg, s, now) {
		switch a := a.(type) {
		case BuildChainedTestRef:
			s = Apply(s, ChainedBatchStarted{BatchID: a.BatchID, BaseSHA: a.BaseSHA, PRs: a.PRs, At: now, ParentBatchID: a.ParentBatchID, Position: a.Position})
			return Apply(s, ChainedTestRefBuilt{BatchID: a.BatchID, TestedSHA: "test-" + a.BatchID, LandingSHA: "test-" + a.BatchID, ParentBatchID: a.ParentBatchID, Position: a.Position, Method: Merge, At: now})
		case BuildTestRef:
			s = Apply(s, BatchStarted{BatchID: a.BatchID, BaseSHA: a.BaseSHA, PRs: a.PRs, At: now})
			return Apply(s, TestRefBuilt{BatchID: a.BatchID, TestedSHA: "test-" + a.BatchID, Method: Merge, At: now})
		}
	}
	t.Fatal("no build", Plan(cfg, s, now))
	return s
}
func rejectWait(t *testing.T, s State, batch *Batch, bases ...ApprovalBasis) State {
	t.Helper()
	e := ApprovalGateRejected{BatchID: batch.ID, TestedSHA: batch.TestedSHA, PRs: slices.Clone(batch.PRs)}
	for _, b := range bases {
		e.Waiting = append(e.Waiting, ApprovalWait{b, s.ApprovalFingerprint(b.ChangeID), []string{"approval-required"}})
	}
	before := s
	s = Apply(s, e)
	if s.Batch(e.BatchID).InvalidationReason != "approval-wait" {
		t.Fatal("rejection ignored")
	}
	if before.Batch(e.BatchID).InvalidationReason != "" {
		t.Fatal("mutated prior state")
	}
	return s
}
func invalidateWait(t *testing.T, s State) State {
	t.Helper()
	a, ok := s.pendingInvalidation()
	if !ok {
		t.Fatal("no invalidation")
	}
	return Apply(s, BatchesInvalidated(a))
}
func plannedWaitBuilds(cfg Config, s State, now time.Time) []BatchPR {
	for _, a := range Plan(cfg, s, now) {
		switch a := a.(type) {
		case BuildTestRef:
			return a.PRs
		case BuildChainedTestRef:
			return a.PRs
		}
	}
	return nil
}
func TestApprovalWaitNoChurnAndProgress(t *testing.T) {
	for _, depth := range []int{1, 4} {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			s, cfg, bases := waitQueue(t, depth, 2, 3)
			now := bases[0].At.Add(time.Hour)
			entries := slices.Clone(s.Entries)
			s = waitBuild(t, s, cfg, now)
			s = rejectWait(t, s, s.Active, bases[0])
			window := s.Window(cfg)
			outcomes := slices.Clone(s.outcomes)
			s = invalidateWait(t, s)
			if !reflect.DeepEqual(entries, s.Entries) || window != s.Window(cfg) || !reflect.DeepEqual(outcomes, s.outcomes) {
				t.Fatal("wait changed admission or defect accounting")
			}
			prs := plannedWaitBuilds(cfg, s, now)
			if len(prs) != 2 || prs[0].PR != 2 || prs[1].PR != 3 {
				t.Fatal("no progress behind wait", prs)
			}
			s = waitBuild(t, s, cfg, now)
			s = rejectWait(t, s, s.Active, bases[1], bases[2])
			s = invalidateWait(t, s)
			for i := 0; i < 5; i++ {
				s = Apply(s, RefreshRequested{})
				for _, b := range bases {
					s = Apply(s, Intent(b, "title", "body"))
					s = Apply(s, eligibilityEvent(b))
				}
				if prs := plannedWaitBuilds(cfg, s, now.Add(24*time.Hour)); len(prs) > 0 {
					t.Fatal("unchanged inputs rebuilt", prs)
				}
			}
			restored := restart(t, s)
			if !reflect.DeepEqual(s.approvalWaits, restored.approvalWaits) || !reflect.DeepEqual(Plan(cfg, s, now), Plan(cfg, restored, now)) {
				t.Fatal("restart changed wait")
			}
			s = Apply(s, Intent(bases[0], "edited", "body"))
			if prs := plannedWaitBuilds(cfg, s, now); len(prs) != 1 || prs[0].PR != 1 {
				t.Fatal("intent did not wake exactly member", prs)
			}
		})
	}
}
func TestApprovalWaitSuffixAndLateResults(t *testing.T) {
	s, cfg, bases := waitQueue(t, 4, 1, 5)
	now := bases[0].At.Add(time.Hour)
	for i := 0; i < 4; i++ {
		s = waitBuild(t, s, cfg, now)
	}
	before := s.Batches()
	first := *before[0]
	s = rejectWait(t, s, before[1], bases[1])
	a, _ := s.pendingInvalidation()
	if !slices.Equal(a.BatchIDs, []string{before[1].ID, before[2].ID, before[3].ID}) || !reflect.DeepEqual(first, *s.Active) {
		t.Fatal("wrong suffix", a)
	}
	if s.Window(cfg) != 4 || s.LastWindowFailure() != nil {
		t.Fatal("approval rejection halved W")
	}
	s = invalidateWait(t, s)
	if len(s.Batches()) != 1 || s.Active.ID != first.ID {
		t.Fatal("earlier composition lost")
	}
	for _, b := range before[1:] {
		s = Apply(s, ChainedTestRefBuilt{BatchID: b.ID, TestedSHA: b.TestedSHA, LandingSHA: b.TestedSHA, Method: Merge, ParentBatchID: b.ParentBatchID, Position: b.ChainPosition, At: now})
		s = Apply(s, ChecksObserved{BatchID: b.ID, TestedSHA: b.TestedSHA, Terminal: true, Success: true, Runs: []CheckRun{{ID: 1, Attempt: 1, Name: "ci", Source: "app:1", State: "success"}}, OriginalDeadline: now.Add(time.Minute), Timeout: time.Minute})
	}
	if len(s.Batches()) != 1 || s.Batch(before[1].ID) != nil {
		t.Fatal("late response revived discarded batch")
	}
	if prs := plannedWaitBuilds(cfg, s, now); len(prs) != 1 || prs[0].PR != 3 {
		t.Fatal("suffix did not recover in order", prs)
	}
}
func TestApprovalWaitPendingBarriersAndMaxWait(t *testing.T) {
	for _, bisection := range []bool{false, true} {
		t.Run(fmt.Sprint(bisection), func(t *testing.T) {
			s, cfg, bases := waitQueue(t, 1, 2, 4)
			now := bases[0].At.Add(time.Hour)
			s = waitBuild(t, s, cfg, now)
			s.Active.FromBisection = bisection
			s.pending = []pendingGroup{{prs: []BatchPR{{PR: 3, HeadSHA: bases[2].Head}}, bisection: true}, {prs: []BatchPR{{PR: 4, HeadSHA: bases[3].Head}}, bisection: true}}
			s = rejectWait(t, s, s.Active, bases[0])
			s = invalidateWait(t, s)
			if len(s.pending) != 3 || s.pending[0].prs[0].PR != 2 || s.pending[0].bisection != bisection {
				t.Fatal("lost recovered group barrier", s.pending)
			}
			// An all-waiting pending half retires; a real later half stays a barrier.
			s.pending = append([]pendingGroup{{prs: []BatchPR{{PR: 1, HeadSHA: bases[0].Head}}, bisection: true}}, s.pending...)
			prs, _ := s.batchCandidates(cfg, now)
			if len(prs) != 1 || prs[0].PR != 2 {
				t.Fatal("empty wait became barrier", prs)
			}
		})
	}
	s, cfg, bases := waitQueue(t, 1, 1, 2)
	now := bases[0].At.Add(time.Hour)
	s = waitBuild(t, s, cfg, now)
	s = rejectWait(t, s, s.Active, bases[0])
	s = invalidateWait(t, s)
	s.Entries[0].EnqueuedAt = now.Add(-24 * time.Hour)
	s.Entries[1].EnqueuedAt = now
	cfg.BatchMaxSize = 2
	cfg.BatchMaxWait = time.Minute
	prs, deadline := s.batchCandidates(cfg, now)
	if len(prs) != 0 || !deadline.Equal(now.Add(time.Minute)) {
		t.Fatal("max_wait used skipped age", prs, deadline)
	}
}
func TestApprovalWaitProbeAndWakeBasis(t *testing.T) {
	s, cfg, bases := waitQueue(t, 1, 1, 1)
	b := bases[0]
	now := b.At.Add(time.Hour)
	s = waitBuild(t, s, cfg, now)
	s = rejectWait(t, s, s.Active, b)
	s = invalidateWait(t, s)
	fresh := eligibilityEvent(b)
	fresh.Basis.ReadID = "poll-2"
	fresh.Basis.At = b.At.Add(time.Minute)
	if _, changed := s.PrepareApprovalInputChange(fresh); changed {
		t.Fatal("ordinary poll woke wait")
	}
	fresh.Known = false
	fresh.Reasons = []string{"eligibility-unknown"}
	event, changed := s.PrepareApprovalInputChange(fresh)
	if !changed {
		t.Fatal("permission change not detected")
	}
	stale := event
	stale.Eligibility.Basis.At = b.At.Add(-time.Second)
	stale.Basis = stale.Eligibility.Basis
	if next := Apply(s, stale); len(next.ApprovalWaits()) != 0 {
		t.Fatal("committed probe from slower clock did not wake wait")
	}
	next := Apply(s, event)
	if len(next.ApprovalWaits()) != 0 || len(plannedWaitBuilds(cfg, next, now)) != 1 {
		t.Fatal("current probe did not wake")
	}
	// A delayed event cannot clear a more recent rejection, even at same revision.
	next = waitBuild(t, next, cfg, now)
	next = rejectWait(t, next, next.Active, fresh.Basis)
	if got := Apply(next, event); len(got.ApprovalWaits()) != 1 {
		t.Fatal("old fingerprint cleared new wait")
	}
	for _, e := range []Event{native(b, 20, cp.Approve, b.At), ConfigApplied{Commit: "config", Digest: strings.Repeat("d", 64)}, Intent(b, "new", "body")} {
		if got := Apply(s, e); len(got.ApprovalWaits()) != 0 {
			t.Fatalf("%T did not wake", e)
		}
	}
}
func TestApprovalWaitPermissionProbeUsesCommitOrder(t *testing.T) {
	s, cfg, bases := waitQueue(t, 1, 1, 1)
	b := bases[0]
	now := b.At.Add(time.Hour)
	s = waitBuild(t, s, cfg, now)
	gateBasis := b
	gateBasis.At = b.At.Add(time.Minute)
	s = rejectWait(t, s, s.Active, gateBasis)
	s = invalidateWait(t, s)
	// A later committed observation may come from a slower worker clock.
	// Overlapping stale reads are rejected by the store before journaling.
	later := eligibilityEvent(b)
	later.Basis.At = b.At.Add(30 * time.Second)
	later.Known = false
	later.Reasons = []string{"unknown-from-slower-clock"}
	if got := Apply(s, later); len(got.ApprovalWaits()) != 0 {
		t.Fatal("later committed eligibility did not wake wait")
	}
	probe, changed := s.PrepareApprovalInputChange(later)
	if !changed {
		t.Fatal("slower clock suppressed permission probe")
	}
	if got := Apply(s, probe); len(got.ApprovalWaits()) != 0 {
		t.Fatal("later committed permission probe did not wake wait")
	}
}

func TestApprovalFingerprintDecisionSubset(t *testing.T) {
	b := approvalBasis()
	s := approvalState(b)
	s = Apply(s, Intent(b, "title", "body"))
	s = Apply(s, eligibilityEvent(b))
	s = Apply(s, native(b, 20, cp.Approve, b.At))
	original := s.ApprovalFingerprint(b.ChangeID)
	poll := native(b, 20, cp.Approve, b.At)
	poll.Basis.ReadID = "next"
	poll.Basis.At = b.At.Add(time.Hour)
	if got := Apply(s, poll).ApprovalFingerprint(b.ChangeID); got != original {
		t.Fatal("duplicate poll changed fingerprint")
	}
	historical := native(b, 1, cp.Approve, b.At.Add(-time.Hour))
	s = Apply(s, historical)
	if s.ApprovalFingerprint(b.ChangeID) != original {
		t.Fatal("non-effective historical source changed fingerprint")
	}
	s.approvalFacts = mapsCloneFactsForTest(s.approvalFacts)
	f := s.approvalFacts[b.ChangeID]
	v := f.Sources[sourceKey(historical)]
	v.Revision = 0
	f.Sources[sourceKey(historical)] = v
	s.approvalFacts[b.ChangeID] = f
	if s.ApprovalFingerprint(b.ChangeID) != original {
		t.Fatal("revision zero changed fingerprint")
	}
	classified := classificationEvent(t)
	s = Apply(s, classified)
	before := s.ApprovalFingerprint(b.ChangeID)
	classified.Evidence.Classification.Inputs.Generation++
	classified.Evidence.Classification.Inputs.PolicyDigest = "other-policy"
	if got := Apply(s, classified).ApprovalFingerprint(b.ChangeID); got == before {
		t.Fatal("policy digest missing")
	}
	if got := Apply(s, ConfigApplied{Commit: "config", Digest: strings.Repeat("e", 64)}).ApprovalFingerprint(b.ChangeID); got == before {
		t.Fatal("ConfigApplied missing")
	}
}
func mapsCloneFactsForTest(in approvalProjection) approvalProjection {
	out := approvalProjection{}
	for id, f := range in {
		out[id] = cloneApprovalFacts(f)
	}
	return out
}
func TestApprovalWaitWireFixtures(t *testing.T) {
	b := approvalBasis()
	w := ApprovalWait{b, strings.Repeat("a", 64), []string{"approval-required"}}
	fresh := eligibilityEvent(b)
	fresh.Actors = map[string]cp.Actor{}
	reject := ApprovalGateRejected{BatchID: "b1", TestedSHA: "tested", PRs: []BatchPR{{PR: 12, HeadSHA: b.Head}}, Waiting: []ApprovalWait{w}}
	change := ApprovalInputsChanged{b, w.Fingerprint, strings.Repeat("b", 64), fresh}
	multi := reject
	second := w
	second.Basis.ChangeID, _ = PRChangeID("7", "Owner/Repo", "release", "", 13)
	multi.PRs = append(slices.Clone(reject.PRs), BatchPR{PR: 13, HeadSHA: b.Head})
	multi.Waiting = append(slices.Clone(reject.Waiting), second)
	stale := change
	stale.Basis.Revision = 2
	stale.Eligibility.Basis = stale.Basis
	unknown := change
	unknown.Eligibility.Known = false
	unknown.Eligibility.Reasons = []string{"membership-unknown"}
	vectors := map[string]Event{"wire/v1/event_approval_gate_rejected": reject, "wire/v1/event_approval_inputs_changed": change, "approvals/wait-multi-member": multi, "approvals/wait-stale-wake": stale, "approvals/wait-unknown-eligibility": unknown}
	for name, e := range vectors {
		_, raw, err := EncodeEvent(e)
		if err != nil {
			t.Fatal(name, err)
		}
		path := filepath.Join("testdata", name+".json")
		if os.Getenv("UPDATE_APPROVAL_FIXTURES") == "1" {
			if err := os.WriteFile(path, raw, 0644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(want, raw) {
			t.Fatal(name, "fixture drift", err)
		}
		decoded, err := DecodeEvent(raw)
		if err != nil || !reflect.DeepEqual(e, decoded) {
			t.Fatal(name, "wire round trip", err)
		}
	}
}

func TestApprovalWaitFastForwardPrefix(t *testing.T) {
	s, cfg, bases := waitQueue(t, 1, 3, 4)
	now := bases[0].At.Add(time.Hour)
	s = waitBuild(t, s, cfg, now)
	// Return to pre-build phase to exercise the existing fast-forward prefix seam.
	s.Active.Phase = Building
	s = Apply(s, FastForwardBatchSelected{BatchID: s.Active.ID, PRs: slices.Clone(s.Active.PRs[:2])})
	s = Apply(s, FastForwardRefBuilt{BatchID: s.Active.ID, TestedSHA: "marker", HeadSHA: bases[1].Head, At: now})
	s = rejectWait(t, s, s.Active, bases[0])
	s = invalidateWait(t, s)
	if prs := plannedWaitBuilds(cfg, s, now); len(prs) != 1 || prs[0].PR != 2 {
		t.Fatal("prefix recovery", prs)
	}
	s = waitBuild(t, s, cfg, now)
	if s.Active.ID == "b1" {
		t.Fatal("reused rejected batch identity")
	}
}
func TestApprovalClassificationGenerationInvalidatesBuildMetadata(t *testing.T) {
	b := approvalBasis()
	now := b.At.Add(time.Hour)
	s := approvalState(b)
	s.BaseSHA = strings.Repeat("a", 40)
	s.BaseBranch = "release"
	s = Apply(s, PRObserved{PR: 12, HeadSHA: b.Head, Trusted: true, Eligible: true})
	s = Apply(s, EnqueueRequested{PR: 12, At: b.At})
	s = Apply(s, Intent(b, "title", "body"))
	c := classificationEvent(t)
	s = Apply(s, c)
	cfg := Config{MergeMethod: Merge}
	s = waitBuild(t, s, cfg, now)
	prior := s
	c.Evidence.Classification.Inputs.Generation++
	s = Apply(s, c)
	if s.Active.InvalidationReason != "classification-changed" || prior.Active.InvalidationReason != "" {
		t.Fatal("generation left stale build metadata valid")
	}
	// Started landing retains its frozen evidence and settles first.
	landing := prior
	landing = Apply(landing, LandingStarted{BatchID: landing.Active.ID, AttemptID: "attempt", FenceToken: "fence", At: now})
	landing = Apply(landing, c)
	if landing.Active.InvalidationReason != "" {
		t.Fatal("generation revoked started attempt")
	}
	// Provenance-only refresh at the same generation does not revoke work.
	c = classificationEvent(t)
	c.Evidence.Classification.Inputs.GoverningCommit = strings.Repeat("e", 40)
	c.Evidence.Governing.CommitSHA = strings.Repeat("e", 40)
	if got := Apply(prior, c); got.Active.InvalidationReason != "" {
		t.Fatal("provenance revoked composition")
	}
}
func TestApprovalWaitSnapshotTailAndInvalidRejection(t *testing.T) {
	s, cfg, bases := waitQueue(t, 1, 2, 2)
	now := bases[0].At.Add(time.Hour)
	s = waitBuild(t, s, cfg, now)
	e := ApprovalGateRejected{BatchID: s.Active.ID, TestedSHA: s.Active.TestedSHA, PRs: slices.Clone(s.Active.PRs), Waiting: []ApprovalWait{{bases[0], s.ApprovalFingerprint(bases[0].ChangeID), []string{"approval-required"}}}}
	for _, mutate := range []func(*ApprovalGateRejected){func(e *ApprovalGateRejected) { e.TestedSHA = "late" }, func(e *ApprovalGateRejected) { e.Waiting[0].Fingerprint = strings.Repeat("f", 64) }, func(e *ApprovalGateRejected) { e.PRs = e.PRs[:1] }} {
		bad := detached(e)
		mutate(&bad)
		if got := Apply(s, bad); got.Active.InvalidationReason != "" || len(got.ApprovalWaits()) != 0 {
			t.Fatal("stale composition rejection applied")
		}
	}
	restored := restart(t, s)
	left := Apply(s, e)
	right := Apply(restored, e)
	left = invalidateWait(t, left)
	right = invalidateWait(t, right)
	a, err := EncodeSnapshot(left)
	if err != nil {
		t.Fatal(err)
	}
	b, err := EncodeSnapshot(right)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatal("snapshot/tail changed wait", err)
	}
}
func TestApprovalWaitCancellationLedger(t *testing.T) {
	for _, depth := range []int{1, 4} {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			s, cfg, bases := waitQueue(t, depth, 1, 2)
			now := bases[0].At.Add(time.Hour)
			s = waitBuild(t, s, cfg, now)
			old := *s.Active
			run := CheckRun{ID: 1, Attempt: 1, WorkflowRunID: 7, Name: "ci", Source: "app:1", State: "pending"}
			s = Apply(s, BatchRunsObserved{BatchID: old.ID, TestedSHA: old.TestedSHA, Runs: []CheckRun{run}})
			s = rejectWait(t, s, s.Active, bases[0])
			s = invalidateWait(t, s)
			var request *RecordCheckCancellation
			for _, a := range Plan(cfg, s, now) {
				if a, ok := a.(RecordCheckCancellation); ok {
					request = &a
				}
			}
			if depth == 1 {
				if request != nil {
					t.Fatal("depth-one cancellation expanded")
				}
				return
			}
			if request == nil || request.BatchID != old.ID {
				t.Fatal("missing speculative cancellation")
			}
			s = Apply(s, CheckCancellationRequested(*request))
			s = waitBuild(t, s, cfg, now)
			newID := s.Active.ID
			s = Apply(s, ChecksCompleted{TestedSHA: old.TestedSHA, Success: true})
			confirmed := slices.Clone(request.Runs)
			confirmed[0].State = "cancelled"
			s = Apply(s, CheckCancellationReported{BatchID: old.ID, TestedSHA: old.TestedSHA, Confirmed: confirmed})
			if s.Active.ID != newID || s.Active.checksPassed {
				t.Fatal("late cancelled work authorized replacement")
			}
			_ = restart(t, s)
		})
	}
}
func TestApprovalFingerprintSetOrderIsNotInputChange(t *testing.T) {
	b := approvalBasis()
	s := approvalState(b)
	first := eligibilityEvent(b)
	first.Groups["team"] = cp.IdentitySet{IDs: []string{person(2).ID, person(3).ID}, Fresh: true, Resolved: true}
	first.OwnerPools["owned:team:org/team"] = first.Groups["team"]
	s = Apply(s, first)
	fingerprint := s.ApprovalFingerprint(b.ChangeID)
	second := detached(first)
	second.Basis.ReadID = "later-poll"
	second.Basis.At = b.At.Add(time.Minute)
	second.Groups["team"] = cp.IdentitySet{IDs: []string{person(3).ID, person(2).ID, person(2).ID}, Fresh: true, Resolved: true}
	second.OwnerPools["owned:team:org/team"] = second.Groups["team"]
	if got := Apply(s, second).ApprovalFingerprint(b.ChangeID); got != fingerprint {
		t.Fatal("membership set order or duplication changed fingerprint")
	}
	account := accountability(b, person(7), person(1), map[string][]cp.Identity{person(1).ID: {person(2), person(3)}})
	s = Apply(s, account)
	fingerprint = s.ApprovalFingerprint(b.ChangeID)
	slices.Reverse(account.Humans)
	if got := Apply(s, account).ApprovalFingerprint(b.ChangeID); got != fingerprint {
		t.Fatal("accountability order changed fingerprint")
	}
}
func TestApprovalWaitCannotRebindTestedRevisionAfterHeadReuse(t *testing.T) {
	s, cfg, bases := waitQueue(t, 1, 1, 1)
	b := bases[0]
	now := b.At.Add(time.Hour)
	s = waitBuild(t, s, cfg, now)
	batch := *s.Active
	// Preserve the old batch while revision evidence advances H1 -> H2 -> H1.
	for revision := ChangeRevision(2); revision <= 3; revision++ {
		head := strings.Repeat("f", 40)
		if revision == 3 {
			head = b.Head
		}
		s = Apply(s, ChangeRevisionObserved{PR: 1, ChangeID: b.ChangeID, Revision: revision, Predecessor: revision - 1, HeadSHA: head, Basis: RevisionObservationBasis{BaseSHA: s.BaseSHA, At: now.Add(time.Duration(revision) * time.Second)}})
	}
	b.Revision = 3
	b.At = now.Add(time.Minute)
	s = Apply(s, Intent(b, "current", "body"))
	e := ApprovalGateRejected{BatchID: batch.ID, TestedSHA: batch.TestedSHA, PRs: batch.PRs, Waiting: []ApprovalWait{{b, s.ApprovalFingerprint(b.ChangeID), []string{"approval-required"}}}}
	if got := Apply(s, e); len(got.ApprovalWaits()) != 0 {
		t.Fatal("old tested composition rebound to reused-head revision")
	}
}

func TestApprovalWaitIntentAndAccountabilityUseCommitOrder(t *testing.T) {
	for _, kind := range []string{"intent", "accountability"} {
		t.Run(kind, func(t *testing.T) {
			s, cfg, bases := waitQueue(t, 1, 1, 1)
			b := bases[0]
			observation := func(b ApprovalBasis, changed bool) Event {
				if kind == "intent" {
					title := "current"
					if changed {
						title = "committed-from-slower-clock"
					}
					return Intent(b, title, "body")
				}
				actor := person(7)
				if changed {
					actor = person(8)
				}
				return accountability(b, actor, person(1), nil)
			}
			newer := b
			newer.At = b.At.Add(time.Minute)
			newer.ReadID = "newer"
			s = Apply(s, observation(newer, false))
			// Commit ordering must survive snapshot recovery, even before a wait.
			s = restart(t, s)
			if got := Apply(s, observation(b, true)); reflect.DeepEqual(got, s) {
				t.Fatal("later committed observation ignored due to clock skew")
			}
			now := newer.At.Add(time.Hour)
			s = waitBuild(t, s, cfg, now)
			s = rejectWait(t, s, s.Active, newer)
			s = invalidateWait(t, s)
			s = restart(t, s)
			if got := Apply(s, observation(b, true)); len(got.ApprovalWaits()) != 0 {
				t.Fatal("later committed observation failed to wake wait")
			}
			newer.At = newer.At.Add(time.Minute)
			newer.ReadID = "fresh-change"
			if got := Apply(s, observation(newer, true)); len(got.ApprovalWaits()) != 0 {
				t.Fatal("fresh changed observation failed to wake wait")
			}
		})
	}
}

func TestApprovalConfigChangeClearsWaitAndDiscardsBatch(t *testing.T) {
	s, cfg, bases := waitQueue(t, 1, 1, 1)
	now := bases[0].At.Add(time.Hour)
	s = waitBuild(t, s, cfg, now)
	s = rejectWait(t, s, s.Active, bases[0])
	if len(s.ApprovalWaits()) != 1 || s.Active == nil {
		t.Fatal("fixture missing wait or batch")
	}
	s = Apply(s, ConfigApplied{Commit: "new-config", Digest: strings.Repeat("e", 64)})
	if len(s.ApprovalWaits()) != 0 || s.Active != nil {
		t.Fatal("config change retained old-policy wait or non-landing batch")
	}
}
