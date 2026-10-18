package classification

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func present(p, content string) forge.GoverningFile {
	return forge.GoverningFile{Path: p, Content: content, Present: true, Mode: "100644", ObjectID: strings.Repeat("1", 40), Coverage: complete}
}
func put(files *[]forge.GoverningFile, p, content string) {
	for i := range *files {
		if (*files)[i].Path == p {
			(*files)[i] = present(p, content)
			return
		}
	}
	*files = append(*files, present(p, content))
}
func ownersSource(g *forge.GoverningFiles, p, content string) {
	put(&g.Codeowners, p, content)
	g.SelectedCodeowners = p
}

// Resolve only supplied PR 3a user/team observations, without forge calls.
func observedResolver(owner Owner) cp.IdentitySet {
	users := map[string]forge.UserIdentity{"alice": {ID: 1, Login: "Alice", Kind: forge.UserAccount}, "bob": {ID: 2, Login: "Bob", Kind: forge.UserAccount}}
	team := forge.TeamIdentity{ID: 10, OrganizationID: 20, Organization: "acme", Slug: "platform"}
	if owner.Kind == "user" {
		if user, ok := users[owner.Name]; ok {
			return cp.IdentitySet{IDs: []string{GitHubUserID(user.ID)}, Resolved: true, Fresh: true}
		}
	}
	if owner.Kind == "team" && owner.Name == strings.ToLower(team.Organization+"/"+team.Slug) {
		members := []forge.UserIdentity{users["alice"], users["bob"]}
		pool := cp.IdentitySet{Resolved: true, Fresh: true}
		for _, user := range members {
			pool.IDs = append(pool.IDs, GitHubUserID(user.ID))
		}
		return pool
	}
	if owner.Kind == "email" {
		panic("email passed to resolver")
	}
	return cp.IdentitySet{Reason: "team unreadable or identity not found"}
}
func derive(t *testing.T, i cp.ClassificationInputs, d forge.RevisionDiff, g forge.GoverningFiles, s Settings) Evidence {
	t.Helper()
	e, err := Classify(i, d, g, s, TreeDeriver{Resolve: observedResolver})
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func evaluation(c cp.Classification, r cp.Requirement, ids ...string) cp.Evaluation {
	i := c.Inputs
	in := cp.Input{Classification: c, Current: cp.ReceiptIdentity{ChangeID: i.ChangeID, Revision: i.Revision, Generation: i.Generation, Base: i.Base, Head: i.Head, MergeBase: i.MergeBase, PolicyDigest: i.PolicyDigest}, AuthorKind: cp.HumanAccount, Actors: map[string]cp.Actor{}, Stances: map[string]cp.Stance{}}
	for _, id := range ids {
		in.Actors[id] = cp.Actor{Identity: cp.Identity{ID: id, Login: "user" + id, Kind: cp.HumanAccount}, Grant: cp.WriteGrant{SubjectID: id, ReturnedID: id, Fresh: true, Write: cp.True}}
		in.Stances[id] = cp.Stance{UserID: id, Kind: cp.Approve, Revision: i.Revision, Head: i.Head, Source: cp.StanceSource{Channel: cp.NativeReview, ID: "review" + id}}
	}
	policy := cp.Policy{Approvals: &cp.Approvals{Default: cp.Requirement{Selector: cp.AnyWriter, Count: 1}, Rules: []cp.Rule{{When: cp.Match{Touches: []string{"**"}}, Require: r}}}}
	return cp.Evaluate(policy, in)
}
func coherent(t *testing.T, e cp.Evaluation) {
	t.Helper()
	for _, r := range e.Requirements {
		if strings.Contains(r.Reason, "owner_evidence_incoherent") {
			t.Fatalf("incoherent: %+v", e)
		}
	}
}
func testDocsCodeownersOwnerRequired(t *testing.T) {
	i, d, g := basis(t, "docs/CODEOWNERS")
	ownersSource(&g, "docs/CODEOWNERS", "* @Alice")
	c := derive(t, i, d, g, Settings{Docs: []string{"docs/**"}}).Classification
	if c.Label("docs") != cp.True || len(c.PolicyOwners) != 1 {
		t.Fatal(c)
	}
	e := evaluation(c, cp.Requirement{Selector: cp.None})
	coherent(t, e)
	if e.Allowed {
		t.Fatal("docs none removed mandatory owner requirement")
	}
	e = evaluation(c, cp.Requirement{Selector: cp.None}, "github.com:user:1")
	coherent(t, e)
	if !e.Allowed {
		t.Fatalf("owner approval rejected %+v", e)
	}
}

func TestOwnerPrecedenceAndCoverage(t *testing.T) {
	t.Run("code-owners-governing-precedence", func(t *testing.T) {
		for _, selected := range []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS", ""} {
			i, d, g := basis(t, "src/a.go")
			if selected != "" {
				ownersSource(&g, selected, "* @Alice")
			}
			c := derive(t, i, d, g, Settings{}).Classification
			if !c.CodeOwnersKnown || len(c.CodeOwners) != 1 {
				t.Fatal(c)
			}
			want := cp.OwnersResolved
			if selected == "" {
				want = cp.OwnersUncovered
			}
			if c.CodeOwners[0].Status != want {
				t.Fatal(c.CodeOwners)
			}
		}
		i, d, g := basis(t, "src/a.go")
		ownersSource(&g, "docs/CODEOWNERS", "* @Bob")
		put(&g.Codeowners, "CODEOWNERS", "* @Alice")
		g.SelectedCodeowners = "CODEOWNERS"
		put(&g.Codeowners, ".github/CODEOWNERS", "* @ACME/Platform")
		g.SelectedCodeowners = ".github/CODEOWNERS"
		c := derive(t, i, d, g, Settings{}).Classification
		if !slices.Equal(c.CodeOwners[0].Eligible.IDs, []string{"github.com:user:1", "github.com:user:2"}) {
			t.Fatal(c)
		}
		for _, mutate := range []func(*forge.GoverningFiles){
			func(g *forge.GoverningFiles) { g.Codeowners = g.Codeowners[1:] },
			func(g *forge.GoverningFiles) { g.Codeowners[0].Coverage = forge.DiffCoverage{} },
			func(g *forge.GoverningFiles) { g.SelectedCodeowners = "docs/CODEOWNERS" },
			func(g *forge.GoverningFiles) { g.Codeowners = append(g.Codeowners, g.Codeowners[0]) },
		} {
			bad := cloneGoverning(g)
			mutate(&bad)
			c := derive(t, i, d, bad, Settings{}).Classification
			if c.CodeOwnersKnown || c.Label("unknown") != cp.True || c.CodeOwners[0].Status != cp.OwnersUnresolved {
				t.Fatal(c)
			}
			e := evaluation(c, cp.Requirement{Selector: cp.CodeOwners}, "github.com:user:1", "github.com:user:2")
			coherent(t, e)
			if e.Allowed {
				t.Fatal("missing ownership coverage allowed")
			}
		}
	})
}

