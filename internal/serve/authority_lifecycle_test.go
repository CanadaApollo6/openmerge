package serve

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func lifecycleConfig(contents string) string {
	return strings.Replace(contents, "version: 1\n", "version: 1\npolicy_authority: openmerge\nbreak_glass: {team: acme/emergency}\n", 1)
}

// The internal adapter stands in for 9c's governing acquisition. It never
// installs worker policy/projection or push hooks from the candidate or journal.
func lifecycleResolver(t *testing.T, s *Server, teamID *string, read func(context.Context) (authority.Observation, error)) func(context.Context, config.AuthorityCandidate, string, string) (authorityLifecycleResolution, error) {
	t.Helper()
	receipt := activationReceipt(t, s.store)
	return func(_ context.Context, c config.AuthorityCandidate, _, _ string) (authorityLifecycleResolution, error) {
		p := authority.Policy{Mode: c.Authority.Mode.Normalized()}
		if p.Mode == authority.OpenMerge {
			p.BreakGlass = authority.ActorBinding{Declared: *c.Authority.BreakGlass, Resolved: authority.ResolvedActor{Team: &authority.ResolvedTeam{OrganizationID: "100", TeamID: *teamID}}}
		}
		return authorityLifecycleResolution{Policy: p, RepositoryID: 123, Activation: authorityActivation{Receipt: receipt, ReceiptMaxAge: time.Hour, Input: forge.WriteRestrictionInput{AppID: "5383059", ExpectedRefs: []string{"refs/heads/main", "refs/heads/release/v1", "refs/heads/release/v2"}}, Read: read}}, nil
	}
}

func lifecycleRead(context.Context) (authority.Observation, error) {
	return authority.Observation{Inventory: authority.ReadFailure, Protections: []authority.Protection{}, Coverage: []authority.Coverage{}, Findings: []authority.Finding{}}, nil
}

func assertLifecyclePair(t *testing.T, s *Server, w *worker, before int64, mode authority.Mode) queue.PolicyAuthorityApplied {
	t.Helper()
	events, err := s.store.EventsAfter(t.Context(), w.id, before)
	must(t, err)
	var found *queue.PolicyAuthorityApplied
	for i, event := range events {
		if pin, ok := event.Event.(queue.PolicyAuthorityApplied); ok {
			if i == 0 {
				t.Fatal("pin lacks adjacent config")
			}
			applied, ok := events[i-1].Event.(queue.ConfigApplied)
			if !ok || applied.Commit != pin.GoverningCommit || applied.Digest != pin.EffectiveDigest || pin.Authority != mode {
				t.Fatalf("mismatched config/pin: %+v %+v", events[i-1], pin)
			}
			found = &pin
		}
	}
	if found == nil {
		t.Fatal("no lifecycle pin emitted")
	}
	state := lifecycleState(t, s, w)
	if !reflect.DeepEqual(state.AuthorityPin(), found) || state.ConfigDigest != found.EffectiveDigest {
		t.Fatal("durable fold differs from emitted pair", state.AuthorityPin())
	}
	return *found
}

