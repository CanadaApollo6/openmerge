package serve

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestStageAApprovalWindowTransitions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		old, next int
		approvals bool
	}{
		{"increase", 1, 4, true}, {"remove and increase", 1, 4, false}, {"decrease", 4, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, w, f := shadowFixture(t, tc.old, "main", "7", false)
			if !shadowState(t, s, w).WindowInitialized() {
				t.Fatal("approval activation omitted bounds")
			}
			raw := fmt.Sprintf("version: 1\nqueues:\n- name: default\n  speculation: {min_depth: 1, max_depth: %d}\n  merge_when: [check-success = ci]\n", tc.next)
			if tc.approvals {
				raw += "  approvals: {default: any_writer}\n"
			}
			_, err := f.PushBase(repo, "main", "change window", map[string]string{".openmerge.yml": raw})
			must(t, err)
			must(t, s.configuration(t.Context(), w))
			state, revision, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			if state.ConfigDigest != w.configDigest || !state.WindowInitialized() || state.Window(w.cfg.QueueConfig()) != 1 || (w.stageA != nil) != tc.approvals {
				t.Fatal("window transition did not apply/clamp")
			}
			restarted, err := New(t.Context(), s.store, []Target{w.Target}, s.options)
			must(t, err)
			must(t, restarted.configuration(t.Context(), restarted.queues[0]))
			_, after, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			if after != revision {
				t.Fatal("restart repeated transition")
			}
		})
	}
}

func TestStageAHistoricalFoldedWindowBlocksActionably(t *testing.T) {
	const raw = "version: 1\nqueues: [{name: default, merge_when: [check-success = ci], approvals: {default: any_writer}}]\n"
	s, w, f, _ := fixture(t, raw)
	tip, err := f.ReadRef(t.Context(), repo, "main")
	must(t, err)
	// Reproduce the pre-fix depth-1 record, before any window could be initialized.
	_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.BaseMoved{SHA: tip}, queue.ConfigApplied{Commit: tip, Digest: strings.Repeat("a", 64)}}, "")
	must(t, err)
	_, err = f.PushBase(repo, "main", "remove approvals and increase depth", map[string]string{".openmerge.yml": "version: 1\nqueues: [{name: default, speculation: {max_depth: 4}, merge_when: [check-success = ci]}]\n"})
	must(t, err)
	must(t, s.configuration(t.Context(), w))
	state := shadowState(t, s, w)
	if w.configError == nil || !state.SourceBlocked(queue.ConfigSource) || !strings.Contains(state.BlockSourceDetail(queue.ConfigSource), "folded digest without recorded window bounds; drain this queue") || state.ConfigDigest != strings.Repeat("a", 64) || state.WindowInitialized() {
		t.Fatalf("missing actionable durable failure: %v, %s", w.configError, state.BlockSourceDetail(queue.ConfigSource))
	}
}

func TestLegacySerialWindowWireUnchanged(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	must(t, s.configuration(t.Context(), w))
	before := shadowState(t, s, w)
	event := w.cfg.QueueConfig().Applied(before.ConfigCommit, before.ConfigDigest)
	_, raw, err := queue.EncodeEvent(event)
	must(t, err)
	if event.Policy != nil || event.Window != nil || bytes.Contains(raw, []byte(`"window"`)) || bytes.Contains(raw, []byte(`"policy"`)) {
		t.Fatalf("legacy bytes changed: %s", raw)
	}
	_, want, err := queue.EncodeEvent(queue.ConfigApplied{Commit: before.ConfigCommit, Digest: before.ConfigDigest})
	must(t, err)
	if !bytes.Equal(raw, want) {
		t.Fatal("serial ConfigApplied bytes changed")
	}
	_, err = f.PushBase(repo, "main", "raise depth", map[string]string{".openmerge.yml": repositoryConfig + "    speculation: {max_depth: 4}\n"})
	must(t, err)
	must(t, s.configuration(t.Context(), w))
	state := shadowState(t, s, w)
	if w.stageA != nil || !state.WindowInitialized() || state.Window(w.cfg.QueueConfig()) != 1 {
		t.Fatal("legacy historical recovery changed")
	}
}

