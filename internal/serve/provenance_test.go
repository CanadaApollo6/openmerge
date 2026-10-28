package serve

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/classification"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	p "github.com/CanadaApollo6/openmerge/internal/provenance"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func provenanceGit(t *testing.T, dir, input string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"--git-dir=" + dir}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Dir(dir), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Human", "GIT_AUTHOR_EMAIL=human@example.test", "GIT_COMMITTER_NAME=Human", "GIT_COMMITTER_EMAIL=human@example.test", "GIT_AUTHOR_DATE=2026-10-27T12:00:00Z", "GIT_COMMITTER_DATE=2026-10-27T12:00:00Z"}
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func provenanceGitFixture(t *testing.T) (string, string, string, string, []string) {
	t.Helper()
	scratch := t.TempDir()
	dir := filepath.Join(scratch, "objects.git")
	provenanceGit(t, dir, "", "init", "--bare", "--template=", dir)
	tree := provenanceGit(t, dir, "", "hash-object", "-w", "-t", "tree", "--stdin")
	base := provenanceGit(t, dir, "base\n", "commit-tree", tree)
	agent := provenanceGit(t, dir, "agent\n\nEntire-Checkpoint: abcdef123456\nAgent-Session: codex:session\n", "commit-tree", tree, "-p", base)
	human := provenanceGit(t, dir, "human\n", "commit-tree", tree, "-p", base)
	return dir, scratch, tree, base, []string{agent, human}
}

