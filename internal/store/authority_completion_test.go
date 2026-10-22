package store

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestRepositoryAuthorityCompletion(t *testing.T) {
	s := newStore(t)
	raw, err := os.ReadFile("../queue/testdata/wire/v1/event_write_restriction_verified.json")
	must(t, err)
	event, err := queue.DecodeEvent(raw)
	must(t, err)
	draft := event.(queue.WriteRestrictionVerified)
	request := AuthorityCompletionRequest{Installation: "7", Repository: "owner/repo", Digest: strings.Repeat("a", 64)}
	inventory := []string{"main", "release/v1"}
	check := func(want bool) {
		t.Helper()
		result, err := s.RepositoryAuthorityCompletion(t.Context(), request, inventory)
		must(t, err)
		if result.Complete != want || (result.RequireComplete() == nil) != want {
			t.Fatalf("completion: %+v", result)
		}
	}
	check(false)
	add := func(base, digest string, lifecycle string) QueueID {
		key := QueueKey{Installation: "7", Repository: "owner/repo", BaseBranch: base}
		id, err := s.EnsureQueue(t.Context(), key)
		must(t, err)
		binding := draft.Binding
		binding.InstallationID = 7
		binding.Repository = "owner/repo"
		binding.QueueKey = AuthorityQueueKey(key)
		binding.EffectiveDigest = digest
		selectors := []authority.BaseSelector{{DefaultBranch: true}, {Pattern: "release/**"}}
		binding.CoverageDigest, err = authority.CoverageDigest("main", selectors)
		must(t, err)
		in := forge.WriteRestrictionInput{Repo: forge.RepoRef{Path: key.Repository}, Binding: binding, BaseSelectors: selectors, ExpectedRefs: func() []string {
			refs := []string{}
			for _, base := range inventory {
				refs = append(refs, "refs/heads/"+base)
			}
			return refs
		}(), AppID: "5383059"}
		evidence := authority.CloneObservation(draft.Evidence)
		for _, base := range inventory {
			if base == "main" {
				continue
			}
			evidence.Coverage = append(evidence.Coverage, authority.Coverage{Ref: "refs/heads/" + base, Visibility: authority.Visible, ProtectionIDs: []string{"repository_ruleset/base-10"}})
		}

		// Use complete typed facts from the shared fixture, interpreted for this target.
		evidence, err = forge.InterpretWriteRestriction(in, evidence)
		must(t, err)
		fingerprint, err := evidence.Fingerprint()
		must(t, err)
		proposal := draft
		proposal.Binding = binding
		proposal.Evidence = evidence
		proposal.ProtectionFingerprint = fingerprint
		proposal.Scope = "proposed"
		pin := queue.PolicyAuthorityApplied{Authority: authority.OpenMerge, GoverningCommit: "tip", EffectiveDigest: digest, DefaultBranch: "main", CoverageDigest: binding.CoverageDigest, OpenMerge: &queue.AuthorityPin{Binding: binding}}
		at := proposal.VerifiedAt.Add(time.Second)
		pin = queue.BindAuthorityDraft(pin, 1, proposal, at, at, in, evidence)
		if !queue.AuthorityDraftAdopted(pin) {
			t.Fatal("fixture did not adopt")
		}
		appendEvents(t, s, id, queue.ConfigApplied{Commit: "tip", Digest: digest}, pin)
		if lifecycle != "" {
			appendEvents(t, s, id, queue.LifecycleChanged{State: lifecycle, Detail: "fixture lifecycle"})
		}
		return id
	}
	add("main", request.Digest, "")
	check(false)
	add("release/v1", strings.Repeat("b", 64), "retired")
	check(false)
	request.Targets = map[string]string{"release/v1": strings.Repeat("b", 64)}
	// Different effective queue digests are explicit targets, never auto-selected.
	check(true)
	inventory = append(inventory, "release/v2")
	check(false)
	request.Targets["release/v2"] = request.Digest
	add("release/v2", request.Digest, "dormant")
	check(false) // Existing pins must also adopt the newly discovered inventory.
	add("main", request.Digest, "")
	add("release/v1", request.Targets["release/v1"], "retired")
	check(true)
	mainID, err := s.EnsureQueue(t.Context(), QueueKey{Installation: "7", Repository: "owner/repo", BaseBranch: "main"})
	must(t, err)
	state, _, err := s.Load(t.Context(), mainID)
	must(t, err)
	pin := state.AuthorityPin()
	appendEvents(t, s, mainID, queue.WriteRestrictionObserved{Binding: pin.OpenMerge.Binding, Basis: state.RulesSourceRevision(queue.WriteRestriction), ObservedAt: draft.VerifiedAt.Add(time.Hour), Evidence: authority.Observation{Inventory: authority.ReadFailure, Protections: []authority.Protection{}, Coverage: []authority.Coverage{}, Findings: []authority.Finding{}}, ReadError: "authority write restriction read failed: timeout"})
	check(false)
	state, _, err = s.Load(t.Context(), mainID)
	must(t, err)
	appendEvents(t, s, mainID, queue.WriteRestrictionVerified{Binding: pin.OpenMerge.Binding, Basis: state.RulesSourceRevision(queue.WriteRestriction), Scope: "applied", Nonce: strings.Repeat("c", 64), Operator: "operator", VerifiedAt: draft.VerifiedAt.Add(2 * time.Hour), Evidence: pin.OpenMerge.Activation.Evidence, ProtectionFingerprint: pin.OpenMerge.Activation.ProtectionFingerprint})
	check(true)
	// Pausing never removes a queue from the completion set.
	id, err := s.EnsureQueue(t.Context(), QueueKey{Installation: "7", Repository: "owner/repo", BaseBranch: "paused"})
	must(t, err)
	appendEvents(t, s, id, queue.PauseRequested{Reason: "operator", Detail: "fixture pause"})
	check(false)
	// An unrelated repository/installation cannot supply the missing partition.
	_, err = s.EnsureQueue(t.Context(), QueueKey{Installation: "other", Repository: "owner/repo", BaseBranch: "paused"})
	must(t, err)
	check(false)
	result, err := s.RepositoryAuthorityCompletion(t.Context(), request, nil)
	if err == nil || result.Complete {
		t.Fatal("missing discovery accepted")
	}
}

