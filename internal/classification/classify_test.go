package classification

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

var complete = forge.DiffCoverage{Completeness: forge.DiffComplete}

func basis(t *testing.T, paths ...string) (cp.ClassificationInputs, forge.RevisionDiff, forge.GoverningFiles) {
	t.Helper()
	i := cp.ClassificationInputs{ChangeID: "change", Revision: 1, Base: strings.Repeat("a", 40), Head: strings.Repeat("b", 40), MergeBase: strings.Repeat("c", 40), GoverningCommit: strings.Repeat("d", 40), Generation: 1, PolicyDigest: "policy"}
	d := forge.RevisionDiff{BaseSHA: i.Base, HeadSHA: i.Head, MergeBaseSHA: i.MergeBase, Semantics: forge.DiffSemantics, Inventory: complete, Hunks: complete}
	for _, p := range paths {
		d.Files = append(d.Files, forge.DiffFile{OldPath: p, NewPath: p, OldMode: "100644", NewMode: "100644", OldObjectID: strings.Repeat("e", 40), NewObjectID: strings.Repeat("f", 40), BinaryKnown: true, Rename: forge.DiffNotRenamed, HunkCoverage: complete})
	}
	g, err := forge.CollectGoverningFiles(context.Background(), forge.GoverningFilesRequest{CommitSHA: i.GoverningCommit, Paths: paths, PathsComplete: true}, func(_ context.Context, p string, _ int) (forge.GoverningFile, error) {
		return forge.GoverningFile{Path: p, Coverage: complete}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return i, d, g
}
func classify(t *testing.T, i cp.ClassificationInputs, d forge.RevisionDiff, g forge.GoverningFiles, s Settings) Evidence {
	t.Helper()
	e, err := Classify(i, d, g, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func TestClassificationMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, path   string
		mutate       func(*forge.DiffFile)
		docs, policy cp.Truth
	}{
		{"docs", "docs/a.md", nil, cp.True, cp.False},
		{"code", "a.go", nil, cp.False, cp.False},
		{"whole-path-not-basename", "src/docs/a.txt", nil, cp.False, cp.False},
		{"root-double-star", "README.md", nil, cp.True, cp.False},
		{"newline-path", "docs/a\nb.md", nil, cp.True, cp.False},
		{"binary", "docs/a.md", func(f *forge.DiffFile) { f.Binary = true }, cp.Unknown, cp.False},
		{"unread-blob", "docs/a.md", func(f *forge.DiffFile) { f.BinaryKnown = false }, cp.Unknown, cp.False},
		{"executable-mode-change", "docs/a.md", func(f *forge.DiffFile) { f.NewMode = "100755" }, cp.Unknown, cp.False},
		{"symlink", "docs/a.md", func(f *forge.DiffFile) { f.NewMode = "120000" }, cp.Unknown, cp.False},
		{"submodule", "docs/a.md", func(f *forge.DiffFile) { f.Submodule = true; f.NewMode = "160000" }, cp.Unknown, cp.False},
		{"unpaired", "docs/a.md", func(f *forge.DiffFile) { f.Rename = forge.DiffRenameUnpaired }, cp.Unknown, cp.False},
		{"deleted-policy", ".openmerge.yml", func(f *forge.DiffFile) { f.NewPath = ""; f.NewMode = ""; f.NewObjectID = "" }, cp.False, cp.True},
		{"renamed-away-policy", "CODEOWNERS", func(f *forge.DiffFile) { f.NewPath = "docs/owners.md"; f.Rename = forge.DiffExactRename }, cp.False, cp.True},
		{"renamed-to-policy", "docs/a.md", func(f *forge.DiffFile) { f.NewPath = "docs/CODEOWNERS"; f.Rename = forge.DiffExactRename }, cp.True, cp.True},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i, d, g := basis(t, tc.path)
			if tc.mutate != nil {
				tc.mutate(&d.Files[0])
			}
			e := classify(t, i, d, g, Settings{Docs: []string{"docs/**", "**/*.md"}})
			c := e.Classification
			if c.Label("docs") != tc.docs || c.Label("policy") != tc.policy {
				t.Fatalf("facts %v", c.Facts)
			}
			if c.CodeOwnersKnown || c.PolicyOwnersKnown || c.Label("unknown") != cp.True {
				t.Fatal("4b seam became ready")
			}
		})
	}
	for _, p := range []string{".openmerge.yml", ".openmerge.yaml", ".github/openmerge.yml", ".github/openmerge.yaml", ".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS", ".gitattributes", "a/b/.gitattributes", ".github/workflows/ci.yml", "auth/login.go"} {
		t.Run(p, func(t *testing.T) {
			i, d, g := basis(t, p)
			e := classify(t, i, d, g, Settings{Sensitive: []string{"auth/**"}})
			if e.Classification.Label("policy") != cp.True {
				t.Fatal(p)
			}
		})
	}
}
func TestRepeatedInputsAndVersionGolden(t *testing.T) {
	i, d, g := basis(t, "docs/a.md")
	a := classify(t, i, d, g, Settings{Docs: []string{"docs/**"}})
	b := classify(t, i, d, g, Settings{Docs: []string{"docs/**"}})
	if !reflect.DeepEqual(a, b) || a.InputFingerprint != b.InputFingerprint {
		t.Fatal("nondeterministic")
	}
	raw, err := os.ReadFile("testdata/version.txt")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != Version {
		t.Fatalf("version drift: %s", Version)
	}
}
func TestTruncatedAllDocs(t *testing.T) {
	t.Run("truncated-all-docs", func(t *testing.T) {
		i, d, g := basis(t, "docs/a.md")
		d.Inventory = forge.DiffCoverage{Completeness: forge.DiffIncomplete, Reasons: []forge.DiffFailureReason{forge.DiffFileLimit}}
		e := classify(t, i, d, g, Settings{Docs: []string{"docs/**"}})
		c := e.Classification
		if c.Label("docs") != cp.Unknown || c.PolicyOwnersKnown || c.PathsSafe || c.HunksComplete {
			t.Fatal(c)
		}
		for _, tc := range []struct {
			match cp.Match
			want  cp.Truth
		}{{cp.Match{Paths: []string{"docs/**"}}, cp.Unknown}, {cp.Match{Touches: []string{"docs/**"}}, cp.True}, {cp.Match{Touches: []string{"auth/**"}}, cp.Unknown}} {
			got, err := cp.MatchRule(tc.match, c, cp.HumanAccount)
			if err != nil || got != tc.want {
				t.Fatalf("%v %v", got, err)
			}
		}
		result := cp.Evaluate(cp.Policy{Approvals: &cp.Approvals{Default: cp.Requirement{Selector: cp.AnyWriter, Count: 1}, Rules: []cp.Rule{{When: cp.Match{Labels: []string{"docs"}}, Require: cp.Requirement{Selector: cp.None}}}}}, cp.Input{Classification: c, Current: cp.ReceiptIdentity{ChangeID: i.ChangeID, Revision: i.Revision, Generation: i.Generation, Base: i.Base, Head: i.Head, MergeBase: i.MergeBase, PolicyDigest: i.PolicyDigest}})
		if result.Allowed || !strings.Contains(strings.Join(result.Reasons, " "), "split the change") {
			t.Fatalf("%+v", result)
		}
	})
}
func TestIndependentHunksAndUnreadableAttributes(t *testing.T) {
	i, d, g := basis(t, "gen/a.go")
	g.Coverage = forge.DiffCoverage{Completeness: forge.DiffIncomplete, Reasons: []forge.DiffFailureReason{forge.DiffReadFailure}}
	g.Attributes[0].Coverage = g.Coverage
	d.Hunks = g.Coverage
	d.Files[0].HunkCoverage = g.Coverage
	d.Files[0].Omitted = []forge.DiffHunkRange{{OldStart: 1, OldLines: 30, NewStart: 2, NewLines: 40, Reason: forge.DiffOmittedRangeLimit, Count: 33}}
	c := classify(t, i, d, g, Settings{}).Classification
	if !c.InventoryComplete || !c.PathsSafe || c.HunksComplete || c.Label("generated") != cp.Unknown {
		t.Fatal(c)
	}
	if c.Files[0].Hunks.Omitted[0].Count != 33 || c.Files[0].Hunks.Omitted[0].OldLines != 30 {
		t.Fatal("expanded omissions")
	}
	c = classify(t, i, d, g, Settings{Generated: []string{"gen/**"}}).Classification
	if c.Label("generated") != cp.True {
		t.Fatal("path half lost")
	}
}
func TestGoverningDefaultBranchAndPolicyWeakening(t *testing.T) {
	i, d, g := basis(t, ".openmerge.yml", "docs/CODEOWNERS")
	g.Config[0].Present = true
	g.Config[0].ObjectID = "governing-config"
	g.Config[0].Content = "default branch policy"
	e := classify(t, i, d, g, Settings{Docs: []string{"docs/**"}})
	if e.Classification.Inputs.ConfigBlob != "governing-config" || e.Classification.Inputs.GoverningCommit == i.Base || e.Classification.Label("policy") != cp.True {
		t.Fatal(e)
	}
	i.GoverningCommit = i.Head
	if _, err := Classify(i, d, g, Settings{}, nil); err == nil {
		t.Fatal("accepted head governing mismatch")
	}
	t.Run("docs-codeowners-owner-required", func(t *testing.T) {
		if e.Classification.Files[1].Facts["policy"] != cp.True {
			t.Fatal("owners path not policy")
		}
		t.Skip("effective owners and positive owner-required acceptance belong to Stage A 4b")
	})
}
func TestInvalidationAndRewind(t *testing.T) {
	i, d, g := basis(t, "docs/a.md")
	e := classify(t, i, d, g, Settings{Docs: []string{"docs/**"}})
	old := SemanticInputs(e)
	t.Run("classifier-version-only-refresh", func(t *testing.T) {
		next := SemanticInputs(e)
		next.ClassifierVersion = "next-version"
		r := RefreshReasons(old, next, false, true)
		n, err := NextGeneration(1, r)
		if err != nil || n != 2 || !reflect.DeepEqual(r, []string{"classifier_version"}) {
			t.Fatal(r, n, err)
		}
	})
	if len(RefreshReasons(old, old, true, true)) != 0 {
		t.Fatal("forward landing invalidated")
	}
	g.Config[0].Present = true
	g.Config[0].ObjectID = "format-only"
	g.CommitSHA = strings.Repeat("e", 40)
	i.GoverningCommit = g.CommitSHA
	provenance := classify(t, i, d, g, Settings{Docs: []string{"docs/**"}})
	if len(RefreshReasons(old, SemanticInputs(provenance), false, true)) != 0 || e.InputFingerprint == provenance.InputFingerprint {
		t.Fatal("Q7 provenance")
	}
	r := RefreshReasons(old, old, true, false)
	generation, err := NextGeneration(1, r)
	if err != nil || generation != 2 {
		t.Fatal(r)
	}
	// Unchanged head, rewritten base: shared code re-enters the diff.
	i.Base = strings.Repeat("f", 40)
	i.Generation = generation
	d.BaseSHA = i.Base
	d.Files = append(d.Files, forge.DiffFile{NewPath: "shared.go", NewMode: "100644", BinaryKnown: true, Rename: forge.DiffNotRenamed, HunkCoverage: complete})
	rewound := classify(t, i, d, g, Settings{Docs: []string{"docs/**"}})
	if rewound.Classification.Label("docs") != cp.False || rewound.Classification.Inputs.Head != e.Classification.Inputs.Head {
		t.Fatal("stale docs classification survived rewind")
	}
	for _, mutate := range []func(*RefreshInputs){func(n *RefreshInputs) { n.PolicyDigest = "new" }, func(n *RefreshInputs) { n.Owners = map[string]string{"CODEOWNERS": "new"} }, func(n *RefreshInputs) { n.Attributes = map[string]string{".gitattributes": "new"} }} {
		next := SemanticInputs(e)
		mutate(&next)
		if len(RefreshReasons(old, next, false, true)) == 0 {
			t.Fatal("missing semantic invalidation")
		}
	}
	n, err := NextGeneration(1, nil)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if _, err := NextGeneration(^uint64(0), []string{"refresh"}); err == nil {
		t.Fatal("overflow")
	}
}
func TestDTOInputNotMutated(t *testing.T) {
	i, d, g := basis(t, "docs/a.md")
	before, _ := json.Marshal([]any{i, d, g})
	_ = classify(t, i, d, g, Settings{})
	after, _ := json.Marshal([]any{i, d, g})
	if string(before) != string(after) {
		t.Fatal("mutated inputs")
	}
}

