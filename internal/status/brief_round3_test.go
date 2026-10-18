package status

import (
	"encoding/json"
	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"os"
	"strings"
	"testing"
)

func TestR3TruncatedSecret(t *testing.T) {
	const secret = "opaque-installation-secret-987654"
	// 336 ampersands consume 2016 JSON-escaped bytes, leaving 20 bytes of the secret.
	j, _, _, b := policyJournal(t, true)
	briefAppend(&j, queue.Intent(b, strings.Repeat("&", 336)+secret, "body"))
	v, body := briefHTTPView(t, j, b.ChangeID, diagnostic.NewSanitizer(secret))
	t.Logf("title=%q", v.Intent.Title)
	if strings.Contains(body, secret[:20]) {
		t.Fatal("registered secret prefix leaked across display truncation boundary")
	}
}
func TestR3MachineDigest(t *testing.T) {
	raw, err := os.ReadFile("../queue/testdata/brief-facts.json")
	if err != nil {
		t.Fatal(err)
	}
	var f brief.Fact
	if err = json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	v, err := brief.Project(f, "agent")
	if err != nil {
		t.Fatal(err)
	}
	original := v.Digest
	sanitizeBriefView(&v, diagnostic.NewSanitizer(original[:16]))
	if v.Digest != original {
		t.Fatalf("machine digest corrupted: before=%s after=%s", original, v.Digest)
	}
}
func TestR3InvalidatedBatch(t *testing.T) {
	old := identified("b1", 1, 1, "failure")
	old.Runs[0].Name = "lint"
	events := append(ledgerChain(), old, queue.BaseMoved{SHA: "new-base"}, queue.BatchesInvalidated{BatchIDs: []string{"b1", "b2"}, Reason: "base-moved"},
		queue.ChainedBatchStarted{BatchID: "b3", BaseSHA: "new-base", PRs: []queue.BatchPR{{PR: 1, HeadSHA: "head"}}, At: at, Position: 1},
		queue.ChainedTestRefBuilt{BatchID: "b3", TestedSHA: "tested-b3", LandingSHA: "tested-b3", Position: 1, Method: queue.Merge, At: at})
	newer := identified("b3", 3, 1, "success")
	newer.Runs[0].Name = "lint"
	failing := identified("b3", 4, 1, "failure")
	failing.Runs[0].Name = "unit"
	newer.Success = false
	newer.Runs = append(newer.Runs, failing.Runs...)
	events = append(events, newer, queue.Dequeued{PR: 1, AdmissionID: 1, Reason: queue.ChecksFailed})
	j := journal(events...)
	id, _ := j.Key.ChangeID(1)
	_, e, _ := Project(j, 1, 0, nil)
	for _, r := range e.RunLedger {
		t.Logf("run=%+v identity=%+v", r, r.Identity)
	}
	v, _ := briefHTTPView(t, j, id, nil)
	t.Logf("progress=%+v", v.Progress)
	current := false
	for _, w := range v.Warnings {
		if w.NextStep.Action == "fix_check" {
			t.Logf("repair=%+v", w.NextStep)
			if w.NextStep.Name == "lint" {
				t.Error("invalidated batch lint failure still produces fix_check despite current pass")
			}
			if w.NextStep.Name == "unit" {
				current = true
			}
		}
	}
	if !current {
		t.Fatal("no current unit failure")
	}
}
func TestR3ManualReruns(t *testing.T) {
	for _, late := range []bool{false, true} {
		for _, sameID := range []bool{false, true} {
			old := identified("b1", 1, 0, "failure")
			old.Runs[0].Name = "lint"
			newer := identified("b1", 2, 1, "success")
			if sameID {
				newer = identified("b1", 1, 2, "success")
			}
			newer.Runs[0].Name = "lint"
			fail := identified("b1", 3, 1, "failure")
			fail.Runs[0].Name = "unit"
			events := ledgerChain()
			if late {
				events = append(events, newer, old)
			} else {
				events = append(events, old, newer)
			}
			events = append(events, fail, queue.Dequeued{PR: 1, AdmissionID: 1, Reason: queue.ChecksFailed})
			j := journal(events...)
			id, _ := j.Key.ChangeID(1)
			v, _ := briefHTTPView(t, j, id, nil)
			for _, w := range v.Warnings {
				if w.NextStep.Action == "fix_check" && w.NextStep.Name == "lint" {
					t.Errorf("late=%t sameID=%t superseded repair %+v", late, sameID, w)
				}
			}
		}
	}
}

func TestR3SanitizeBeforeBoundsAndPreserveHunks(t *testing.T) {
	const secret = "opaque-installation-secret-987654"
	raw, err := os.ReadFile("../queue/testdata/brief-facts.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{strings.Repeat("&", 336), strings.Repeat("<", 334)} {
		var f brief.Fact
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatal(err)
		}
		f.Intent.Title = prefix + secret
		f.Intent.Digest = cp.IntentDigest(f.Intent.Title, f.Intent.Body)
		f.Classification.Files[0].Hunks.Hunks[0].Text = "@@ -1 +1 @@\n-" + prefix + secret + "\n+safe\n"
		before, err := brief.Digest(f)
		if err != nil {
			t.Fatal(err)
		}
		v, err := brief.Project(f, "operator", diagnostic.NewSanitizer(secret))
		if err != nil {
			t.Fatal(err)
		}
		body := mustBriefJSON(t, v)
		if strings.Contains(body, "opaque-installation-") || v.Digest != before {
			t.Fatalf("leak or changed digest: %s", body)
		}
		if !strings.Contains(body, "display_redacted") {
			t.Fatal("missing redaction marker")
		}
	}
	var f brief.Fact
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	const hunk = "@@ -1 +1 @@\n-old line\n+\tnew  line\n+  indented\n"
	f.Classification.Files[0].Hunks.Hunks[0].Text = hunk
	v, err := brief.Project(f, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if v.Classification.Files[0].Hunks[0].Text != hunk || strings.Contains(mustBriefJSON(t, v), "display_redacted") {
		t.Fatalf("hunk whitespace changed: %+v", v)
	}
}

func TestR3MachineFieldsPreservedDuringProjection(t *testing.T) {
	raw, err := os.ReadFile("../queue/testdata/brief-facts.json")
	if err != nil {
		t.Fatal(err)
	}
	var f brief.Fact
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	original, err := brief.Project(f, "agent")
	if err != nil {
		t.Fatal(err)
	}
	s := diagnostic.NewSanitizer(original.Digest[:16], original.Classification.Head, original.ChangeID, original.Classification.PolicyDigest, "agent", "true")
	v, err := brief.Project(f, "agent", s)
	if err != nil {
		t.Fatal(err)
	}
	sanitizeBriefView(&v, s)
	if v.Digest != original.Digest || v.ChangeID != original.ChangeID || v.Audience != original.Audience || v.Classification.Head != original.Classification.Head || v.Classification.PolicyDigest != original.Classification.PolicyDigest {
		t.Fatalf("machine fields corrupted: %+v", v)
	}
}

func TestNextStepMachineValuesRemainVerbatim(t *testing.T) {
	path := strings.Repeat("branch/", 1000)
	link := "https://example.test/" + strings.Repeat("segment/", 1000)
	v := brief.View{Warnings: []brief.Warning{{NextStep: brief.Step{Path: path, URL: link}}}}
	sanitizeBriefView(&v, diagnostic.NewSanitizer())
	if v.Warnings[0].NextStep.Path != path || v.Warnings[0].NextStep.URL != link {
		t.Fatal("machine value display-truncated")
	}
}
