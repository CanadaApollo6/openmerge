package gitops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
			opts := validationOptions(t, forge.PushBuild, []forge.PushRefIntent{{Ref: "refs/heads/openmerge/queue/main/rejected", BuildBaseSHA: f.base}}, []authority.Rule{rule})
			prs := f.prs[:2]
			if method == forge.FastForward {
				prs = f.prs[:1]
			} // base -> contributor -> marker is a valid chain.
			_, err := f.g.BuildTestRef(t.Context(), testRepo, forge.BuildRequest{Ref: "openmerge/queue/main/rejected", BaseSHA: f.base, PRs: prs, Method: method, BatchID: "rejected"}, opts)
			if !errors.Is(err, forge.ErrInvalidInput) || !strings.Contains(err.Error(), "commit lacks verified signature") {
				t.Fatalf("expected signature validation rejection, got %v", err)
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
			opts := validationOptions(t, forge.PushBuild, []forge.PushRefIntent{{Ref: "refs/heads/test-middle", BuildBaseSHA: f.base}}, []authority.Rule{rule})
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
	p, err := s.preparedPush(ctx, forge.PushBuild, []forge.PushRef{{Ref: "refs/heads/accepted", NewSHA: top, CreationBaseSHA: f.base}})
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
	opts := validationOptions(t, forge.PushBuild, []forge.PushRefIntent{{Ref: "refs/heads/accepted", NewSHA: top, BuildBaseSHA: f.base}}, []authority.Rule{})
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
			opts := validationOptions(t, forge.PushBuild, []forge.PushRefIntent{{Ref: ref, BuildBaseSHA: f.base}}, []authority.Rule{})
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
	for _, kind := range []string{"max_file_size", "file_path_restriction", "required_linear_history"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			method := forge.Squash
			params := []authority.Parameter{}
			switch kind {
			case "max_file_size":
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
			opts := validationOptions(t, forge.PushBuild, []forge.PushRefIntent{{Ref: "refs/heads/reject-content", BuildBaseSHA: f.base}}, []authority.Rule{{Kind: kind, Parameters: params}})
			_, err := f.g.BuildTestRef(t.Context(), testRepo, forge.BuildRequest{Ref: "reject-content", BaseSHA: f.base, PRs: f.prs[:1], Method: method, BatchID: "content"}, opts)
			if err == nil {
				t.Fatal("invalid actual object accepted")
			}
			assert()
		})
	}
}

func TestBuildCreationBoundaryRejectsUnprovenSources(t *testing.T) {
	for _, name := range []string{"caller-selected-base", "absent-target-base", "failed-ancestry-proof"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			base := f.base
			rules := []authority.Rule{}
			want := ""
			switch name {
			case "caller-selected-base":
				// An arbitrary request base cannot change the immutable build
				// base authenticated before dispatch and hide introduced paths.
				base = f.heads[1]
				want = "missing or mismatched authority context"
				rules = []authority.Rule{{Kind: "file_path_restriction", Parameters: []authority.Parameter{{Name: "restricted_file_paths/~20", Values: []string{`"pr-1.txt"`}}}}}
			case "absent-target-base":
				gitCommand(t, f.remote, "", nil, "update-ref", "-d", "refs/heads/main")
				want = "creation boundary base is absent"
			case "failed-ancestry-proof":
				wrapper := filepath.Join(t.TempDir(), "git")
				writeExecutable(t, wrapper, "for arg do\nif [ \"$arg\" = --is-ancestor ]; then\necho 'injected ancestry proof failure' >&2\nexit 1\nfi\ndone\nexec "+shellQuote(f.g.git)+" \"$@\"\n")
				f.g.git = wrapper
				want = "prepared boundary proof"
			}
			assert := spyPushes(t, f)
			defer assert()
			ref := "refs/heads/reject-boundary"
			opts := validationOptions(t, forge.PushBuild, []forge.PushRefIntent{{Ref: ref, BuildBaseSHA: f.base}}, rules)
			_, err := f.g.BuildTestRef(t.Context(), testRepo, forge.BuildRequest{Ref: ref, BaseSHA: base, PRs: f.prs[1:2], Method: forge.Squash, BatchID: "boundary"}, opts)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("expected creation boundary rejection %q, got %v", want, err)
			}
		})
	}
}

