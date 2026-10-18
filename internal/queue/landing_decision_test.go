package queue

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/classification"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func landingDecisionSetup(t *testing.T) (State, Config, cp.Policy, ApprovalEligibilityObserved, Land, LandingStarted, []Event) {
	t.Helper()
	s, b, _, policy, fresh := decisionSetup(t, ownerPool(2))
	fresh.OwnerPools["owned:user:owner"] = cp.IdentitySet{IDs: []string{person(2).ID}, Resolved: true, Fresh: true}
	classified, _ := s.Classification(b.ChangeID, b.Revision)
	// Use an exact rename so the golden pins independent old/new obligations.
	policy.Digest = strings.Repeat("6", 64)
	input := classified.Evidence.Classification.Inputs
	input.PolicyDigest = policy.Digest
	full := forge.DiffCoverage{Completeness: forge.DiffComplete}
	diff := forge.RevisionDiff{BaseSHA: input.Base, HeadSHA: input.Head, MergeBaseSHA: input.MergeBase, Semantics: forge.DiffSemantics, Inventory: full, Hunks: full, Files: []forge.DiffFile{{OldPath: "docs/old.md", NewPath: "docs/a.md", OldMode: "100644", NewMode: "100644", OldObjectID: strings.Repeat("e", 40), NewObjectID: strings.Repeat("e", 40), BinaryKnown: true, Rename: forge.DiffExactRename, HunkCoverage: full}}}
	evidence, err := classification.Classify(input, diff, classified.Evidence.Governing, classification.Settings{Docs: []string{"docs/**"}}, classification.TreeDeriver{Resolve: ownerPool(2)})
	if err != nil {
		t.Fatal(err)
	}
	classified = ChangeClassified{Evidence: evidence}
	facts, _ := s.ApprovalFacts(b.ChangeID)
	facts.Accountability.PolicyDigest = policy.Digest
	revision, _ := s.PRRevision(12)
	events := []Event{BaseMoved{SHA: strings.Repeat("a", 40)}, ChangeRevisionObserved{PR: 12, ChangeID: b.ChangeID, Revision: 1, HeadSHA: b.Head, Basis: revision.Basis}, classified, *facts.Accountability, *facts.Intent, native(b, 10, cp.Approve, b.At), PRObserved{PR: 12, HeadSHA: b.Head, Trusted: true, Eligible: true}, EnqueueRequested{PR: 12, At: b.At}, BatchStarted{BatchID: "b1", BaseSHA: strings.Repeat("a", 40), PRs: []BatchPR{{12, b.Head}}, At: b.At.Add(time.Minute)}, TestRefBuilt{BatchID: "b1", TestedSHA: strings.Repeat("e", 40), Method: Squash, At: b.At.Add(2 * time.Minute)}, ChecksCompleted{TestedSHA: strings.Repeat("e", 40), Success: true}}
	s = State{BaseBranch: "release", Fence: "fence"}
	for _, e := range events {
		s = Apply(s, e)
	}
	cfg := Config{MergeMethod: Squash, CheckTimeout: time.Hour, RefreshInterval: time.Hour}
	start := LandingStarted{BatchID: "b1", AttemptID: "random-attempt", FenceToken: "fence", At: b.At.Add(3 * time.Minute)}
	fresh.Basis.At = start.At
	fresh.Basis.ReadID = "landing-read"
	var land Land
	for _, a := range Plan(cfg, s, start.At) {
		if l, ok := a.(Land); ok {
			land = l
		}
	}
	if land.BatchID == "" {
		t.Fatal("no land", Plan(cfg, s, start.At))
	}
	return s, cfg, policy, fresh, land, start, events
}