func TestAuthorityLifecycleMixedRepositoriesAndBases(t *testing.T) {
	s, f := managedFixture(t, releaseConfig, Options{})
	other := forge.RepoRef{Path: "acme/ordinary"}
	f.Init(other, "main", "openmerge/fence/main")
	_, err := f.PushBase(other, "main", "ordinary config", map[string]string{".openmerge.yml": repositoryConfig})
	must(t, err)
	ordinary := managedRepository(f, other.Path)
	s.repositories = append(s.repositories, ordinary)
	must(t, s.RefreshBranches(t.Context()))
	var ordinaryWorker *worker
	for _, w := range s.workers() {
		if w.Key.Repository == other.Path {
			ordinaryWorker = w
		}
	}
	_, ordinaryBefore, err := s.store.Load(t.Context(), ordinaryWorker.id)
	must(t, err)
	team := "200"
	reads := 0
	resolver := lifecycleResolver(t, s, &team, func(ctx context.Context) (authority.Observation, error) { reads++; return lifecycleRead(ctx) })
	for _, w := range s.workers() {
		if w.Key.Repository == repo.Path {
			w.AuthorityRequired = true
			w.resolveAuthorityLifecycle = resolver
		}
	}
	original := s.repositories[0].Target
	s.repositories[0].Target = func(base string) Target {
		target := original(base)
		target.AuthorityRequired = true
		target.resolveAuthorityLifecycle = resolver
		return target
	}
	before := map[store.QueueID]int64{}
	for _, w := range s.workers() {
		_, before[w.id], err = s.store.Load(t.Context(), w.id)
		must(t, err)
	}
	_, err = f.PushBase(repo, "main", "enter authority", map[string]string{".openmerge.yml": lifecycleConfig(releaseConfig)})
	must(t, err)
	must(t, s.RefreshBranches(t.Context()))
	digests := map[store.QueueID]string{}
	for _, w := range s.workers() {
		if w.Key.Repository == repo.Path {
			pin := assertLifecyclePair(t, s, w, before[w.id], authority.OpenMerge)
			digests[w.id] = pin.EffectiveDigest
			if pin.OpenMerge.Binding.ResolvedActor.Team.TeamID != team {
				t.Fatal("unresolved actor")
			}
		}
	}
	// An unchanged cycle keeps the exact durable config/pin receipts.
	for _, w := range s.workers() {
		_, before[w.id], err = s.store.Load(t.Context(), w.id)
		must(t, err)
	}
	must(t, s.RefreshBranches(t.Context()))
	for _, w := range s.workers() {
		_, after, err := s.store.Load(t.Context(), w.id)
		must(t, err)
		if after != before[w.id] {
			t.Fatal("unchanged lifecycle appended events", w.Key)
		}
	}
	// A governing commit change with identical policy still emits a matching
	// pair; the pin records the new commit without inventing a changed digest.
	for _, w := range s.workers() {
		_, before[w.id], err = s.store.Load(t.Context(), w.id)
		must(t, err)
	}
	rebindTip, err := f.PushBase(repo, "main", "same authority policy", map[string]string{".openmerge.yml": lifecycleConfig(releaseConfig)})
	must(t, err)
	must(t, s.RefreshBranches(t.Context()))
	for _, w := range s.workers() {
		if w.Key.Repository == repo.Path {
			pin := assertLifecyclePair(t, s, w, before[w.id], authority.OpenMerge)
			if pin.GoverningCommit != rebindTip || pin.EffectiveDigest != digests[w.id] {
				t.Fatal("same policy failed to rebind actual governing commit")
			}
		}
	}
	// A repository-wide actor resolution change reaches every served base even
	// with an unchanged governing tip. New bases receive the same current policy.
	team = "201"
	tip, err := f.ReadRef(t.Context(), repo, "main")
	must(t, err)
	f.Init(repo, "release/v2", "openmerge/fence/release/v2")
	must(t, f.PushRef(repo, "release/v2", tip))
	for _, w := range s.workers() {
		_, before[w.id], err = s.store.Load(t.Context(), w.id)
		must(t, err)
	}
	must(t, s.RefreshBranches(t.Context()))
	for _, w := range s.workers() {
		if w.Key.Repository == repo.Path {
			pin := assertLifecyclePair(t, s, w, before[w.id], authority.OpenMerge)
			if pin.EffectiveDigest == digests[w.id] {
				t.Fatal("actor digest failed to propagate", w.Key)
			}
			if pin.OpenMerge.Binding.ResolvedActor.Team.TeamID != team {
				t.Fatal("stale actor")
			}
		}
	}
	// Per-queue atomicity is not a repository transaction. A failed base keeps
	// its old pair while siblings advance, then catches up on a successful retry.
	team = "202"
	failed := baseWorker(t, s, "release/v1")
	oldFailed := lifecycleState(t, s, failed).AuthorityPin()
	failure := errors.New("release actor quota exhausted")
	failed.resolveAuthorityLifecycle = func(context.Context, config.AuthorityCandidate, string, string) (authorityLifecycleResolution, error) {
		return authorityLifecycleResolution{}, failure
	}
	if err := s.RefreshBranches(t.Context()); !errors.Is(err, failure) {
		t.Fatal("missing per-base refresh failure", err)
	}
	if !reflect.DeepEqual(lifecycleState(t, s, failed).AuthorityPin(), oldFailed) {
		t.Fatal("failed base published a partial pair")
	}
	for _, w := range s.workers() {
		if w.Key.Repository == repo.Path && w != failed {
			if lifecycleState(t, s, w).AuthorityPin().OpenMerge.Binding.ResolvedActor.Team.TeamID != team {
				t.Fatal("sibling base failed to advance", w.Key)
			}
		}
	}
	failed.resolveAuthorityLifecycle = resolver
	must(t, s.RefreshBranches(t.Context()))
	if lifecycleState(t, s, failed).AuthorityPin().OpenMerge.Binding.ResolvedActor.Team.TeamID != team {
		t.Fatal("failed base did not catch up")
	}
	// Rollback emits the github discriminant once, then ordinary github config
	// changes and new queues acquire no authority events.
	for _, w := range s.workers() {
		_, before[w.id], err = s.store.Load(t.Context(), w.id)
		must(t, err)
	}
	readsBeforeRollback := reads
	_, err = f.PushBase(repo, "main", "rollback", map[string]string{".openmerge.yml": releaseConfig})
	must(t, err)
	must(t, s.RefreshBranches(t.Context()))
	if reads != readsBeforeRollback {
		t.Fatal("rollback acquired authority observation")
	}
	for _, w := range s.workers() {
		if w.Key.Repository == repo.Path {
			pin := assertLifecyclePair(t, s, w, before[w.id], authority.GitHub)
			if pin.OpenMerge != nil {
				t.Fatal("rollback retained actor")
			}
		}
	}
	for _, w := range s.workers() {
		_, before[w.id], err = s.store.Load(t.Context(), w.id)
		must(t, err)
	}
	_, err = f.PushBase(repo, "main", "ordinary github change", map[string]string{".openmerge.yml": strings.Replace(releaseConfig, "check-success = ci", "check-success = ci-new", 1)})
	must(t, err)
	must(t, s.RefreshBranches(t.Context()))
	for _, w := range s.workers() {
		events, err := s.store.EventsAfter(t.Context(), w.id, before[w.id])
		must(t, err)
		for _, e := range events {
			if _, ok := e.Event.(queue.PolicyAuthorityApplied); ok {
				t.Fatal("ordinary github change emitted pin")
			}
		}
	}
	events, err := s.store.EventsAfter(t.Context(), ordinaryWorker.id, ordinaryBefore)
	must(t, err)
	for _, e := range events {
		switch e.Event.(type) {
		case queue.PolicyAuthorityApplied, queue.WriteRestrictionObserved, queue.WriteRestrictionVerified:
			t.Fatal("mixed ordinary repository emitted authority event")
		}
	}
}