func TestPreparedModeOnlyChangeRecordsPathWithoutPostImage(t *testing.T) {
	f := newFixture(t)
	tip := f.diffCommit(f.base, "make executable", map[string]diffEntry{"base.txt": {mode: "100755", content: "base\n"}})
	before := gitCommand(t, f.source, "", nil, "rev-parse", f.base+":base.txt")
	after := gitCommand(t, f.source, "", nil, "rev-parse", tip+":base.txt")
	if before != after {
		t.Fatal("mode-only fixture changed the blob")
	}
	s, unlock, err := f.g.open(t.Context(), testRepo)
	must(t, err)
	defer unlock()
	p, err := s.preparedPush(t.Context(), forge.PushBuild, []forge.PushRef{{Ref: "refs/heads/mode-only", NewSHA: tip, CreationBaseSHA: f.base}})
	must(t, err)
	if len(p.Refs[0].Commits) != 1 {
		t.Fatalf("expected one mode-only commit: %+v", p)
	}
	c := p.Refs[0].Commits[0]
	if len(c.Diffs) != 1 || !slices.Equal(c.Diffs[0].Paths, []string{"base.txt"}) {
		t.Fatalf("mode-only changed path lost: %+v", c.Diffs)
	}
	if len(c.Files) != 0 {
		t.Fatalf("mode-only change invented post-images: %+v", c.Files)
	}
}

func TestPreparedChangedPathsAndCreationBoundary(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	old := f.diffCommit(f.base, "existing restricted files", map[string]diffEntry{"secret.pem": {mode: "100644", content: "secret"}, "large.bin": {mode: "100644", content: strings.Repeat("x", 2<<20)}})
	f.push("main", old, false)
	unrelated := f.diffCommit(old, "unrelated change", map[string]diffEntry{"base.txt": {mode: "100644", content: "changed"}})
	renamed := f.diffCommit(old, "rename restricted source", map[string]diffEntry{"secret.pem": {}, "renamed.txt": {mode: "100644", content: "secret"}})
	deleted := f.diffCommit(old, "delete restricted source", map[string]diffEntry{"secret.pem": {}})
	right := f.diffCommit(old, "other merge parent", map[string]diffEntry{"secret.pem": {mode: "100644", content: "other secret"}})
	merged := f.diffCommit(unrelated, "merge keeps first parent tree", nil, right)
	s, unlock, err := f.g.open(ctx, testRepo)
	must(t, err)
	defer unlock()
	for _, tc := range []struct {
		name, tip string
		reject    bool
	}{{"unrelated", unrelated, false}, {"rename", renamed, true}, {"deletion", deleted, true}, {"merge", merged, true}} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := s.preparedPush(ctx, forge.PushBuild, []forge.PushRef{{Ref: "refs/heads/new-queue", NewSHA: tc.tip, CreationBaseSHA: old}})
			must(t, err)
			if len(p.Refs[0].Boundary) != 1 || p.Refs[0].Boundary[0] != old {
				t.Fatalf("unexpected frontier %v", p.Refs[0].Boundary)
			}
			rule := authority.Rule{Kind: "file_path_restriction", Parameters: []authority.Parameter{{Name: "restricted_file_paths/~20", Values: []string{`"secret.pem"`}}}}
			opts := validationOptions(t, forge.PushBuild, []forge.PushRefIntent{{Ref: p.Refs[0].Ref, NewSHA: tc.tip, BuildBaseSHA: old}}, []authority.Rule{rule})
			if err := opts.ValidateBeforePush(ctx, testRepo, p); (err != nil) != tc.reject {
				t.Fatalf("changed-path validation: %v", err)
			}
			if tc.name == "unrelated" {
				if len(p.Refs[0].Commits) != 1 || len(p.Refs[0].Commits[0].Files) != 1 || p.Refs[0].Commits[0].Files[0].Path != "base.txt" {
					t.Fatalf("creation inventoried existing history/tree: %+v", p)
				}
				size := validationOptions(t, forge.PushBuild, []forge.PushRefIntent{{Ref: p.Refs[0].Ref, NewSHA: tc.tip, BuildBaseSHA: old}}, []authority.Rule{{Kind: "max_file_size", Parameters: []authority.Parameter{{Name: "max_file_size", Values: []string{"1"}}}}})
				must(t, size.ValidateBeforePush(ctx, testRepo, p))
			}
			if tc.name == "merge" {
				for _, c := range p.Refs[0].Commits {
					if c.OID == merged {
						if len(c.Diffs) != 2 || len(c.Diffs[0].Paths) != 0 || !slices.Contains(c.Diffs[1].Paths, "secret.pem") {
							t.Fatalf("lost parent comparison: %+v", c.Diffs)
						}
					}
				}
			}
		})
	}
}

