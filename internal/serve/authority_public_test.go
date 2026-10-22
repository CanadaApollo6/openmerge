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
	"github.com/CanadaApollo6/openmerge/internal/gitops"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"path/filepath"
)

type publicAuthorityProbe struct {
	*fake.Forge
	reads  int
	onRead func()
}

func (p *publicAuthorityProbe) ObserveWriteRestriction(ctx context.Context, in forge.WriteRestrictionInput) (authority.Observation, error) {
	p.reads++
	if p.onRead != nil {
		hook := p.onRead
		p.onRead = nil
		hook()
	}
	return p.Forge.ObserveWriteRestriction(ctx, in)
}

type publicCompletionSpy struct {
	*store.Store
	calls int
}

func (p *publicCompletionSpy) RepositoryAuthorityCompletion(ctx context.Context, request store.AuthorityCompletionRequest, inventory store.AuthorityBranchInventory) (store.AuthorityCompletion, error) {
	p.calls++
	return p.Store.RepositoryAuthorityCompletion(ctx, request, inventory)
}

type publicFixture struct {
	s               *Server
	w               *worker
	f               *fake.Forge
	probe           *publicAuthorityProbe
	receipt         *authority.EmissionReceipt
	receiptReads    *int
	branchReads     *int
	completionReads *int
}

func newPublicFixture(t *testing.T, verified bool, configs ...string) publicFixture {
	t.Helper()
	s, w, f, _ := authorityWorkerFixture(t)
	if len(configs) > 0 {
		_, err := f.PushBase(repo, "main", "reviewed policy", map[string]string{".openmerge.yml": lifecycleConfig(configs[0])})
		must(t, err)
	}
	tip, err := f.ReadRef(t.Context(), repo, "main")
	must(t, err)
	must(t, s.loadConfiguration(t.Context(), w, "main", tip))
	must(t, s.refreshStageAPolicy(t.Context(), w, tip))
	prepared, err := s.prepareAuthorityLifecycle(t.Context(), w, lifecycleState(t, s, w), "main", tip)
	must(t, err)
	input := prepared.Input
	input.ExpectedRefs = []string{"refs/heads/main"}
	raw, err := os.ReadFile("../queue/testdata/wire/v1/event_write_restriction_verified.json")
	must(t, err)
	event, err := queue.DecodeEvent(raw)
	must(t, err)
	draft := event.(queue.WriteRestrictionVerified)
	draft.Binding = input.Binding
	draft.Scope = "proposed"
	draft.Basis = 0
	draft.Nonce = "public-activation"
	draft.VerifiedAt = time.Now().UTC()
	coverage, err := authority.CompileRefCoverage("main", input.BaseSelectors)
	must(t, err)
	draft.Evidence.Protections[0].Includes = coverage.Includes
	draft.Evidence.Protections[0].Excludes = coverage.Excludes
	draft.Evidence, err = forge.InterpretWriteRestriction(input, draft.Evidence)
	must(t, err)
	draft.ProtectionFingerprint, err = draft.Evidence.Fingerprint()
	must(t, err)
	receipt := activationReceipt(t, s.store)
	var draftID int64
	if verified {
		receipts, err := s.store.CommitWriteRestrictionVerification(t.Context(), []store.VerificationRecord{{QueueID: w.id, Event: draft}})
		must(t, err)
		draftID = receipts[0].EventID
	}
	probe := &publicAuthorityProbe{Forge: f}
	must(t, f.SetWriteRestrictionObservation(repo, draft.Evidence, nil))
	reads, branches := new(int), new(int)
	target := w.Target
	target.Driver = probe
	target.AuthorityCompletionGit = f
	target.AuthorityRequired = false
	target.resolveAuthorityLifecycle = nil
	s.options.Authority = &AuthorityRuntime{Targets: []AuthorityTarget{{Key: w.Key, Input: input, DraftID: draftID}}, ReceiptMaxAge: time.Hour, Receipt: func() (authority.EmissionReceipt, error) { *reads++; return receipt, nil }}
	s.repositories = []Repository{{Installation: w.Key.Installation, Name: w.Key.Repository, Target: func(base string) Target { target.Key.BaseBranch = base; return target }, Branches: func(context.Context) ([]string, error) { *branches++; return []string{"main"}, nil }}}
	spy := &publicCompletionSpy{Store: s.store}
	s.authorityCompletionStore = spy
	w.Target = s.publicAuthorityTarget(target)
	w.configTip = ""
	return publicFixture{s, w, f, probe, &receipt, reads, branches, &spy.calls}
}