func TestAuthorityLifecycleInvalidatesPolicyChangesAndReenable(t *testing.T) {
	for _, change := range []string{"default-branch", "selector", "actor", "method", "queue-rule", "reenable"} {
		t.Run(change, func(t *testing.T) {
			s, w, f, _ := fixture(t, repositoryConfig)
			must(t, s.configuration(t.Context(), w))
			team := "200"
			w.AuthorityRequired = true
			w.resolveAuthorityLifecycle = lifecycleResolver(t, s, &team, lifecycleRead)
			contents := lifecycleConfig(repositoryConfig)
			_, err := f.PushBase(repo, "main", "authority", map[string]string{".openmerge.yml": contents})
			must(t, err)
			must(t, s.configuration(t.Context(), w))
			state := lifecycleState(t, s, w)
			pin := state.AuthorityPin()
			// Seed a complete applied observation to prove the fold invalidates it.
			raw, err := os.ReadFile("../queue/testdata/wire/v1/event_write_restriction_verified.json")
			must(t, err)
			event, err := queue.DecodeEvent(raw)
			must(t, err)
			evidence := event.(queue.WriteRestrictionVerified).Evidence
			_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.WriteRestrictionObserved{Binding: pin.OpenMerge.Binding, Basis: state.RulesSourceRevision(queue.WriteRestriction), ObservedAt: time.Now(), Evidence: evidence}}, "")
			must(t, err)
			if lifecycleState(t, s, w).WriteRestrictionStatus().State != "verified" {
				t.Fatal("test did not establish complete observation")
			}
			switch change {
			case "default-branch":
				tip, err := f.ReadRef(t.Context(), repo, "main")
				must(t, err)
				must(t, f.PushRef(repo, "trunk", tip))
				f.SetDefaultBranch(repo, "trunk")
				contents = strings.Replace(contents, "name: default", "name: default\n    base: main", 1)
				_, err = f.PushBase(repo, "trunk", "default rename", map[string]string{".openmerge.yml": contents})
				must(t, err)
			case "selector":
				contents += "  - name: release\n    base: release/*\n    merge_when: [check-success = ci]\n"
				_, err = f.PushBase(repo, "main", "selector", map[string]string{".openmerge.yml": contents})
				must(t, err)
			case "actor":
				team = "201"
			case "method":
				contents = strings.Replace(contents, "name: default", "name: default\n    merge_method: merge", 1)
				_, err = f.PushBase(repo, "main", "method", map[string]string{".openmerge.yml": contents})
				must(t, err)
			case "queue-rule":
				contents = strings.Replace(contents, "name: default", "name: default\n    enter_when: [label = ready]", 1)
				_, err = f.PushBase(repo, "main", "queue rule", map[string]string{".openmerge.yml": contents})
				must(t, err)
			case "reenable":
				_, err = f.PushBase(repo, "main", "rollback", map[string]string{".openmerge.yml": repositoryConfig})
				must(t, err)
				must(t, s.configuration(t.Context(), w))
				_, err = f.PushBase(repo, "main", "reenable", map[string]string{".openmerge.yml": contents})
				must(t, err)
			}
			_, before, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			must(t, s.configuration(t.Context(), w))
			next := assertLifecyclePair(t, s, w, before, authority.OpenMerge)
			if change != "reenable" && next.EffectiveDigest == pin.EffectiveDigest {
				t.Fatal("policy change did not change digest")
			}
			if status := lifecycleState(t, s, w).WriteRestrictionStatus(); status.State != "unverified" || !status.LastVerifiedAt.IsZero() || !status.LastObservedAt.IsZero() {
				t.Fatal("policy change revived observation", status)
			}
		})
	}
}

