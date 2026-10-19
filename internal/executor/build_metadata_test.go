package executor_test

import (
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

const metadataPolicy = "semantic-policy"

type metadataPin struct {
	pr   int
	head string
}

func metadataClassification(t *testing.T, key store.QueueKey, pin metadataPin, base string) queue.ChangeClassified {
	t.Helper()
	raw, err := os.ReadFile("../queue/testdata/wire/v1/event_change_classified.json")
	if err != nil {
		t.Fatal(err)
	}
	event, err := queue.DecodeEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	e := event.(queue.ChangeClassified)
	id, err := key.ChangeID(pin.pr)
	if err != nil {
		t.Fatal(err)
	}
	e.Evidence.Classification.Inputs.ChangeID = string(id)
	e.Evidence.Classification.Inputs.Revision = 1
	e.Evidence.Classification.Inputs.Base = base
	e.Evidence.Classification.Inputs.Head = pin.head
	e.Evidence.Classification.Inputs.PolicyDigest = metadataPolicy
	e.Evidence.Classification.Facts["owned:user:alice"] = cp.True
	return e
}

func metadataState(t *testing.T, key store.QueueKey, pins []metadataPin) (queue.State, queue.Batch) {
	t.Helper()
	base := strings.Repeat("a", 40)
	configDigest := strings.Repeat("c", 64)
	s := queue.State{BaseBranch: key.BaseBranch, BaseSHA: base}
	s = queue.Apply(s, queue.ConfigApplied{Commit: "config", Digest: configDigest})
	batchPRs := make([]queue.BatchPR, len(pins))
	for i, pin := range pins {
		batchPRs[i] = queue.BatchPR{PR: pin.pr, HeadSHA: pin.head}
		s = queue.Apply(s, metadataClassification(t, key, pin, base))
		id, err := key.ChangeID(pin.pr)
		if err != nil {
			t.Fatal(err)
		}
		s = queue.Apply(s, queue.ChangeRevisionObserved{
			PR: pin.pr, ChangeID: id, Revision: 1, HeadSHA: pin.head,
			Basis: queue.RevisionObservationBasis{BaseSHA: base, At: epoch},
		})
		s = queue.Apply(s, queue.PRObserved{PR: pin.pr, HeadSHA: pin.head, Eligible: true, Trusted: true})
		s = queue.Apply(s, queue.EnqueueRequested{PR: pin.pr, At: epoch})
	}
	s = queue.Apply(s, queue.BatchStarted{BatchID: "b1", BaseSHA: base, PRs: batchPRs, At: epoch})
	b := s.Batch("b1")
	if b == nil {
		t.Fatal("metadata fixture did not create a batch")
	}
	return s, *b
}

func TestLoadBuildMetadataMatchesPinnedClassification(t *testing.T) {
	key := store.QueueKey{Installation: "7", Repository: "Owner/Repo", BaseBranch: "main"}
	state, batch := metadataState(t, key, []metadataPin{{pr: 1, head: strings.Repeat("b", 40)}, {pr: 2, head: strings.Repeat("d", 40)}})
	got, err := executor.LoadBuildMetadata(state, key, batch, metadataPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("metadata members = %d, want 2", len(got))
	}
	for i, pin := range batch.PRs {
		id, err := key.ChangeID(pin.PR)
		if err != nil {
			t.Fatal(err)
		}
		if got[i].ChangeID != string(id) || got[i].Revision != 1 || !slices.Equal(got[i].Labels, []string{"docs", "owned:user:alice", "unknown"}) {
			t.Fatalf("metadata[%d] = %+v", i, got[i])
		}
	}
	// A speculative predecessor changes the build base, not the recorded diff
	// basis. The loader still binds the classification to the queue base.
	batch.BaseSHA = strings.Repeat("e", 40)
	if _, err := executor.LoadBuildMetadata(state, key, batch, metadataPolicy); err != nil {
		t.Fatalf("speculative build base rejected recorded classification: %v", err)
	}
}

func TestLoadBuildMetadataAllowsRefreshedRewriteBaseAfterForwardMove(t *testing.T) {
	key := store.QueueKey{Installation: "7", Repository: "Owner/Repo", BaseBranch: "main"}
	state, batch := metadataState(t, key, []metadataPin{{pr: 1, head: strings.Repeat("b", 40)}})
	original := state.BaseSHA
	refreshed, current := strings.Repeat("e", 40), strings.Repeat("f", 40)
	id, err := key.ChangeID(1)
	if err != nil {
		t.Fatal(err)
	}
	classification, ok := state.Classification(id, 1)
	if !ok {
		t.Fatal("missing original classification")
	}
	// A sibling rewrite refreshes the classification at B; a later ordinary
	// forward move B -> D leaves the revision's original observation at A.
	classification.Evidence.Classification.Inputs.Base = refreshed
	classification.Evidence.Classification.Inputs.Generation++
	state = queue.Apply(state, queue.BaseMoved{SHA: current})
	state = queue.Apply(state, classification)
	state = queue.Apply(state, queue.BatchStarted{BatchID: "b2", BaseSHA: current, PRs: batch.PRs, At: epoch})
	refreshedBatch := state.Batch("b2")
	if refreshedBatch == nil {
		t.Fatal("rewrite sequence did not start a fresh pinned batch")
	}
	batch = *refreshedBatch
	if _, err := executor.LoadBuildMetadata(state, key, batch, metadataPolicy); err == nil {
		t.Fatal("plain loader accepted a refreshed base outside its exact legacy pins")
	}

	calls := 0
	got, err := executor.LoadBuildMetadataWithAncestry(state, key, batch, metadataPolicy, func(ancestor, descendant string) (bool, error) {
		calls++
		if ancestor != refreshed || descendant != current {
			t.Fatalf("ancestry = %s -> %s, want %s -> %s", ancestor, descendant, refreshed, current)
		}
		return true, nil
	})
	if err != nil || len(got) != 1 || calls != 1 {
		t.Fatalf("refreshed forward pin = %+v, calls=%d, err=%v", got, calls, err)
	}
	if _, err := executor.LoadBuildMetadataWithAncestry(state, key, batch, metadataPolicy, func(string, string) (bool, error) { return false, nil }); err == nil {
		t.Fatal("accepted a refreshed base from discarded history")
	}
	if original == refreshed || original == current {
		t.Fatal("rewrite sequence fixture collapsed")
	}
}

func TestLoadBuildMetadataRejectsMismatchedBindings(t *testing.T) {
	t.Run("missing queue key", func(t *testing.T) {
		if _, err := executor.LoadBuildMetadata(queue.State{}, nil, queue.Batch{}, metadataPolicy); err == nil {
			t.Fatal("accepted absent canonical queue key")
		}
	})
	key := store.QueueKey{Installation: "7", Repository: "Owner/Repo", BaseBranch: "main"}
	state, batch := metadataState(t, key, []metadataPin{{pr: 1, head: strings.Repeat("b", 40)}})
	for _, tc := range []struct {
		name   string
		state  queue.State
		batch  queue.Batch
		key    store.QueueKey
		policy string
	}{
		{name: "head", state: state, batch: func() queue.Batch {
			b := batch
			b.PRs = []queue.BatchPR{{PR: 1, HeadSHA: strings.Repeat("9", 40)}}
			return b
		}(), key: key, policy: metadataPolicy},
		{name: "base", state: func() queue.State {
			id, _ := key.ChangeID(1)
			e, _ := state.Classification(id, 1)
			e.Evidence.Classification.Inputs.Base = strings.Repeat("9", 40)
			e.Evidence.Classification.Inputs.Generation++
			return queue.Apply(state, e)
		}(), batch: batch, key: key, policy: metadataPolicy},
		{name: "policy", state: state, batch: batch, key: key, policy: "other-policy"},
		{name: "config", state: state, batch: func() queue.Batch { b := batch; b.ConfigDigest = strings.Repeat("9", 64); return b }(), key: key, policy: metadataPolicy},
		{name: "missing state config", state: func() queue.State { s := state; s.ConfigDigest = ""; return s }(), batch: batch, key: key, policy: metadataPolicy},
		{name: "missing batch config", state: state, batch: func() queue.Batch { b := batch; b.ConfigDigest = ""; return b }(), key: key, policy: metadataPolicy},
		{name: "queue key", state: state, batch: batch, key: store.QueueKey{Installation: "7", Repository: "Owner/Repo", BaseBranch: "release"}, policy: metadataPolicy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := executor.LoadBuildMetadata(tc.state, tc.key, tc.batch, tc.policy); err == nil {
				t.Fatal("accepted mismatched recorded build inputs")
			}
		})
	}
	missing := state
	missing = queue.State{BaseBranch: missing.BaseBranch, BaseSHA: missing.BaseSHA, ConfigDigest: missing.ConfigDigest}
	if _, err := executor.LoadBuildMetadata(missing, key, batch, metadataPolicy); err == nil {
		t.Fatal("accepted missing recorded classification")
	}
}