func TestPublicAuthorityActivationConsultsCompletion(t *testing.T) {
	for _, kind := range []string{"complete", "unverified", "other-installation", "paused", "retired", "excluded", "missing-discovery", "stale-discovery"} {
		t.Run(kind, func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				p := newPublicFixture(t, true)
				s, w := p.s, p.w
				must(t, s.configuration(t.Context(), w))
				request := s.publicAuthorityRequest(w)
				if !w.AuthorityRequired || !w.publicAuthority || w.resolveAuthorityLifecycle == nil || *p.receiptReads == 0 || p.probe.reads == 0 {
					t.Fatal("public constructor did not consult receipt and protection reader")
				}
				if result, err := s.AuthorityCompletion(t.Context(), request); err != nil || result.RequireComplete() != nil {
					t.Fatalf("initial completion: %+v err=%v status=%+v findings=%+v", result, err, lifecycleState(t, s, w).WriteRestrictionStatus(), lifecycleState(t, s, w).AuthorityPin().OpenMerge.Activation.Evidence.Findings)
				}
				if *p.branchReads < 2 || *p.completionReads == 0 {
					t.Fatal("public activation skipped fresh discovery")
				}
				switch kind {
				case "complete":
				case "unverified":
					p.f.FailNext("ObserveWriteRestriction", forge.ErrRulesUnavailable)
					_, err := s.store.Append(t.Context(), w.id, []queue.Event{queue.WriteRestrictionObserved{Binding: lifecycleState(t, s, w).AuthorityPin().OpenMerge.Binding, Basis: lifecycleState(t, s, w).RulesSourceRevision(queue.WriteRestriction), ObservedAt: time.Now().UTC(), ReadError: "unavailable", Evidence: forge.EmptyWriteRestrictionObservation()}}, "")
					must(t, err)
				case "other-installation", "paused", "retired", "excluded":
					if kind == "excluded" {
						s.options.BaseAllowlist = []string{"main"}
					}
					key := w.Key
					key.BaseBranch = "release/hidden"
					if kind == "other-installation" {
						key.Installation = "8"
					}
					id, err := s.store.EnsureQueue(t.Context(), key)
					must(t, err)
					if kind == "paused" {
						_, err = s.store.Append(t.Context(), id, []queue.Event{queue.PauseRequested{Reason: "test", Detail: "test"}}, "")
						must(t, err)
					}
					if kind == "retired" {
						_, err = s.store.Append(t.Context(), id, []queue.Event{queue.LifecycleChanged{State: "retired", Detail: "test"}}, "")
						must(t, err)
					}
				case "missing-discovery":
					s.repositories = nil
				case "stale-discovery":
					s.repositories[0].Branches = func(context.Context) ([]string, error) { return []string{"release/new"}, nil }
				}
				err := s.RequireAuthorityActivation(t.Context(), request)
				if (err == nil) != (kind == "complete") {
					t.Fatalf("public activation completion gate bypassed: %s: %v", kind, err)
				}
				options := s.authorityPushOptions(t.Context(), w, forge.PushBuild, []forge.PushRefIntent{{Ref: "refs/heads/openmerge/queue/main/public", BuildBaseSHA: lifecycleState(t, s, w).BaseSHA}})
				if kind != "complete" && options.Guard != nil && options.Guard(t.Context(), options.Context) == nil {
					t.Fatal("production push guard bypassed public completion")
				}
			})
		})
	}
}

func TestPublicAuthorityActivationRequiresVerification(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		p := newPublicFixture(t, false)
		// Match the live journal to reach the actual missing-proposal gate.
		events, err := p.s.store.EventsAfter(t.Context(), p.w.id, 0)
		must(t, err)
		p.receipt.Reconciliation.BackupWatermark = events[len(events)-1].ID
		p.receipt.Reconciliation.RestoredWatermark = p.receipt.Reconciliation.BackupWatermark
		err = p.s.configuration(t.Context(), p.w)
		if !errors.Is(err, ErrAuthorityVerificationRequired) {
			t.Fatalf("public activation skipped setup verification: %v", err)
		}
		if lifecycleState(t, p.s, p.w).AuthorityPin() != nil {
			t.Fatal("missing setup verification emitted authority pin")
		}
	})
}