func TestAuthorityLifecycleReadFailuresDoNotPublishConfigOrPin(t *testing.T) {
	for _, phase := range []string{"resolve", "read", "receipt-expiry"} {
		t.Run(phase, func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				s, w, f, _ := fixture(t, repositoryConfig)
				must(t, s.configuration(t.Context(), w))
				old := lifecycleState(t, s, w)
				_, before, err := s.store.Load(t.Context(), w.id)
				must(t, err)
				w.AuthorityRequired = true
				team := "200"
				sentinel := errors.New("lifecycle quota exhausted")
				read := func(context.Context) (authority.Observation, error) {
					if phase == "receipt-expiry" {
						time.Sleep(2 * time.Hour)
						return lifecycleRead(t.Context())
					}
					return authority.Observation{}, sentinel
				}
				w.resolveAuthorityLifecycle = lifecycleResolver(t, s, &team, read)
				if phase == "resolve" {
					w.resolveAuthorityLifecycle = func(context.Context, config.AuthorityCandidate, string, string) (authorityLifecycleResolution, error) {
						return authorityLifecycleResolution{}, sentinel
					}
				}
				_, err = f.PushBase(repo, "main", "authority", map[string]string{".openmerge.yml": lifecycleConfig(repositoryConfig)})
				must(t, err)
				w.managed, w.ready = true, true
				for len(s.refresh) > 0 {
					<-s.refresh
				}
				err = s.configuration(t.Context(), w)
				if w.ready || !strings.Contains(w.waiting, "discovery will retry") || len(s.refresh) == 0 {
					t.Fatal("failed lifecycle did not notify discovery", w.ready, w.waiting)
				}
				if phase == "receipt-expiry" {
					if err == nil || !strings.Contains(err.Error(), "stale emission receipt") {
						t.Fatal(err)
					}
				} else if !errors.Is(err, sentinel) {
					t.Fatal(err)
				}
				after := lifecycleState(t, s, w)
				if after.ConfigDigest != old.ConfigDigest || after.ConfigCommit != old.ConfigCommit || after.AuthorityPin() != nil || w.configError == nil {
					t.Fatal("failed lifecycle published policy")
				}
				events, err := s.store.EventsAfter(t.Context(), w.id, before)
				must(t, err)
				for _, e := range events {
					switch e.Event.(type) {
					case queue.ConfigApplied, queue.PolicyAuthorityApplied:
						t.Fatalf("failed lifecycle emitted %T", e.Event)
					}
				}
			})
		})
	}
}