func TestStageAIdentityPinBudgetConfigError(t *testing.T) {
	for _, owners := range []bool{false, true} {
		t.Run(fmt.Sprint("governing owners=", owners), func(t *testing.T) {
			s, w, f := shadowFixture(t, 1, "main", "7", false)
			oldDigest := shadowState(t, s, w).ConfigDigest
			count := cp.MaxPolicyIdentities + 1
			if owners {
				count--
			}
			var members []string
			for i := 0; i < count; i++ {
				name := fmt.Sprintf("member%d", i)
				members = append(members, name)
				f.SetIdentity(forge.UserIdentity{ID: int64(100 + i), Login: name, Kind: forge.UserAccount})
			}
			raw := "version: 1\ngroups: {g: [" + strings.Join(members, ",") + "]}\nqueues: [{name: default, merge_when: [check-success = ci], approvals: {default: {group: g}}}]\n"
			files := map[string]string{".openmerge.yml": raw}
			if owners {
				_, err := f.PushBase(repo, "main", "maximum-sized policy", files)
				must(t, err)
				must(t, s.configuration(t.Context(), w))
				if w.configError != nil || w.stageA.pin == nil || len(w.stageA.pin.Resolved) != cp.MaxPolicyIdentities {
					t.Fatalf("exact identity budget rejected: %v", w.configError)
				}
				oldDigest = shadowState(t, s, w).ConfigDigest
				f.SetIdentity(forge.UserIdentity{ID: 9999, Login: "extra", Kind: forge.UserAccount})
				files[".github/CODEOWNERS"] = "* @extra\n"
			}
			_, err := f.PushBase(repo, "main", "oversized policy", files)
			must(t, err)
			must(t, s.configuration(t.Context(), w))
			state := shadowState(t, s, w)
			want := "Default-branch configuration: line 0: approvals: approval policy resolves 1025 identities; maximum is 1024; reduce configured groups, agents or governing owners"
			if got := state.BlockSourceDetail(queue.ConfigSource); got != want {
				t.Fatalf("got %q; want %q", got, want)
			}
			if w.configError == nil || !state.SourceBlocked(queue.ConfigSource) || !strings.Contains(state.BlockSourceDetail(queue.ConfigSource), "1025 identities; maximum is 1024") || state.ConfigDigest != oldDigest {
				t.Fatalf("not an actionable config failure: %v %s", w.configError, state.BlockSourceDetail(queue.ConfigSource))
			}
		})
	}
}

func TestStageAEmptyPolicyKeysDoNotActivate(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	must(t, s.configuration(t.Context(), w))
	digest := w.configDigest
	_, err := f.PushBase(repo, "main", "empty policy", map[string]string{".openmerge.yml": repositoryConfig + "    changes: {}\nagents: []\ngroups: {}\nclassification: {}\n"})
	must(t, err)
	must(t, s.configuration(t.Context(), w))
	if w.stageA != nil || w.configDigest != digest {
		t.Fatal("empty keys activated policy or changed digest")
	}
}

func TestStageAAccountableIdentityDiagnosticExact(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, f := shadowFixture(t, 1, "main", "7", false)
		f.SetIdentity(forge.UserIdentity{ID: 30, Login: "robot", Kind: forge.BotAccount})
		f.SetIdentity(forge.UserIdentity{ID: 31, Login: "human", Kind: forge.BotAccount})
		_, err := f.PushBase(repo, "main", "accountable identity", map[string]string{".openmerge.yml": githubApprovalsConfig + "agents: [{identity: robot, accountable: [human]}]\n"})
		must(t, err)
		must(t, s.configuration(t.Context(), w))
		want := "Default-branch configuration: line 0: approvals: accountable identity \"human\" must resolve to a human"
		if got := shadowState(t, s, w).BlockSourceDetail(queue.ConfigSource); got != want {
			t.Fatalf("got %q; want %q", got, want)
		}
	})
}