func goldenLandingDecision(t *testing.T, s State, p cp.Policy, fresh ApprovalEligibilityObserved, land Land, start LandingStarted) LandingDecisionRecorded {
	t.Helper()
	d, err := s.PrepareLandingDecision(p, fresh, start, land)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestLandingDecisionGolden(t *testing.T) {
	s, _, p, fresh, land, start, events := landingDecisionSetup(t)
	// Keep a second native reviewer alongside the receipt, so the full golden
	// explicitly pins native absent bindings and shared receipt references.
	third := person(3)
	fresh.Actors[third.ID] = cp.Actor{Identity: third, Grant: cp.WriteGrant{SubjectID: third.ID, ReturnedID: third.ID, Fresh: true, Write: cp.True}}
	review := native(fresh.Basis, 11, cp.Approve, fresh.Basis.At.Add(-time.Minute))
	review.Reviewer = third
	review.Stance.UserID = third.ID
	review.Stance.Source.ActingUserID = third.ID
	s = Apply(s, review)
	events = append(events, review)
	// A receipt-only second obligation shares the same approval with the first.
	p.Approvals.Rules = append(p.Approvals.Rules, cp.Rule{When: cp.Match{Touches: []string{"docs/**"}}, Require: cp.Requirement{Selector: cp.AnyWriter, Count: 1, Receipt: true}})
	p.Approvals.Rules = append(p.Approvals.Rules, cp.Rule{When: cp.Match{Touches: []string{"docs/**"}}, Require: cp.Requirement{Selector: cp.AnyWriter, Count: 2}})
	receipt, _ := realReceipt(t, s, fresh.Basis, p, "20", fresh.Basis.At.Add(-time.Second))
	s = Apply(s, receipt)
	events = append(events, receipt)
	// Policy is a pinned semantic input in production; this fixture's policy was
	// constructed before activation and uses the same test digest throughout.
	d := goldenLandingDecision(t, s, p, fresh, land, start)
	_, raw, err := EncodeEvent(d)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("UPDATE_9") == "1" {
		if err := os.WriteFile("testdata/wire/v1/event_landing_decision_recorded.json", raw, 0644); err != nil {
			t.Fatal(err)
		}
		input := struct {
			Events []json.RawMessage
			Policy cp.Policy
			Fresh  ApprovalEligibilityObserved
			Land   Land
			Start  LandingStarted
			Config Config
		}{Policy: p, Fresh: fresh, Land: land, Start: start, Config: Config{MergeMethod: Squash, CheckTimeout: time.Hour, RefreshInterval: time.Hour}}
		for _, e := range events {
			_, v, err := EncodeEvent(e)
			if err != nil {
				t.Fatal(err)
			}
			input.Events = append(input.Events, v)
		}
		data, _ := json.MarshalIndent(input, "", "  ")
		if err := os.WriteFile("testdata/landing-start.json", data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/wire/v1/event_landing_decision_recorded.json")
	if err != nil || !bytes.Equal(want, raw) {
		t.Fatal("decision golden drift", err)
	}
	if err := s.ValidateLandingDecisions(p, land, start, []ApprovalEligibilityObserved{fresh}, []LandingDecisionRecorded{d}); err != nil {
		t.Fatal(err)
	}
	if len(d.Approval.Requirements) != 4 || !d.Approval.Requirements[2].Receipt || !reflect.DeepEqual(d.Approval.Requirements[0].ReliedOn, d.Approval.Requirements[2].ReliedOn) {
		t.Fatal("shared receipt not referenced by each obligation")
	}
	next := Apply(Apply(s, fresh), d)
	again := restart(t, next).LandingDecisions()
	if len(again) != 1 || !reflect.DeepEqual(again[0], d) {
		t.Fatal("decision lost at snapshot")
	}
	again[0].Approval.Requirements[0].ReliedOn[0].SourceID = "mutated"
	if next.LandingDecisions()[0].Approval.Requirements[0].ReliedOn[0].SourceID == "mutated" {
		t.Fatal("snapshot alias")
	}
	changed := d
	changed.Approval = detached(d.Approval)
	changed.Approval.InputFingerprint = strings.Repeat("0", 64)
	if !reflect.DeepEqual(Apply(next, changed).LandingDecisions(), next.LandingDecisions()) {
		t.Fatal("immutable decision overwritten")
	}
}

func TestGeneratedRemedyDecision(t *testing.T) {
	b := approvalBasis()
	e := LandingDecisionRecorded{BatchID: "remedy-batch", AttemptID: "random-remedy-attempt", TestedSHA: strings.Repeat("a", 40), LandedSHA: strings.Repeat("b", 40), Variant: "generated_remedy", Remedy: &GeneratedRemedyDecision{ChangeID: b.ChangeID, Revision: 1, Head: b.Head, Suspect: strings.Repeat("c", 40), Evidence: []string{"suspect-confirmed/123"}, OnRed: "revert"}}
	_, raw, err := EncodeEvent(e)
	if err != nil {
		t.Fatal(err)
	}
	path := "testdata/wire/v1/variants/generated-remedy-decision.json"
	if os.Getenv("UPDATE_9") == "1" {
		if err := os.WriteFile(path, raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(want, raw) {
		t.Fatal("generated-remedy-decision golden", err)
	}
	decoded, err := DecodeEvent(raw)
	if err != nil || !reflect.DeepEqual(e, decoded) {
		t.Fatal("remedy round trip", err)
	}
	e.Remedy.OnRed = "pause"
	if e.Validate() == nil {
		t.Fatal("pause cannot generate a remedy")
	}
}

func TestLandingDecisionStaleBasisAndLegacyAbsence(t *testing.T) {
	s, cfg, p, fresh, land, start, _ := landingDecisionSetup(t)
	d := goldenLandingDecision(t, s, p, fresh, land, start)
	for name, mutate := range map[string]func(*State, *[]ApprovalEligibilityObserved, *[]LandingDecisionRecorded){
		"missing": func(_ *State, _ *[]ApprovalEligibilityObserved, d *[]LandingDecisionRecorded) { *d = nil },
		"duplicate": func(_ *State, f *[]ApprovalEligibilityObserved, d *[]LandingDecisionRecorded) {
			*f = append(*f, (*f)[0])
			*d = append(*d, (*d)[0])
		},
		"head": func(s *State, _ *[]ApprovalEligibilityObserved, _ *[]LandingDecisionRecorded) {
			*s = Apply(*s, PRObserved{12, strings.Repeat("f", 40), true, true})
		},
		"intent": func(s *State, _ *[]ApprovalEligibilityObserved, _ *[]LandingDecisionRecorded) {
			*s = Apply(*s, Intent(fresh.Basis, "edited", "intent"))
		},
		"eligibility": func(s *State, _ *[]ApprovalEligibilityObserved, _ *[]LandingDecisionRecorded) {
			bad := detached(fresh)
			bad.Actors[person(2).ID] = cp.Actor{}
			bad.Known = false
			bad.Reasons = []string{"unavailable"}
			bad.Actors = map[string]cp.Actor{}
			*s = Apply(*s, bad)
		},
		"member": func(_ *State, _ *[]ApprovalEligibilityObserved, d *[]LandingDecisionRecorded) {
			(*d)[0].Approval.Basis.Revision++
		},
		"generation": func(_ *State, _ *[]ApprovalEligibilityObserved, d *[]LandingDecisionRecorded) {
			(*d)[0].Approval.Identity.Generation++
		},
	} {
		t.Run(name, func(t *testing.T) {
			next := s
			fs := []ApprovalEligibilityObserved{detached(fresh)}
			ds := []LandingDecisionRecorded{detached(d)}
			mutate(&next, &fs, &ds)
			if err := next.ValidateLandingDecisions(p, land, start, fs, ds); err == nil {
				t.Fatal("stale decision accepted")
			}
		})
	}
	mismatch := p
	mismatch.Digest = "other"
	_, err := s.PrepareLandingDecision(mismatch, fresh, start, land)
	var typed BriefPolicyMismatchError
	if !errors.As(err, &typed) {
		t.Fatal("policy mismatch not typed", err)
	}
	// Native reviews carry explicit absent brief/intent bindings.
	if src := d.Approval.Requirements[0].ReliedOn[0]; src.Channel != cp.NativeReview || src.BriefDigest != "" || src.IntentDigest != "" {
		t.Fatal("native binding invented")
	}
	legacy := Apply(State{}, BaseMoved{SHA: "base"})
	if len(legacy.LandingDecisions()) != 0 {
		t.Fatal("legacy evidence synthesized")
	}
	raw, err := EncodeSnapshot(legacy)
	if err != nil || bytes.Contains(raw, []byte("LandingDecisions")) {
		t.Fatal("legacy snapshot changed", err)
	}
	if !CanStart(cfg, s, land, start) {
		t.Fatal("setup is not startable")
	}
}

func TestLandingDecisionStrictNestedWire(t *testing.T) {
	s, _, p, fresh, land, start, _ := landingDecisionSetup(t)
	d := goldenLandingDecision(t, s, p, fresh, land, start)
	_, raw, _ := EncodeEvent(d)
	for _, change := range []struct{ old, new string }{
		{`"receipt":false`, `"receipt":null`},
		{`"source_kind":"review_submission"`, `"source_kind":"future"`},
		{`"sequence":10`, `"sequence":11`},
		{`"count":1`, `"count":0`},
		{`"side":"new"`, `"side":"future"`},
		{`"cause":"rule_code_owners"`, `"cause":"future"`},
		{`"channel":"native_review"`, `"channel":"future"`},
		{`"intent_digest":""`, `"intent_digest":"invented"`},
		{`"direct_protection":"not_applicable_pr"`, `"direct_protection":"approved"`},
		{`"rule_index":0`, `"rule_index":-1`},
		{`"group":""`, `"Group":""`},
		{`"group":""`, `"group":"","Group":""`},
		{`"count":1,`, `"extra":1,"count":1,`},
	} {
		bad := bytes.Replace(raw, []byte(change.old), []byte(change.new), 1)
		if bytes.Equal(raw, bad) {
			t.Fatal("ineffective mutation", change.old)
		}
		if _, err := DecodeEvent(bad); err == nil {
			t.Fatal("invalid nested wire accepted", change.new)
		}
	}
}

func TestLandingDecisionCommitIdentitiesAndDiagnostics(t *testing.T) {
	s, _, p, fresh, land, start, _ := landingDecisionSetup(t)
	land.Method = FastForward
	d := goldenLandingDecision(t, s, p, fresh, land, start)
	if d.TestedSHA == d.LandedSHA || d.LandedSHA != land.PRs[len(land.PRs)-1].HeadSHA {
		t.Fatal("marker T conflated with landed L")
	}
	recorded, _ := s.Classification(fresh.Basis.ChangeID, fresh.Basis.Revision)
	digest := classificationDecisionDigest(recorded)
	recorded.Evidence.Classification.Reasons = []cp.LabelReasons{{Label: "docs", Reasons: []string{"arbitrary human explanation"}}}
	recorded.Evidence.Classification.CodeOwners[0].Eligible.Reason = "fresh diagnostic"
	recorded.Evidence.Classification.Files[0].Reasons = []cp.LabelReasons{{Label: "docs", Reasons: []string{"file diagnostic"}}}
	if classificationDecisionDigest(recorded) != digest {
		t.Fatal("free-text reasons entered classification digest")
	}
}

func TestLandingDecisionRetentionAndReplay(t *testing.T) {
	for _, outcome := range []string{"landed", "failed", "cancelled", "superseded"} {
		t.Run(outcome, func(t *testing.T) {
			_, _, p, fresh, land, start, history := landingDecisionSetup(t)
			for i, e := range history {
				_, raw, err := EncodeEvent(e)
				if err != nil {
					t.Fatal(err)
				}
				history[i], err = DecodeEvent(raw)
				if err != nil {
					t.Fatal(err)
				}
			}
			s := historyState(history)
			d := goldenLandingDecision(t, s, p, fresh, land, start)
			events := []Event{fresh, d, start}
			for _, e := range events {
				s = Apply(s, e)
			}
			live := s
			if !reflect.DeepEqual(restart(t, s).LandingDecisions(), []LandingDecisionRecorded{d}) {
				t.Fatal("lost before settlement")
			}
			// Changes to mutable policy/input state cannot discard an in-flight attempt.
			change := LifecycleChanged{State: "retired", Detail: "removed during landing"}
			s = Apply(s, change)
			events = append(events, change)
			if len(s.LandingDecisions()) != 1 {
				t.Fatal("lost during landing")
			}
			var settle Event
			switch outcome {
			case "landed":
				settle = Landed{BatchID: start.BatchID, NewBaseSHA: land.TestedSHA, NewFence: "next"}
			case "failed":
				settle = LandingAbandoned{BatchID: start.BatchID, NewFence: "next"}
			case "cancelled":
				// Production cancellation during Landing retains the attempt until the
				// fence protocol settles it; it cannot return to a staged-only state.
				cancelled := ConfigApplied{Commit: "next", Digest: strings.Repeat("f", 64)}
				s = Apply(s, cancelled)
				events = append(events, cancelled)
				if len(s.LandingDecisions()) != 1 {
					t.Fatal("cancelled in-flight evidence lost")
				}
				settle = LandingAbandoned{BatchID: start.BatchID, NewFence: "next"}
			case "superseded":
				// An abandoned attempt settles before a replacement is staged/started.
				abandoned := LandingAbandoned{BatchID: start.BatchID, NewFence: "next"}
				s = Apply(s, abandoned)
				events = append(events, abandoned)
				if len(s.LandingDecisions()) != 0 {
					t.Fatal("superseded attempt retained")
				}
				replacement := []Event{
					BatchStarted{BatchID: "b2", BaseSHA: land.ExpectedBaseSHA, PRs: land.PRs, At: start.At.Add(time.Minute)},
					TestRefBuilt{BatchID: "b2", TestedSHA: strings.Repeat("f", 40), Method: Squash, At: start.At.Add(2 * time.Minute)},
					ChecksCompleted{TestedSHA: strings.Repeat("f", 40), Success: true},
				}
				newer := d
				newer.BatchID, newer.AttemptID = "b2", "replacement-attempt"
				newer.TestedSHA, newer.LandedSHA = strings.Repeat("f", 40), strings.Repeat("f", 40)
				nextStart := start
				nextStart.BatchID, nextStart.AttemptID, nextStart.FenceToken = newer.BatchID, newer.AttemptID, "next"
				nextStart.At = start.At.Add(3 * time.Minute)
				replacement = append(replacement, newer, nextStart)
				for _, e := range replacement {
					s = Apply(s, e)
				}
				events = append(events, replacement...)
				if s.Active == nil || s.Active.Phase != Landing || len(s.LandingDecisions()) != 1 || s.LandingDecisions()[0].AttemptID != newer.AttemptID {
					t.Fatal("replacement start did not retain its own evidence")
				}
				settle = LandingAbandoned{BatchID: newer.BatchID, NewFence: "settled"}
			}
			s = Apply(s, settle)
			events = append(events, settle)
			if len(s.LandingDecisions()) != 0 || len(restart(t, s).LandingDecisions()) != 0 {
				t.Fatal("settled decision retained")
			}
			if len(live.LandingDecisions()) != 1 {
				t.Fatal("prior projection mutated")
			}
			replay := State{BaseBranch: "release", Fence: "fence"}
			for _, e := range append(history, events...) {
				_, raw, err := EncodeEvent(e)
				if err != nil {
					t.Fatal(err)
				}
				decoded, err := DecodeEvent(raw)
				if err != nil {
					t.Fatal(err)
				}
				replay = Apply(replay, decoded)
			}
			want, _ := EncodeSnapshot(s)
			got, _ := EncodeSnapshot(replay)
			if !bytes.Equal(want, got) {
				t.Fatalf("journal replay differs\nWANT=%s\nGOT=%s", want, got)
			}
		})
	}
}

func historyState(events []Event) State {
	s := State{BaseBranch: "release", Fence: "fence"}
	for _, e := range events {
		s = Apply(s, e)
	}
	return s
}

func TestLandingDecisionOldSnapshotPrunesHistory(t *testing.T) {
	s, _, p, fresh, land, start, _ := landingDecisionSetup(t)
	d := goldenLandingDecision(t, s, p, fresh, land, start)
	s = Apply(Apply(Apply(s, fresh), d), start)
	s = Apply(s, Landed{BatchID: start.BatchID, NewBaseSHA: land.TestedSHA, NewFence: "next"})
	// Model the old codec's retained historical map; wire format stays readable.
	raw, err := EncodeSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	var old map[string]json.RawMessage
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	old["LandingDecisions"], err = json.Marshal(map[string]LandingDecisionRecorded{d.decisionKey(): d})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.LandingDecisions()) != 0 {
		t.Fatal("old historical projection retained")
	}
}

func TestLandingDecisionPrunesInvalidTesting(t *testing.T) {
	for _, reason := range []string{"invalidation", "provisional-failure"} {
		t.Run(reason, func(t *testing.T) {
			s, _, p, fresh, land, start, _ := landingDecisionSetup(t)
			d := goldenLandingDecision(t, s, p, fresh, land, start)
			s = Apply(s, d)
			prior := s
			// Detach the batch exactly as Apply does before changing its fields.
			b := *s.Active
			s.Active = &b
			if reason == "invalidation" {
				s.Active.InvalidationReason = "cancelled"
			} else {
				s.Active.ProvisionalFailure = "failed"
			}
			s.retainLandingDecisions()
			if len(s.LandingDecisions()) != 0 || len(prior.LandingDecisions()) != 1 {
				t.Fatal("prune mutated prior state or retained invalid testing")
			}
		})
	}
}