func TestFailureReasonsRetainPolicyHits(t *testing.T) {
	for _, reason := range []forge.DiffFailureReason{forge.DiffReadFailure, forge.DiffFileLimit, forge.DiffByteLimit, forge.DiffMissingObject, forge.DiffNoMergeBase, forge.DiffMultipleMergeBases, forge.DiffShallowHistory, forge.DiffAlteredHistory, forge.DiffAncestryLimit} {
		t.Run(string(reason), func(t *testing.T) {
			i, d, g := basis(t, ".openmerge.yml")
			d.Inventory = forge.DiffCoverage{Completeness: forge.DiffIncomplete, Reasons: []forge.DiffFailureReason{reason}}
			c := classify(t, i, d, g, Settings{}).Classification
			if c.Label("unknown") != cp.True || c.Label("policy") != cp.True || c.Label("docs") != cp.Unknown {
				t.Fatal(c.Facts)
			}
		})
	}
}
func TestRejectUnpinnedAndUnsupportedInputs(t *testing.T) {
	for _, mutate := range []func(*cp.ClassificationInputs, *forge.RevisionDiff, *forge.GoverningFiles){
		func(i *cp.ClassificationInputs, _ *forge.RevisionDiff, _ *forge.GoverningFiles) { i.ChangeID = "" },
		func(i *cp.ClassificationInputs, _ *forge.RevisionDiff, _ *forge.GoverningFiles) {
			i.GoverningCommit = ""
		},
		func(i *cp.ClassificationInputs, _ *forge.RevisionDiff, _ *forge.GoverningFiles) { i.Base = "different" },
		func(_ *cp.ClassificationInputs, d *forge.RevisionDiff, _ *forge.GoverningFiles) {
			d.Semantics = "older"
		},
		func(_ *cp.ClassificationInputs, _ *forge.RevisionDiff, g *forge.GoverningFiles) {
			g.Semantics = "older"
		},
	} {
		i, d, g := basis(t, "a.go")
		mutate(&i, &d, &g)
		if _, err := Classify(i, d, g, Settings{}, nil); err == nil {
			t.Fatal("accepted unpinned inputs")
		}
	}
	i, d, g := basis(t, "a.go")
	if _, err := Classify(i, d, g, Settings{Docs: []string{"trailing\\"}}, nil); err == nil {
		t.Fatal("accepted malformed pattern")
	}
}

type attributeStub struct{}

func (attributeStub) Generated(p string, _ forge.GoverningFiles) cp.Truth {
	if p == "a.go" {
		return cp.False
	}
	return cp.Unknown
}
func (attributeStub) Owners(_ *cp.Classification, _ forge.GoverningFiles) {}
func TestUnknownRelevantInputsNeverBecomeFalse(t *testing.T) {
	i, d, g := basis(t, "a.go", "docs/b.md")
	d.Files[1].BinaryKnown = false
	e, err := Classify(i, d, g, Settings{Docs: []string{"docs/**"}}, attributeStub{})
	if err != nil {
		t.Fatal(err)
	}
	if e.Classification.Label("generated") != cp.Unknown || e.Classification.Label("docs") != cp.Unknown {
		t.Fatal(e.Classification.Facts)
	}
}