func TestAuthorityLifecycleAdoptsDraftWithAtomicConfig(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	must(t, s.configuration(t.Context(), w))
	old := lifecycleState(t, s, w)
	raw, err := os.ReadFile("../queue/testdata/wire/v1/event_write_restriction_verified.json")
	must(t, err)
	event, err := queue.DecodeEvent(raw)
	must(t, err)
	draft := event.(queue.WriteRestrictionVerified)
	team := "200"
	reads := 0
	resolver := lifecycleResolver(t, s, &team, func(context.Context) (authority.Observation, error) { reads++; return draft.Evidence, nil })
	draftID := int64(0)
	w.AuthorityRequired = true
	w.resolveAuthorityLifecycle = func(ctx context.Context, c config.AuthorityCandidate, branch, tip string) (authorityLifecycleResolution, error) {
		resolved, err := resolver(ctx, c, branch, tip)
		resolved.Activation.DraftID = draftID
		resolved.Activation.Input.ExpectedRefs = []string{"refs/heads/main"}
		return resolved, err
	}
	tip, err := f.PushBase(repo, "main", "activate verified draft", map[string]string{".openmerge.yml": lifecycleConfig(repositoryConfig)})
	must(t, err)
	must(t, s.loadConfiguration(t.Context(), w, "main", tip))
	request, err := s.prepareAuthorityLifecycle(t.Context(), w, old, "main", tip)
	must(t, err)
	draft.Binding = request.Pin.OpenMerge.Binding
	draft.VerifiedAt = time.Now().Add(-time.Minute)
	draft.Basis = 0
	draft.Nonce = "atomic-lifecycle-proposal"
	draft.Evidence, err = forge.InterpretWriteRestriction(request.Input, draft.Evidence)
	must(t, err)
	draft.ProtectionFingerprint, err = draft.Evidence.Fingerprint()
	must(t, err)
	receipts, err := s.store.CommitWriteRestrictionVerification(t.Context(), []store.VerificationRecord{{QueueID: w.id, Event: draft}})
	must(t, err)
	draftID = receipts[0].EventID
	_, before, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	must(t, s.configuration(t.Context(), w))
	pin := assertLifecyclePair(t, s, w, before, authority.OpenMerge)
	if !queue.AuthorityDraftAdopted(pin) || pin.OpenMerge.Activation.Proposed.EventID != draftID {
		t.Fatal("atomic config path lost interpreted adoption")
	}
	if lifecycleState(t, s, w).WriteRestrictionStatus().State != "verified" {
		t.Fatal("atomic adopted pin did not fold verified")
	}

	// An unchanged adopted queue needs neither a seeing read nor another pair.
	draftID = 0
	_, before, err = s.store.Load(t.Context(), w.id)
	must(t, err)
	readsBefore := reads
	must(t, s.configuration(t.Context(), w))
	_, after, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if reads != readsBefore || after != before {
		t.Errorf("unchanged adopted cycle read or wrote: reads %d -> %d, events %d -> %d", readsBefore, reads, before, after)
	}
	// Runtime observation is a separate seam (2c); seed it before testing push
	// options so this test isolates activation retention across the rebind.
	state := lifecycleState(t, s, w)
	_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.WriteRestrictionObserved{Binding: pin.OpenMerge.Binding, Basis: state.RulesSourceRevision(queue.WriteRestriction), ObservedAt: time.Now(), Evidence: draft.Evidence}}, "")
	must(t, err)
	_, err = f.PushBase(repo, "main", "unrelated rebind", map[string]string{"README.md": "unrelated"})
	must(t, err)
	must(t, s.configuration(t.Context(), w))
	rebound := lifecycleState(t, s, w).AuthorityPin()
	if !queue.AuthorityDraftAdopted(*rebound) || !reflect.DeepEqual(rebound.OpenMerge.Activation, pin.OpenMerge.Activation) {
		t.Fatal("same-binding rebind lost adopted activation")
	}
	w.PrepareAuthorityPush = func(context.Context) (AuthorityPushPolicy, error) {
		return AuthorityPushPolicy{Mode: authority.OpenMerge, GoverningCommit: w.configTip, EffectiveDigest: w.configDigest, AppID: "5383059"}, nil
	}
	w.PinAuthorityPush = func(context.Context, AuthorityPushPolicy) error { return nil }
	w.ObserveAuthorityPush = func(context.Context, AuthorityPushPolicy) error { return nil }
	opts := s.authorityPushOptions(t.Context(), w, forge.PushLand, []forge.PushRefIntent{{Ref: "refs/heads/main", OldSHA: w.configTip, NewSHA: w.configTip}})
	if opts.Context.Binding().GoverningCommit == "" {
		t.Fatalf("rebind has no valid push context: %v", opts.Guard(t.Context(), opts.Context))
	}
	must(t, opts.Guard(t.Context(), opts.Context))
	// Queue display names are not effective rules or selector coverage.
	_, err = f.PushBase(repo, "main", "rename queue", map[string]string{".openmerge.yml": strings.Replace(lifecycleConfig(repositoryConfig), "name: default", "name: renamed", 1)})
	must(t, err)
	must(t, s.configuration(t.Context(), w))
	renamed := lifecycleState(t, s, w).AuthorityPin()
	if renamed.EffectiveDigest != pin.EffectiveDigest || !queue.AuthorityDraftAdopted(*renamed) {
		t.Fatal("queue display name invalidated verification")
	}

}

