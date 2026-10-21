package serve

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// Persisted evidence is data, not permission to install an authority hook. This
// exercises the normal config path with both setup's candidate and journal data
// available, rather than relying only on the public parser's rejection.
func TestAuthorityBarrierJournalCannotInstallRuntimeAuthority(t *testing.T) {
	for _, history := range []string{"draft", "pin", "raw-activation"} {
		t.Run(history, func(t *testing.T) {
			s, w, _, _ := fixture(t, repositoryConfig)
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