func TestPublicAuthorityActivationConsultsReceipt(t *testing.T) {
	for _, kind := range []string{"missing", "binary", "expired", "inventory", "backup", "reconciliation"} {
		t.Run(kind, func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				p := newPublicFixture(t, true)
				switch kind {
				case "missing":
					*p.receipt = authority.EmissionReceipt{}
				case "binary":
					p.receipt.BinaryRevision = "wrong"
				case "expired":
					p.receipt.DrainedAt = time.Now().Add(-48 * time.Hour)
				case "inventory":
					p.receipt.Inventory = nil
				case "backup":
					p.receipt.BackupPath = "/missing-public-activation-backup"
				case "reconciliation":
					p.receipt.Reconciliation = nil
				}
				err := p.s.configuration(t.Context(), p.w)
				if err == nil {
					t.Fatal("public activation skipped emission receipt validation")
				}
				if *p.receiptReads == 0 || p.probe.reads != 0 {
					t.Fatal("receipt refusal did not precede seeing read")
				}
				if kind == "binary" && !errors.Is(err, authority.ErrBinaryRevisionMismatch) {
					t.Fatal(err)
				}
				if lifecycleState(t, p.s, p.w).AuthorityPin() != nil {
					t.Fatal("invalid receipt emitted authority pin")
				}
			})
		})
	}
}

func TestPublicAuthorityProductionConstructor(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		p := newPublicFixture(t, true)
		s, err := NewRepositories(t.Context(), p.s.store, p.s.repositories, p.s.options)
		must(t, err)
		w := baseWorker(t, s, "main")
		if !w.publicAuthority || !w.AuthorityRequired || w.resolveAuthorityLifecycle == nil {
			t.Fatal("production constructor skipped authority opt-in")
		}
		if pin := lifecycleState(t, s, w).AuthorityPin(); pin == nil || !queue.AuthorityDraftAdopted(*pin) {
			t.Fatal("production constructor did not adopt verified policy")
		}
		must(t, s.RequireAuthorityActivation(t.Context(), s.publicAuthorityRequest(w)))
	})
}

func TestPublicAuthorityGithubModeIgnoresReceipt(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		p := newPublicFixture(t, true)
		_, err := p.f.PushBase(repo, "main", "ordinary github", map[string]string{".openmerge.yml": repositoryConfig})
		must(t, err)
		*p.receipt = authority.EmissionReceipt{}
		must(t, p.s.configuration(t.Context(), p.w))
		if *p.receiptReads != 0 || p.probe.reads != 0 || p.w.stageA != nil || lifecycleState(t, p.s, p.w).AuthorityPin() != nil {
			t.Fatal("opted-in github worker changed legacy behavior")
		}
	})
}

