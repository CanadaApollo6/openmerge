package store

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestAuthorityAdmissionLockedSourcesAndSettlement(t *testing.T) {
	_, err := net.LookupHost("localhost")
	must(t, err)
	for _, depth := range []int{1, 4} {
		for _, race := range []string{"raised", "repaired", "digest", "admitted-drift", "unverified", "remedy-bypass"} {
			t.Run(fmt.Sprintf("depth-%d/%s", depth, race), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					s := newStore(t)
					q, claim, c, d, e := authorityCheckLand(t, s)
					c.Config.SpeculationMaxDepth = depth
					state, _, err := s.Load(t.Context(), q)
					must(t, err)
					pin := state.AuthorityPin()
					_, v, _ := verificationFixture(t)
					observe := func(negative bool) {
						current, _, err := s.Load(t.Context(), q)
						must(t, err)
						evidence := authority.CloneObservation(v.Evidence)
						if negative {
							evidence.Protections[0].BypassActors = append(evidence.Protections[0].BypassActors, authority.BypassActor{Kind: "user", ID: "rogue", Mode: "always"})
							evidence.Findings = []authority.Finding{{Element: "rogue", Kind: "unexpected_bypass", ProtectionID: "repository_ruleset/base-10", Actor: &authority.BypassActor{Kind: "user", ID: "rogue", Mode: "always"}}}
						}
						appendEvents(t, s, q, queue.WriteRestrictionObserved{Binding: pin.OpenMerge.Binding, Basis: current.RulesSourceRevision(queue.WriteRestriction), ObservedAt: c.Start.At, Evidence: evidence})
					}
					if race == "repaired" {
						observe(true)
						state, _, err = s.Load(t.Context(), q)
						must(t, err)
						e.RequiredCheckSource = state.LandingCheckSources()
					}
					// The network read has finished. A competing writer commits immediately
					// before StartLanding's queue lock, without sharing its test hook.
					other := openTestStore(t, s.pool.Config().ConnString())
					switch race {
					case "raised", "repaired", "digest":
						s.beforeQueueLock = func() {
							if race == "digest" {
								appendEvents(t, other, q, queue.ConfigApplied{Commit: "new-governing", Digest: fmt.Sprintf("%064x", 42)})
								return
							}
							current, _, err := other.Load(t.Context(), q)
							must(t, err)
							evidence := authority.CloneObservation(v.Evidence)
							if race == "raised" {
								evidence.Protections[0].BypassActors = append(evidence.Protections[0].BypassActors, authority.BypassActor{Kind: "user", ID: "rogue", Mode: "always"})
								evidence.Findings = []authority.Finding{{Element: "rogue", Kind: "unexpected_bypass", ProtectionID: "repository_ruleset/base-10", Actor: &authority.BypassActor{Kind: "user", ID: "rogue", Mode: "always"}}}
							}
							appendEvents(t, other, q, queue.WriteRestrictionObserved{Binding: pin.OpenMerge.Binding, Basis: current.RulesSourceRevision(queue.WriteRestriction), ObservedAt: c.Start.At, Evidence: evidence})
						}
					case "unverified":
						// A new binding has no current observation, even if historical checks pass.
						pin.GoverningCommit = "unverified"
						pin.OpenMerge.Binding.MergeMethod = string(authority.Squash)
						appendEvents(t, s, q, queue.ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest}, *pin)
					}
					before := eventCount(t, s, q)
					if race == "remedy-bypass" {
						// Generated remedies have an exemption record, never an exemption from
						// the common LandingStarted authorization boundary.
						d = queue.LandingDecisionRecorded{BatchID: c.Start.BatchID, AttemptID: c.Start.AttemptID, TestedSHA: c.Land.TestedSHA, LandedSHA: c.Land.TestedSHA, Variant: "generated_remedy", Remedy: &queue.GeneratedRemedyDecision{ChangeID: c.Fresh.Basis.ChangeID, Revision: c.Fresh.Basis.Revision, Head: c.Fresh.Basis.Head, Suspect: c.Land.TestedSHA, Evidence: []string{"suspect-confirmed/123"}, OnRed: "revert"}}
						must(t, d.Validate())
						appendEvents(t, s, q, d)
						before = eventCount(t, s, q)
						if err := s.Emit(t.Context(), claim, c.Config)(c.Start); err == nil {
							t.Fatal("remedy record bypassed admission")
						}
						if _, err := s.Append(t.Context(), q, []queue.Event{d, c.Start}, ""); err == nil {
							t.Fatal("raw remedy start bypassed admission")
						}

					} else {
						err = s.StartLanding(t.Context(), claim, c.Config, c.Policy, []queue.ApprovalEligibilityObserved{c.Fresh}, []queue.LandingDecisionRecorded{d}, c.Start, e)
						if race == "admitted-drift" {
							must(t, err)
							observe(true)
							appendEvents(t, s, q, queue.ConfigApplied{Commit: "drift", Digest: fmt.Sprintf("%064x", 43)})
							state, _, err = s.Load(t.Context(), q)
							must(t, err)
							var resolve queue.Action
							for _, a := range queue.Plan(c.Config, state, c.Start.At.Add(c.Config.ResolveAfter+time.Second)) {
								if _, ok := a.(queue.ResolveLanding); ok {
									resolve = a
								}
							}
							if resolve == nil {
								t.Fatal("admitted attempt lost fence settlement after drift")
							}
							must(t, s.EmitWithPolicy(t.Context(), claim, c.Config, c.Policy)(queue.Landed{BatchID: c.Land.BatchID, NewBaseSHA: c.Land.TestedSHA, NewFence: "settled"}))
							state, _, err = s.Load(context.Background(), q)
							must(t, err)
							if state.BaseSHA != c.Land.TestedSHA {
								t.Fatal("drift prevented settlement")
							}
							return
						}
						if err == nil || !errors.Is(err, ErrStale) {
							t.Fatalf("race admitted or misclassified: %v", err)
						}
					}
					s.beforeQueueLock = nil
					// Raced evidence may commit, but never decisions/checks/start.
					rows, err := s.EventsAfter(t.Context(), q, 0)
					must(t, err)
					for _, row := range rows {
						switch row.Event.(type) {
						case queue.LandingStarted, queue.LandingChecksRecorded:
							t.Fatal("rejected admission leaked landing evidence")
						}
					}
					expected := before
					if race == "raised" || race == "repaired" || race == "digest" {
						expected++
					}
					if got := eventCount(t, s, q); got != expected {
						t.Fatalf("rejected admission leaked atomic events: got %d, want %d", got, expected)
					}
				})
			})
		}
	}
}

