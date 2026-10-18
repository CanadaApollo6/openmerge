package brief

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
)

func sample() Fact {
	c := cp.Classification{Inputs: cp.ClassificationInputs{ChangeID: "change", Revision: 1, Generation: 1, Base: "base", Head: "head", MergeBase: "merge", PolicyDigest: "policy", ClassifierVersion: "classifier"}, InventoryComplete: true, PathsSafe: true, Paths: []string{"a"}, Files: []cp.FileEvidence{{NewPath: "a", NewBlob: "blob", NewMode: 0100644, Facts: map[string]cp.Truth{"docs": cp.True}, Hunks: cp.HunkEvidence{Complete: true, Hunks: []cp.Hunk{{NewStart: 1, NewLines: 1, Text: "+hello\n"}}}}}, Facts: map[string]cp.Truth{"docs": cp.True}, Reasons: []cp.LabelReasons{{Label: "docs", Reasons: []string{"matched"}}}, HunksComplete: true, CodeOwnersKnown: true, PolicyOwnersKnown: true, CodeOwners: []cp.CodeOwner{{Path: "a", Side: cp.NewSide, Status: cp.OwnersResolved, LastMatchingEntry: "* @owner", Eligible: cp.IdentitySet{IDs: []string{"user"}, Resolved: true, Fresh: true}}}}
	return Fact{Classification: &c, Intent: &Intent{true, "Title", "Body", cp.IntentDigest("Title", "Body")}, Obligations: []Obligation{{Origin: "default", Requirement: cp.Requirement{Selector: cp.AnyWriter, Count: 1}}}}
}
func clone(f Fact) Fact {
	raw, _ := json.Marshal(f)
	var out Fact
	_ = json.Unmarshal(raw, &out)
	return out
}
func TestCanonicalVectors(t *testing.T) {
	cases := map[string]Fact{"complete": sample(), "incomplete": sample(), "empty-intent": sample()}
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
func TestDigestIncludedFields(t *testing.T) {
	mutations := map[string]func(*Fact){
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
	}, "tests": func(f *Fact) { f.TestLinks = []string{"new"} }, "queue": func(f *Fact) { f.QueuePosition = 123 }, "progress": func(f *Fact) {
		f.ApprovalProgress = "approved"
		f.Obligations[0].Met = true
		f.Obligations[0].ApprovedIDs = []string{"user"}
		f.Obligations[0].EligibleIDs = []string{"user"}
		f.Obligations[0].PoolIDs = []string{"user"}
		f.Obligations[0].Unresolved = true
		f.Obligations[0].Code = "met"
		f.Obligations[0].Explanation = "anything"
	}, "timestamp": func(f *Fact) { f.Timestamp = "today" }, "rendering": func(f *Fact) { f.RenderedText = "new" }, "eligible-reason": func(f *Fact) {
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