func TestPublicAuthorityExpectedRefsRecovery(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		p := newPublicFixture(t, true, releaseConfig)
		s, w := p.s, p.w
		must(t, s.configuration(t.Context(), w))
		must(t, s.RequireAuthorityActivation(t.Context(), s.publicAuthorityRequest(w)))
		original := lifecycleState(t, s, w).AuthorityPin()
		tip, err := p.f.ReadRef(t.Context(), repo, "main")
		must(t, err)
		must(t, p.f.SetRef(repo, "release/new", tip))
		s.repositories[0].Branches = func(context.Context) ([]string, error) { return []string{"main", "release/new"}, nil }
		observation := authority.CloneObservation(original.OpenMerge.Activation.Evidence)
		observation.Coverage = append(observation.Coverage,
			authority.Coverage{Ref: "refs/heads/release/new", Visibility: authority.Visible, ProtectionIDs: []string{"repository_ruleset/base-10"}},
			authority.Coverage{Ref: "refs/heads/openmerge/fence/release/new", Visibility: authority.Visible, ProtectionIDs: []string{"repository_ruleset/refs-11"}})
		must(t, p.f.SetWriteRestrictionObservation(repo, observation, nil))
		must(t, s.configuration(t.Context(), w))
		if queue.AuthorityDraftAdopted(*lifecycleState(t, s, w).AuthorityPin()) {
			t.Fatal("ExpectedRefs drift retained adoption")
		}
		if s.RequireAuthorityActivation(t.Context(), s.publicAuthorityRequest(w)) == nil {
			t.Fatal("drift passed public completion")
		}
		// The owner restores GitHub protection before this policy rollback. The test
		// drives the public lifecycle and durable verification, with no forge mutation.
		_, err = p.f.PushBase(repo, "main", "rollback", map[string]string{".openmerge.yml": releaseConfig})
		must(t, err)
		must(t, s.configuration(t.Context(), w))
		if lifecycleState(t, s, w).AuthorityPin().Authority != authority.GitHub {
			t.Fatal("public rollback failed")
		}

		// Prepare both reviewed bindings from the immutable authority tree. The new
		// branch stays selected throughout recovery; its proposal cannot be skipped.
		newKey := w.Key
		newKey.BaseBranch = "release/new"
		newID, err := s.store.EnsureQueue(t.Context(), newKey)
		must(t, err)
		target := w.Target
		target.Key = newKey
		target.publicAuthority = false
		target.AuthorityRequired = true
		target.resolveAuthorityLifecycle = func(_ context.Context, c config.AuthorityCandidate, _, _ string) (authorityLifecycleResolution, error) {
			policy := authority.Policy{Mode: c.Authority.Mode.Normalized(), BreakGlass: authority.ActorBinding{Declared: original.OpenMerge.Binding.DeclaredActor, Resolved: original.OpenMerge.Binding.ResolvedActor}}
			return authorityLifecycleResolution{Policy: policy, RepositoryID: original.OpenMerge.Binding.RepositoryID, Activation: authorityActivation{Receipt: *p.receipt, ReceiptMaxAge: time.Hour, Input: forge.WriteRestrictionInput{AppID: s.options.Authority.Targets[0].Input.AppID, ExpectedRefs: []string{"refs/heads/main", "refs/heads/release/new"}}, Read: func(context.Context) (authority.Observation, error) { return observation, nil }}}, nil
		}
		preparedWorker := &worker{Target: target, id: newID}
		must(t, s.loadConfiguration(t.Context(), preparedWorker, "main", original.GoverningCommit))
		must(t, s.refreshStageAPolicy(t.Context(), preparedWorker, original.GoverningCommit))
		request, err := s.prepareAuthorityLifecycle(t.Context(), preparedWorker, lifecycleState(t, s, preparedWorker), "main", original.GoverningCommit)
		must(t, err)
		mainInput := s.options.Authority.Targets[0].Input
		mainInput.ExpectedRefs = []string{"refs/heads/main", "refs/heads/release/new"}
		records := []store.VerificationRecord{}
		for i, input := range []forge.WriteRestrictionInput{mainInput, request.Input} {
			id := w.id
			if i == 1 {
				id = newID
			}
			state, _, err := s.store.Load(t.Context(), id)
			must(t, err)
			evidence, err := forge.InterpretWriteRestriction(input, observation)
			must(t, err)
			fingerprint, err := evidence.Fingerprint()
			must(t, err)
			records = append(records, store.VerificationRecord{QueueID: id, Event: queue.WriteRestrictionVerified{Binding: input.Binding, Scope: "proposed", Basis: state.RulesSourceRevision(queue.WriteRestriction), Nonce: "public-after-rollback", Operator: "operator", VerifiedAt: time.Now().UTC(), Evidence: evidence, ProtectionFingerprint: fingerprint}})
		}
		receipts, err := s.store.CommitWriteRestrictionVerification(t.Context(), records)
		must(t, err)
		ids := map[store.QueueID]int64{}
		for _, receipt := range receipts {
			ids[receipt.QueueID] = receipt.EventID
		}
		if ids[w.id] <= original.OpenMerge.Activation.Proposed.EventID {
			t.Fatal("reused old proposal")
		}
		s.options.Authority.Targets[0].Input = mainInput
		s.options.Authority.Targets[0].DraftID = ids[w.id]
		s.options.Authority.Targets = append(s.options.Authority.Targets, AuthorityTarget{Key: newKey, Input: request.Input, DraftID: ids[newID]})
		target.resolveAuthorityLifecycle = nil
		target.AuthorityRequired = false
		newWorker, err := s.register(t.Context(), target)
		must(t, err)
		newWorker.managed = false
		rawTarget := w.Target
		rawTarget.resolveAuthorityLifecycle = nil
		rawTarget.publicAuthority = false
		w.Target = s.publicAuthorityTarget(rawTarget)
		_, err = p.f.PushBase(repo, "main", "re-enable", map[string]string{".openmerge.yml": lifecycleConfig(releaseConfig)})
		must(t, err)
		must(t, s.configuration(t.Context(), w))
		if s.RequireAuthorityActivation(t.Context(), s.publicAuthorityRequest(w)) == nil {
			t.Fatal("main adoption substituted for new selected queue completion")
		}
		must(t, s.configuration(t.Context(), newWorker))
		for _, worker := range []*worker{w, newWorker} {
			pin := lifecycleState(t, s, worker).AuthorityPin()
			if !queue.AuthorityDraftAdopted(*pin) || pin.OpenMerge.Activation.Proposed.EventID != ids[worker.id] || !queue.AuthorityInputsMatch(pin.OpenMerge.VerificationInput, forge.WriteRestrictionInput{BaseSelectors: mainInput.BaseSelectors, ExpectedRefs: mainInput.ExpectedRefs, AppID: mainInput.AppID}) {
				t.Fatal("public re-enable did not adopt current proposal/inputs")
			}
		}
		must(t, s.RequireAuthorityActivation(t.Context(), s.publicAuthorityRequest(w)))
		s.repositories[0].Branches = func(context.Context) ([]string, error) { return nil, errors.New("SECRET") }
		if err := s.RequireAuthorityActivation(t.Context(), s.publicAuthorityRequest(w)); err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("recovery reused completion: %v", err)
		}
	})
}