func TestDeletionInventoryDoesNotHydrateOldObject(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	s, unlock, err := f.g.open(ctx, testRepo)
	must(t, err)
	defer unlock()
	// This valid OID is deliberately absent from both cache and remote. Fetching
	// it would fail, but a deletion's empty content inventory needs no old object.
	p, err := s.preparedPush(ctx, forge.PushDeleteBranch, []forge.PushRef{{Ref: "refs/heads/topic", OldSHA: strings.Repeat("e", 40)}})
	must(t, err)
	if len(p.Refs[0].Commits) != 0 || len(p.Refs[0].Boundary) != 0 {
		t.Fatal("deletion introduced evidence")
	}
}

func TestAuthorityFinalGuardEveryPushSite(t *testing.T) {
	for _, op := range []forge.PushOperation{forge.PushBuild, forge.PushLand, forge.PushEnsureFence, forge.PushAdvanceFence, forge.PushDeleteBranch} {
		for _, reject := range []bool{false, true} {
			t.Run(string(op)+"/reject="+strconv.FormatBool(reject), func(t *testing.T) {
				f := newFixture(t)
				ctx := t.Context()
				var fence, tested string
				if op == forge.PushAdvanceFence || op == forge.PushLand {
					var err error
					fence, err = f.g.EnsureFence(ctx, testRepo, testFence, forge.PushOptions{})
					must(t, err)
				}
				if op == forge.PushLand {
					tested = f.build(forge.Squash, f.prs[:1], "guard-land")
				}
				if op == forge.PushDeleteBranch {
					f.push("topic", f.heads[1], false)
				}
				intents := []forge.PushRefIntent{{Ref: "refs/heads/" + testFence, OldSHA: fence}}
				switch op {
				case forge.PushBuild:
					intents = []forge.PushRefIntent{{Ref: "refs/heads/guard-build", BuildBaseSHA: f.base}}
				case forge.PushLand:
					intents = []forge.PushRefIntent{{Ref: "refs/heads/main", OldSHA: f.base, NewSHA: tested}, {Ref: "refs/heads/" + testFence, OldSHA: fence}}
				case forge.PushDeleteBranch:
					intents = []forge.PushRefIntent{{Ref: "refs/heads/topic", OldSHA: f.heads[1], Delete: true}}
				}
				opts := validationOptions(t, op, intents, []authority.Rule{})
				var assert func()
				if reject {
					assert = spyPushes(t, f)
				}
				armed, first := filepath.Join(t.TempDir(), "armed"), filepath.Join(t.TempDir(), "first-command")
				wrapper := filepath.Join(t.TempDir(), "git")
				writeExecutable(t, wrapper, "if [ -f "+shellQuote(armed)+" ] && [ ! -f "+shellQuote(first)+" ]; then\nkind=other\nfor arg do\n[ \"$arg\" != push ] || kind=push\ndone\nprintf '%s' \"$kind\" > "+shellQuote(first)+"\nfi\nexec "+shellQuote(f.g.git)+" \"$@\"\n")
				f.g.git = wrapper
				calls := 0
				sentinel := errors.New("durable source changed after preparation")
				opts.Guard = func(context.Context, forge.PushValidationContext) error {
					calls++
					if calls == 2 {
						if reject {
							return sentinel
						}
						must(t, os.WriteFile(armed, nil, 0600))
					}
					return nil
				}
				var err error
				switch op {
				case forge.PushBuild:
					_, err = f.g.BuildTestRef(ctx, testRepo, forge.BuildRequest{Ref: "guard-build", BaseSHA: f.base, PRs: f.prs[:1], Method: forge.Squash, BatchID: "guard"}, opts)
				case forge.PushLand:
					_, err = f.g.Land(ctx, testRepo, forge.LandRequest{Base: "main", ExpectedBaseSHA: f.base, TestedSHA: tested, Fence: testFence, ExpectedFence: fence, NextFenceID: "land/guard", PRs: f.prs[:1]}, opts)
				case forge.PushEnsureFence:
					_, err = f.g.EnsureFence(ctx, testRepo, testFence, opts)
				case forge.PushAdvanceFence:
					_, err = f.g.AdvanceFence(ctx, testRepo, testFence, fence, "abandon/guard", opts)
				case forge.PushDeleteBranch:
					err = f.g.DeleteBranch(ctx, testRepo, "topic", f.heads[1], opts)
				}
				if calls != 2 {
					t.Fatalf("guard calls=%d err=%v", calls, err)
				}
				if reject {
					if !errors.Is(err, sentinel) {
						t.Fatal(err)
					}
					assert()
				} else {
					must(t, err)
					command, err := os.ReadFile(first)
					must(t, err)
					if string(command) != "push" {
						t.Fatalf("I/O between final guard and push: %s", command)
					}
				}
			})
		}
	}
}