func TestLoadBuildMetadataDoesNotRebindToLatestHead(t *testing.T) {
	key := store.QueueKey{Installation: "7", Repository: "Owner/Repo", BaseBranch: "main"}
	state, batch := metadataState(t, key, []metadataPin{{pr: 1, head: strings.Repeat("b", 40)}})
	id, err := key.ChangeID(1)
	if err != nil {
		t.Fatal(err)
	}
	latest := queue.Apply(state, queue.ChangeRevisionObserved{
		PR: 1, ChangeID: id, Revision: 2, Predecessor: 1, HeadSHA: strings.Repeat("e", 40),
		Basis: queue.RevisionObservationBasis{BaseSHA: state.BaseSHA, At: epoch},
	})
	latest = queue.Apply(latest, queue.PRObserved{PR: 1, HeadSHA: strings.Repeat("e", 40), Eligible: true, Trusted: true})
	got, err := executor.LoadBuildMetadata(latest, key, batch, metadataPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].ChangeID != string(id) || got[0].Revision != 1 {
		t.Fatalf("metadata rebound to latest revision: %+v", got[0])
	}
}

type recordingBuildGit struct {
	forge.Git
	onBuild func(forge.BuildRequest)
}

func (g *recordingBuildGit) BuildTestRef(ctx context.Context, repo forge.RepoRef, req forge.BuildRequest) (string, error) {
	if g.onBuild != nil {
		g.onBuild(req)
	}
	return g.Git.BuildTestRef(ctx, repo, req)
}

