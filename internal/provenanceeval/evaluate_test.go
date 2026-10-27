package provenanceeval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/classification"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	p "github.com/CanadaApollo6/openmerge/internal/provenance"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func expr(t *testing.T, text string) condition.Expression {
	t.Helper()
	e, err := p.ParseCondition(text)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func identity(n int) cp.Identity {
	login := map[int]string{1: "author", 2: "alice", 3: "bob", 4: "carol", 5: "submitter", 6: "accountable"}[n]
	return cp.Identity{ID: fmt.Sprintf("github.com:user:%d", n), Login: login, Kind: cp.HumanAccount}
}

func fixture(t *testing.T, rules []p.Rule) (queue.State, cp.Policy, queue.ApprovalEligibilityObserved, *p.Policy, p.Inventory) {
	t.Helper()
	policy := cp.Policy{Digest: strings.Repeat("9", 64), Approvals: &cp.Approvals{Default: cp.Requirement{Selector: cp.Group, Group: "platform", Count: 1}}, Groups: map[string]cp.IdentitySet{"platform": {}, "security": {}, "other": {}}}
	opt, err := p.NewPolicy(policy, rules, nil, p.NewParserPolicy("git version 2.56.0"), p.DefaultInventoryLimits())
	if err != nil {
		t.Fatal(err)
	}
	policy.Digest = opt.Digest()
	id, err := queue.PRChangeID("7", "Owner/Repo", "main", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	b := queue.ApprovalBasis{ChangeID: id, Revision: 1, Head: strings.Repeat("b", 40), ReadID: "observation-1", At: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)}
	base := strings.Repeat("a", 40)
	state := queue.Apply(queue.State{}, queue.ChangeRevisionObserved{PR: 1, ChangeID: id, Revision: 1, HeadSHA: b.Head, Basis: queue.RevisionObservationBasis{BaseSHA: base, At: b.At.Add(-time.Hour)}})
	full := forge.DiffCoverage{Completeness: forge.DiffComplete}
	inputs := cp.ClassificationInputs{ChangeID: string(id), Revision: 1, Generation: 1, Base: base, Head: b.Head, MergeBase: base, GoverningCommit: base, PolicyDigest: policy.Digest}
	evidence, err := classification.Classify(inputs, forge.RevisionDiff{BaseSHA: base, HeadSHA: b.Head, MergeBaseSHA: base, Semantics: forge.DiffSemantics, Inventory: full, Hunks: full}, forge.GoverningFiles{CommitSHA: base, Semantics: forge.DiffSemantics, Coverage: full}, classification.Settings{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	state = queue.Apply(state, queue.ChangeClassified{Evidence: evidence})
	account := queue.Accountability(b, identity(1), identity(1), nil, policy.Digest)
	account.SubmitterID = identity(5).ID
	account.Humans = append(account.Humans, identity(6))
	state = queue.Apply(state, account)
	state = queue.Apply(state, queue.Intent(b, "title", "body"))
	fresh := queue.ApprovalEligibilityObserved{Basis: b, Known: true, Reasons: []string{}, Actors: map[string]cp.Actor{}, Groups: map[string]cp.IdentitySet{}, OwnerPools: map[string]cp.IdentitySet{}}
	for n := 1; n <= 6; n++ {
		who := identity(n)
		fresh.Actors[who.ID] = cp.Actor{Identity: who, Grant: cp.WriteGrant{SubjectID: who.ID, ReturnedID: who.ID, Write: cp.True, Fresh: true}}
		state = withStance(t, state, b, n, cp.Approve)
	}
	for name, n := range map[string]int{"platform": 2, "security": 3, "other": 4} {
		fresh.Groups[name] = cp.IdentitySet{IDs: []string{identity(n).ID}, Resolved: true, Fresh: true}
	}
	_, parser, limits := opt.ReaderInputs()
	commits := []p.Commit{{ObjectID: b.Head, Complete: true, Declarations: p.ParseGitTrailers("Agent-Session: codex:session\n")}}
	inventory := p.Inventory{Request: p.InventoryRequest{Repository: "Owner/Repo", Queue: "main", ChangeID: string(id), Revision: "1", Generation: b.ReadID, Head: b.Head, PinnedBase: base, BuildBase: base}, Policy: parser, PolicyID: parser.Identity(), Limits: limits, Stable: []string{b.Head}, MergeBases: []string{base}, BuildMergeBases: []string{base}, Commits: commits, Complete: true, Diagnostic: p.Diagnostic{Outcome: p.ReadComplete}}
	return state, policy, fresh, opt, inventory
}

func withStance(t *testing.T, s queue.State, b queue.ApprovalBasis, n int, kind cp.StanceKind) queue.State {
	t.Helper()
	who := identity(n)
	status := forge.ReviewApproved
	if kind == cp.RequestChanges {
		status = forge.ReviewChangesRequested
	}
	r, ok := queue.NativeStance(b, forge.NativeReview{ID: int64(n + 10), CommitID: b.Head, Author: forge.UserIdentity{ID: int64(n), Login: who.Login, Kind: forge.UserAccount}, State: status, Submitted: forge.NativeTime{Time: b.At, Source: forge.ReviewSubmissionTime}})
	if !ok {
		t.Fatal("invalid fixture stance")
	}
	return queue.Apply(s, r)
}

func securityRule(t *testing.T, second bool) p.Rule {
	when := expr(t, "#agent-commits > 0")
	if second {
		when = condition.Not(expr(t, "approved-reviews-by = bob"))
	}
	return p.Rule{When: when, Approvals: &cp.Requirement{Selector: cp.Group, Group: "security", Count: 1}}
}

func TestEligibilityAcceptance(t *testing.T) {
	for _, pending := range []bool{false, true} {
		for _, second := range []bool{false, true} {
			t.Run(fmt.Sprintf("case2=%v/pending=%v", second, pending), func(t *testing.T) {
				rule := securityRule(t, second)
				for _, extraFirst := range []bool{false, true} {
					extra := p.Rule{When: expr(t, "#agent-commits > 0"), Approvals: &cp.Requirement{Selector: cp.Group, Group: "other", Count: 1}}
					rules := []p.Rule{rule, extra}
					index := 0
					if extraFirst {
						rules = []p.Rule{extra, rule}
						index = 1
					}
					s, policy, fresh, opt, inventory := fixture(t, rules)
					if pending {
						pool := fresh.Groups["platform"]
						pool.Fresh = false
						pool.Reason = "membership pending"
						fresh.Groups["platform"] = pool
					}
					expression := expr(t, "#approved-reviews-by >= 2")
					values := condition.Snapshot{"approved-reviews-by": condition.Strings("forged-expanded-pool")}
					before, _ := json.Marshal(struct {
						State queue.State
						Fresh queue.ApprovalEligibilityObserved
					}{s, fresh})
					var previous Evaluation
					for range 2 {
						got, err := Evaluate(s, policy, fresh, expression, values, opt, inventory)
						if err != nil {
							t.Fatal(err)
						}
						baseline := s.EvaluateApprovals(policy, got.Identity, fresh)
						baselineInputs := condition.Snapshot{"approved-reviews-by": baseline.ReviewAttribute()}
						if !reflect.DeepEqual(baseline, got.BaselineApprovals) || !reflect.DeepEqual(expression.Record(baselineInputs), got.BaselineInputs) || got.Layers.Baseline != p.Truth(expression, baselineInputs) {
							t.Fatal("baseline changed", got)
						}
						wantTrigger := cp.True
						wantBaseline := cp.False
						if pending {
							wantBaseline = cp.Unknown
							if second {
								wantTrigger = cp.Unknown
							}
						}
						if got.Layers.Result == cp.True || got.Layers.Baseline != wantBaseline || got.Layers.Rules[index].Trigger != wantTrigger || !got.LocalApprovals[index].Allowed {
							t.Fatalf("acceptance: %+v", got)
						}
						if !pending && (!got.BaselineApprovals.Allowed || !reflect.DeepEqual(got.BaselineApprovals.ReviewLogins, []string{"alice"})) {
							t.Fatal("baseline pools", got)
						}
						if previous.Identity.Recorded() && !reflect.DeepEqual(previous, got) {
							t.Fatal("reevaluation changed snapshot")
						}
						previous = got
					}
					after, _ := json.Marshal(struct {
						State queue.State
						Fresh queue.ApprovalEligibilityObserved
					}{s, fresh})
					if !bytes.Equal(before, after) {
						t.Fatal("evaluation mutated input")
					}
				}
			})
		}
	}
}

func TestRuleLocalConditionsAndNoCrossRuleLeak(t *testing.T) {
	bob := expr(t, "approved-reviews-by = bob")
	carol := expr(t, "approved-reviews-by = carol")
	first := securityRule(t, false)
	first.Conditions = &bob
	other := p.Rule{When: condition.All(), Conditions: &bob, Approvals: &cp.Requirement{Selector: cp.Group, Group: "other", Count: 1}}
	for _, reverse := range []bool{false, true} {
		rules := []p.Rule{first, other, {When: condition.All(), Conditions: &carol}}
		if reverse {
			rules[0], rules[1] = rules[1], rules[0]
		}
		s, policy, fresh, opt, inv := fixture(t, rules)
		for _, pending := range []bool{false, true} {
			if pending {
				g := fresh.Groups["platform"]
				g.Fresh = false
				g.Reason = "pending"
				fresh.Groups["platform"] = g
			}
			got, err := Evaluate(s, policy, fresh, condition.All(), nil, opt, inv)
			if err != nil {
				t.Fatal(err)
			}
			index := 0
			if reverse {
				index = 1
			}
			want := cp.True
			if pending {
				want = cp.Unknown
			}
			if got.Layers.Rules[index].Obligation != want || got.Layers.Rules[1-index].Obligation == cp.True || got.Layers.Rules[2].Obligation == cp.True {
				t.Fatal("rule-local leak", got)
			}
		}
	}
}

func TestRecommendedHumanRuleAndBaselineVeto(t *testing.T) {
	rule := p.Rule{When: expr(t, "#agent-commits > 0"), Approvals: &cp.Requirement{Selector: cp.Human, Count: 1}}
	s, policy, fresh, opt, inv := fixture(t, []p.Rule{rule})
	got, err := Evaluate(s, policy, fresh, condition.All(), nil, opt, inv)
	if err != nil || got.Layers.Result != cp.True {
		t.Fatal(got, err)
	}
	if !reflect.DeepEqual(got.LocalApprovals[0].Requirements[0].ApprovedIDs, []string{identity(2).ID, identity(3).ID, identity(4).ID}) {
		t.Fatal("exclusions", got)
	}
	// Alice's existing baseline approval may satisfy both layers.
	for _, n := range []int{3, 4} {
		a := fresh.Actors[identity(n).ID]
		a.Identity.Kind = cp.AgentAccount
		fresh.Actors[identity(n).ID] = a
	}
	got, err = Evaluate(s, policy, fresh, condition.All(), nil, opt, inv)
	if err != nil || got.Layers.Result != cp.True || len(got.LocalApprovals[0].Requirements[0].ApprovedIDs) != 1 {
		t.Fatal(got, err)
	}
	// Only excluded writers and configured agents remain: no self exception.
	a := fresh.Actors[identity(2).ID]
	a.Identity.Kind = cp.AgentAccount
	fresh.Actors[identity(2).ID] = a
	got, err = Evaluate(s, policy, fresh, condition.All(), nil, opt, inv)
	if err != nil || got.Layers.Result != cp.False || len(got.LocalApprovals[0].Requirements[0].EligibleIDs) != 0 {
		t.Fatal("single writer", got, err)
	}
	// A baseline request-for-changes remains a veto independently of added pools.
	s, policy, fresh, opt, inv = fixture(t, []p.Rule{securityRule(t, false)})
	request, ok := queue.NativeStance(fresh.Basis, forge.NativeReview{ID: 100, CommitID: fresh.Basis.Head, Author: forge.UserIdentity{ID: 2, Login: "alice", Kind: forge.UserAccount}, State: forge.ReviewChangesRequested, Submitted: forge.NativeTime{Time: fresh.Basis.At.Add(time.Second), Source: forge.ReviewSubmissionTime}})
	if !ok {
		t.Fatal("request")
	}
	s = queue.Apply(s, request)
	got, err = Evaluate(s, policy, fresh, condition.All(), nil, opt, inv)
	if err != nil || got.Layers.Result != cp.False || len(got.BaselineApprovals.BlockingIDs) != 1 || !got.LocalApprovals[0].Allowed {
		t.Fatal(got, err)
	}
}

func TestInventoryFailuresAndBindings(t *testing.T) {
	s, policy, fresh, opt, inv := fixture(t, []p.Rule{securityRule(t, false)})
	for _, outcome := range []p.ReadOutcome{p.ReadPending, p.LimitExceeded, p.InvalidInput} {
		bad := inv
		bad.Complete = false
		bad.Diagnostic = p.Diagnostic{Outcome: outcome, Resource: "test"}
		// Retain a convincing partial human declaration: it must remain unknown.
		bad.Commits = []p.Commit{{ObjectID: inv.Request.Head, Complete: true, Declarations: []p.Declaration{{Source: "git-identities", Version: p.ClassifierVersion, Kind: "author", Value: "human@example.test", Class: p.Human, Outcome: p.NotRequested}}}}
		got, err := Evaluate(s, policy, fresh, condition.All(), nil, opt, bad)
		want := cp.Unknown
		if outcome != p.ReadPending {
			want = cp.False
		}
		if err != nil || got.PreCI != want || got.Layers.Result == cp.True || got.Identity.Recorded() {
			t.Fatal(got, err)
		}
		if p.Truth(expr(t, "provenance = human"), p.DeclarationAttributes(got.Inventory.Commits, got.Inventory.Complete)) != cp.Unknown {
			t.Fatal("invented human")
		}
		pending := fresh
		pending.Known, pending.Reasons = false, []string{"fresh membership pending"}
		blocked, err := Evaluate(s, policy, pending, condition.All(), nil, opt, bad)
		if err != nil || blocked.Layers.Result != want {
			t.Fatal("terminal inventory outcome lost behind pending approval inputs", blocked, err)
		}

		if _, err := Brief(s, policy, fresh.Basis, opt, bad); err == nil {
			t.Fatal("issued incomplete brief")
		}
	}
	for _, mutate := range []func(*p.Inventory){
		func(i *p.Inventory) { i.Request.Head = strings.Repeat("c", 40) }, func(i *p.Inventory) { i.Request.Revision = "2" }, func(i *p.Inventory) { i.Request.ChangeID = "other" }, func(i *p.Inventory) { i.Request.Generation = "old" }, func(i *p.Inventory) { i.Request.PinnedBase = strings.Repeat("d", 40) }, func(i *p.Inventory) { i.PolicyID = "other" }, func(i *p.Inventory) { i.Limits.SourceCommits-- },
	} {
		bad := inv
		mutate(&bad)
		if _, err := Evaluate(s, policy, fresh, condition.All(), nil, opt, bad); err == nil {
			t.Fatal("stale inventory accepted")
		}
	}
}

func TestDeclarationBoundReceiptIdentity(t *testing.T) {
	rule := securityRule(t, true)
	s, policy, fresh, opt, inv := fixture(t, []p.Rule{rule})
	got, err := Evaluate(s, policy, fresh, condition.All(), nil, opt, inv)
	if err != nil {
		t.Fatal(err)
	}
	receipt := got.Identity
	// Change all mutable approval inputs, observation generation, and build-base
	// observation. None changes the declaration-derived receipt identity.
	fresh.Basis.ReadID = "observation-2"
	inv.Request.Generation = fresh.Basis.ReadID
	inv.Request.BuildBase = strings.Repeat("d", 40)
	pool := fresh.Groups["platform"]
	pool.Fresh = false
	pool.Reason = "membership moved"
	fresh.Groups["platform"] = pool
	refreshed, err := Evaluate(s, policy, fresh, condition.All(), nil, opt, inv)
	if err != nil || refreshed.Identity != receipt {
		t.Fatal("mutable receipt churn", refreshed, err)
	}
	if err := brief.ValidateReceipt(receipt, refreshed.Brief); err != nil {
		t.Fatal(err)
	}
	// A mutable review-based trigger can become definitely false on this same
	// head. The potential rule remains bound to the existing receipt.
	fresh.Groups["platform"] = cp.IdentitySet{IDs: []string{identity(2).ID, identity(3).ID}, Resolved: true, Fresh: true}
	skipped, err := Evaluate(s, policy, fresh, condition.All(), nil, opt, inv)
	if err != nil || skipped.Layers.Rules[0].Trigger != cp.False || skipped.Identity != receipt {
		t.Fatal("review-based applicability churn", skipped, err)
	}
	// Changes to declarations and potential requirement definitions invalidate.
	changed := inv
	changed.Commits = []p.Commit{{ObjectID: inv.Request.Head, Complete: true}}
	f, err := Brief(s, policy, fresh.Basis, opt, changed)
	if err != nil || brief.ValidateReceipt(receipt, f) == nil {
		t.Fatal("changed inventory accepted", err)
	}
	newRule := rule
	r := *rule.Approvals
	r.Count++
	newRule.Approvals = &r
	other := fixtureOpt(t, []p.Rule{newRule})
	_, parser, limits := other.ReaderInputs()
	changed = inv
	changed.Policy = parser
	changed.PolicyID = parser.Identity()
	changed.Limits = limits
	// Independent policy digest rejects this edit before stale classification
	// can produce any new receipt, even if declarations have not changed.
	if _, err := Brief(s, policy, fresh.Basis, other, changed); err == nil {
		t.Fatal("edited policy accepted")
	}
	newDigest, err := p.DeclarationDigest(inv.Commits, true, other.Rules())
	if err != nil || newDigest == got.Brief.DeclarationDigest {
		t.Fatal("potential rule dropped", err)
	}
	canonical, err := brief.Canonical(got.Brief)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "brief-provenance.json", canonical)
	golden(t, "brief-provenance.sha256", []byte(receipt.BriefDigest))
}

func fixtureOpt(t *testing.T, rules []p.Rule) *p.Policy {
	t.Helper()
	_, _, _, opt, _ := fixture(t, rules)
	return opt
}
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := "testdata/" + name
	if os.Getenv("UPDATE_PROVENANCE_4A") == "1" {
		if err := os.MkdirAll("testdata", 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("golden %s differs: %v", name, err)
	}
}

func TestDisabledProvenanceBaselineBytes(t *testing.T) {
	s, policy, fresh, _, _ := fixture(t, []p.Rule{securityRule(t, false)})
	expression := expr(t, "#approved-reviews-by >= 2")
	got, err := Evaluate(s, policy, fresh, expression, nil, nil, p.Inventory{})
	if err != nil {
		t.Fatal(err)
	}
	fact, err := s.RecordedBrief(policy, fresh.Basis)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := brief.Identity(fact)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(fact)
	if err != nil {
		t.Fatal(err)
	}
	again, err := json.Marshal(got.Brief)
	if err != nil || !bytes.Equal(raw, again) || got.Identity != identity || !reflect.DeepEqual(got.BaselineApprovals, s.EvaluateApprovals(policy, identity, fresh)) {
		t.Fatal("disabled baseline drift", err)
	}
	golden(t, "baseline-brief.json", raw)
	canonical, err := brief.Canonical(got.Brief)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "baseline-brief-canonical.json", canonical)
	golden(t, "baseline-receipt.sha256", []byte(identity.BriefDigest))
}
