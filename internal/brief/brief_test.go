package brief

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
)

func sample() Fact {
	c := cp.Classification{Inputs: cp.ClassificationInputs{ChangeID: "change", Revision: 1, Generation: 1, Base: "base", Head: "head", MergeBase: "merge", PolicyDigest: "policy", ClassifierVersion: "classifier"}, InventoryComplete: true, PathsSafe: true, Paths: []string{"a"}, Files: []cp.FileEvidence{{NewPath: "a", NewBlob: "blob", NewMode: 0100644, Facts: map[string]cp.Truth{"docs": cp.True}, Hunks: cp.HunkEvidence{Complete: true, Hunks: []cp.Hunk{{NewStart: 1, NewLines: 1, Text: "+hello\n"}}}}}, Facts: map[string]cp.Truth{"docs": cp.True}, Reasons: []cp.LabelReasons{{Label: "docs", Reasons: []string{"matched"}}}, HunksComplete: true, CodeOwnersKnown: true, PolicyOwnersKnown: true, CodeOwners: []cp.CodeOwner{{Path: "a", Side: cp.NewSide, Status: cp.OwnersResolved, LastMatchingEntry: "* @owner", Eligible: cp.IdentitySet{IDs: []string{"user"}, Resolved: true, Fresh: true}}}}
	return Fact{Classification: &c, Intent: &Intent{true, "Title", "Body", cp.IntentDigest("Title", "Body")}, ApprovalsRequired: true, Obligations: []Obligation{{Origin: "default", Cause: cp.CauseDefault, Requirement: cp.Requirement{Selector: cp.AnyWriter, Count: 1}}}}
}
func clone(f Fact) Fact {
	raw, _ := json.Marshal(f)
	var out Fact
	_ = json.Unmarshal(raw, &out)
	return out
}
func TestCanonicalVectors(t *testing.T) {
	cases := map[string]Fact{"complete": sample(), "incomplete": sample(), "empty-intent": sample(), "complex": complexVector()}
	none := sample()
	none.Obligations = nil
	cases["no-requirements"] = none
	f := cases["incomplete"]
	f.Classification.InventoryComplete = false
	f.Classification.HunksComplete = false
	f.Classification.Files[0].Hunks.Complete = false
	f.Classification.Files[0].Hunks.Omitted = []cp.HunkRange{{NewStart: 2, NewLines: 3, Reason: "hunk_count_limit"}}
	cases["incomplete"] = f
	f = cases["empty-intent"]
	f.Intent = &Intent{true, "", "", cp.IntentDigest("", "")}
	cases["empty-intent"] = f
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := Canonical(f)
			if err != nil {
				t.Fatal(err)
			}
			digest, _ := Digest(f)
			path := filepath.Join("testdata", name)
			if os.Getenv("UPDATE_7A") == "1" {
				if err = os.MkdirAll("testdata", 0755); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path+".json", raw, 0644); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path+".sha256", []byte(digest+"\n"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path + ".json")
			if err != nil || !bytes.Equal(want, raw) {
				t.Fatalf("canonical vector mismatch %s: %v", name, err)
			}
			hash, err := os.ReadFile(path + ".sha256")
			if err != nil || strings.TrimSpace(string(hash)) != digest {
				t.Fatalf("digest vector mismatch: %s %v", digest, err)
			}
		})
	}
}