func TestBuildMetadataHookPinsCIInputAcrossExternalApprovalChange(t *testing.T) {
	h := setup(t)
	key := store.QueueKey{Installation: "7", Repository: "Owner/Repo", BaseBranch: "main"}
	state, _ := metadataState(t, key, []metadataPin{{pr: 1, head: h.prs[0].HeadSHA}})
	var builtEvent queue.TestRefBuilt
	var built *forge.ChangeMetadata
	h.x.Git = &recordingBuildGit{Git: h.f, onBuild: func(req forge.BuildRequest) {
		built = req.PRs[0].Metadata
	}}
	h.x.BuildMetadata = executor.LoadBuildMetadata
	h.x.BuildMetadataState = state
	h.x.BuildMetadataKey = key
	h.x.BuildPolicyDigest = metadataPolicy
	build := h.buildAction()
	build.BatchID, build.Ref = "b1", "openmerge/queue/main/b1"
	must(t, h.x.Execute(t.Context(), build, func(e queue.Event) error {
		if event, ok := e.(queue.TestRefBuilt); ok {
			builtEvent = event
		}
		return nil
	}))
	if builtEvent.TestedSHA == "" || built == nil {
		t.Fatalf("build did not capture tested SHA and metadata: event=%+v metadata=%v", builtEvent, built)
	}
	builtCommit, err := h.f.Commit(repo, builtEvent.TestedSHA)
	must(t, err)

	// CI is still pending when an external approval fact changes. This branch
	// has no receipt event until PR 11; this external approval is never applied
	// to queue state. This test asserts landing reuses the tested SHA/message
	// without calling the loader again; it does not test approval invalidation.
	h.f.SetCheckRuns(repo, builtEvent.TestedSHA, []forge.CheckRun{
		{Name: "ci", State: forge.Pending}, {Name: "lint", State: forge.Pending},
	})
	id, err := key.ChangeID(1)
	if err != nil {
		t.Fatal(err)
	}
	externalApproval := cp.Stance{
		UserID: "alice", Kind: cp.Approve, Revision: 1, Head: h.prs[0].HeadSHA,
		Source: cp.StanceSource{Channel: cp.CommentCommand, ID: "receipt-1"},
		Receipt: &cp.ReceiptIdentity{
			ChangeID: string(id), Revision: 1, Generation: 1,
			Base: state.BaseSHA, Head: h.prs[0].HeadSHA, MergeBase: h.base,
			IntentDigest: "intent", PolicyDigest: metadataPolicy, BriefDigest: "brief",
		},
	}
	if externalApproval.Receipt == nil || !externalApproval.Receipt.Recorded() {
		t.Fatal("external approval did not carry a complete receipt identity")
	}
	h.f.SetCheckRuns(repo, builtEvent.TestedSHA, []forge.CheckRun{
		{Name: "ci", State: forge.Success}, {Name: "lint", State: forge.Success},
	})
	h.x.BuildMetadata = func(queue.State, executor.BuildChangeKey, queue.Batch, string) ([]*forge.ChangeMetadata, error) {
		t.Fatal("build metadata loader was called during landing")
		return nil, nil
	}
	land := queue.Land{
		BatchID: build.BatchID, ExpectedBaseSHA: h.base, TestedSHA: builtEvent.TestedSHA,
		Ref: build.Ref, FenceToken: h.fence, Method: queue.Merge,
		PRs: []queue.BatchPR{{PR: 1, HeadSHA: h.prs[0].HeadSHA}},
	}
	var landed queue.Landed
	must(t, h.x.Execute(t.Context(), land, func(e queue.Event) error {
		if event, ok := e.(queue.Landed); ok {
			landed = event
		}
		return nil
	}))
	if landed.NewBaseSHA != builtEvent.TestedSHA || h.read(t, "main") != builtEvent.TestedSHA {
		t.Fatalf("landed SHA = %q / ref %q, want tested %q", landed.NewBaseSHA, h.read(t, "main"), builtEvent.TestedSHA)
	}
	landedCommit, err := h.f.Commit(repo, h.read(t, "main"))
	must(t, err)
	if landedCommit.Message != builtCommit.Message {
		t.Fatalf("tested commit message changed during approval/landing: before %q, after %q", builtCommit.Message, landedCommit.Message)
	}
	want := &forge.ChangeMetadata{ChangeID: string(id), Revision: 1, Labels: []string{"docs", "owned:user:alice", "unknown"}}
	if !reflect.DeepEqual(built, want) {
		t.Fatalf("build metadata changed during CI: got %+v, want %+v", built, want)
	}
}

