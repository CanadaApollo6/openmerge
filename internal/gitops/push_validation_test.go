package gitops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func validationOptions(t *testing.T, op forge.PushOperation, refs []forge.PushRefIntent, rules []authority.Rule) forge.PushOptions {
	t.Helper()
	evidence := authority.Observation{Inventory: authority.Visible, Findings: []authority.Finding{}, Coverage: []authority.Coverage{{Ref: "refs/heads/main", Visibility: authority.Visible, ProtectionIDs: []string{"repository_ruleset/one"}}}, Protections: []authority.Protection{{ID: "one", Source: "repository_ruleset", Target: "push", Enforcement: "active", TargetsVisibility: authority.Visible, RulesVisibility: authority.Visible, BypassVisibility: authority.Visible, Includes: []string{}, Excludes: []string{}, Conditions: []authority.Parameter{}, Rules: rules, BypassActors: []authority.BypassActor{{Kind: "app", ID: "42", Mode: "always"}}}}}
	fp, err := evidence.Fingerprint()
	must(t, err)
	c, err := forge.NewPushValidationContext(forge.PushBinding{Repository: testRepo, QueueKey: "fixture/main", GoverningCommit: strings.Repeat("a", 40), EffectiveDigest: strings.Repeat("b", 64), ProtectionBasis: 1, ProtectionFingerprint: fp}, op, refs, forge.PushRuleContext{DefaultBranch: "main", BaseRef: "refs/heads/main", AppID: "42", Observation: evidence})
	must(t, err)
	return forge.PushOptions{AuthorityRequired: true, Context: c, Guard: func(context.Context, forge.PushValidationContext) error { return nil }}
}

func spyPushes(t *testing.T, f *fixture) func() {
	t.Helper()
	record := filepath.Join(t.TempDir(), "pushes")
	wrapper := filepath.Join(t.TempDir(), "git")
	var evidenceDir string
	script := "for arg do\nif [ \"$arg\" = push ]; then\nprintf push >> " + shellQuote(record) + "\nfi\ndone\n"
	if root := os.Getenv("OPENMERGE_ADR0008_PUSH_EVIDENCE"); root != "" {
		evidenceDir = filepath.Join(root, strings.ReplaceAll(t.Name(), "/", "__"))
		must(t, os.MkdirAll(evidenceDir, 0755))
		// Preserve the exact bytes consumed by inspection, including rejected
		// objects later collected by cache maintenance. Only local fixtures use it.
		script += "kind=''\nlast=''\nfor arg do\ncase \"$arg\" in\ncommit) kind=commit ;;\nls-tree) kind=tree ;;\nesac\nlast=\"$arg\"\ndone\nif [ -n \"$kind\" ]; then\nfile=" + shellQuote(evidenceDir) + "/\"$last.$kind\"\n" + shellQuote(f.g.git) + " \"$@\" > \"$file\"\nstatus=$?\ncat \"$file\"\nexit \"$status\"\nfi\n"
	}
	writeExecutable(t, wrapper, script+"exec "+shellQuote(f.g.git)+" \"$@\"\n")
	f.g.git = wrapper
	before := gitCommand(t, f.remote, "", nil, "show-ref")
	if evidenceDir != "" {
		must(t, os.WriteFile(filepath.Join(evidenceDir, "before.refs"), []byte(before), 0600))
	}
	return func() {
		t.Helper()
		raw, err := os.ReadFile(record)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if len(raw) != 0 {
			t.Fatalf("rejected operation invoked push: %s", raw)
		}
		after := gitCommand(t, f.remote, "", nil, "show-ref")
		if before != after {
			t.Fatalf("remote refs changed\nbefore %s\nafter %s", before, after)
		}
		if evidenceDir != "" {
			must(t, os.WriteFile(filepath.Join(evidenceDir, "after.refs"), []byte(after), 0600))
			must(t, os.WriteFile(filepath.Join(evidenceDir, "push-count"), []byte("0\n"), 0600))
		}
	}
}