func TestAuthorityAdmissionRollbackPreservesOtherRequirements(t *testing.T) {
	_, err := net.LookupHost("localhost")
	must(t, err)
	for _, block := range []string{"none", "rulesets", "classic", "permissions", "operator", "approvals"} {
		t.Run(block, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := newStore(t)
				q, claim, c, d, _ := authorityCheckLand(t, s)
				state, _, err := s.Load(t.Context(), q)
				must(t, err)
				pin := state.AuthorityPin()
				rollback := queue.PolicyAuthorityApplied{GoverningCommit: pin.GoverningCommit, EffectiveDigest: pin.EffectiveDigest, Authority: authority.GitHub, DefaultBranch: pin.DefaultBranch, CoverageDigest: pin.CoverageDigest}
				var events []queue.Event
				switch block {
				case "rulesets":
					events = append(events, queue.RulesSourceBlocked{Source: queue.Rulesets, Problems: []string{"independent ruleset"}})
				case "classic":
					events = append(events, queue.RulesSourceBlocked{Source: queue.ClassicProtection, Problems: []string{"independent classic protection"}})
				case "permissions":
					events = append(events, queue.RulesSourceBlocked{Source: queue.Permissions, Problems: []string{"independent permission"}})
				case "operator":
					events = append(events, queue.PauseRequested{Reason: "operator", Detail: "independent operator hold"})
				}
				events = append(events, queue.ConfigApplied{Commit: rollback.GoverningCommit, Digest: rollback.EffectiveDigest}, rollback)
				appendEvents(t, s, q, events...)
				before := eventCount(t, s, q)
				fresh := []queue.ApprovalEligibilityObserved{c.Fresh}
				decisions := []queue.LandingDecisionRecorded{d}
				if block == "approvals" {
					fresh = nil
					decisions = nil
				}
				// No authority checks/observation needed after rollback. Ordinary approvals
				// and every other source still pass through the unchanged authorization.
				err = s.StartLanding(t.Context(), claim, c.Config, c.Policy, fresh, decisions, c.Start)
				if block == "none" {
					must(t, err)
					return
				}
				if err == nil {
					t.Fatal("rollback waived unrelated protection", block)
				}
				assertNoLandingSideEffects(t, s, q, claim, before)
			})
		})
	}
}