func TestAttributeStatesAndPrecedence(t *testing.T) {
	for _, tc := range []struct {
		root, nested string
		want         cp.Truth
	}{
		{"* linguist-generated", "", cp.True},
		{"* linguist-generated\n* -linguist-generated", "", cp.False},
		{"* -linguist-generated\n* linguist-generated", "", cp.True},
		{"* linguist-generated", "* -linguist-generated", cp.False},
		{"* linguist-generated", "* !linguist-generated", cp.False},
		{"* linguist-generated", "* linguist-generated=false", cp.False},
		{"* linguist-generated=false", "* linguist-generated=true", cp.True},
		{"* linguist-generated", "* binary", cp.True},
		{"* binary", "", cp.False},
		{`"\x61.go" linguist-generated`, "", cp.Unknown},
		{"* # linguist-generated", "", cp.Unknown},
		{"* linguist-generated # comment", "", cp.Unknown},
		{"* --text linguist-generated", "", cp.Unknown},
		{"* -diff=foo linguist-generated", "", cp.Unknown},
		{"gen/a.go\u00a0 linguist-generated", "", cp.False},
		{"gen/ linguist-generated", "", cp.False},
		{"gen/** linguist-generated", "", cp.True},
		{"[ab].go linguist-generated", "", cp.True},
		{"# comment\n\"gen/a.go\" linguist-generated", "", cp.True},
		{"* linguist-generated=maybe", "", cp.Unknown},
		{"[attr]generated linguist-generated\n* generated", "", cp.Unknown},
		{"[attr]binary linguist-generated\n* binary", "", cp.Unknown},
		{"* linguist-generated", "[attr]custom -linguist-generated", cp.Unknown},
		{"!a.go linguist-generated", "", cp.Unknown},
		{"\"bad pattern linguist-generated", "", cp.Unknown},
	} {
		t.Run(fmt.Sprintf("%s/%s", tc.root, tc.nested), func(t *testing.T) {
			i, d, g := basis(t, "gen/a.go")
			put(&g.Attributes, ".gitattributes", tc.root)
			put(&g.Attributes, "gen/.gitattributes", tc.nested)
			c := derive(t, i, d, g, Settings{}).Classification
			if c.Label("generated") != tc.want {
				t.Fatalf("%+v", c)
			}
			if tc.want == cp.Unknown && c.Label("unknown") != cp.True {
				t.Fatal("uncertainty lost")
			}
		})
	}
	i, d, g := basis(t, "a/b/c.go")
	put(&g.Attributes, ".gitattributes", "* linguist-generated")
	put(&g.Attributes, "a/.gitattributes", "* -linguist-generated")
	put(&g.Attributes, "a/b/.gitattributes", "* linguist-generated")
	slices.Reverse(g.Attributes)
	c := derive(t, i, d, g, Settings{}).Classification
	if c.Label("generated") != cp.True {
		t.Fatal("probe order replaced directory precedence")
	}
}