func TestAuthorityBuildCreationBoundsLargeHistoryAndCleanedRef(t *testing.T) {
	f := newFixture(t)
	// Fast-import creates more history than the introduced-commit cap without
	// thousands of subprocesses. Only the new build belongs in its inventory.
	var input strings.Builder
	for i := 0; i <= forge.MaxPushCommits; i++ {
		fmt.Fprintf(&input, "commit refs/heads/long-history\ncommitter Fixture <fixture@localhost> %d +0000\ndata 7\nhistory\n", 1700000000+i)
		if i == 0 {
			fmt.Fprintf(&input, "from %s\n", f.base)
		}
		input.WriteByte('\n')
	}
	gitCommand(t, f.source, input.String(), nil, "fast-import", "--quiet")
	f.base = gitCommand(t, f.source, "", nil, "rev-parse", "refs/heads/long-history")
	f.push("main", f.base, false)
	head := f.commit(f.base, "new PR", map[string]string{"fresh.txt": "fresh"})
	gitCommand(t, f.source, "", nil, "push", "--force", f.remote, head+":"+headRef(1))
	f.prs[0].HeadSHA = head
	ref := "refs/heads/cleaned-build"
	for attempt := 0; attempt < 2; attempt++ {
		opts := validationOptions(t, forge.PushBuild, []forge.PushRefIntent{{Ref: ref, BuildBaseSHA: f.base}}, []authority.Rule{})
		_, err := f.g.BuildTestRef(t.Context(), testRepo, forge.BuildRequest{Ref: ref, BaseSHA: f.base, PRs: f.prs[:1], Method: forge.Squash, BatchID: "large-history"}, opts)
		must(t, err)
		gitCommand(t, f.remote, "", nil, "update-ref", "-d", ref)
	}
}