func TestV1VectorsPreserved(t *testing.T) {
	for name, want := range map[string]string{
		"complete":     "3afa3ae419e1691b99efedebaccf78dfcde22a4419fe122c45006f8cb627a31b",
		"incomplete":   "41a0a7a649f620188dc1f39bc03050695d61acce1e12b95c2ee77e77b5d6b34b",
		"empty-intent": "237afe4a534dff8ca82a0e76dd93b47f7f26d85da8924c883a8a2198616d8663",
	} {
		supplied, err := Identity(sample())
		if err != nil {
			t.Fatal(err)
		}
		supplied.BriefDigest = want
		if err = ValidateReceipt(supplied, sample()); err == nil || err.Error() != "receipt_binding_mismatch" {
			t.Fatal("v1 receipt accepted", err)
		}
		raw, err := os.ReadFile(filepath.Join("testdata", "v1-"+name+".sha256"))
		if err != nil || strings.TrimSpace(string(raw)) != want {
			t.Fatalf("v1 vector %s changed: %v", name, err)
		}
		canonical, err := os.ReadFile(filepath.Join("testdata", "v1-"+name+".json"))
		sum := sha256.Sum256(canonical)
		if err != nil || hex.EncodeToString(sum[:]) != want {
			t.Fatalf("v1 canonical bytes %s changed: %v", name, err)
		}
	}
}

func TestCanonicalRequirementsAndFactExclusions(t *testing.T) {
	f := sample()
	f.Obligations = nil
	if _, err := Canonical(f); err != nil {
		t.Fatal("require:none rejected", err)
	}
	f.ApprovalsRequired = false
	if _, err := Canonical(f); err != nil {
		t.Fatalf("legacy approvals rejected empty requirements: %v", err)
	}
	for _, name := range []string{"TestLinks", "QueuePosition", "ApprovalProgress", "Timestamp", "RenderedText"} {
		if _, exists := reflect.TypeFor[Fact]().FieldByName(name); exists {
			t.Fatalf("placeholder exclusion %s remained on Fact", name)
		}
	}
}

func TestBuildValidatesAndUsesCanonicalReceiptBinding(t *testing.T) {
	policy := cp.Policy{Approvals: &cp.Approvals{Default: cp.Requirement{Selector: cp.AnyWriter, Count: 1, Receipt: true}}}
	in := cp.Input{Classification: *sample().Classification, Current: receiptBasis(sample()), AuthorKind: cp.HumanAccount, Actors: map[string]cp.Actor{}, Stances: map[string]cp.Stance{}}
	in.Classification.Facts["policy"] = cp.False
	in.Classification.Files[0].Facts["policy"] = cp.False
	first, err := Build(policy, in, "Title", "Body")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := Identity(first)
	if err != nil {
		t.Fatal(err)
	}
	in.Actors["reviewer"] = cp.Actor{Identity: cp.Identity{ID: "reviewer", Login: "reviewer", Kind: cp.HumanAccount}, Grant: cp.WriteGrant{SubjectID: "reviewer", ReturnedID: "reviewer", Fresh: true, Write: cp.True}}
	in.Stances["reviewer"] = cp.Stance{UserID: "reviewer", Kind: cp.Approve, Revision: identity.Revision, Head: identity.Head, Source: cp.StanceSource{Channel: cp.CommentCommand, ID: "comment-1"}, Receipt: &identity}
	got, err := Build(policy, in, "Title", "Body")
	if err != nil || !metDefault(got) {
		t.Fatalf("receipt progress did not use canonical binding: %+v, %v", got, err)
	}
	native := in
	nativeStance := native.Stances["reviewer"]
	nativeStance.Source.Channel, nativeStance.Receipt = cp.NativeReview, nil
	native.Stances["reviewer"] = nativeStance
	if got, err := Build(policy, native, "Title", "Body"); err != nil || metDefault(got) {
		t.Fatalf("native approval satisfied receipt requirement: %+v, %v", got, err)
	}
	wrong := in
	wrongIdentity := identity
	wrongIdentity.BriefDigest = "wrong"
	wrongStance := wrong.Stances["reviewer"]
	wrongStance.Receipt = &wrongIdentity
	wrong.Stances["reviewer"] = wrongStance
	if got, err := Build(policy, wrong, "Title", "Body"); err != nil || metDefault(got) {
		t.Fatalf("wrong receipt satisfied requirement: %+v, %v", got, err)
	}
	if _, err := Build(cp.Policy{Approvals: &cp.Approvals{Default: cp.Requirement{Selector: cp.None}}}, in, "Title", "Body"); err == nil {
		t.Fatal("Build accepted invalid default")
	}
	if _, err := Build(policy, cp.Input{}, "Title", "Body"); err == nil {
		t.Fatal("Build accepted unrecorded classification")
	}
	mismatched := in
	mismatched.Current.Head = "stale"
	if _, err := Build(policy, mismatched, "Title", "Body"); err == nil {
		t.Fatal("Build accepted stale current basis")
	}
	empty := in
	empty.Classification.Facts = map[string]cp.Truth{"docs": cp.True, "policy": cp.False}
	empty.Classification.Files[0].Facts["policy"] = cp.False
	emptyPolicy := cp.Policy{Approvals: &cp.Approvals{Default: cp.Requirement{Selector: cp.AnyWriter, Count: 1}, Rules: []cp.Rule{{When: cp.Match{Labels: []string{"docs"}}, Require: cp.Requirement{Selector: cp.None}}}}}
	if got, err := Build(emptyPolicy, empty, "Title", "Body"); err != nil || len(got.Obligations) != 0 || !got.ApprovalsRequired {
		t.Fatal("Build rejected require:none", got, err)
	}
}