func TestRealDeriverEvaluateComposition(t *testing.T) {
	for _, tc := range []struct {
		name, path, entry                 string
		status                            cp.CodeOwnerStatus
		known, codeAllowed, policyAllowed bool
	}{
		{"resolved", "src/a.go", "* @Alice", cp.OwnersResolved, true, true, true},
		{"uncovered", "src/a.go", "other @Alice", cp.OwnersUncovered, true, true, true},
		{"code-owners-ownerless-evidence", "src/a.go", "* @Alice\nsrc/a.go", cp.OwnersOwnerless, true, true, true},
		{"email-only", "src/a.go", "* Ops@EXAMPLE.COM", cp.OwnersUnresolved, false, false, true},
		{"code-owners-unknown-team-evidence", "src/a.go", "* @acme/unreadable", cp.OwnersUnresolved, false, false, true},
		{"readable-team", "src/a.go", "* @ACME/Platform", cp.OwnersResolved, true, true, true},
		{"code-owners-mixed-entry-evidence", "src/a.go", "* @Alice Ops@EXAMPLE.COM", cp.OwnersUnresolved, false, false, true},
		{"mixed-team-user", "src/a.go", "* @Alice @acme/unreadable", cp.OwnersUnresolved, false, false, true},
		{"invalid-owner", "src/a.go", "* @Alice @@@", cp.OwnersUnresolved, false, false, true},
		{"policy-resolved", "docs/CODEOWNERS", "* @Alice", cp.OwnersResolved, true, true, true},
		{"policy-uncovered", "docs/CODEOWNERS", "other @Alice", cp.OwnersUncovered, true, false, false},
		{"policy-ownerless", "docs/CODEOWNERS", "*", cp.OwnersOwnerless, true, false, false},
		{"policy-email", "docs/CODEOWNERS", "* ops@example.com", cp.OwnersUnresolved, false, false, false},
		{"policy-team-unreadable", "docs/CODEOWNERS", "* @acme/unreadable", cp.OwnersUnresolved, false, false, false},
		{"policy-mixed-email", "docs/CODEOWNERS", "* @Alice ops@example.com", cp.OwnersUnresolved, false, false, true},
		{"policy-mixed-team", "docs/CODEOWNERS", "* @Alice @acme/unreadable", cp.OwnersUnresolved, false, false, false},
		{"spaces-ownerless", "src/a b.go", `src/a\ b.go`, cp.OwnersOwnerless, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i, d, g := basis(t, tc.path)
			ownersSource(&g, "CODEOWNERS", tc.entry)
			c := derive(t, i, d, g, Settings{}).Classification
			if c.CodeOwnersKnown != tc.known || len(c.CodeOwners) != 1 || c.CodeOwners[0].Status != tc.status {
				t.Fatalf("%+v", c)
			}
			for _, check := range []struct {
				r       cp.Requirement
				allowed bool
			}{{cp.Requirement{Selector: cp.CodeOwners}, tc.codeAllowed}, {cp.Requirement{Selector: cp.None}, tc.policyAllowed}} {
				e := evaluation(c, check.r, "github.com:user:1")
				coherent(t, e)
				if e.Allowed != check.allowed {
					t.Fatalf("allowed=%v want %v: %+v", e.Allowed, check.allowed, e)
				}
			}
			if !tc.known && c.Label("unknown") != cp.True {
				t.Fatal("unknown ownership lost")
			}
		})
	}
	t.Run("code-owners-delete-rename-evidence", func(t *testing.T) {
		for _, name := range []string{"rename", "deletion", "renamed-policy", "deleted-policy"} {
			t.Run(name, func(t *testing.T) {
				oldPath, newPath := "src/a.go", "src/b.go"
				if strings.Contains(name, "policy") {
					oldPath = "docs/CODEOWNERS"
				}
				if strings.Contains(name, "delet") {
					newPath = ""
				}
				i, d, g := basis(t, oldPath, newPathForProbe(newPath, oldPath))
				d.Files = d.Files[:1]
				d.Files[0].NewPath = newPath
				if newPath == "" {
					d.Files[0].NewMode = ""
					d.Files[0].NewObjectID = ""
				} else {
					d.Files[0].Rename = forge.DiffExactRename
				}
				ownersSource(&g, "CODEOWNERS", oldPath+" @Alice\n"+"src/b.go @Bob")
				c := derive(t, i, d, g, Settings{}).Classification
				want := 1
				if newPath != "" {
					want = 2
				}
				if len(c.CodeOwners) != want {
					t.Fatal(c.CodeOwners)
				}
				e := evaluation(c, cp.Requirement{Selector: cp.CodeOwners}, "github.com:user:1")
				coherent(t, e)
				if e.Allowed != (newPath == "") {
					t.Fatalf("per-file owners unioned: %+v", e)
				}
				e = evaluation(c, cp.Requirement{Selector: cp.CodeOwners}, "github.com:user:1", "github.com:user:2")
				coherent(t, e)
				if !e.Allowed {
					t.Fatalf("both owners rejected: %+v", e)
				}
				if newPath == "" && c.CodeOwners[0].Side != cp.OldSide {
					t.Fatal("deletion owner used newPath side")
				}
				if strings.Contains(name, "policy") && len(c.PolicyOwners) != want {
					t.Fatal(c.PolicyOwners)
				}
			})
		}
	})
}
func newPathForProbe(newPath, oldPath string) string {
	if newPath != "" {
		return newPath
	}
	return oldPath
}