func TestAuthorityLifecycleRollbackReceiptAndUnchangedWire(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	must(t, s.configuration(t.Context(), w))
	team := "200"
	w.AuthorityRequired = true
	resolver := lifecycleResolver(t, s, &team, func(context.Context) (authority.Observation, error) { return lifecycleRead(t.Context()) })
	w.resolveAuthorityLifecycle = resolver
	_, err := f.PushBase(repo, "main", "authority", map[string]string{".openmerge.yml": lifecycleConfig(repositoryConfig)})
	must(t, err)
	must(t, s.configuration(t.Context(), w))
	original := lifecycleState(t, s, w)
	_, before, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	tip, err := f.PushBase(repo, "main", "rollback", map[string]string{".openmerge.yml": repositoryConfig})
	must(t, err)
	readerCalled := false
	w.resolveAuthorityLifecycle = func(ctx context.Context, c config.AuthorityCandidate, branch, tip string) (authorityLifecycleResolution, error) {
		r, err := resolver(ctx, c, branch, tip)
		r.Activation.Receipt.BinaryRevision = "wrong-binary"
		r.Activation.Read = func(context.Context) (authority.Observation, error) {
			readerCalled = true
			return authority.Observation{}, errors.New("rollback must not read")
		}
		return r, err
	}
	if err := s.configuration(t.Context(), w); !errors.Is(err, authority.ErrBinaryRevisionMismatch) {
		t.Fatal(err)
	}
	if readerCalled || !reflect.DeepEqual(lifecycleState(t, s, w).AuthorityPin(), original.AuthorityPin()) {
		t.Fatal("invalid rollback receipt changed pin or read authority")
	}
	events, err := s.store.EventsAfter(t.Context(), w.id, before)
	must(t, err)
	for _, event := range events {
		switch event.Event.(type) {
		case queue.ConfigApplied, queue.PolicyAuthorityApplied:
			t.Fatal("invalid receipt emitted lifecycle")
		}
	}
	w.resolveAuthorityLifecycle = func(ctx context.Context, c config.AuthorityCandidate, branch, tip string) (authorityLifecycleResolution, error) {
		r, err := resolver(ctx, c, branch, tip)
		r.Activation.Read = func(context.Context) (authority.Observation, error) {
			t.Fatal("rollback read authority")
			return authority.Observation{}, nil
		}
		return r, err
	}
	must(t, s.configuration(t.Context(), w))
	assertLifecyclePair(t, s, w, before, authority.GitHub)
	events, err = s.store.EventsAfter(t.Context(), w.id, before)
	must(t, err)
	want := w.cfg.QueueConfig().Applied(tip, w.configDigest)
	_, wantRaw, err := queue.EncodeEvent(want)
	must(t, err)
	for _, event := range events {
		if applied, ok := event.Event.(queue.ConfigApplied); ok {
			_, got, err := queue.EncodeEvent(applied)
			must(t, err)
			if string(got) != string(wantRaw) {
				t.Fatal("lifecycle changed normal ConfigApplied wire", string(got), string(wantRaw))
			}
		}
	}

	// A folded rollback receipt must survive later GitHub governing commits.
	w.AuthorityRequired = false
	for _, files := range []map[string]string{{"README.md": "unrelated"}, {".openmerge.yml": strings.Replace(repositoryConfig, "name: default", "name: default\n    enter_when: [label = ready]", 1)}} {
		_, err = f.PushBase(repo, "main", "github rebind", files)
		must(t, err)
		must(t, s.configuration(t.Context(), w))
		for _, operation := range []forge.PushOperation{forge.PushBuild, forge.PushLand, forge.PushEnsureFence} {
			opts := s.authorityPushOptions(t.Context(), w, operation, nil)
			if !reflect.DeepEqual(opts, forge.PushOptions{}) {
				t.Fatalf("rollback rebind returned guarded options for %s", operation)
			}
		}
	}

}