func TestAuthorityDowngradeGate(t *testing.T) {
	s := newStore(t)
	id := ensureQueue(t, s, "")
	reader := &JournalReader{store: s, timeout: time.Minute}
	must(t, reader.CheckAuthorityDowngrade(t.Context()))
	snapshot, err := queue.EncodeSnapshot(queue.State{})
	must(t, err)
	var value map[string]any
	must(t, json.Unmarshal(snapshot, &value))
	value["Version"] = 7
	snapshot, err = json.Marshal(value)
	must(t, err)
	cursor, err := json.Marshal(outsidePushProgress{State: snapshot})
	must(t, err)
	_, err = s.pool.Exec(t.Context(), `INSERT INTO observations(queue_id,key,value) VALUES($1,$2,$3)`, id, outsidePushCursorKey, cursor)
	must(t, err)
	if reader.CheckAuthorityDowngrade(t.Context()) == nil {
		t.Fatal("v7-only downgrade accepted")
	}
	_, err = s.pool.Exec(t.Context(), `DELETE FROM observations WHERE queue_id=$1`, id)
	must(t, err)
	must(t, reader.CheckAuthorityDowngrade(t.Context()))
	raw, err := os.ReadFile("../queue/testdata/wire/v1/event_policy_authority_applied.json")
	must(t, err)
	e, err := queue.DecodeEvent(raw)
	must(t, err)
	appendEvents(t, s, id, e)
	// Retirement and mode rollback cannot erase retained journal compatibility.
	appendEvents(t, s, id, queue.LifecycleChanged{State: "retired", Detail: "fixture lifecycle"})
	if reader.CheckAuthorityDowngrade(t.Context()) == nil {
		t.Fatal("journal downgrade accepted")
	}
}