func TestMissingAndUnreadableAttributesCannotRelaxPolicy(t *testing.T) {
	for _, name := range []string{"missing-nested", "unreadable-nested", "missing-root", "rename-old", "deletion-old"} {
		t.Run(name, func(t *testing.T) {
			i, d, g := basis(t, "gen/a.go", "other/b.go")
			d.Files = d.Files[:1]
			put(&g.Attributes, ".gitattributes", "* linguist-generated")
			switch name {
			case "missing-root":
				g.Attributes = g.Attributes[1:]
			case "unreadable-nested":
				g.Attributes[1].Coverage = forge.DiffCoverage{}
			case "rename-old":
				d.Files[0].NewPath = "other/b.go"
				d.Files[0].Rename = forge.DiffExactRename
				fallthrough
			case "missing-nested", "deletion-old":
				g.Attributes = slices.DeleteFunc(g.Attributes, func(f forge.GoverningFile) bool { return f.Path == "gen/.gitattributes" })
			}
			if name == "deletion-old" {
				d.Files[0].NewPath = ""
				d.Files[0].NewMode = ""
				d.Files[0].NewObjectID = ""
			}
			c := derive(t, i, d, g, Settings{}).Classification
			if c.Label("generated") != cp.Unknown || c.Label("unknown") != cp.True {
				t.Fatal(c.Facts)
			}
			in := cp.Input{Classification: c, Current: cp.ReceiptIdentity{ChangeID: i.ChangeID, Revision: i.Revision, Generation: i.Generation, Base: i.Base, Head: i.Head, MergeBase: i.MergeBase, PolicyDigest: i.PolicyDigest}}
			policy := cp.Policy{Approvals: &cp.Approvals{Default: cp.Requirement{Selector: cp.AnyWriter, Count: 1}, Rules: []cp.Rule{{When: cp.Match{Labels: []string{"generated"}}, Require: cp.Requirement{Selector: cp.None}}}}}
			result := cp.Evaluate(policy, in)
			coherent(t, result)
			if result.Allowed || len(result.Requirements) == 0 {
				t.Fatalf("unreadable attributes relaxed policy %+v", result)
			}
		})
	}
}