func TestAuthorityRejectsBeforeEveryPushFamily(t *testing.T) {
	for _, method := range []forge.MergeMethod{forge.Merge, forge.Squash, forge.Rebase, forge.FastForward} {
		t.Run("build/"+string(method), func(t *testing.T) {
			f := newFixture(t)
			assert := spyPushes(t, f)
			// The final commit of a multi-member build carries the batch trailer;
			// an intermediate commit does not. Fast-forward's marker is unsigned too.
			rule := authority.Rule{Kind: "required_signatures", Parameters: []authority.Parameter{}}
			opts := validationOptions(t, forge.PushBuild, []forge.PushRefIntent{{Ref: "refs/heads/openmerge/queue/main/rejected"}}, []authority.Rule{rule})
			_, err := f.g.BuildTestRef(t.Context(), testRepo, forge.BuildRequest{Ref: "openmerge/queue/main/rejected", BaseSHA: f.base, PRs: f.prs[:2], Method: method, BatchID: "rejected"}, opts)
			if err == nil {
				t.Fatal("accepted unsigned prepared objects")
			}
			assert()
		})
	}
	for _, op := range []forge.PushOperation{forge.PushEnsureFence, forge.PushAdvanceFence, forge.PushLand, forge.PushDeleteBranch} {
		t.Run(string(op), func(t *testing.T) {
			f := newFixture(t)
			ctx := t.Context()
			fence := ""
			tested := ""
			if op == forge.PushAdvanceFence || op == forge.PushLand {
				var err error
				fence, err = f.g.EnsureFence(ctx, testRepo, testFence, forge.PushOptions{})
				must(t, err)
			}
			if op == forge.PushLand {
				tested = f.build(forge.Squash, f.prs[:1], "land")
			}
			if op == forge.PushDeleteBranch {
				f.push("topic", f.heads[1], false)
			}
			assert := spyPushes(t, f)
			intents := []forge.PushRefIntent{{Ref: "refs/heads/" + testFence, OldSHA: fence}}
			rule := authority.Rule{Kind: "required_signatures", Parameters: []authority.Parameter{}}
			if op == forge.PushLand {
				intents = []forge.PushRefIntent{{Ref: "refs/heads/main", OldSHA: f.base, NewSHA: tested}, {Ref: "refs/heads/" + testFence, OldSHA: fence}}
			}
			if op == forge.PushDeleteBranch {
				intents = []forge.PushRefIntent{{Ref: "refs/heads/topic", OldSHA: f.heads[1], Delete: true}}
				rule.Kind = "future_deletion_rule"
			}
			opts := validationOptions(t, op, intents, []authority.Rule{rule})
			var err error
			switch op {
			case forge.PushEnsureFence:
				_, err = f.g.EnsureFence(ctx, testRepo, testFence, opts)
			case forge.PushAdvanceFence:
				_, err = f.g.AdvanceFence(ctx, testRepo, testFence, fence, "abandon/x", opts)
			case forge.PushLand:
				_, err = f.g.Land(ctx, testRepo, forge.LandRequest{Base: "main", ExpectedBaseSHA: f.base, TestedSHA: tested, Fence: testFence, ExpectedFence: fence, NextFenceID: "land/x", PRs: f.prs[:1]}, opts)
			case forge.PushDeleteBranch:
				err = f.g.DeleteBranch(ctx, testRepo, "topic", f.heads[1], opts)
			}
			if err == nil {
				t.Fatal("rejected rule passed")
			}
			assert()
		})
	}
}