func TestAuthorityLifecycleRequiresFlagAndResolver(t *testing.T) {
	for _, setup := range []string{"neither", "flag-only", "resolver-only"} {
		t.Run(setup, func(t *testing.T) {
			s, w, f, _ := fixture(t, repositoryConfig)
			must(t, s.configuration(t.Context(), w))
			team := "200"
			resolver := lifecycleResolver(t, s, &team, func(context.Context) (authority.Observation, error) {
				t.Fatal("partial opt-in read authority")
				return authority.Observation{}, nil
			})
			w.AuthorityRequired = setup == "flag-only"
			if setup == "resolver-only" {
				w.resolveAuthorityLifecycle = func(ctx context.Context, c config.AuthorityCandidate, branch, tip string) (authorityLifecycleResolution, error) {
					t.Fatal("partial opt-in resolved authority")
					return resolver(ctx, c, branch, tip)
				}
			}
			_, before, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			_, err = f.PushBase(repo, "main", "candidate", map[string]string{".openmerge.yml": lifecycleConfig(repositoryConfig)})
			must(t, err)
			must(t, s.configuration(t.Context(), w))
			if w.configError == nil || !strings.Contains(w.configError.Error(), config.AuthorityActivationUnavailable) {
				t.Fatal("partial opt-in parsed public candidate", w.configError)
			}
			events, err := s.store.EventsAfter(t.Context(), w.id, before)
			must(t, err)
			for _, event := range events {
				switch event.Event.(type) {
				case queue.PolicyAuthorityApplied, queue.WriteRestrictionObserved, queue.WriteRestrictionVerified:
					t.Fatalf("partial opt-in emitted %T", event.Event)
				}
			}
		})
	}
}