func TestOwnerFactsAndRepeatability(t *testing.T) {
	i, d, g := basis(t, "src/a.go", "docs/b.md")
	ownersSource(&g, "CODEOWNERS", "src/** @Alice\ndocs/** @ACME/Platform Ops@EXAMPLE.COM\nother @Bob")
	a := derive(t, i, d, g, Settings{})
	b := derive(t, i, d, g, Settings{})
	if !reflect.DeepEqual(a, b) || a.InputFingerprint != b.InputFingerprint {
		t.Fatal("unstable classification")
	}
	for label, want := range map[string]cp.Truth{"owned:user:alice": cp.True, "owned:team:acme/platform": cp.True, "owned:email:Ops@example.com": cp.True, "owned:user:bob": cp.False} {
		if a.Classification.Label(label) != want {
			t.Fatal(a.Classification.Facts)
		}
	}
	if a.Classification.Files[0].Facts["owned:team:acme/platform"] != cp.False || a.Classification.Files[1].Facts["owned:user:alice"] != cp.False {
		t.Fatal("per-file owner absences missing")
	}
	t.Run("code-owners-head-edits-ignored", func(t *testing.T) {
		// The head diff may replace CODEOWNERS/attributes, but has no policy content
		// input. Their governing blobs and ownership survive a self-weakening edit.
		i, d, g := basis(t, "CODEOWNERS", ".gitattributes")
		ownersSource(&g, "CODEOWNERS", "* @Alice")
		put(&g.Attributes, ".gitattributes", "* -linguist-generated")
		for j := range d.Files {
			d.Files[j].NewObjectID = strings.Repeat("2", 40)
			d.Files[j].Hunks = []forge.DiffHunk{{Text: "-* @Alice\n+*\n-* -linguist-generated\n+* linguist-generated\n"}}
		}
		c := derive(t, i, d, g, Settings{}).Classification
		if c.Label("generated") != cp.False || c.Inputs.OwnersBlob != strings.Repeat("1", 40) {
			t.Fatal(c)
		}
		e := evaluation(c, cp.Requirement{Selector: cp.None})
		coherent(t, e)
		if e.Allowed {
			t.Fatal("head weakened governing ownership")
		}
	})
}

type mutatingDeriver struct{ mutate func(*cp.Classification) }