func enableAcceptancePublic(t *testing.T, f acceptanceGit) {
	t.Helper()
	s, w := f.s, f.w
	pin := lifecycleState(t, s, w).AuthorityPin()
	input := forge.WriteRestrictionInput{Repo: w.repo(), Binding: pin.OpenMerge.Binding, BaseSelectors: pin.OpenMerge.VerificationInput.BaseSelectors, ExpectedRefs: pin.OpenMerge.VerificationInput.ExpectedRefs, AppID: pin.OpenMerge.VerificationInput.AppID}
	receipt := activationReceipt(t, s.store)
	s.options.Authority = &AuthorityRuntime{Targets: []AuthorityTarget{{Key: w.Key, Input: input, DraftID: pin.OpenMerge.Activation.Proposed.EventID}}, ReceiptMaxAge: time.Hour, Receipt: func() (authority.EmissionReceipt, error) { return receipt, nil }}
	target := w.Target
	target.resolveAuthorityLifecycle = nil
	independent, err := gitops.New(t.Context(), gitops.Config{WorkDir: filepath.Join(t.TempDir(), "completion-cache"), Remote: func(context.Context, forge.RepoRef) (string, gitops.Auth, error) { return f.remote, gitops.Auth{}, nil }, HeadRef: func(int) string { return "refs/pull/1/head" }})
	must(t, err)
	target.AuthorityCompletionGit = independent
	s.repositories = []Repository{{Installation: w.Key.Installation, Name: w.Key.Repository, Target: func(base string) Target { target.Key.BaseBranch = base; return target }, Branches: func(context.Context) ([]string, error) { return []string{"main"}, nil }}}
	must(t, f.driver.SetWriteRestrictionObservation(w.repo(), pin.OpenMerge.Activation.Evidence, nil))
	w.Target = s.publicAuthorityTarget(target)
	must(t, s.RequireAuthorityActivation(t.Context(), s.publicAuthorityRequest(w)))
}

func TestPublicAuthorityActivationRechecksGoverningTip(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		p := newPublicFixture(t, true)
		p.probe.onRead = func() {
			_, err := p.f.PushBase(repo, "main", "race during seeing read", map[string]string{"README.md": "changed"})
			must(t, err)
		}
		err := p.s.configuration(t.Context(), p.w)
		if err == nil || !strings.Contains(err.Error(), "governing ref changed") {
			t.Fatalf("public activation skipped final governing recheck: %v", err)
		}
		if lifecycleState(t, p.s, p.w).AuthorityPin() != nil {
			t.Fatal("stale governing read emitted authority pin")
		}
	})
}

