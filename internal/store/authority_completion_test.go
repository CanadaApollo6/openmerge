package store

import (
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
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
		result, err := s.RepositoryAuthorityCompletion(t.Context(), request, AuthorityBranchInventory{DefaultBranch: "main", DefaultTip: "tip", Branches: inventory})
		must(t, err)
		if result.Complete != want || (result.RequireComplete() == nil) != want {
			t.Fatalf("completion: %+v", result)
		}
	}
	// reason reports the explanation for one partition; every incomplete state
	// must be attributable, not just a false aggregate.
	reason := func(base, lane string, inv AuthorityBranchInventory) string {
		t.Helper()
		result, err := s.RepositoryAuthorityCompletion(t.Context(), request, inv)
		must(t, err)
		for _, q := range result.Queues {
			if q.Base == base && q.Lane == lane && q.Installation == "7" {
				return q.Reason
			}
		}
		return ""
	}
	current := func() AuthorityBranchInventory {
		return AuthorityBranchInventory{DefaultBranch: "main", DefaultTip: "tip", Branches: inventory}
	}
	check(false)
	if got := reason("main", "", current()); got != "registration_missing" {
		t.Fatalf("empty repository reason %q", got)
	}
	addWith := func(key QueueKey, digest string, lifecycle string, adopt bool) QueueID {
		id, err := s.EnsureQueue(t.Context(), key)
		must(t, err)
		binding := draft.Binding
		binding.InstallationID, err = strconv.ParseInt(key.Installation, 10, 64)
		must(t, err)
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
		if adopt {
			pin = queue.BindAuthorityDraft(pin, 1, proposal, at, at, in, evidence)
			if !queue.AuthorityDraftAdopted(pin) {
				t.Fatal("fixture did not adopt")
			}
		}
		appendEvents(t, s, id, queue.ConfigApplied{Commit: "tip", Digest: digest}, pin)
		if lifecycle != "" {
			appendEvents(t, s, id, queue.LifecycleChanged{State: lifecycle, Detail: "fixture lifecycle"})
		}
		return id
	}
	add := func(base, digest string, lifecycle string) QueueID {
		return addWith(QueueKey{Installation: "7", Repository: "owner/repo", BaseBranch: base}, digest, lifecycle, true)
	}
	mainID := add("main", request.Digest, "")
	check(false)
	if got := reason("release/v1", "", current()); got != "registration_missing" {
		t.Fatalf("selected unregistered base reason %q", got)
	}
	add("release/v1", strings.Repeat("b", 64), "retired")
	check(false)
	// Mixed digests: a retired partition on another policy is not silently excluded.
	if got := reason("release/v1", "", current()); got != "digest_mismatch" {
		t.Fatalf("mixed digest reason %q", got)
	}
	if got := reason("main", "", current()); got != "complete" {
		t.Fatalf("matching partition reason %q", got)
	}
	request.Targets = map[string]string{"release/v1": strings.Repeat("b", 64)}
	// Different effective queue digests are explicit targets, never auto-selected.
	check(true)
	t.Run("second installation", func(t *testing.T) {
		// Keep every selected partition complete; only tenancy changes.
		_, err := s.EnsureQueue(t.Context(), QueueKey{Installation: "8", Repository: "other/repo", BaseBranch: "main"})
		must(t, err)
		check(false)
		result, err := s.RepositoryAuthorityCompletion(t.Context(), request, current())
		must(t, err)
		if result.InstallationCount != 2 {
			t.Fatalf("installation count: %d", result.InstallationCount)
		}
		_, err = s.pool.Exec(t.Context(), `DELETE FROM canonical_queues WHERE installation='8'`)
		must(t, err)
		_, err = s.pool.Exec(t.Context(), `DELETE FROM queues WHERE installation='8'`)
		must(t, err)
	})
	t.Run("case variant repository", func(t *testing.T) {
		request.Repository = "OWNER/Repo"
		defer func() { request.Repository = "owner/repo" }()
		result, err := s.RepositoryAuthorityCompletion(t.Context(), request, current())
		must(t, err)
		if !result.Complete || result.RequireComplete() != nil {
			t.Fatalf("case variant repository: %+v", result)
		}
	})
	t.Run("durable config digest independent of pin", func(t *testing.T) {
		appendEvents(t, s, mainID, queue.ConfigApplied{Commit: "tip", Digest: strings.Repeat("d", 64)})
		defer appendEvents(t, s, mainID, queue.ConfigApplied{Commit: "tip", Digest: request.Digest})
		if got := reason("main", "", current()); got != "digest_mismatch" {
			t.Fatalf("durable config drift reason %q", got)
		}
	})
	t.Run("default branch absent from live inventory", func(t *testing.T) {
		result, err := s.RepositoryAuthorityCompletion(t.Context(), request, AuthorityBranchInventory{DefaultBranch: "main", DefaultTip: "tip", Branches: []string{"release/v1"}})
		if err == nil || result.Complete {
			t.Fatal("missing live default accepted")
		}
	})
	t.Run("selected expected ref deleted", func(t *testing.T) {
		if got := reason("main", "", AuthorityBranchInventory{DefaultBranch: "main", DefaultTip: "tip", Branches: []string{"main"}}); got != "discovery_inputs_stale" {
			t.Fatalf("deleted selected branch reason %q", got)
		}
	})
	t.Run("explicit unregistered target", func(t *testing.T) {
		request.Targets["unselected"] = request.Digest
		defer delete(request.Targets, "unselected")
		if got := reason("unselected", "", current()); got != "registration_missing" {
			t.Fatalf("explicit target reason %q", got)
		}
	})
	inventory = append(inventory, "release/v2")
	check(false)
	// Incomplete propagation: a newly selected branch is unregistered and every
	// existing pin still carries the old ExpectedRefs.
	if got := reason("release/v2", "", current()); got != "registration_missing" {
		t.Fatalf("new branch reason %q", got)
	}
	if got := reason("main", "", current()); got != "discovery_inputs_stale" {
		t.Fatalf("stale inputs reason %q", got)
	}
	request.Targets["release/v2"] = request.Digest
	add("release/v2", request.Digest, "dormant")
	check(false) // Existing pins must also adopt the newly discovered inventory.
	add("main", request.Digest, "")
	add("release/v1", request.Targets["release/v1"], "retired")
	check(true)
	mainID, err = s.EnsureQueue(t.Context(), QueueKey{Installation: "7", Repository: "owner/repo", BaseBranch: "main"})
	must(t, err)
	state, _, err := s.Load(t.Context(), mainID)
	must(t, err)
	pin := state.AuthorityPin()
	appendEvents(t, s, mainID, queue.WriteRestrictionObserved{Binding: pin.OpenMerge.Binding, Basis: state.RulesSourceRevision(queue.WriteRestriction), ObservedAt: draft.VerifiedAt.Add(time.Hour), Evidence: authority.Observation{Inventory: authority.ReadFailure, Protections: []authority.Protection{}, Coverage: []authority.Coverage{}, Findings: []authority.Finding{}}, ReadError: "authority write restriction read failed: timeout"})
	check(false)
	if got := reason("main", "", current()); got != "verification_incomplete" {
		t.Fatalf("read failure reason %q", got)
	}
	state, _, err = s.Load(t.Context(), mainID)
	must(t, err)
	appendEvents(t, s, mainID, queue.WriteRestrictionVerified{Binding: pin.OpenMerge.Binding, Basis: state.RulesSourceRevision(queue.WriteRestriction), Scope: "applied", Nonce: strings.Repeat("c", 64), Operator: "operator", VerifiedAt: draft.VerifiedAt.Add(2 * time.Hour), Evidence: pin.OpenMerge.Activation.Evidence, ProtectionFingerprint: pin.OpenMerge.Activation.ProtectionFingerprint})
	check(true)
	// Changing the default setting to another EXISTING served branch must
	// invalidate completion even if all refs share the same commit and digests.
	renamedDefault, err := s.RepositoryAuthorityCompletion(t.Context(), request, AuthorityBranchInventory{DefaultBranch: "release/v1", DefaultTip: "tip", Branches: inventory})
	must(t, err)
	if renamedDefault.Complete {
		t.Fatal("default-branch name drift accepted with matching commit/digests")
	}
	if got := reason("main", "", AuthorityBranchInventory{DefaultBranch: "release/v1", DefaultTip: "tip", Branches: inventory}); got != "discovery_inputs_stale" {
		t.Fatalf("default rename reason %q", got)
	}
	otherID, err := s.EnsureQueue(t.Context(), QueueKey{Installation: "another", Repository: "owner/repo", BaseBranch: "historical"})
	must(t, err)
	resultWithHistory, err := s.RepositoryAuthorityCompletion(t.Context(), request, AuthorityBranchInventory{DefaultBranch: "main", DefaultTip: "tip", Branches: inventory})
	must(t, err)
	if resultWithHistory.Complete {
		t.Fatal("different installation history was omitted")
	}
	foundHistory := false
	for _, q := range resultWithHistory.Queues {
		if q.ID == otherID && q.Installation == "another" {
			foundHistory = true
		}
	}
	if !foundHistory {
		t.Fatal("different installation partition not reported")
	}
	// Pausing never removes a queue from the completion set.
	id, err := s.EnsureQueue(t.Context(), QueueKey{Installation: "7", Repository: "owner/repo", BaseBranch: "paused"})
	must(t, err)
	appendEvents(t, s, id, queue.PauseRequested{Reason: "operator", Detail: "fixture pause"})
	check(false)
	// A different installation of the same repository cannot be omitted from
	// the repository-wide proof, even when its current worker is absent.
	_, err = s.EnsureQueue(t.Context(), QueueKey{Installation: "other", Repository: "owner/repo", BaseBranch: "paused"})
	must(t, err)
	check(false)
	result, err := s.RepositoryAuthorityCompletion(t.Context(), request, AuthorityBranchInventory{DefaultBranch: "main", DefaultTip: "tip"})
	if err == nil || result.Complete {
		t.Fatal("missing discovery accepted")
	}
	if result, err := s.RepositoryAuthorityCompletion(t.Context(), request, AuthorityBranchInventory{DefaultBranch: "main", Branches: inventory}); err == nil || result.Complete {
		t.Fatal("missing governing tip accepted")
	}
	// The live default tip advanced: every pin bound to the old commit is stale
	// until it rebinds, even with matching digests and adoption.
	if got := reason("main", "", AuthorityBranchInventory{DefaultBranch: "main", DefaultTip: "advanced", Branches: inventory}); got != "governing_commit_stale" {
		t.Fatalf("advanced tip reason %q", got)
	}
	// A pinned partition under another installation cannot be certified by this
	// installation's discovery.
	addWith(QueueKey{Installation: "8", Repository: "owner/repo", BaseBranch: "main"}, request.Digest, "", true)
	foreign, err := s.RepositoryAuthorityCompletion(t.Context(), request, current())
	must(t, err)
	foundForeign := false
	for _, q := range foreign.Queues {
		if q.Installation == "8" && q.Base == "main" {
			foundForeign = q.Reason == "installation_inventory_unavailable"
		}
	}
	if !foundForeign || foreign.Complete {
		t.Fatalf("foreign installation pin: %+v", foreign)
	}
	if got := reason("paused", "", current()); got != "pin_missing" {
		t.Fatalf("paused reason %q", got)
	}
	// A lane partition with an adopted, matching pin is still unsupported, and
	// a pinned but unadopted partition never counts as propagated.
	addWith(QueueKey{Installation: "7", Repository: "owner/repo", BaseBranch: "main", Lane: "priority"}, request.Digest, "", true)
	if got := reason("main", "priority", current()); got != "unsupported_lane" {
		t.Fatalf("lane reason %q", got)
	}
	if got := reason("release/v2", "", current()); got != "complete" {
		t.Fatalf("release/v2 before unadopted repin %q", got)
	}
	addWith(QueueKey{Installation: "7", Repository: "owner/repo", BaseBranch: "release/v2"}, request.Digest, "", false)
	if got := reason("release/v2", "", current()); got != "adoption_missing" {
		t.Fatalf("unadopted reason %q", got)
	}
}