func provenanceState(t *testing.T, pass *provenancePass, base string, heads ...string) (queue.State, []provenanceMember) {
	t.Helper()
	s := queue.State{}
	members := []provenanceMember{}
	for i, head := range heads {
		pr := i + 1
		id, err := queue.PRChangeID("7", "Owner/Repo", "main", "", pr)
		if err != nil {
			t.Fatal(err)
		}
		b := queue.ApprovalBasis{ChangeID: id, Revision: 1, Head: head, ReadID: fmt.Sprintf("read-%d", pr), At: time.Date(2026, 10, 27, 13, 0, 0, 0, time.UTC)}
		s = queue.Apply(s, queue.ChangeRevisionObserved{PR: pr, ChangeID: id, Revision: 1, HeadSHA: head, Basis: queue.RevisionObservationBasis{BaseSHA: base, At: b.At.Add(-time.Hour)}})
		full := forge.DiffCoverage{Completeness: forge.DiffComplete}
		in := cp.ClassificationInputs{ChangeID: string(id), Revision: 1, Generation: 1, Base: base, Head: head, MergeBase: base, GoverningCommit: base, PolicyDigest: pass.policy.Digest}
		evidence, err := classification.Classify(in, forge.RevisionDiff{BaseSHA: base, HeadSHA: head, MergeBaseSHA: base, Semantics: forge.DiffSemantics, Inventory: full, Hunks: full}, forge.GoverningFiles{CommitSHA: base, Semantics: forge.DiffSemantics, Coverage: full}, classification.Settings{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		s = queue.Apply(s, queue.ChangeClassified{Evidence: evidence})
		author := cp.Identity{ID: "github.com:user:1", Login: "author", Kind: cp.HumanAccount}
		s = queue.Apply(s, queue.Accountability(b, author, author, nil, pass.policy.Digest))
		s = queue.Apply(s, queue.Intent(b, "title", "body"))
		fresh := queue.ApprovalEligibilityObserved{Basis: b, Known: true, Reasons: []string{}, Actors: map[string]cp.Actor{}, Groups: map[string]cp.IdentitySet{}, OwnerPools: map[string]cp.IdentitySet{}}
		members = append(members, provenanceMember{Pin: queue.BatchPR{PR: pr, HeadSHA: head}, Fresh: fresh, PR: forge.PullRequest{Number: pr, HeadSHA: head}})
	}
	return s, members
}

func provenanceTestPass(t *testing.T, dir string, limits p.InventoryLimits) *provenancePass {
	t.Helper()
	when, err := p.ParseCondition("#agent-commits > 0")
	if err != nil {
		t.Fatal(err)
	}
	pass, err := newProvenancePass(config.Queue{MergeWhen: condition.All()}, cp.Policy{Digest: strings.Repeat("9", 64), Approvals: &cp.Approvals{Default: cp.Requirement{Selector: cp.AnyWriter, Count: 1}}}, []p.Rule{{When: when, Approvals: &cp.Requirement{Selector: cp.Human, Count: 1}}}, []p.IdentityRule{{ID: "human", Mailbox: "human@example.test", Class: p.Human}}, p.NewParserPolicy(provenanceGit(t, dir, "", "version")), limits)
	if err != nil {
		t.Fatal(err)
	}
	return pass
}

func TestProvenanceNoNetworkPassAndCheckpointMovement(t *testing.T) {
	dir, scratch, _, base, pair := provenanceGitFixture(t)
	pass := provenanceTestPass(t, dir, p.DefaultInventoryLimits())
	state, members := provenanceState(t, pass, base, pair...)
	before, err := queue.EncodeSnapshot(state)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pass.evaluate(t.Context(), state, "Owner/Repo", "main", base, dir, scratch, members, nil)
	if err != nil || len(got.Included) != 2 {
		t.Fatal(got, err)
	}
	if got.Members[0].Layers.Rules[0].Trigger != cp.True || got.Members[1].Layers.Rules[0].Trigger != cp.False {
		t.Fatal("member declarations pooled", got)
	}
	if got.Members[0].Layers.Result == cp.True {
		t.Fatal("missing human review passed")
	}
	// Reviews may remain speculative at CI. Checkpoint refs have no influence
	// over this MVP's observation or receipt; no remote exists in this fixture.
	ref := "refs/entire/checkpoints/56/abcdef123456"
	provenanceGit(t, dir, "", "update-ref", ref, pair[0])
	provenanceGit(t, dir, "", "update-ref", ref, pair[1])
	refreshed, err := pass.evaluate(t.Context(), state, "Owner/Repo", "main", base, dir, scratch, members, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range got.Members {
		if got.Members[i].Identity != refreshed.Members[i].Identity {
			t.Fatal("checkpoint churned receipt")
		}
		if err := brief.ValidateReceipt(got.Members[i].Identity, refreshed.Members[i].Brief); err != nil {
			t.Fatal(err)
		}
	}
	after, err := queue.EncodeSnapshot(state)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("pass wrote projection", err)
	}
	if refs := provenanceGit(t, dir, "", "for-each-ref", "--format=%(refname)"); refs != ref {
		t.Fatal("unexpected refs", refs)
	}
}

func TestProvenancePerChangePreCIExclusion(t *testing.T) {
	dir, scratch, tree, base, pair := provenanceGitFixture(t)
	oversized := provenanceGit(t, dir, "more work\n", "commit-tree", tree, "-p", pair[0])
	limits := p.DefaultInventoryLimits()
	limits.SourceCommits = 1
	pass := provenanceTestPass(t, dir, limits)
	nul := provenanceGit(t, dir, "tree "+tree+"\nparent "+base+"\nauthor Human <human@example.test> 1793102400 +0000\ncommitter Human <human@example.test> 1793102400 +0000\n\nHuman\x00message\n", "hash-object", "--literally", "-w", "-t", "commit", "--stdin")
	for _, tc := range []struct {
		name, head string
		want       cp.Truth
		resource   string
	}{
		{"terminal source limit", oversized, cp.False, "source_commits"},
		{"NUL pending", nul, cp.Unknown, "message_nul"},
		{"git failure pending", strings.Repeat("f", 40), cp.Unknown, "commit_shape"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, members := provenanceState(t, pass, base, tc.head, pair[1])
			got, err := pass.evaluate(t.Context(), state, "Owner/Repo", "main", base, dir, scratch, members, nil)
			if err != nil || len(got.Included) != 1 || got.Included[0] != members[1].Pin {
				t.Fatal("member did not progress independently", got, err)
			}
			if got.Members[0].PreCI != tc.want || got.Members[0].Inventory.Diagnostic.Resource != tc.resource || got.Members[0].Identity.Recorded() || got.Members[0].Layers.Result == cp.True {
				t.Fatal(got)
			}
		})
	}
	// Alternates remain fail-closed at the slice-2 boundary, even an empty file.
	if err := os.MkdirAll(filepath.Join(dir, "objects/info"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "objects/info/alternates"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	state, members := provenanceState(t, pass, base, pair[1])
	got, err := pass.evaluate(t.Context(), state, "Owner/Repo", "main", base, dir, scratch, members, nil)
	if err != nil || len(got.Included) != 0 || got.Members[0].PreCI != cp.Unknown {
		t.Fatal("alternates escaped", got, err)
	}
}

func TestProvenancePublicLoaderAndNestedBypassRejected(t *testing.T) {
	for _, fragment := range []string{
		"provenance: {requirements: [{when: ['#agent-commits > 0'], require: {approvals: {human: true, count: 1}}}]}",
		"enter_when: [{or: [draft, {not: {and: ['provenance = human']}}]}]",
		"merge_when: [{or: [check-success = ci, {not: {or: ['#agent-commits > 0']}}]}]",
		"approvals: {default: any_writer, rules: [{when: {provenance: human}, require: none}]}",
	} {
		data := []byte("version: 1\nqueues:\n- name: main\n  " + fragment + "\n")
		if _, _, err := config.Load(data); err == nil {
			t.Fatal("public activation", fragment)
		}
	}
	atom, err := p.ParseCondition("provenance = human")
	if err != nil {
		t.Fatal(err)
	}
	bypass := condition.Any(condition.All(), condition.Not(condition.Any(atom)))
	for _, entry := range []bool{false, true} {
		cfg := config.Queue{MergeWhen: bypass}
		if entry {
			cfg = config.Queue{EnterWhen: bypass}
		}
		_, err := newProvenancePass(cfg, cp.Policy{Digest: strings.Repeat("9", 64)}, []p.Rule{{When: condition.All(), Conditions: &atom}}, nil, p.NewParserPolicy("git version 2.56.0"), p.DefaultInventoryLimits())
		if err == nil {
			t.Fatal("internal nested bypass")
		}
	}
}

// provenanceSeamViolations checks production syntax across the repository. Only
// the dormant seam may construct a pass/policy or import its evaluator.
func provenanceSeamViolations(path string, file *ast.File) []string {
	const prefix = "github.com/CanadaApollo6/openmerge/internal/"
	seam := path == "internal/serve/provenance.go"
	var violations []string
	var constructorBody *ast.BlockStmt
	if seam {
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "newProvenancePass" {
				constructorBody = fn.Body
			}
		}
	}
	inConstructor := func(n ast.Node) bool {
		return constructorBody != nil && n.Pos() >= constructorBody.Pos() && n.End() <= constructorBody.End()
	}
	policyAliases := map[string]bool{}
	var policyDeclaration *ast.Ident
	if strings.HasPrefix(path, "internal/provenance/") && file.Name.Name == "provenance" {
		policyAliases["."] = true
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "NewPolicy" {
				policyDeclaration = fn.Name
			}
		}
	}
	for _, imp := range file.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			violations = append(violations, "invalid import path")
			continue
		}
		if importPath == prefix+"provenanceeval" && !seam && path != "internal/store/side_append.go" {
			violations = append(violations, "runtime imported provenance evaluator")
		}
		if importPath == prefix+"provenance" {
			name := "provenance"
			if imp.Name != nil {
				name = imp.Name.Name
			}
			policyAliases[name] = true
		}
		if strings.HasPrefix(path, "internal/provenanceeval/") {
			if importPath == prefix+"store" || importPath == prefix+"journalreader" || strings.HasPrefix(importPath, prefix+"provenance/journal") {
				violations = append(violations, "writer dependency: "+importPath)
			}
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			switch fn := n.Fun.(type) {
			case *ast.Ident:
				if fn.Name == "newProvenancePass" {
					violations = append(violations, "runtime installed provenance")
				}
			case *ast.SelectorExpr:
				if fn.Sel.Name == "newProvenancePass" {
					violations = append(violations, "runtime installed provenance")
				}
			}
		case *ast.CompositeLit:
			if inConstructor(n) {
				return true
			}
			switch typ := n.Type.(type) {
			case *ast.Ident:
				if typ.Name == "provenancePass" {
					violations = append(violations, "runtime constructed provenance pass")
				}
			case *ast.SelectorExpr:
				if typ.Sel.Name == "provenancePass" {
					violations = append(violations, "runtime constructed provenance pass")
				}
			}
		case *ast.SelectorExpr:
			if pkg, ok := n.X.(*ast.Ident); ok && !inConstructor(n) && policyAliases[pkg.Name] && n.Sel.Name == "NewPolicy" {
				violations = append(violations, "runtime used provenance.NewPolicy")
			}
		case *ast.Ident:
			if !inConstructor(n) && policyAliases["."] && n.Name == "NewPolicy" && n != policyDeclaration {
				violations = append(violations, "runtime used provenance.NewPolicy")
			}
		}
		return true
	})
	return violations
}