func TestAuthorityAdmissionIndependentSourceRepairRaces(t *testing.T) {
	_, err := net.LookupHost("localhost")
	must(t, err)
	for _, source := range []queue.RulesSource{queue.Rulesets, queue.ClassicProtection, queue.Permissions} {
		for _, repair := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/repair-%v", source, repair), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					s := newStore(t)
					q, claim, c, d, e := authorityCheckLand(t, s)
					if repair {
						appendEvents(t, s, q, queue.RulesSourceBlocked{Source: source, Problems: []string{"finding before network read"}})
						state, _, err := s.Load(t.Context(), q)
						must(t, err)
						e.RequiredCheckSource = state.LandingCheckSources()
					}
					other := openTestStore(t, s.pool.Config().ConnString())
					s.beforeQueueLock = func() {
						state, _, err := other.Load(t.Context(), q)
						must(t, err)
						var event queue.Event = queue.RulesSourceBlocked{Source: source, Basis: state.RulesSourceRevision(source), Problems: []string{"finding after network read"}}
						if repair {
							event = queue.RulesSourceCleared{Source: source, Basis: state.RulesSourceRevision(source)}
						}
						appendEvents(t, other, q, event)
					}
					err := s.StartLanding(t.Context(), claim, c.Config, c.Policy, []queue.ApprovalEligibilityObserved{c.Fresh}, []queue.LandingDecisionRecorded{d}, c.Start, e)
					s.beforeQueueLock = nil
					if !errors.Is(err, ErrStale) {
						t.Fatalf("source race admitted or misclassified: %v", err)
					}
					rows, err := s.EventsAfter(t.Context(), q, 0)
					must(t, err)
					for _, row := range rows {
						switch row.Event.(type) {
						case queue.LandingStarted, queue.LandingChecksRecorded:
							t.Fatal("source race leaked start/check evidence")
						}
					}
				})
			})
		}
	}
}

func TestAuthorityAdmissionSameDigestGoverningRebind(t *testing.T) {
	_, err := net.LookupHost("localhost")
	must(t, err)
	synctest.Test(t, func(t *testing.T) {
		s := newStore(t)
		q, claim, c, d, e := authorityCheckLand(t, s)
		state, _, err := s.Load(t.Context(), q)
		must(t, err)
		original := state.ConfigCommit
		pin := state.AuthorityPin()
		pin.GoverningCommit = "same-policy-new-governing-commit"
		appendEvents(t, s, q, queue.ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest}, *pin)
		state, _, err = s.Load(t.Context(), q)
		must(t, err)
		if state.ConfigCommit != original || state.AuthorityPin().GoverningCommit == original {
			t.Fatal("fixture lacks retained-config/current-pin divergence")
		}
		before := eventCount(t, s, q)
		commit := func() error {
			return s.StartLanding(t.Context(), claim, c.Config, c.Policy, []queue.ApprovalEligibilityObserved{c.Fresh}, []queue.LandingDecisionRecorded{d}, c.Start, e)
		}
		if err := commit(); !errors.Is(err, ErrStale) {
			t.Fatalf("old governing evidence accepted: %v", err)
		}
		assertNoLandingSideEffects(t, s, q, claim, before)
		e.ConfigCommit = pin.GoverningCommit
		must(t, commit())
	})
}
