package brief

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func viewSample() Fact {
	f := sample()
	f.Intent.Title = "<script>untrusted & title</script>"
	f.Intent.Digest = "" // Keep claimed intent consistent with its recorded binding.
	f.Intent.Digest = cp.IntentDigest(f.Intent.Title, f.Intent.Body)
	f.Obligations[0].Code = "approval_required"
	f.Presentation = &Presentation{Stances: []StanceEvidence{{UserID: "reviewer", Kind: "request_changes", Channel: "native_review", SourceID: "12", Head: "head", Revision: 1, UpperBound: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC), Precision: "observation_fallback"}}, Tests: []TestEvidence{{"ci", "https://ci.example/run/1", "success", "tested"}}, Progress: &ProgressView{1, "waiting", "approval_required"}}
	return f
}
func TestViewGoldensAndJSONAgreement(t *testing.T) {
	for _, a := range []string{"reviewer", "operator", "agent"} {
		t.Run(a, func(t *testing.T) {
			f := viewSample()
			before, _ := json.Marshal(f)
			v, err := Project(f, a)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.MarshalIndent(v, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			var decoded View
			if err = json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			rendered, err := Render(v)
			if err != nil {
				t.Fatal(err)
			}
			fromJSON, _ := Render(decoded)
			if !bytes.Equal(rendered, fromJSON) {
				t.Fatal("rendered and public JSON disagree")
			}
			after, _ := json.Marshal(f)
			if !bytes.Equal(before, after) {
				t.Fatal("projection mutated recorded evidence")
			}
			goldenJSON := raw
			goldenAudience := a
			if a == "operator" {
				normalized := v
				normalized.Audience = "reviewer"
				goldenJSON, _ = json.MarshalIndent(normalized, "", "  ")
				goldenAudience = "reviewer"
			}
			goldenView(t, goldenAudience+".json", append(goldenJSON, '\n'))
			if !bytes.HasSuffix(rendered, []byte("\n")) {
				rendered = append(rendered, '\n')
			}
			if a == "agent" {
				if !bytes.Equal(bytes.TrimSpace(rendered), raw) {
					t.Fatal("agent render differs from public JSON")
				}
			} else {
				goldenView(t, a+".txt", rendered)
			}
			if bytes.Contains(rendered, []byte("<script>")) {
				t.Fatal("unsafe HTML")
			}
		})
	}
}
func goldenView(t *testing.T, name string, raw []byte) {
	t.Helper()
	path := filepath.Join("testdata", "views", name)
	if os.Getenv("UPDATE_7B") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(want, raw) {
		t.Fatalf("golden %s mismatch: %v", name, err)
	}
}
func TestPresentationPreservesAllFiveV2Vectors(t *testing.T) {
	for _, name := range []string{"complete", "incomplete", "empty-intent", "complex", "no-requirements"} {
		raw, err := os.ReadFile(filepath.Join("testdata", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var canonical struct {
			Classification    json.RawMessage
			IntentDigest      string
			ApprovalsRequired bool
			Requirements      []Obligation
		}
		if err = json.Unmarshal(raw, &canonical); err != nil {
			t.Fatal(err)
		}
		f := sample()
		if err = json.Unmarshal(canonical.Classification, f.Classification); err != nil {
			t.Fatal(err)
		}
		f.Obligations = canonical.Requirements
		f.ApprovalsRequired = canonical.ApprovalsRequired
		if name == "empty-intent" {
			f.Intent.Title = ""
			f.Intent.Body = ""
			f.Intent.Digest = cp.IntentDigest("", "")
		}
		if name == "complex" {
			f = complexVector()
		}
		baseline, err := Canonical(f)
		if err != nil || !bytes.Equal(raw, baseline) {
			t.Fatalf("%s reconstruction: %v", name, err)
		}
		f.Presentation = viewSample().Presentation
		for _, a := range []string{"reviewer", "operator", "agent"} {
			v, err := Project(f, a)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = Render(v); err != nil {
				t.Fatal(err)
			}
		}
		got, err := Canonical(f)
		hash, _ := Digest(f)
		wantHash, _ := os.ReadFile(filepath.Join("testdata", name+".sha256"))
		sum := sha256.Sum256(got)
		if err != nil || !bytes.Equal(raw, got) || hash != strings.TrimSpace(string(wantHash)) || hash != hex.EncodeToString(sum[:]) {
			t.Fatalf("%s changed canonical bytes/hash", name)
		}
	}
}
func TestBoundedViewsRetainSafetyWarnings(t *testing.T) {
	f := viewSample()
	f.Intent.Body = strings.Repeat("界", 10000) + "\x1b[31m"
	f.Classification.InventoryComplete = false
	f.Classification.CodeOwnersKnown = false
	f.Presentation.Saturated = true
	f.Presentation.Stances = append(make([]StanceEvidence, 100), f.Presentation.Stances...)
	for i := 0; i < 100; i++ {
		f.Classification.Files = append(f.Classification.Files, f.Classification.Files[0])
		f.Obligations = append(f.Obligations, f.Obligations[0])
	}
	v, err := Project(f, "operator")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := Render(v)
	if len(raw) > MaxViewBytes || !v.Truncated || bytes.Contains(raw, []byte{27}) {
		t.Fatal("unbounded or unsafe view")
	}
	for _, code := range []string{"unknown_inventory", "unknown_owners", "approval_saturated", "stance_barrier", "display_truncated"} {
		if !bytes.Contains(raw, []byte(code)) {
			t.Fatalf("lost safety warning %s", code)
		}
	}
	f.Presentation.Tests[0].URL = "javascript:alert(1)"
	v, _ = Project(f, "reviewer")
	if v.Tests[0].URL != NotRecorded {
		t.Fatal("unsafe link")
	}
}
func TestMissingAndKnownEmptyViews(t *testing.T) {
	v, _ := Project(Fact{}, "reviewer")
	raw, _ := Render(v)
	if !bytes.Contains(raw, []byte(NotRecorded)) || v.Digest != NotRecorded || v.Tests != nil || v.ApprovalsRequired != nil {
		t.Fatal("invented historical evidence")
	}
	f := sample()
	f.Obligations = []Obligation{}
	f.ApprovalsRequired = false
	f.Presentation = &Presentation{Stances: []StanceEvidence{}, Tests: []TestEvidence{}}
	v, _ = Project(f, "agent")
	if v.Obligations == nil || v.Tests == nil || v.Stances == nil {
		t.Fatal("known empty lost")
	}
	if v.ApprovalsRequired == nil || *v.ApprovalsRequired {
		t.Fatal("known no-approval queue lost")
	}
	if _, err := Project(f, "unknown"); err == nil {
		t.Fatal("invalid audience accepted")
	}
}

func TestViewStableFieldsSurviveHunkBudgetExhaustion(t *testing.T) {
	f := viewSample()
	f.Classification.Files = nil
	path := strings.Repeat("p", 884)
	hunk := strings.Repeat("h", 2000)
	for i := 0; i < maxRows; i++ {
		row := cp.FileEvidence{NewPath: path, Hunks: cp.HunkEvidence{Complete: true}}
		for j := 0; j < 8; j++ {
			row.Hunks.Hunks = append(row.Hunks.Hunks, cp.Hunk{Text: hunk})
		}
		f.Classification.Files = append(f.Classification.Files, row)
	}
	v, err := Project(f, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if v.Progress.Status != "waiting" || v.Progress.Reason != "approval_required" || v.Stances[0].Channel != "native_review" || v.Stances[0].Head != "head" || v.Stances[0].Precision != "observation_fallback" || v.Tests[0].Status != "success" || v.Tests[0].SHA != "tested" || v.Tests[0].URL != "https://ci.example/run/1" {
		t.Fatal("machine fields lost to hunk budget")
	}
	if !v.Truncated || v.ApprovalsRequired == nil || !*v.ApprovalsRequired || v.ChangeID != "change" || v.Classification.Head != "head" || v.Obligations[0].Code != "approval_required" || v.Obligations[0].NextStep.Action != "wait_for_approval" || v.Stances[0].UserID != "reviewer" || v.Tests[0].Name != "ci" {
		t.Fatalf("stable display fields were consumed by hunks: %+v", v)
	}
}

func TestViewClaimedIntentTruthAndDigestAvailability(t *testing.T) {
	f := sample()
	f.Classification.Facts["unknown"] = cp.Unknown
	f.Obligations = nil
	v, err := Project(f, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if v.ApprovalsRequired != nil || v.Digest != NotRecorded {
		t.Fatal("unavailable obligations still exposed approval identity")
	}
	if v.Classification.Labels[0].Truth != "true" {
		t.Fatalf("truth was not textual: %+v", v.Classification.Labels)
	}
	var raw map[string]json.RawMessage
	data, _ := json.Marshal(v)
	if err = json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["claimed_intent"]; !ok {
		t.Fatal("claimed intent key missing")
	}
	if _, ok := raw["intent"]; ok {
		t.Fatal("legacy intent key retained")
	}
}

func TestViewRecoveryActionsUseRecordedBounds(t *testing.T) {
	f := viewSample()
	f.Presentation.Stances[0].Time = time.Date(2026, 10, 18, 13, 0, 0, 0, time.UTC)
	f.Presentation.Stances[0].TimeKnown = true
	v, _ := Project(f, "reviewer")
	if got := v.Stances[0].NextStep.After; got != "2026-10-18T12:00:00Z" {
		t.Fatalf("upper bound did not win: %q", got)
	}
	f.Presentation.Stances[0].UpperBound = time.Time{}
	v, _ = Project(f, "reviewer")
	if got := v.Stances[0].NextStep.After; got != "2026-10-18T13:00:00Z" {
		t.Fatalf("known source time was not used: %q", got)
	}
	f.Presentation.Stances = make([]StanceEvidence, 0, maxRows+1)
	for i := 0; i < maxRows; i++ {
		f.Presentation.Stances = append(f.Presentation.Stances, StanceEvidence{UserID: "ambiguous", Revision: 0})
	}
	f.Presentation.Stances = append(f.Presentation.Stances, StanceEvidence{UserID: "attributed", Revision: 1})
	v, _ = Project(f, "reviewer")
	if len(v.Stances) != maxRows+1 || v.Stances[maxRows].UserID != "attributed" {
		t.Fatalf("ambiguous revision zero displaced attributed evidence: %+v", v.Stances)
	}
	var ambiguous bool
	for _, warning := range v.Warnings {
		if warning.Code == "ambiguous_attribution" {
			ambiguous = true
		}
	}
	if !ambiguous {
		t.Fatal("revision zero evidence lacked attribution warning")
	}
	f.Presentation.Progress.Reason = "checks-failed"
	f.Presentation.Tests = []TestEvidence{{"lint", "https://ci.example/lint", "failure", "tested"}}
	v, _ = Project(f, "agent")
	var check Warning
	for _, warning := range v.Warnings {
		if warning.Code == "check_failed" {
			check = warning
		}
	}
	if check.NextStep.Action != "fix_check" || check.NextStep.Name != "lint" || check.NextStep.URL != "https://ci.example/lint" || check.NextStep.Commit != "tested" {
		t.Fatalf("unstructured check recovery: %+v", check.NextStep)
	}
}

func TestViewStanceWarningsSurviveRowLimit(t *testing.T) {
	f := viewSample()
	f.Presentation.Stances = make([]StanceEvidence, maxRows, maxRows+1)
	for i := range f.Presentation.Stances {
		f.Presentation.Stances[i] = StanceEvidence{Kind: "approve", Revision: 1}
	}
	f.Presentation.Stances = append(f.Presentation.Stances, StanceEvidence{Kind: "request_changes", Revision: 1})
	v, _ := Project(f, "agent")
	for _, warning := range v.Warnings {
		if warning.Code == "stance_barrier" {
			return
		}
	}
	t.Fatal("late stance barrier was lost with the omitted row")
}

func TestViewOnlyTerminalCheckFailuresGetRecoveryActions(t *testing.T) {
	for _, status := range []string{"pending", "in_progress", "neutral", "skipped", "success"} {
		f := viewSample()
		f.Presentation.Progress.Reason = "checks-failed"
		f.Presentation.Tests = []TestEvidence{{"lint", "https://ci.example/lint", status, "tested"}}
		v, _ := Project(f, "agent")
		for _, warning := range v.Warnings {
			if warning.Code == "check_failed" {
				t.Fatalf("status %q incorrectly produced check recovery: %+v", status, warning)
			}
		}
	}
	for _, status := range []string{"failure", "failed", "error", "cancelled", "canceled", "timeout", "timed_out", "timed-out", "action_required", "action-required"} {
		f := viewSample()
		f.Presentation.Progress.Reason = "checks-failed"
		f.Presentation.Tests = []TestEvidence{{"lint", "https://ci.example/lint", status, "tested"}}
		v, _ := Project(f, "agent")
		var found bool
		for _, warning := range v.Warnings {
			if warning.Code == "check_failed" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("status %q did not produce check recovery", status)
		}
	}
}

func TestEscapedTextBudgetAndRecordedFactFixture(t *testing.T) {
	truncated := false
	text := bounded(strings.Repeat("<&\n", maxText), &truncated)
	raw, _ := json.Marshal(text)
	if !truncated || len(raw) > maxText+2 {
		t.Fatal("escaped text exceeded budget")
	}
	raw, err := os.ReadFile("../queue/testdata/brief-facts.json")
	if err != nil {
		t.Fatal(err)
	}
	var f Fact
	if err = json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	digest, err := Digest(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, audience := range []string{"reviewer", "operator", "agent"} {
		v, err := Project(f, audience)
		if err != nil || v.Digest != digest {
			t.Fatal("recorded 7a fixture changed binding")
		}
		raw, _ := json.Marshal(v)
		var decoded View
		if err = json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		a, _ := Render(v)
		b, _ := Render(decoded)
		if !bytes.Equal(a, b) {
			t.Fatal("recorded fixture JSON/render disagreement")
		}
	}
}

func TestAggregateViewBudget(t *testing.T) {
	f := sample()
	many := strings.Repeat("<", maxText)
	for i := 0; i < maxRows; i++ {
		file := f.Classification.Files[0]
		file.NewPath = many
		file.Hunks.Hunks = nil
		for j := 0; j < 8; j++ {
			file.Hunks.Hunks = append(file.Hunks.Hunks, cp.Hunk{Text: many})
		}
		f.Classification.Files = append(f.Classification.Files, file)
	}
	for _, audience := range []string{"reviewer", "operator", "agent"} {
		v, err := Project(f, audience)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(v)
		rendered, _ := Render(v)
		if !v.Truncated || len(raw) > MaxViewBytes || len(rendered) > MaxViewBytes {
			t.Fatal("aggregate view not bounded")
		}
	}
}

func TestViewDoesNotRecommendRepairingHistoricalFailures(t *testing.T) {
	f := viewSample()
	f.Presentation.Tests = []TestEvidence{{"ci", "https://ci.example/old", "failure", "old"}, {"ci", "https://ci.example/new", "success", "new"}}
	v, _ := Project(f, "agent")
	for _, w := range v.Warnings {
		if w.NextStep.Action == "fix_check" {
			t.Fatal("historical failure produced a current repair action")
		}
	}
}