func TestProvenanceSeamTripwire(t *testing.T) {
	for _, tc := range []struct {
		name, path, source string
		want               string
	}{
		{"constructor", "internal/serve/worker.go", `package serve; func f() { newProvenancePass() }`, "runtime installed provenance"},
		{"constructor in seam", "internal/serve/provenance.go", `package serve; func activate() { newProvenancePass() }`, "runtime installed provenance"},
		{"literal", "internal/serve/worker.go", `package serve; var pass = provenancePass{}`, "runtime constructed provenance pass"},
		{"literal outside constructor in seam", "internal/serve/provenance.go", `package serve; var pass = provenancePass{}`, "runtime constructed provenance pass"},
		{"policy outside constructor in seam", "internal/serve/provenance.go", `package serve; import p "github.com/CanadaApollo6/openmerge/internal/provenance"; var construct = p.NewPolicy`, "runtime used provenance.NewPolicy"},
		{"pointer literal", "internal/serve/worker.go", `package serve; var pass = &provenancePass{}`, "runtime constructed provenance pass"},
		{"importer outside internal", "cmd/openmerge/main.go", `package main; import _ "github.com/CanadaApollo6/openmerge/internal/provenanceeval"`, "runtime imported provenance evaluator"},
		{"policy constructor", "internal/serve/worker.go", `package serve; import "github.com/CanadaApollo6/openmerge/internal/provenance"; var policy = provenance.NewPolicy()`, "runtime used provenance.NewPolicy"},
		{"aliased policy value", "internal/serve/worker.go", `package serve; import p "github.com/CanadaApollo6/openmerge/internal/provenance"; var construct = p.NewPolicy`, "runtime used provenance.NewPolicy"},
		{"dot policy value", "internal/serve/worker.go", `package serve; import . "github.com/CanadaApollo6/openmerge/internal/provenance"; var construct = NewPolicy`, "runtime used provenance.NewPolicy"},
		{"same-package policy value", "internal/provenance/activate.go", `package provenance; var construct = NewPolicy`, "runtime used provenance.NewPolicy"},
		{"policy definition", "internal/provenance/policy.go", `package provenance; func NewPolicy() {}`, ""},
		{"writer", "internal/provenanceeval/evaluate.go", `package provenanceeval; import "github.com/CanadaApollo6/openmerge/internal/store"`, "writer dependency"},
		{"dormant seam", "internal/serve/provenance.go", `package serve; import p "github.com/CanadaApollo6/openmerge/internal/provenance"; import "github.com/CanadaApollo6/openmerge/internal/provenanceeval"; func newProvenancePass() { p.NewPolicy(); _ = &provenancePass{} }`, ""},
		{"unrelated policy", "internal/config/config.go", `package config; import "example.test/other"; var construct = other.NewPolicy`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), tc.path, tc.source, 0)
			if err != nil {
				t.Fatal(err)
			}
			got := provenanceSeamViolations(tc.path, file)
			if tc.want == "" && len(got) != 0 || tc.want != "" && !strings.Contains(strings.Join(got, "\n"), tc.want) {
				t.Fatalf("violations = %v, want %q", got, tc.want)
			}
		})
	}
}