func metDefault(f Fact) bool {
	for _, obligation := range f.Obligations {
		if obligation.Cause == cp.CauseDefault {
			return obligation.Met
		}
	}
	return false
}

func complexVector() Fact {
	f := sample()
	f.Classification.Paths = []string{"src/new<&>.go", "src/old\u2028name.go", "docs/a.md", "src/new<&>.go"}
	f.Classification.Files = []cp.FileEvidence{
		{OldPath: "src/old\u2028name.go", NewPath: "src/new<&>.go", OldBlob: "old", NewBlob: "new", OldMode: 0100644, NewMode: 0100755, Renamed: true, RenamePaired: true, Facts: map[string]cp.Truth{"docs": cp.False, "policy": cp.True}, Hunks: cp.HunkEvidence{Complete: true, Hunks: []cp.Hunk{{OldStart: 3, OldLines: 1, NewStart: 3, NewLines: 1, Text: "-old\n+new <&>\u2028\x01\n"}}}},
		{NewPath: "docs/a.md", NewBlob: "docs", NewMode: 0100644, Facts: map[string]cp.Truth{"docs": cp.True, "policy": cp.False}, Hunks: cp.HunkEvidence{Complete: true}},
	}
	f.Classification.Facts = map[string]cp.Truth{"docs": cp.False, "policy": cp.True}
	f.Classification.CodeOwners = []cp.CodeOwner{
		{Path: "src/new<&>.go", Side: cp.NewSide, Status: cp.OwnersResolved, LastMatchingEntry: "src/** @zoe <ops>", Eligible: cp.IdentitySet{IDs: []string{"zoe", "amy", "zoe"}, Resolved: true, Fresh: true}},
		{Path: "src/old\u2028name.go", Side: cp.OldSide, Status: cp.OwnersResolved, LastMatchingEntry: "src/** @amy", Eligible: cp.IdentitySet{IDs: []string{"amy"}, Resolved: true, Fresh: true}},
	}
	f.Classification.PolicyOwners = nil
	i := 3
	f.Obligations = []Obligation{
		{Origin: "rules[3] code owner: src/new<&>.go (new)", Cause: cp.CauseRuleCodeOwners, RuleIndex: &i, Path: "src/new<&>.go", Side: cp.NewSide, Requirement: cp.Requirement{Selector: cp.CodeOwners, Count: 1}},
		{Origin: "rendered duplicate", Cause: cp.CauseRuleCodeOwners, RuleIndex: &i, Path: "src/new<&>.go", Side: cp.NewSide, Requirement: cp.Requirement{Selector: cp.CodeOwners, Count: 1}},
		{Origin: "default", Cause: cp.CauseDefault, Requirement: cp.Requirement{Selector: cp.AnyWriter, Count: 1}},
	}
	return f
}
func TestDigestIncludedFields(t *testing.T) {
	mutations := map[string]func(*Fact){
		"old-path": func(f *Fact) { f.Classification.Files[0].OldPath = "old" }, "old-blob": func(f *Fact) { f.Classification.Files[0].OldBlob = "old" }, "old-mode": func(f *Fact) { f.Classification.Files[0].OldMode = 0100644 },
		"owner-path": func(f *Fact) { f.Classification.CodeOwners[0].Path = "other" }, "owner-side": func(f *Fact) { f.Classification.CodeOwners[0].Side = cp.OldSide }, "owner-entry": func(f *Fact) { f.Classification.CodeOwners[0].LastMatchingEntry = "* @other" }, "owner-resolution": func(f *Fact) { f.Classification.CodeOwners[0].Eligible.Resolved = false },
		"requirement-selector": func(f *Fact) { f.Obligations[0].Requirement.Selector = cp.Human }, "requirement-group": func(f *Fact) { f.Obligations[0].Requirement.Group = "group" }, "requirement-human": func(f *Fact) { f.Obligations[0].Requirement.Human = true }, "requirement-receipt": func(f *Fact) { f.Obligations[0].Requirement.Receipt = true }, "requirement-cause": func(f *Fact) { f.Obligations[0].Cause = cp.CauseRule }, "requirement-rule-index": func(f *Fact) { i := 3; f.Obligations[0].RuleIndex = &i }, "requirement-side": func(f *Fact) { f.Obligations[0].Side = cp.OldSide },
		"change": func(f *Fact) { f.Classification.Inputs.ChangeID += "2" }, "revision": func(f *Fact) { f.Classification.Inputs.Revision++ }, "generation": func(f *Fact) { f.Classification.Inputs.Generation++ }, "base": func(f *Fact) { f.Classification.Inputs.Base += "2" }, "head": func(f *Fact) { f.Classification.Inputs.Head += "2" }, "merge-base": func(f *Fact) { f.Classification.Inputs.MergeBase += "2" }, "policy": func(f *Fact) { f.Classification.Inputs.PolicyDigest += "2" }, "classifier": func(f *Fact) { f.Classification.Inputs.ClassifierVersion += "2" }, "owner-content": func(f *Fact) { f.Classification.Inputs.OwnersBlob = "new" }, "attribute-content": func(f *Fact) { f.Classification.Inputs.AttributeBlobs = map[string]string{".gitattributes": "new"} },
		"inventory": func(f *Fact) { f.Classification.InventoryComplete = false }, "paths-safe": func(f *Fact) { f.Classification.PathsSafe = false }, "paths": func(f *Fact) { f.Classification.Paths = append(f.Classification.Paths, "b") }, "hunks-complete": func(f *Fact) { f.Classification.HunksComplete = false }, "file": func(f *Fact) { f.Classification.Files[0].NewPath = "b" }, "blob": func(f *Fact) { f.Classification.Files[0].NewBlob = "new" }, "mode": func(f *Fact) { f.Classification.Files[0].NewMode = 0100755 }, "binary": func(f *Fact) { f.Classification.Files[0].Binary = cp.True }, "submodule": func(f *Fact) { f.Classification.Files[0].Submodule = cp.True }, "rename": func(f *Fact) { f.Classification.Files[0].Renamed = true }, "paired": func(f *Fact) { f.Classification.Files[0].RenamePaired = true }, "hunk": func(f *Fact) { f.Classification.Files[0].Hunks.Hunks[0].Text += "+new\n" }, "range": func(f *Fact) { f.Classification.Files[0].Hunks.Hunks[0].NewStart++ }, "omission": func(f *Fact) {
			f.Classification.Files[0].Hunks.Omitted = []cp.HunkRange{{Reason: "hunk_count_limit", Count: 2}}
		}, "file-completeness": func(f *Fact) { f.Classification.Files[0].Hunks.Complete = false }, "file-label": func(f *Fact) { f.Classification.Files[0].Facts["docs"] = cp.False }, "file-reason": func(f *Fact) {
			f.Classification.Files[0].Reasons = []cp.LabelReasons{{Label: "docs", Reasons: []string{"why"}}}
		}, "label": func(f *Fact) { f.Classification.Facts["docs"] = cp.False }, "reason": func(f *Fact) { f.Classification.Reasons[0].Reasons[0] = "different" }, "owner-known": func(f *Fact) { f.Classification.CodeOwnersKnown = false }, "owner-status": func(f *Fact) { f.Classification.CodeOwners[0].Status = cp.OwnersUnresolved }, "owner-pool": func(f *Fact) {
			f.Classification.CodeOwners[0].Eligible.IDs = append(f.Classification.CodeOwners[0].Eligible.IDs, "another")
		}, "policy-owner-known": func(f *Fact) { f.Classification.PolicyOwnersKnown = false }, "policy-owner": func(f *Fact) { f.Classification.PolicyOwners = []cp.PolicyOwner{{Path: "a"}} }, "intent": func(f *Fact) {
			f.Intent.Body = "edited"
			f.Intent.Digest = cp.IntentDigest(f.Intent.Title, f.Intent.Body)
		}, "requirement": func(f *Fact) { f.Obligations[0].Requirement.Count++ }, "requirement-path": func(f *Fact) { f.Obligations[0].Path = "a" },
	}
	original := sample()
	digest, _ := Digest(original)
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			f := clone(original)
			mutate(&f)
			got, err := Digest(f)
			if err != nil || got == digest {
				t.Fatalf("included field did not change hash: %v", err)
			}
		})
	}
}
func TestDigestExclusionsAndNormalization(t *testing.T) {
	original := sample()
	digest, _ := Digest(original)
	mutations := map[string]func(*Fact){"provenance": func(f *Fact) {
		f.Classification.Inputs.ConfigBlob = "new"
		f.Classification.Inputs.GoverningCommit = "new"
	}, "origin-prose": func(f *Fact) { f.Obligations[0].Origin = "rendered prose only" }, "progress": func(f *Fact) {
		f.Obligations[0].Met = true
		f.Obligations[0].ApprovedIDs = []string{"user"}
		f.Obligations[0].EligibleIDs = []string{"user"}
		f.Obligations[0].PoolIDs = []string{"user"}
		f.Obligations[0].Unresolved = true
		f.Obligations[0].Code = "met"
		f.Obligations[0].Explanation = "anything"
	}, "eligible-reason": func(f *Fact) {
		f.Classification.CodeOwners[0].Eligible.Reason = "arbitrary diagnostic"
		f.Classification.CodeOwners[0].Eligible.Fresh = false
	}, "intent-normalization": func(f *Fact) { f.Intent.Title = " \r\nTitle\r\n "; f.Intent.Body = " Body\r\n" }, "nil-empty": func(f *Fact) {
		f.Classification.PolicyOwners = []cp.PolicyOwner{}
		f.Classification.Files[0].Hunks.Omitted = []cp.HunkRange{}
		f.Classification.Inputs.AttributeBlobs = map[string]string{}
	}}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			f := clone(original)
			mutate(&f)
			got, err := Digest(f)
			if err != nil || got != digest {
				t.Fatalf("excluded change affected digest: %v", err)
			}
		})
	}
	f := clone(original)
	f.Classification.Paths = []string{"z", "a", "z"}
	f.Classification.CodeOwners[0].Eligible.IDs = []string{"z", "user", "z"}
	one, _ := Digest(f)
	f.Classification.Paths = []string{"a", "z"}
	f.Classification.CodeOwners[0].Eligible.IDs = []string{"user", "z"}
	two, _ := Digest(f)
	if one != two {
		t.Fatal("set order changed digest")
	}
	for _, f := range []Fact{{}, {Classification: original.Classification}, {Classification: original.Classification, Intent: &Intent{}}} {
		if _, err := Digest(f); err == nil {
			t.Fatal("absent evidence accepted")
		}
	}
}
func TestReceiptValidationAtBothBoundaries(t *testing.T) {
	f := sample()
	identity, err := Identity(f)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateReceipt(identity, f); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*Fact){func(f *Fact) {
		f.Intent.Body = "edit"
		f.Intent.Digest = cp.IntentDigest(f.Intent.Title, f.Intent.Body)
	}, func(f *Fact) { f.Classification.Inputs.Generation++ }} {
		current := clone(f)
		edit(&current)
		if ValidateReceipt(identity, current) == nil {
			t.Fatal("stale receipt accepted at landing")
		}
	}
	if identity.BriefDigest == "" {
		t.Fatal("missing supplied digest")
	}
}