func TestPreparedPathBudgetsArePerCommitRealGit(t *testing.T) {
	f := newFixture(t)
	blob := gitCommand(t, f.source, "", nil, "rev-parse", f.base+":base.txt")
	// Construct wide trees directly so this boundary test does not spawn a
	// process for each path or repeatedly rewrite fast-import's flat tree.
	wideTree := func(executable int) string {
		var tree strings.Builder
		for i := 0; i <= forge.MaxPushPaths; i++ {
			mode := "100644"
			if i < executable {
				mode = "100755"
			}
			fmt.Fprintf(&tree, "%s blob %s\tp%06d\n", mode, blob, i)
		}
		return gitCommand(t, f.source, tree.String(), nil, "mktree")
	}
	baseTree := wideTree(0)
	base := gitCommand(t, f.source, "wide base\n", nil, "commit-tree", baseTree, "-p", f.base)
	middle := gitCommand(t, f.source, "first bounded\n", nil, "commit-tree", wideTree(60_000), "-p", base)
	good := gitCommand(t, f.source, "second bounded\n", nil, "commit-tree", baseTree, "-p", middle)
	bad := gitCommand(t, f.source, "overflow\n", nil, "commit-tree", wideTree(forge.MaxPushPaths+1), "-p", base)
	f.push("wide-base", base, false)
	f.push("two-bounded", good, false)
	f.push("one-overflow", bad, false)
	s, unlock, err := f.g.open(t.Context(), testRepo)
	must(t, err)
	defer unlock()
	p, err := s.preparedPush(t.Context(), forge.PushBuild, []forge.PushRef{{Ref: "refs/heads/paths", NewSHA: good, CreationBaseSHA: base}})
	must(t, err)
	if len(p.Refs[0].Commits) != 2 {
		t.Fatalf("expected two introduced commits, got %d", len(p.Refs[0].Commits))
	}
	for _, c := range p.Refs[0].Commits {
		if len(c.Diffs[0].Paths) != 60_000 || len(c.Files) != 0 {
			t.Fatalf("mode-only inventory: paths=%d files=%d", len(c.Diffs[0].Paths), len(c.Files))
		}
	}
	_, err = s.preparedPush(t.Context(), forge.PushBuild, []forge.PushRef{{Ref: "refs/heads/paths-overflow", NewSHA: bad, CreationBaseSHA: base}})
	if err == nil || !strings.Contains(err.Error(), "path budget exceeded") {
		t.Fatalf("expected independent commit path rejection, got %v", err)
	}
}

func TestDisabledAuthorityOptionsPreserveLegacyBuildBytesAndIO(t *testing.T) {
	f := newFixture(t)
	ref := "refs/heads/legacy-options"
	disabled := validationOptions(t, forge.PushBuild, []forge.PushRefIntent{{Ref: ref, BuildBaseSHA: f.base}}, []authority.Rule{{Kind: "required_signatures", Parameters: []authority.Parameter{}}})
	disabled.AuthorityRequired = false
	disabled.Guard = func(context.Context, forge.PushValidationContext) error {
		t.Fatal("disabled authority invoked guard")
		return nil
	}
	disabled.Warn = func([]forge.PushWarning) { t.Fatal("disabled authority warned") }
	var traces, tips []string
	for _, opts := range []forge.PushOptions{{}, disabled} {
		g := f.client(t, 0)
		record := filepath.Join(t.TempDir(), "commands")
		wrapper := filepath.Join(t.TempDir(), "git")
		writeExecutable(t, wrapper, "printf 'CALL\\n' >> "+shellQuote(record)+"\nprintf '%s\\n' \"$@\" >> "+shellQuote(record)+"\nexec "+shellQuote(g.git)+" \"$@\"\n")
		g.git = wrapper
		tip, err := g.BuildTestRef(t.Context(), testRepo, forge.BuildRequest{Ref: ref, BaseSHA: f.base, PRs: f.prs[:1], Method: forge.Squash, BatchID: "legacy-options"}, opts)
		must(t, err)
		raw, err := os.ReadFile(record)
		must(t, err)
		traces = append(traces, strings.ReplaceAll(string(raw), g.cfg.WorkDir, "CACHE"))
		tips = append(tips, tip)
	}
	if tips[0] != tips[1] || traces[0] != traces[1] {
		t.Fatalf("disabled options changed legacy build: OIDs=%v\nzero commands:\n%s\ndisabled commands:\n%s", tips, traces[0], traces[1])
	}
}