func (mutatingDeriver) Generated(string, forge.GoverningFiles) cp.Truth       { return cp.True }
func (d mutatingDeriver) Owners(c *cp.Classification, _ forge.GoverningFiles) { d.mutate(c) }
func TestDeriverPostconditions(t *testing.T) {
	for _, mutate := range []func(*cp.Classification){
		func(c *cp.Classification) { c.Paths = nil }, func(c *cp.Classification) { c.Inputs.PolicyDigest = "bad" },
		func(c *cp.Classification) { c.Inputs.AttributeBlobs["newPath"] = "bad" }, func(c *cp.Classification) { c.InventoryComplete = false },
		func(c *cp.Classification) { c.PathsSafe = false }, func(c *cp.Classification) { c.HunksComplete = false },
		func(c *cp.Classification) { c.Files[0].Hunks.Complete = false }, func(c *cp.Classification) { c.Files[0].OldPath = "other" },
		func(c *cp.Classification) { c.Facts["docs"] = cp.True }, func(c *cp.Classification) { c.Files[0].Facts["policy"] = cp.True },
		func(c *cp.Classification) { c.Facts["generated"] = cp.False },
	} {
		i, d, g := basis(t, "src/a.go")
		if _, err := Classify(i, d, g, Settings{}, mutatingDeriver{mutate}); err == nil {
			t.Fatal("Deriver modified protected evidence")
		}
	}
	i, d, g := basis(t, "src/a.go")
	c, err := Classify(i, d, g, Settings{}, mutatingDeriver{func(c *cp.Classification) { c.Facts["unknown"] = cp.False }})
	if err != nil || c.Classification.Label("unknown") != cp.True {
		t.Fatal("unknown owners did not force unknown", err)
	}
}
func TestConfigAmbiguityAndCompositeActions(t *testing.T) {
	i, d, g := basis(t, ".github/actions/build/action.yml")
	put(&g.Config, ".openmerge.yml", "version: 1")
	put(&g.Config, ".github/openmerge.yaml", "version: 1")
	c := derive(t, i, d, g, Settings{}).Classification
	if c.Inputs.ConfigBlob != "" || c.Label("unknown") != cp.True || c.Label("policy") != cp.False {
		t.Fatal(c)
	}
	c = derive(t, i, d, g, Settings{Sensitive: []string{".github/actions/**"}}).Classification
	if c.Label("policy") != cp.True {
		t.Fatal("sensitive composite action not policy")
	}
}

type governingMutator struct{}

func (governingMutator) Generated(_ string, g forge.GoverningFiles) cp.Truth {
	g.Attributes[0].Content = "forged"
	return cp.True
}
func (governingMutator) Owners(*cp.Classification, forge.GoverningFiles) {}
func TestDeriverCannotMutateGoverningProbes(t *testing.T) {
	i, d, g := basis(t, "a.go")
	before := fingerprint(g)
	if _, err := Classify(i, d, g, Settings{}, governingMutator{}); err == nil {
		t.Fatal("governing mutation accepted")
	}
	if before != fingerprint(g) {
		t.Fatal("caller governing probes mutated")
	}
}
func TestIncompleteInventoryAndStaleResolution(t *testing.T) {
	i, d, g := basis(t, "a.go")
	ownersSource(&g, "CODEOWNERS", "a.go @Alice\nother @Bob")
	d.Inventory = forge.DiffCoverage{Completeness: forge.DiffIncomplete}
	c := derive(t, i, d, g, Settings{}).Classification
	if c.CodeOwnersKnown || c.PolicyOwnersKnown || c.Label("owned:user:alice") != cp.True || c.Label("owned:user:bob") != cp.Unknown {
		t.Fatal(c)
	}
	e := evaluation(c, cp.Requirement{Selector: cp.CodeOwners}, "github.com:user:1")
	coherent(t, e)
	if e.Allowed {
		t.Fatal("incomplete inventory authorized")
	}
	d.Inventory = complete
	for _, pool := range []cp.IdentitySet{{IDs: []string{"github.com:user:1"}, Resolved: true}, {IDs: []string{""}, Resolved: true, Fresh: true}, {IDs: []string{" "}, Resolved: true, Fresh: true}} {
		derived := TreeDeriver{Resolve: func(Owner) cp.IdentitySet { return pool }}
		e, err := Classify(i, d, g, Settings{}, derived)
		if err != nil {
			t.Fatal(err)
		}
		if e.Classification.CodeOwnersKnown || e.Classification.Label("unknown") != cp.True {
			t.Fatal("stale resolution authorized")
		}
		coherent(t, evaluation(e.Classification, cp.Requirement{Selector: cp.CodeOwners}, "github.com:user:1"))
	}
}