func TestAuthorityActualIntermediateCommitAndAtomicFence(t *testing.T) {
	for _, method := range []forge.MergeMethod{forge.Merge, forge.Squash, forge.Rebase} {
		t.Run(string(method), func(t *testing.T) {
			f := newFixture(t)
			assert := spyPushes(t, f)
			rule := authority.Rule{Kind: "commit_message_pattern", Parameters: []authority.Parameter{{Name: "operator", Values: []string{`"contains"`}}, {Name: "pattern", Values: []string{`"OpenMerge-Batch: middle"`}}}}
			opts := validationOptions(t, forge.PushBuild, []forge.PushRefIntent{{Ref: "refs/heads/test-middle"}}, []authority.Rule{rule})
			_, err := f.g.BuildTestRef(t.Context(), testRepo, forge.BuildRequest{Ref: "test-middle", BaseSHA: f.base, PRs: f.prs[:2], Method: method, BatchID: "middle"}, opts)
			if err == nil {
				t.Fatal("intermediate commit skipped")
			}
			assert()
		})
	}
	f := newFixture(t)
	initial, err := f.g.EnsureFence(t.Context(), testRepo, testFence, forge.PushOptions{})
	must(t, err)
	tested := f.build(forge.Squash, f.prs[:1], "atomic")
	assert := spyPushes(t, f)
	rule := authority.Rule{Kind: "commit_message_pattern", Parameters: []authority.Parameter{{Name: "operator", Values: []string{`"contains"`}}, {Name: "pattern", Values: []string{`"OpenMerge-Batch"`}}}}
	opts := validationOptions(t, forge.PushLand, []forge.PushRefIntent{{Ref: "refs/heads/main", OldSHA: f.base, NewSHA: tested}, {Ref: "refs/heads/" + testFence, OldSHA: initial}}, []authority.Rule{rule})
	_, err = f.g.Land(t.Context(), testRepo, forge.LandRequest{Base: "main", ExpectedBaseSHA: f.base, TestedSHA: tested, Fence: testFence, ExpectedFence: initial, NextFenceID: "land/atomic", PRs: f.prs[:1]}, opts)
	if err == nil {
		t.Fatal("fence message skipped")
	}
	assert()
}

func TestAuthorityGuardAndReadFirstNoOps(t *testing.T) {
	f := newFixture(t)
	ref := "refs/heads/" + testFence
	opts := validationOptions(t, forge.PushEnsureFence, []forge.PushRefIntent{{Ref: ref}}, []authority.Rule{})
	calls := 0
	sentinel := errors.New("policy changed during preparation")
	opts.Guard = func(context.Context, forge.PushValidationContext) error {
		calls++
		if calls == 2 {
			return sentinel
		}
		return nil
	}
	assert := spyPushes(t, f)
	_, err := f.g.EnsureFence(t.Context(), testRepo, testFence, opts)
	if !errors.Is(err, sentinel) || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	assert()
	f = newFixture(t)
	initial, err := f.g.EnsureFence(t.Context(), testRepo, testFence, forge.PushOptions{})
	must(t, err)
	assert = spyPushes(t, f)
	missing := forge.PushOptions{AuthorityRequired: true}
	got, err := f.g.EnsureFence(t.Context(), testRepo, testFence, missing)
	must(t, err)
	if got != initial {
		t.Fatal("existing fence changed")
	}
	must(t, f.g.DeleteBranch(t.Context(), testRepo, "absent", f.base, missing))
	must(t, f.g.DeleteBranch(t.Context(), testRepo, "main", f.heads[1], missing))
	assert()
}

func TestAuthorityAcceptedObjectIsTheInspectedOID(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	top := f.build(forge.Squash, f.prs[:1], "accepted")
	s, unlock, err := f.g.open(ctx, testRepo)
	must(t, err)
	p, err := s.preparedPush(ctx, forge.PushBuild, []forge.PushRef{{Ref: "refs/heads/accepted", NewSHA: top}})
	must(t, err)
	unlock()
	if p.Refs[0].NewSHA != top {
		t.Fatal("inventory rebuilt a preview")
	}
	for _, c := range p.Refs[0].Commits {
		message := gitCommand(t, f.remote, "", nil, "cat-file", "commit", c.OID)
		if !strings.Contains(message, strings.TrimSpace(c.Message)) {
			t.Fatal("inventory message differs from object")
		}
	}
	// The very OID inspected above can be accepted and published without rebuild.
	opts := validationOptions(t, forge.PushBuild, []forge.PushRefIntent{{Ref: "refs/heads/accepted", NewSHA: top}}, []authority.Rule{})
	must(t, opts.ValidateBeforePush(ctx, testRepo, p))
}