func TestProvenanceSeamHasNoRuntimeCallerOrWriter(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		for _, violation := range provenanceSeamViolations(filepath.ToSlash(rel), file) {
			t.Errorf("%s: %s", rel, violation)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// A status-safe legacy Fact keeps omitting the optional declaration binding.
	raw, err := json.Marshal(brief.Fact{})
	if err != nil || strings.Contains(string(raw), "DeclarationDigest") {
		t.Fatal(string(raw), err)
	}
}

func TestProvenanceQueuePolicyEditsInvalidateIdentity(t *testing.T) {
	when, err := p.ParseCondition("#agent-commits > 0")
	if err != nil {
		t.Fatal(err)
	}
	rules := []p.Rule{{When: when, Approvals: &cp.Requirement{Selector: cp.Human, Count: 1}}}
	baseline := cp.Policy{Digest: strings.Repeat("9", 64), Approvals: &cp.Approvals{Default: cp.Requirement{Selector: cp.AnyWriter, Count: 1}}}
	cfg := config.Queue{MergeWhen: condition.All()}
	build := func(cfg config.Queue) *provenancePass {
		t.Helper()
		pass, err := newProvenancePass(cfg, baseline, rules, nil, p.NewParserPolicy("git version 2.56.0"), p.DefaultInventoryLimits())
		if err != nil {
			t.Fatal(err)
		}
		return pass
	}
	original := build(cfg)
	for _, edit := range []func(*config.Queue){
		func(c *config.Queue) { c.MergeWhen = condition.Not(condition.All()) },
		func(c *config.Queue) { c.EnterWhen = condition.Not(condition.All()) },
		func(c *config.Queue) { c.MergeMethod = queue.Rebase },
		func(c *config.Queue) { c.Batch.MaxSize = 2 },
	} {
		changed := cfg
		edit(&changed)
		if build(changed).policy.Digest == original.policy.Digest {
			t.Fatal("queue policy edit did not invalidate receipt policy identity")
		}
	}
	if baseline.Digest != strings.Repeat("9", 64) {
		t.Fatal("baseline mutated")
	}
}
