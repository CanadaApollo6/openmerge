package serve

import (
	"context"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestAuthorityBarrierPinCannotSelectAuthorityCheckGate(t *testing.T) {
	for _, depth := range []int{1, 4} {
		t.Run(strconv.Itoa(depth), func(t *testing.T) {
			s, w, _ := shadowFixture(t, depth, "main", "7", false)
			must(t, s.configuration(t.Context(), w))
			state := shadowState(t, s, w)
			raw, err := os.ReadFile("../queue/testdata/wire/v1/event_policy_authority_applied.json")
			must(t, err)
			event, err := queue.DecodeEvent(raw)
			must(t, err)
			pin := event.(queue.PolicyAuthorityApplied)
			pin.GoverningCommit, pin.EffectiveDigest = state.ConfigCommit, state.ConfigDigest
			pin.OpenMerge.Binding.EffectiveDigest = pin.EffectiveDigest
			_, err = s.store.Append(t.Context(), w.id, []queue.Event{pin}, "")
			must(t, err)
			state = shadowState(t, s, w)
			if state.AuthorityPin() == nil || w.stageA == nil {
				t.Fatal("test needs a folded pin and ordinary approval policy")
			}
			gate := s.stageAGate(t.Context(), w, store.ClaimedAction{}, w.plannerConfig(s.options.PollInterval), state)
			if gate.ReadInputs == nil || gate.ReadInputsWithChecks != nil {
				t.Fatal("pin alone selected authority check evidence on a public GitHub target")
			}
		})
	}
}

// Persisted evidence is data, not permission to install an authority hook. This
// exercises the normal config path with both setup's candidate and journal data
// available, rather than relying only on the public parser's rejection.
func TestAuthorityBarrierJournalCannotInstallRuntimeAuthority(t *testing.T) {
	for _, history := range []string{"draft", "pin", "raw-activation"} {
		t.Run(history, func(t *testing.T) {
			s, w, f, _ := fixture(t, repositoryConfig)
			must(t, s.configuration(t.Context(), w))
			legacy := w.plannerConfig(s.options.PollInterval)
			read := func(name string) queue.Event {
				raw, err := os.ReadFile("../queue/testdata/wire/v1/" + name + ".json")
				must(t, err)
				e, err := queue.DecodeEvent(raw)
				must(t, err)
				return e
			}
			var seed queue.Event
			if history == "draft" {
				seed = read("event_write_restriction_verified")
			} else {
				pin := read("variants/policy_authority_applied_activation").(queue.PolicyAuthorityApplied)
				pin.EffectiveDigest = w.configDigest
				pin.GoverningCommit = w.configTip
				pin.OpenMerge.Binding.EffectiveDigest = pin.EffectiveDigest
				if history == "pin" {
					pin.OpenMerge.Activation = nil
				} else {
					pin.OpenMerge.Activation.Proposed.Binding = pin.OpenMerge.Binding
				}
				seed = pin
			}
			_, err := s.store.Append(t.Context(), w.id, []queue.Event{seed}, "")
			must(t, err)
			state, before, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			if history != "draft" && state.AuthorityPin() == nil {
				t.Fatal("test did not fold the handcrafted pin")
			}
			proposed := strings.Replace(repositoryConfig, "version: 1\n", "version: 1\npolicy_authority: openmerge\nbreak_glass: {team: acme/emergency}\n", 1)
			candidate, _, err := config.LoadAuthorityCandidate([]byte(proposed))
			must(t, err)
			if candidate.Authority.Mode != authority.OpenMerge {
				t.Fatal("setup did not parse the candidate")
			}
			if _, _, err := config.Load([]byte(proposed)); err == nil || !strings.Contains(err.Error(), config.AuthorityActivationUnavailable) {
				t.Fatal("public activation barrier missing", err)
			}
			// Reloading the real github config, even after replaying a pin, must
			// neither promote the candidate nor install any internal callbacks.
			w.configTip = ""
			must(t, s.configuration(t.Context(), w))
			must(t, s.configuration(t.Context(), w))
			if w.AuthorityRequired || w.PrepareAuthorityPush != nil || w.PinAuthorityPush != nil || w.ObserveAuthorityPush != nil || w.stageA != nil {
				t.Fatal("journal data enabled runtime authority")
			}
			// Even explicitly invoking the new internal caller cannot use this
			// candidate/draft/pin as its opt-in, even with a valid receipt for the
			// configured database. It rejects before any I/O.
			request := authorityActivation{Receipt: activationReceipt(t, s.store), ReceiptMaxAge: time.Hour, Config: &queue.ConfigApplied{Commit: w.configTip, Digest: w.configDigest}}
			w.resolveAuthorityLifecycle = func(context.Context, config.AuthorityCandidate, string, string) (authorityLifecycleResolution, error) {
				t.Fatal("resolver ran without opt-in")
				return authorityLifecycleResolution{}, nil
			}
			if _, err := s.prepareAuthorityLifecycle(t.Context(), w, state, w.configBranch, w.configTip); err == nil || !strings.Contains(err.Error(), "opt-in required") {
				t.Fatal("lifecycle preparation bypassed opt-in", err)
			}
			w.configTip = ""
			must(t, s.configuration(t.Context(), w))
			// New worker projections/enforcement require the same explicit
			// runtime seam; candidates, receipts and replayed pins cannot run it.
			must(t, s.observeWorkerAuthority(t.Context(), w))
			if _, err := s.readAuthorityAdmission(t.Context(), t.Context(), w, state); err == nil || !strings.Contains(err.Error(), "opt-in/configuration unavailable") {
				t.Fatal("candidate/journal/receipt enabled complete admission seam", err)
			}
			if w.stageA != nil || w.authorityObservation != nil || w.approvalStatus.Load() != nil {
				t.Fatal("journal data installed owner/observation/status policy")
			}
			if receipt, err := s.activateAuthority(t.Context(), w, request); err == nil || !strings.Contains(err.Error(), "internal authority activation opt-in required") || receipt != (store.AuthorityAdoptionReceipt{}) {
				t.Fatal("journal data enabled internal activation", receipt, err)
			}
			if !reflect.DeepEqual(legacy, w.plannerConfig(s.options.PollInterval)) {
				t.Fatal("journal data changed github planner configuration")
			}
			// A surviving pin already selects the fail-closed push path in the
			// baseline. Preserve that protection: it must not manufacture a
			// resolved authority context or invoke the dormant pin writer.
			opts := s.authorityPushOptions(t.Context(), w, forge.PushBuild, nil)
			if history == "draft" {
				if opts.AuthorityRequired || opts.Guard != nil {
					t.Fatal("proposed evidence changed legacy push options")
				}
			} else if !opts.AuthorityRequired || opts.Guard == nil || opts.Guard(t.Context(), opts.Context) == nil {
				t.Fatal("unresolved journal pin supplied usable authority push options")
			}
			_, err = f.PushBase(w.repo(), "main", "public candidate remains blocked", map[string]string{".openmerge.yml": proposed})
			must(t, err)
			must(t, s.configuration(t.Context(), w))
			if w.configError == nil || !strings.Contains(w.configError.Error(), config.AuthorityActivationUnavailable) {
				t.Fatal("lifecycle reader bypassed public candidate barrier", w.configError)
			}
			tip, err := f.ReadRef(t.Context(), w.repo(), "main")
			must(t, err)
			governingReads := f.GoverningFilesCalls()
			for _, acquire := range []func() error{
				func() error { _, err := s.repositoryConfig(t.Context(), w.Target, "main", tip, nil); return err },
				func() error { return s.loadConfiguration(t.Context(), w, "main", tip) },
			} {
				if err := acquire(); err == nil || !strings.Contains(err.Error(), config.AuthorityActivationUnavailable) {
					t.Fatal("acquisition entry point bypassed public barrier", err)
				}
			}
			if f.GoverningFilesCalls() != governingReads || w.AuthorityRequired || w.authorityObservation != nil {
				t.Fatal("acquisition enabled authority or read governing evidence without opt-in")
			}

			events, err := s.store.EventsAfter(t.Context(), w.id, before)
			must(t, err)
			for _, event := range events {
				switch event.Event.(type) {
				case queue.PolicyAuthorityApplied, queue.WriteRestrictionObserved, queue.WriteRestrictionVerified:
					t.Fatalf("public github config emitted authority event %T", event.Event)
				}
			}
		})
	}
}