func TestAuthorityAcceptedBuildInspectsOnceBeforePublishing(t *testing.T) {
	for _, method := range []forge.MergeMethod{forge.Merge, forge.Squash, forge.Rebase, forge.FastForward} {
		t.Run(string(method), func(t *testing.T) {
			f := newFixture(t)
			record := filepath.Join(t.TempDir(), "commands")
			wrapper := filepath.Join(t.TempDir(), "git")
			writeExecutable(t, wrapper, "printf 'CALL\\n' >> "+shellQuote(record)+"\nprintf '%s\\n' \"$@\" >> "+shellQuote(record)+"\nexec "+shellQuote(f.g.git)+" \"$@\"\n")
			f.g.git = wrapper
			ref := "refs/heads/accepted-build"
			opts := validationOptions(t, forge.PushBuild, []forge.PushRefIntent{{Ref: ref}}, []authority.Rule{})
			top, err := f.g.BuildTestRef(t.Context(), testRepo, forge.BuildRequest{Ref: ref, BaseSHA: f.base, PRs: f.prs[:1], Method: method, BatchID: "accepted"}, opts)
			must(t, err)
			raw, err := os.ReadFile(record)
			must(t, err)
			log := string(raw)
			inspected := strings.Index(log, "\ncat-file\ncommit\n"+top+"\n")
			pushed := strings.Index(log, "\npush\n")
			if inspected < 0 || pushed < inspected || !strings.Contains(log, top+":"+ref) {
				t.Fatalf("prepared OID not inspected before publication: %s", log)
			}
			expected := 1
			if method == forge.Rebase && !f.g.explicitBase {
				expected++
			} // existing unreferenced replay-base object
			if count := strings.Count(log, "\ncommit-tree\n"); count != expected {
				t.Fatalf("commit constructions %d, expected %d without preview rebuild", count, expected)
			}
			got := gitCommand(t, f.remote, "", nil, "rev-parse", ref)
			if got != top {
				t.Fatal("published different OID")
			}
		})
	}
}

func TestAuthorityPathSizeAndLinearRulesUseRealObjects(t *testing.T) {
	for _, kind := range []string{"file_size_restriction", "file_path_restriction", "required_linear_history"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			method := forge.Squash
			params := []authority.Parameter{}
			switch kind {
			case "file_size_restriction":
				head := f.commit(f.heads[1], "large blob", map[string]string{"large.bin": strings.Repeat("x", (1<<20)+1)})
				gitCommand(t, f.source, "", nil, "push", f.remote, head+":"+headRef(1))
				f.prs[0].HeadSHA = head
				params = []authority.Parameter{{Name: "max_file_size", Values: []string{"1"}}}
			case "file_path_restriction":
				params = []authority.Parameter{{Name: "restricted_file_paths/~20", Values: []string{`"pr-1.txt"`}}}
			case "required_linear_history":
				method = forge.Merge
			}
			assert := spyPushes(t, f)
			opts := validationOptions(t, forge.PushBuild, []forge.PushRefIntent{{Ref: "refs/heads/reject-content"}}, []authority.Rule{{Kind: kind, Parameters: params}})
			_, err := f.g.BuildTestRef(t.Context(), testRepo, forge.BuildRequest{Ref: "reject-content", BaseSHA: f.base, PRs: f.prs[:1], Method: method, BatchID: "content"}, opts)
			if err == nil {
				t.Fatal("invalid actual object accepted")
			}
			assert()
		})
	}
}