type eagerAttributeDeriver struct{ ownerStub }

func (eagerAttributeDeriver) Generated(string, forge.GoverningFiles) cp.Truth { return cp.True }
func TestClassifyCoverageGuardsInjectedDeriver(t *testing.T) {
	i, d, g := basis(t, "nested/a.go")
	put(&g.Attributes, ".gitattributes", "* linguist-generated")
	g.Attributes = g.Attributes[:1]
	e, err := Classify(i, d, g, Settings{}, eagerAttributeDeriver{})
	if err != nil {
		t.Fatal(err)
	}
	if e.Classification.Label("generated") != cp.Unknown {
		t.Fatal("Classify trusted an eager Deriver across a missing probe")
	}
	i, d, g = basis(t, "a.go")
	d.Files[0].Binary = true
	_, err = Classify(i, d, g, Settings{}, mutatingDeriver{func(c *cp.Classification) {
		c.Facts["unknown"] = cp.False
		c.CodeOwnersKnown = true
		c.PolicyOwnersKnown = true
	}})
	if err == nil {
		t.Fatal("Deriver erased a preexisting unknown fact")
	}
}

func TestReviewGeneratedOverridesCannotExemptApproval(t *testing.T) {
	for name, setup := range map[string]func(*forge.GoverningFiles){
		"three-stars": func(g *forge.GoverningFiles) {
			put(&g.Attributes, ".gitattributes", "* linguist-generated\na/***/b.go -linguist-generated\n")
		},
		"nested-bom": func(g *forge.GoverningFiles) {
			put(&g.Attributes, ".gitattributes", "* linguist-generated\n")
			put(&g.Attributes, "a/.gitattributes", "\ufeff* -linguist-generated\n")
		},
		"overlong-set": func(g *forge.GoverningFiles) {
			put(&g.Attributes, ".gitattributes", "*.go linguist-generated"+strings.Repeat(" a", 1100)+"\n")
		},
		"overlong-unset": func(g *forge.GoverningFiles) {
			put(&g.Attributes, ".gitattributes", "* linguist-generated\n*.go -linguist-generated"+strings.Repeat(" a", 1100)+"\n")
		},
	} {
		t.Run(name, func(t *testing.T) {
			i, d, g := basis(t, "a/x/y/b.go")
			setup(&g)
			c := derive(t, i, d, g, Settings{}).Classification
			if c.Label("generated") == cp.True {
				t.Fatal("override became generated exemption")
			}
			in := cp.Input{Classification: c, AuthorKind: cp.HumanAccount, Current: cp.ReceiptIdentity{ChangeID: i.ChangeID, Revision: i.Revision, Generation: i.Generation, Base: i.Base, Head: i.Head, MergeBase: i.MergeBase, PolicyDigest: i.PolicyDigest}}
			policy := cp.Policy{Approvals: &cp.Approvals{Default: cp.Requirement{Selector: cp.AnyWriter, Count: 1}, Rules: []cp.Rule{{When: cp.Match{Labels: []string{"generated"}}, Require: cp.Requirement{Selector: cp.None}}}}}
			if cp.Evaluate(policy, in).Allowed {
				t.Fatal("missing approval allowed")
			}
		})
	}
}