func TestAuthorityDowngradeGate(t *testing.T) {
	t.Run("landing_checks_recorded only", func(t *testing.T) {
		s := newStore(t)
		id := ensureQueue(t, s, "")
		raw, err := os.ReadFile("../queue/testdata/wire/v1/event_landing_checks_recorded.json")
		must(t, err)
		event, err := queue.DecodeEvent(raw)
		must(t, err)
		appendEvents(t, s, id, event)
		reader := &JournalReader{store: s, timeout: time.Minute}
		if err := reader.CheckAuthorityDowngrade(t.Context()); !errors.Is(err, ErrAuthorityDowngradeRefused) {
			t.Fatalf("landing checks only downgrade: %v", err)
		}
	})
	s := newStore(t)
	id := ensureQueue(t, s, "")
	reader := &JournalReader{store: s, timeout: time.Minute}
	must(t, reader.CheckAuthorityDowngrade(t.Context()))
	raw, err := os.ReadFile("../queue/testdata/wire/v1/event_policy_authority_applied.json")
	must(t, err)
	e, err := queue.DecodeEvent(raw)
	must(t, err)
	pin := e.(queue.PolicyAuthorityApplied)
	// Persist a valid v7 snapshot without appending any authority journal event.
	state := queue.Apply(queue.State{}, queue.ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest})
	state = queue.Apply(state, pin)
	snapshot, err := queue.EncodeSnapshot(state)
	must(t, err)
	_, err = queue.DecodeSnapshot(snapshot)
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
	appendEvents(t, s, id, e)
	// Retirement and mode rollback cannot erase retained journal compatibility.
	appendEvents(t, s, id, queue.LifecycleChanged{State: "retired", Detail: "fixture lifecycle"})
	if reader.CheckAuthorityDowngrade(t.Context()) == nil {
		t.Fatal("journal downgrade accepted")
	}
}

