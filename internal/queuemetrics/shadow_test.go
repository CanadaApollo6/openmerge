package queuemetrics

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/treededupe"
)

func shadowLedgerFixture(t *testing.T, dropped int) []byte {
	t.Helper()
	l := treededupe.Ledger{Dropped: dropped}
	sha := func(c byte) string { return strings.Repeat(string(c), 40) }
	// Before the window: a passing source whose rebuild hits inside it.
	l.ObserveBuild(treededupe.Build{At: at(-50), BatchID: "s1", SHA: sha('1'), Kind: treededupe.KindBatch, Key: "k1"})
	l.ObserveOutcome(sha('1'), at(-20), true)
	l.ObserveBuild(treededupe.Build{At: at(-10), BatchID: "s0", SHA: sha('0'), Kind: treededupe.KindBatch, Key: "k1"}) // hit outside the window
	l.ObserveBuild(treededupe.Build{At: at(200), BatchID: "s2", SHA: sha('2'), Kind: treededupe.KindBatch, Key: "k1"})
	l.ObserveOutcome(sha('2'), at(260), true)
	// A singleton failure with no retry is confirmed; a bisection build with
	// the same key would reuse it.
	l.ObserveBuild(treededupe.Build{At: at(410), BatchID: "s3", SHA: sha('3'), Kind: treededupe.KindBisection, Key: "k2", Singleton: true})
	l.ObserveOutcome(sha('3'), at(470), false)
	l.ObserveBuild(treededupe.Build{At: at(500), BatchID: "s4", SHA: sha('4'), Kind: treededupe.KindBisection, Key: "k2"})
	l.ObserveBuild(treededupe.Build{At: at(600), BatchID: "s5", SHA: sha('5'), Kind: treededupe.KindBatch, Unknown: []string{"runner_image"}})
	raw, err := l.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSavingsShadowSectionIsSeparateAndNeverApplied(t *testing.T) {
	plain := partnerWeek(t)
	base := mustSavings(t, plain, window(0, 1200))
	withShadow := partnerWeek(t)
	withShadow.j.TreeDedupeShadow = shadowLedgerFixture(t, 0)
	got := mustSavings(t, withShadow, window(0, 1200))
	if got.Shadow == nil {
		t.Fatal("shadow section missing")
	}
	measured := got
	measured.Shadow = nil
	if !reflect.DeepEqual(measured, base) {
		t.Fatalf("shadow changed measured savings:\n%+v\n%+v", measured, base)
	}
	s := got.Shadow.Summary
	if s.Builds != 4 || s.Keyed != 3 || s.WouldReuseSuccess != 1 || s.WouldReuseFailure != 1 || s.AvoidableMinutes != 1.5 || s.HitsByKind[treededupe.KindBisection] != 1 || s.KeyUnknown["runner_image"] != 1 {
		t.Fatalf("window summary = %+v", s)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"potential_additional_savings_shadow":{"definition":`) || !strings.Contains(string(raw), `"would_reuse_failure":1`) {
		t.Fatalf("shadow JSON = %s", raw)
	}
	plainJSON, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plainJSON), "shadow") {
		t.Fatal("report without a ledger mentions the shadow")
	}

	var text, plainText bytes.Buffer
	if err := WriteSavingsText(&text, got); err != nil {
		t.Fatal(err)
	}
	if err := WriteSavingsText(&plainText, base); err != nil {
		t.Fatal(err)
	}
	flat := strings.Join(strings.Fields(text.String()), " ")
	for _, want := range []string{
		"Potential additional savings (shadow, not applied)",
		"Would reuse a success 1",
		"Would reuse a confirmed failure 1",
		"Hits by build kind batch 1; bisection 1",
		"Avoidable CI minutes 1.5 source runs' observed wall-clock, not billed",
		"unknown inputs: runner_image 1",
		"Shadow verdicts 1 1 agreed; 0 disagreed",
		"Nothing was reused; every build still ran CI.",
	} {
		if !strings.Contains(flat, want) {
			t.Fatalf("text lacks %q:\n%s", want, text.String())
		}
	}
	// Removing the shadow section restores the measured report byte for byte.
	start := strings.Index(text.String(), "\nPotential additional savings")
	end := strings.Index(text.String(), ShadowDefinition+"\n") + len(ShadowDefinition) + 1
	if start < 0 || end <= start || text.String()[:start]+text.String()[end:] != plainText.String() {
		t.Fatalf("shadow text altered the measured report:\n%s", text.String())
	}

	priced := mustSavings(t, withShadow, SavingsWindow{Since: at(0), Until: at(1200), FullSuiteMinutes: 20})
	if priced.Shadow.AvoidableFullSuiteMinutes != 40 {
		t.Fatalf("priced shadow = %+v", priced.Shadow)
	}
}

func TestSavingsShadowLedgerWarnings(t *testing.T) {
	f := partnerWeek(t)
	f.j.TreeDedupeShadow = []byte(`{"version":7,"builds":[]}`)
	got := mustSavings(t, f, window(0, 1200))
	if got.Shadow != nil || !strings.Contains(strings.Join(got.Warnings, "\n"), "shadow ledger is unreadable") {
		t.Fatalf("unreadable ledger: %+v %q", got.Shadow, got.Warnings)
	}
	f.j.TreeDedupeShadow = shadowLedgerFixture(t, 3)
	got = mustSavings(t, f, window(-100, 1200))
	if !strings.Contains(strings.Join(got.Warnings, "\n"), "retains builds only since") {
		t.Fatalf("truncation not warned: %q", got.Warnings)
	}
}