// First-fence authority bootstrap remains 9c. Managed expansion can apply the
// lifecycle before validating an existing fence; New retains its old ordering
// and cannot create an authority fence from candidate/receipt data alone.
func TestAuthorityLifecycleDirectConstructorDoesNotBootstrapFence(t *testing.T) {
	s, _, f, _ := fixture(t, repositoryConfig)
	tip, err := f.ReadRef(t.Context(), repo, "main")
	must(t, err)
	must(t, f.PushRef(repo, "release/new", tip))
	team := "200"
	target := Target{Key: store.QueueKey{Installation: "7", Repository: repo.Path, BaseBranch: "release/new"}, Driver: f, Git: f, BotLogin: fake.Identity, AuthorityRequired: true}
	target.resolveAuthorityLifecycle = lifecycleResolver(t, s, &team, func(context.Context) (authority.Observation, error) {
		t.Fatal("constructor read authority before complete bootstrap gate")
		return authority.Observation{}, nil
	})
	_, err = New(t.Context(), s.store, []Target{target}, s.options)
	if err == nil || !strings.Contains(err.Error(), "no durable pin") {
		t.Fatal("direct constructor bootstrapped authority", err)
	}
	id, err := s.store.EnsureQueue(t.Context(), target.Key)
	must(t, err)
	state, _, err := s.store.Load(t.Context(), id)
	must(t, err)
	if state.Fence != "" || state.AuthorityPin() != nil {
		t.Fatal("incomplete direct gate emitted pin or fence")
	}
}

func TestAuthorityLifecycleUnchangedApprovalReadiness(t *testing.T) {
	s, w, f := shadowFixture(t, 1, "main", "7", false)
	tip, err := f.ReadRef(t.Context(), repo, "main")
	must(t, err)
	contents, err := f.ReadFile(t.Context(), repo, tip, ".openmerge.yml")
	must(t, err)
	team := "200"
	w.AuthorityRequired = true
	w.resolveAuthorityLifecycle = lifecycleResolver(t, s, &team, lifecycleRead)
	_, err = f.PushBase(repo, "main", "authority", map[string]string{".openmerge.yml": lifecycleConfig(string(contents))})
	must(t, err)
	must(t, s.configuration(t.Context(), w))
	digest := w.configDigest
	_, before, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	// A successful managed branch refresh ends with ready=true.
	w.managed = true
	w.ready = true
	w.summaries[123] = forge.PullRequestSummary{}
	must(t, s.configuration(t.Context(), w))
	_, after, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if digest != w.configDigest || before != after {
		t.Fatal("policy or journal changed")
	}
	if !w.ready {
		t.Fatalf("unchanged authority+approvals config cleared readiness; summaries=%d", len(w.summaries))
	}
}

func TestAuthorityLifecycleDepthOneToFour(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	must(t, s.configuration(t.Context(), w))
	team := "200"
	w.AuthorityRequired = true
	w.resolveAuthorityLifecycle = lifecycleResolver(t, s, &team, lifecycleRead)
	contents := lifecycleConfig(repositoryConfig)
	_, err := f.PushBase(repo, "main", "authority depth one", map[string]string{".openmerge.yml": contents})
	must(t, err)
	must(t, s.configuration(t.Context(), w))
	contents = strings.Replace(contents, "name: default", "name: default\n    speculation: {min_depth: 1, max_depth: 4}", 1)
	_, err = f.PushBase(repo, "main", "authority depth four", map[string]string{".openmerge.yml": contents})
	must(t, err)
	if err := s.configuration(t.Context(), w); err != nil {
		t.Fatalf("valid authority depth transition rejected: %v", err)
	}
}