func TestPublicAuthorityMutableActorFailureRetriesOnRulesCadence(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		p := newPublicFixture(t, true)
		p.f.SetTeam(forge.TeamIdentity{ID: 201, OrganizationID: 100, Organization: "acme", Slug: "emergency"}, nil)
		err := p.s.configuration(t.Context(), p.w)
		if err == nil || p.w.configFailureRetryAt.IsZero() {
			t.Fatalf("mutable actor failure lost retry cadence: %v", err)
		}
		calls := p.f.IdentityCalls("ResolveTeam")
		p.f.SetTeam(forge.TeamIdentity{ID: 200, OrganizationID: 100, Organization: "acme", Slug: "emergency"}, nil)
		must(t, p.s.configuration(t.Context(), p.w))
		if p.f.IdentityCalls("ResolveTeam") != calls || p.w.configError == nil {
			t.Fatal("mutable actor failure retried before rules cadence")
		}
		// Expire the virtual-clock cache explicitly; no wall-clock ordering or sleep.
		p.w.configFailureRetryAt = time.Now().Add(-time.Nanosecond)
		must(t, p.s.configuration(t.Context(), p.w))
		if p.f.IdentityCalls("ResolveTeam") == calls || p.w.configError != nil {
			t.Fatal("mutable actor failure did not retry after rules cadence")
		}
	})
}

func TestPublicAuthorityGithubConfigurationBytes(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		p := newPublicFixture(t, true)
		_, err := p.f.PushBase(repo, "main", "github", map[string]string{".openmerge.yml": repositoryConfig})
		must(t, err)
		must(t, p.s.configuration(t.Context(), p.w))
		for _, data := range []string{
			"# retained source line\npolicy_authority: github\n" + repositoryConfig,
			"# retained source line\npolicy_authority: github\n" + repositoryConfig + "    unsupported: true\n",
			"# retained source line\npolicy_authority: github\n" + repositoryConfig + "    approvals: {default: unknown_requirement}\n",
		} {
			tip, err := p.f.PushBase(repo, "main", "github source", map[string]string{".openmerge.yml": data})
			must(t, err)
			want, warnings, wantErr := config.Load([]byte(data))
			got, gotErr := p.s.repositoryCandidate(t.Context(), p.w.Target, tip, p.w)
			if (gotErr == nil) != (wantErr == nil) {
				t.Fatalf("GitHub loader result differs: got %v want %v", gotErr, wantErr)
			}
			if wantErr != nil {
				if gotErr.Error() != wantErr.Error() {
					t.Fatalf("GitHub diagnostics changed: got %q want %q", gotErr.Error(), wantErr.Error())
				}
			} else if len(warnings) != 0 || !reflect.DeepEqual(got.Config, want) {
				t.Fatal("GitHub configuration changed through public opt-in")
			}
		}
		if *p.receiptReads != 0 || p.probe.reads != 0 {
			t.Fatal("GitHub parsing consulted authority receipt/forge")
		}
	})
}

func TestPublicAuthorityReaderPreservesSanitizedErrorClasses(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		p := newPublicFixture(t, true)
		quota := &workerAuthorityQuota{until: time.Now().Add(time.Hour)}
		for _, tc := range []struct {
			err   error
			class string
		}{
			{quota, "quota"},
			{errors.Join(errors.New("SECRET"), context.Canceled), "canceled"},
			{errors.Join(errors.New("SECRET"), context.DeadlineExceeded), "timeout"},
			{errors.New("SECRET"), "unavailable"},
		} {
			tip, err := p.f.ReadRef(t.Context(), repo, "main")
			must(t, err)
			resolution, err := p.w.resolveAuthorityLifecycle(t.Context(), p.w.configCandidate, "main", tip)
			must(t, err)
			evidence, err := lifecycleRead(t.Context())
			must(t, err)
			must(t, p.f.SetWriteRestrictionObservation(repo, evidence, tc.err))
			_, err = resolution.Activation.Read(t.Context())
			if !errors.Is(err, tc.err) || !strings.Contains(err.Error(), tc.class) || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("public reader lost sanitized error class: %v", err)
			}
			if tc.err == quota {
				var retry interface{ RetryAfter() time.Time }
				if !errors.As(err, &retry) || !retry.RetryAfter().Equal(quota.until) {
					t.Fatal("public reader lost quota retry deadline")
				}
			}
		}
	})
}