func TestBuildMetadataHookUsesSelectedFastForwardPrefix(t *testing.T) {
	h := setup(t)
	stacked, err := h.f.OpenPR(repo, 4, h.prs[0].HeadSHA, true)
	must(t, err)
	key := store.QueueKey{Installation: "7", Repository: "Owner/Repo", BaseBranch: "main"}
	pins := []metadataPin{{pr: 1, head: h.prs[0].HeadSHA}, {pr: 4, head: stacked.HeadSHA}, {pr: 2, head: h.prs[1].HeadSHA}}
	state, _ := metadataState(t, key, pins)
	var request forge.BuildRequest
	h.x.Git = &recordingBuildGit{Git: h.f, onBuild: func(got forge.BuildRequest) { request = got }}
	h.x.BuildMetadata = executor.LoadBuildMetadata
	h.x.BuildMetadataState = state
	h.x.BuildMetadataKey = key
	h.x.BuildPolicyDigest = metadataPolicy
	a := queue.BuildTestRef{BatchID: "b1", BaseSHA: h.base, Ref: "openmerge/queue/main/b1", Method: queue.FastForward,
		PRs: []queue.BatchPR{{PR: 1, HeadSHA: h.prs[0].HeadSHA}, {PR: 4, HeadSHA: stacked.HeadSHA}, {PR: 2, HeadSHA: h.prs[1].HeadSHA}}}
	var events []queue.Event
	must(t, h.x.Execute(t.Context(), a, func(e queue.Event) error {
		events = append(events, e)
		state = queue.Apply(state, e)
		return nil
	}))
	if len(request.PRs) != 2 || request.PRs[0].Number != 1 || request.PRs[1].Number != 4 {
		t.Fatalf("BuildTestRef members = %+v, want selected linear prefix", request.PRs)
	}
	for i, pr := range request.PRs {
		id, err := key.ChangeID(pr.Number)
		if err != nil {
			t.Fatal(err)
		}
		if pr.Metadata == nil || pr.Metadata.ChangeID != string(id) || pr.Metadata.Revision != 1 {
			t.Fatalf("BuildTestRef metadata[%d] = %+v", i, pr.Metadata)
		}
	}
	if !slices.ContainsFunc(events, func(e queue.Event) bool {
		selected, ok := e.(queue.FastForwardBatchSelected)
		return ok && len(selected.PRs) == 2
	}) {
		t.Fatal("selected prefix was not recorded")
	}
}