func TestPolicyOwnerDiagnosticsExcluded(t *testing.T) {
	f := sample()
	f.Classification.PolicyOwners = []cp.PolicyOwner{{Path: "a", Eligible: cp.IdentitySet{Resolved: true, Fresh: true, IDs: []string{"owner"}, Reason: "diagnostic one"}}}
	one, _ := Digest(f)
	f.Classification.PolicyOwners[0].Eligible.Reason = "diagnostic two"
	f.Classification.PolicyOwners[0].Eligible.Fresh = false
	two, _ := Digest(f)
	if one != two {
		t.Fatal("policy Eligible.Reason entered digest")
	}
	f.Classification.PolicyOwners[0].Eligible.Resolved = false
	three, _ := Digest(f)
	if two == three {
		t.Fatal("stable resolution value omitted")
	}
}

func TestOverlayJoinsOnlyExactRecordedRequirements(t *testing.T) {
	recorded := complexVector()
	before, _ := Digest(recorded)
	for _, mutate := range []func(*cp.RequirementResult){
		func(r *cp.RequirementResult) { r.Cause = cp.CauseDefault },
		func(r *cp.RequirementResult) { i := 4; r.RuleIndex = &i },
		func(r *cp.RequirementResult) { r.Path = "other" },
		func(r *cp.RequirementResult) { r.Side = cp.OldSide },
		func(r *cp.RequirementResult) { r.Requirement.Count++ },
	} {
		row := recorded.Obligations[0]
		result := cp.RequirementResult{Cause: row.Cause, RuleIndex: row.RuleIndex, Path: row.Path, Side: row.Side, Requirement: row.Requirement, Met: true}
		mutate(&result)
		overlaid := OverlayProgress(recorded, cp.Evaluation{Requirements: []cp.RequirementResult{result}})
		if overlaid.Obligations[0].Met {
			t.Fatal("nonmatching row joined")
		}
		after, err := Digest(overlaid)
		if err != nil || before != after {
			t.Fatal("overlay changed digest", err)
		}
	}
}

func TestDeclarationBindingDomainAndValidation(t *testing.T) {
	legacy := sample()
	legacyDigest, err := Digest(legacy)
	if err != nil {
		t.Fatal(err)
	}
	opted := clone(legacy)
	opted.DeclarationDigest = strings.Repeat("a", 64)
	current, err := Canonical(opted)
	if err != nil || !bytes.HasPrefix(current, []byte(`{"Version":"openmerge/brief/provenance-v1"`)) {
		t.Fatal(string(current), err)
	}
	optedDigest, err := Digest(opted)
	if err != nil || optedDigest == legacyDigest {
		t.Fatal("declaration binding omitted", err)
	}
	for _, bad := range []string{"a", strings.Repeat("A", 64), strings.Repeat("g", 64), strings.Repeat("a", 65)} {
		opted.DeclarationDigest = bad
		if _, err := Canonical(opted); err == nil {
			t.Fatal("malformed declaration digest accepted", bad)
		}
	}
}