func TestAuthorityDowngradeRejectsDependencyWaitsWithheld(t *testing.T) {
	s := newStore(t)
	id := ensureQueue(t, s, "")
	appendEvents(t, s, id, queue.DependencyWaitsWithheld{PRs: []queue.BatchPR{{PR: 2, HeadSHA: "head"}}})

	var kind string
	must(t, s.pool.QueryRow(t.Context(), `SELECT type FROM events WHERE queue_id=$1 ORDER BY id DESC LIMIT 1`, id).Scan(&kind))
	if kind != "dependency_waits_withheld" {
		t.Fatalf("stored event kind = %q", kind)
	}

	reader := &JournalReader{store: s, timeout: time.Minute}
	if err := reader.CheckAuthorityDowngrade(t.Context()); !errors.Is(err, ErrAuthorityDowngradeRefused) {
		t.Fatalf("dependency waits withheld downgrade: %v", err)
	}
}

// Frozen DecodeEvent type set from addec7d^, before ADR 0008 PR 1.
// Compare against the decoder itself so a new type cannot evade the gate merely
// by omitting a fixture. Baseline types may also acquire incompatible fields;
// the source/cursor checks cover those separately.
func TestAuthorityDowngradeEventCoverage(t *testing.T) {
	baseline := strings.Fields(`change_revision_observed batch_runs_observed check_cancellation_requested check_cancellation_deferred check_cancellation_reported chained_test_ref_built batches_invalidated batch_diagnosed chained_batch_started checks_observed check_rerun_requested check_rerun_dispatch_progress check_rerun_dispatched checks_failure_confirmed lifecycle_changed landing_cleanup_policy_recorded config_applied fast_forward_merge_commits_blocked fast_forward_batch_selected fast_forward_ref_built fast_forward_blocked fence_initialized pr_metadata_observed pr_observed landing_decision_recorded approval_gate_rejected approval_inputs_changed change_accountability_observed change_intent_observed approval_receipt_recorded review_stance_observed approval_eligibility_observed change_classified outside_push_recorded base_moved check_provenance_observed checks_completed checks_reopened checks_refreshed pr_closed enqueue_requested dequeue_requested refresh_requested pause_requested resume_requested rules_blocked rules_cleared block_source_blocked block_source_cleared rules_source_blocked rules_source_cleared batch_started rebase_composition_observed batch_failed test_ref_built test_ref_build_failed landing_started landed head_cleanup_requested landing_abandoned dequeued action_completed action_poisoned`)
	file, err := parser.ParseFile(token.NewFileSet(), "../queue/wire.go", nil, 0)
	must(t, err)
	var added []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "DecodeEvent" {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			clause, ok := node.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, expr := range clause.List {
				literal, ok := expr.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				name, err := strconv.Unquote(literal.Value)
				must(t, err)
				if !slices.Contains(baseline, name) {
					added = append(added, name)
				}
			}
			return true
		})
	}
	slices.Sort(added)
	listed := slices.DeleteFunc(slices.Clone(authorityDowngradeEventTypes), func(kind string) bool { return kind == "dependency_composition_recorded" })
	slices.Sort(listed)
	if !slices.Equal(added, listed) {
		t.Fatalf("ADR event boundary drift: decoder additions %v; downgrade gate %v", added, listed)
	}
}

func TestAuthorityCompletionRequireComplete(t *testing.T) {
	c := AuthorityCompletion{Complete: true, Queues: []AuthorityQueueCompletion{{Reason: "complete"}}}
	must(t, c.RequireComplete())
	for _, reason := range []string{"", "digest_mismatch", "unknown"} {
		c.Queues[0].Reason = reason
		if c.RequireComplete() == nil {
			t.Fatalf("accepted row reason %q", reason)
		}
	}
}