func TestBuildMetadataLoaderErrorStopsBuild(t *testing.T) {
	for _, method := range []queue.MergeMethod{queue.Merge, queue.FastForward} {
		t.Run(string(method), func(t *testing.T) {
			h := setup(t)
			called := false
			h.x.Git = &recordingBuildGit{Git: h.f, onBuild: func(forge.BuildRequest) { called = true }}
			failure := errors.New("classification unavailable")
			h.x.BuildMetadata = func(queue.State, executor.BuildChangeKey, queue.Batch, string) ([]*forge.ChangeMetadata, error) {
				return nil, failure
			}
			var failures []queue.TestRefBuildFailed
			err := h.x.Execute(t.Context(), queue.BuildTestRef{BatchID: "b1", BaseSHA: h.base, Ref: "openmerge/queue/main/b1", Method: method, PRs: []queue.BatchPR{{PR: 1, HeadSHA: h.prs[0].HeadSHA}}}, func(e queue.Event) error {
				if f, ok := e.(queue.TestRefBuildFailed); ok {
					failures = append(failures, f)
				}
				return nil
			})
			if !errors.Is(err, failure) || called || len(failures) != 1 || failures[0].Conflict || failures[0].BatchID != "b1" {
				t.Fatalf("err=%v built=%v failures=%+v", err, called, failures)
			}
		})
	}
}

func TestBuildMetadataMissingBatchFailsClosed(t *testing.T) {
	h := setup(t)
	key := store.QueueKey{Installation: "7", Repository: "Owner/Repo", BaseBranch: "main"}
	state, _ := metadataState(t, key, []metadataPin{{pr: 1, head: h.prs[0].HeadSHA}})
	h.x.BuildMetadataState = state
	h.x.BuildMetadata = executor.LoadBuildMetadata
	h.x.BuildMetadataKey = key
	h.x.BuildPolicyDigest = metadataPolicy
	h.x.Git = &recordingBuildGit{Git: h.f, onBuild: func(forge.BuildRequest) { t.Fatal("built absent batch") }}
	err := h.x.Execute(t.Context(), queue.BuildTestRef{BatchID: "absent", BaseSHA: h.base, Method: queue.Merge, PRs: []queue.BatchPR{{PR: 1, HeadSHA: h.prs[0].HeadSHA}}}, func(queue.Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "config digest") {
		t.Fatalf("err=%v", err)
	}
}