func TestReviewAmbiguousOwnersCannotDropObligation(t *testing.T) {
	for _, content := range []string{"\ufeff* @alice", "a#b @alice", "*.go @alice#frag", "* @a[bot]/team"} {
		i, d, g := basis(t, "a.go")
		ownersSource(&g, "CODEOWNERS", content)
		c := derive(t, i, d, g, Settings{}).Classification
		if c.CodeOwnersKnown || c.Label("unknown") != cp.True || c.CodeOwners[0].Status != cp.OwnersUnresolved {
			t.Fatalf("%q: %+v", content, c.CodeOwners)
		}
		e := evaluation(c, cp.Requirement{Selector: cp.CodeOwners})
		coherent(t, e)
		if e.Allowed {
			t.Fatal("ambiguous source cleared owners")
		}
	}
}

func TestResolveOwnerEligibilityUsesFreshObservation(t *testing.T) {
	i, d, g := basis(t, "CODEOWNERS")
	ownersSource(&g, "CODEOWNERS", "* @acme/platform")
	e := derive(t, i, d, g, Settings{})
	before := fingerprint(e)
	fresh := ResolveOwnerEligibility(e, func(Owner) cp.IdentitySet {
		return cp.IdentitySet{IDs: []string{GitHubUserID(9)}, Resolved: true, Fresh: true}
	})
	if !fresh.CodeOwnersKnown || !fresh.PolicyOwnersKnown || !slices.Equal(fresh.CodeOwners[0].Eligible.IDs, []string{"github.com:user:9"}) || !slices.Equal(fresh.PolicyOwners[0].Eligible.IDs, []string{"github.com:user:9"}) {
		t.Fatal(fresh)
	}
	stale := ResolveOwnerEligibility(e, func(Owner) cp.IdentitySet {
		return cp.IdentitySet{IDs: []string{GitHubUserID(1)}, Resolved: true, Reason: "stale"}
	})
	if stale.CodeOwnersKnown || stale.PolicyOwnersKnown {
		t.Fatal("stale membership authorized")
	}
	if before != fingerprint(e) {
		t.Fatal("re-resolution mutated stored evidence")
	}
	if GitHubUserID(0) != "" || GitHubUserID(-1) != "" {
		t.Fatal("invalid stable ID accepted")
	}
}

func TestDeriverCannotEraseDiagnosticReasons(t *testing.T) {
	for _, mutate := range []func(*cp.Classification){
		func(c *cp.Classification) { c.Reasons = nil },
		func(c *cp.Classification) { c.Reasons[0].Reasons[0] = "erased" },
		func(c *cp.Classification) { c.Files[0].Reasons = nil },
	} {
		i, d, g := basis(t, "a.go")
		d.Files[0].Binary = true
		g.Coverage = forge.DiffCoverage{Completeness: forge.DiffIncomplete}
		if _, err := Classify(i, d, g, Settings{}, mutatingDeriver{mutate}); err == nil {
			t.Fatal("diagnostic mutation accepted")
		}
	}
}

func TestWildcardDirectoryAmbiguityCannotOverrideOwners(t *testing.T) {
	for _, pattern := range []string{"docs/*", "*.md"} {
		for _, owners := range []string{"", " @bob"} {
			p := "docs/sub/a"
			if pattern == "*.md" {
				p = "x.md/b"
			}
			i, d, g := basis(t, p)
			ownersSource(&g, "CODEOWNERS", "* @alice\n"+pattern+owners)
			c := derive(t, i, d, g, Settings{}).Classification
			if c.CodeOwnersKnown || c.Label("unknown") != cp.True || c.CodeOwners[0].Status != cp.OwnersUnresolved {
				t.Fatal("ambiguous override became definite", c)
			}
			if c.Label("owned:user:alice") != cp.Unknown {
				t.Fatal("inherited obligation lost")
			}
			if owners != "" && c.Label("owned:user:bob") != cp.Unknown {
				t.Fatal("ambiguous match published positive label")
			}
			result := evaluation(c, cp.Requirement{Selector: cp.CodeOwners}, GitHubUserID(2))
			coherent(t, result)
			if result.Allowed {
				t.Fatal("ambiguous override exempted inherited owner")
			}
		}
	}
}
