package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

const releaseConfig = `version: 1
queues:
  - name: releases
    base: release/*
    merge_method: merge
    merge_when: [check-success = release-ci]
  - name: default
    merge_when: [check-success = ci]
`

type configReadDriver struct {
	forge.Driver
	refs     []string
	onRead   func()
	onChecks func()
}

func (d *configReadDriver) CheckRuns(ctx context.Context, repo forge.RepoRef, sha string) ([]forge.CheckRun, error) {
	checks, err := d.Driver.CheckRuns(ctx, repo, sha)
	if hook := d.onChecks; hook != nil {
		d.onChecks = nil
		hook()
	}
	return checks, err
}

func (d *configReadDriver) ReadFile(ctx context.Context, repo forge.RepoRef, ref, path string) ([]byte, error) {
	d.refs = append(d.refs, ref)
	data, err := d.Driver.ReadFile(ctx, repo, ref, path)
	if hook := d.onRead; hook != nil {
		d.onRead = nil
		hook()
	}
	return data, err
}

type configReadGit struct {
	forge.Git
	failRef string
}

func (g *configReadGit) ReadRef(ctx context.Context, repo forge.RepoRef, ref string) (string, error) {
	if ref == g.failRef {
		return "", errors.New("default branch tip unreadable")
	}
	return g.Git.ReadRef(ctx, repo, ref)
}

func releaseFixture(t *testing.T, contents string) (*Server, *worker, *fake.Forge, *configReadDriver) {
	t.Helper()
	s, main, f, _ := fixture(t, contents)
	tip, err := f.ReadRef(t.Context(), repo, "main")
	must(t, err)
	must(t, f.PushRef(repo, "release/v1", tip))
	_, err = f.PushBase(repo, "release/v1", "untrusted release policy", map[string]string{".openmerge.yml": "version: invalid"})
	must(t, err)
	d := &configReadDriver{Driver: f}
	target := main.Target
	target.Key.BaseBranch = "release/v1"
	target.Driver = d
	s, err = New(t.Context(), s.store, []Target{target}, s.options)
	must(t, err)
	return s, s.queues[0], f, d
}

func TestSingleQueueMainDeploymentConfigResolutionUnchanged(t *testing.T) {
	// Use the repository's real deployment file, without changing it or its log.
	data, err := os.ReadFile("../../.openmerge.yml")
	must(t, err)
	expected, _, err := config.Load(data)
	must(t, err)
	s, w, f, _ := fixture(t, string(data))
	key, id := w.Key, w.id
	d := &configReadDriver{Driver: w.Driver}
	w.Driver = d
	must(t, s.configuration(t.Context(), w))
	tip, err := f.ReadRef(t.Context(), repo, "main")
	must(t, err)
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if w.configError != nil || w.configBranch != "main" || w.configTip != tip || state.BaseSHA != tip || !reflect.DeepEqual(w.cfg, expected.Queues[0]) {
		t.Fatalf("main policy resolution changed: worker=%+v state=%+v", w, state)
	}
	if len(d.refs) != 4 {
		t.Fatalf("configuration paths = %v", d.refs)
	}
	for _, ref := range d.refs {
		if ref != tip {
			t.Fatalf("configuration was not pinned to main's observed tip: %v", d.refs)
		}
	}
	must(t, s.configuration(t.Context(), w))
	if len(d.refs) != 4 || w.Key != key || w.id != id || len(s.queues) != 1 {
		t.Fatal("unchanged main policy reread, rekeyed, or spawned queues")
	}
}

func TestReleaseBaseUsesDefaultBranchAuthorityAndReloads(t *testing.T) {
	s, w, f, d := releaseFixture(t, releaseConfig)
	var output bytes.Buffer
	s.options.Logger = slog.New(slog.NewJSONHandler(&output, nil))
	must(t, s.configuration(t.Context(), w))
	mainTip, err := f.ReadRef(t.Context(), repo, "main")
	must(t, err)
	baseTip, err := f.ReadRef(t.Context(), repo, "release/v1")
	must(t, err)
	if w.configError != nil || w.cfg.Name != "releases" || w.cfg.MergeMethod != queue.Merge || w.configTip != mainTip {
		t.Fatalf("release policy = %+v, %v", w.cfg, w.configError)
	}
	for _, ref := range d.refs {
		if ref != mainTip || ref == baseTip {
			t.Fatalf("release configuration used a non-authoritative commit: %v", d.refs)
		}
	}
	if len(s.queues) != 1 {
		t.Fatal("single-target constructor unexpectedly expanded repositories")
	}
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.BaseSHA != baseTip {
		t.Fatalf("queue base changed to config authority: %s", state.BaseSHA)
	}
	_, err = f.PushBase(repo, "release/v1", "release-only update", nil)
	must(t, err)
	must(t, s.configuration(t.Context(), w))
	if len(d.refs) != 4 || w.configTip != mainTip {
		t.Fatal("release base movement reloaded default-branch policy")
	}
	updated := strings.ReplaceAll(releaseConfig, "merge_method: merge", "merge_method: rebase") + "delete_head_branch: false\n"
	newTip, err := f.PushBase(repo, "main", "change repository policy", map[string]string{".openmerge.yml": updated})
	must(t, err)
	must(t, s.configuration(t.Context(), w))
	if w.configError != nil || w.configTip != newTip || w.cfg.MergeMethod != queue.Rebase || len(d.refs) != 8 || w.deleteHeadBranch == nil || *w.deleteHeadBranch {
		t.Fatalf("default branch update not loaded: worker=%+v reads=%v", w, d.refs)
	}
	// The chosen policy and observed commit travel together across a restart.
	restarted, err := New(t.Context(), s.store, []Target{w.Target}, s.options)
	must(t, err)
	must(t, restarted.configuration(t.Context(), restarted.queues[0]))
	if restarted.queues[0].id != w.id || restarted.queues[0].configTip != newTip {
		t.Fatal("restart changed queue identity or policy authority")
	}
}

func TestReleaseBatchNeverLandsUnderChangedDefaultPolicy(t *testing.T) {
	for _, window := range []string{"while-testing", "landing-preflight"} {
		t.Run(window, func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				s, w, f, d := releaseFixture(t, releaseConfig)
				must(t, f.SetPRMetadata(repo, 1, "release/v1", false, "contributor"))
				f.AddComment(repo, 1, "writer", "@openmerge queue")
				must(t, s.cycle(t.Context(), w))
				state, _, err := s.store.Load(t.Context(), w.id)
				must(t, err)
				if state.Active == nil || state.Active.Phase != queue.Testing || state.Active.Method != queue.Merge {
					t.Fatalf("release batch did not reach testing under merge policy: %+v", state.Active)
				}
				old := *state.Active
				base := state.BaseSHA
				updated := strings.ReplaceAll(strings.ReplaceAll(releaseConfig, "merge_method: merge", "merge_method: squash"), "release-ci", "new-ci")
				changePolicy := func() {
					_, err := f.PushBase(repo, "main", "change release policy", map[string]string{".openmerge.yml": updated})
					must(t, err)
				}
				oldResult := forge.Failure
				if window == "landing-preflight" {
					oldResult = forge.Success
				}
				f.SetCheckRuns(repo, old.TestedSHA, []forge.CheckRun{
					{ID: 1, Name: "release-ci", State: oldResult, HeadBranch: old.TestRef},
					{ID: 2, Name: "new-ci", State: forge.Success, HeadBranch: old.TestRef},
				})
				if window == "while-testing" {
					changePolicy()
					must(t, s.cycle(t.Context(), w))
				} else {
					_, err := s.store.Append(t.Context(), w.id, []queue.Event{queue.ChecksCompleted{TestedSHA: old.TestedSHA, Success: true}}, "")
					must(t, err)
					_, err = s.store.Plan(t.Context(), w.id, w.plannerConfig(time.Millisecond), time.Now())
					must(t, err)
					claims, err := s.store.ClaimQueue(t.Context(), w.id, 1, time.Minute, time.Now())
					must(t, err)
					if len(claims) != 1 {
						t.Fatalf("missing landing claim: %v", claims)
					}
					if _, ok := claims[0].Action.(queue.Land); !ok {
						t.Fatalf("expected landing claim, got %T", claims[0].Action)
					}
					d.onChecks = changePolicy
					must(t, s.execute(t.Context(), w, claims[0]))
					must(t, s.cycle(t.Context(), w))
				}
				state, _, err = s.store.Load(t.Context(), w.id)
				must(t, err)
				actualBase, err := f.ReadRef(t.Context(), repo, "release/v1")
				must(t, err)
				if actualBase != base || state.BaseSHA != base || state.Active == nil || state.Active.ID == old.ID || state.Active.Method != queue.Squash || len(state.Entries) != 1 {
					t.Fatalf("old policy batch landed or was not rebuilt: base=%s active=%+v entries=%v", actualBase, state.Active, state.Entries)
				}
				if state.ConfigCommit != w.configTip || state.ConfigDigest != w.configDigest || state.Active.ConfigDigest != state.ConfigDigest {
					t.Fatal("rebuilt batch is not pinned to durable policy")
				}
			})
		})
	}
}

func TestUnchangedEffectiveReleasePolicyPreservesActiveBatchAcrossRestart(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, f, _ := releaseFixture(t, releaseConfig)
		must(t, f.SetPRMetadata(repo, 1, "release/v1", false, "contributor"))
		f.AddComment(repo, 1, "writer", "@openmerge queue")
		must(t, s.cycle(t.Context(), w))
		before, watermark, err := s.store.Load(t.Context(), w.id)
		must(t, err)
		if before.Active == nil || before.Active.Phase != queue.Testing {
			t.Fatal("missing testing release batch")
		}
		// Formatting, comments, and another queue's policy are not this queue's inputs.
		updated := "# repository policy\n" + strings.ReplaceAll(releaseConfig, "check-success = ci", "check-success = other-ci")
		_, err = f.PushBase(repo, "main", "unrelated default branch update", map[string]string{".openmerge.yml": updated})
		must(t, err)
		must(t, s.reconcile(t.Context(), w, false))
		after, next, err := s.store.Load(t.Context(), w.id)
		must(t, err)
		if next != watermark || !reflect.DeepEqual(before.Active, after.Active) || after.ConfigCommit != before.ConfigCommit {
			t.Fatal("unchanged effective release policy invalidated work or appended events")
		}
		restarted, err := New(t.Context(), s.store, []Target{w.Target}, s.options)
		must(t, err)
		must(t, restarted.configuration(t.Context(), restarted.queues[0]))
		after, next, err = s.store.Load(t.Context(), w.id)
		must(t, err)
		if next != watermark || !reflect.DeepEqual(before.Active, after.Active) {
			t.Fatal("restart invalidated work with an identical durable policy")
		}
	})
}

func TestConfigurationReadsStayPinnedWhileDefaultBranchMoves(t *testing.T) {
	s, w, f, d := releaseFixture(t, releaseConfig)
	observed, err := f.ReadRef(t.Context(), repo, "main")
	must(t, err)
	var next string
	d.onRead = func() {
		var err error
		next, err = f.PushBase(repo, "main", "policy moves during read", map[string]string{".openmerge.yml": strings.ReplaceAll(releaseConfig, "merge_method: merge", "merge_method: rebase")})
		must(t, err)
	}
	must(t, s.configuration(t.Context(), w))
	if w.configTip != observed || w.cfg.MergeMethod != queue.Merge {
		t.Fatal("policy was not paired with its observed commit")
	}
	for _, ref := range d.refs {
		if ref != observed {
			t.Fatalf("mixed policy reads: %v", d.refs)
		}
	}
	must(t, s.configuration(t.Context(), w))
	if w.configTip != next || w.cfg.MergeMethod != queue.Rebase {
		t.Fatal("next default tip was not loaded on the next observation")
	}
}

func TestDefaultPolicyMoveReobservesUnchangedReleasePR(t *testing.T) {
	s, w, f, _ := releaseFixture(t, releaseConfig)
	must(t, f.SetPRMetadata(repo, 1, "release/v1", false, "contributor"))
	must(t, s.reconcile(t.Context(), w, false))
	decision := func() queue.PRObserved {
		data, err := s.store.Observation(t.Context(), w.id, "pr/1")
		must(t, err)
		var observation struct{ Decision queue.PRObserved }
		must(t, json.Unmarshal(data, &observation))
		return observation.Decision
	}
	if !decision().Eligible {
		t.Fatal("initial release policy did not admit the PR")
	}
	updated := strings.Replace(releaseConfig, "    base: release/*", "    base: release/*\n    enter_when: [label = ready]", 1)
	_, err := f.PushBase(repo, "main", "tighten release admission", map[string]string{".openmerge.yml": updated})
	must(t, err)
	must(t, s.reconcile(t.Context(), w, false))
	if decision().Eligible {
		t.Fatal("unchanged release PR retained its admission under the old policy")
	}
}

func TestDefaultBranchFailuresPauseAndRetryAtSameTip(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"metadata", "tip", "file", "missing", "invalid"} {
		t.Run(failure, func(t *testing.T) {
			s, w, f, _ := releaseFixture(t, releaseConfig)
			must(t, s.configuration(t.Context(), w))
			validTip := w.configTip
			_, before, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			switch failure {
			case "metadata":
				f.FailNext("DefaultBranch", errors.New("metadata unreadable"))
			case "tip":
				w.Git = &configReadGit{Git: f, failRef: "main"}
			case "file":
				_, err := f.PushBase(repo, "main", "new tip", nil)
				must(t, err)
				f.FailNext("ReadFile", errors.New("contents unreadable"))
			case "missing":
				// The initial root has no config; the release branch still does.
				history, err := f.History(repo, "main")
				must(t, err)
				must(t, f.SetRef(repo, "main", history[0].SHA))
			case "invalid":
				_, err := f.PushBase(repo, "main", "invalid authority", map[string]string{".openmerge.yml": "version: invalid"})
				must(t, err)
			}
			readErr := s.configuration(t.Context(), w)
			transient := failure == "metadata" || failure == "tip" || failure == "file"
			if transient && readErr == nil || !transient && readErr != nil {
				t.Fatalf("unexpected read result: %v", readErr)
			}
			if transient {
				events, err := s.store.EventsAfter(t.Context(), w.id, before)
				must(t, err)
				if len(events) != 0 {
					t.Fatalf("transient read wrote events: %+v", events)
				}
			}
			state, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			if w.configError == nil || w.configTip != "" || state.PauseReason(queue.Config{}) != map[bool]string{true: "", false: "invalid-config"}[transient] {
				t.Fatalf("authority failure did not pause: worker=%+v state=%+v", w, state)
			}
			w.Git = f
			must(t, f.SetRef(repo, "main", validTip))
			must(t, s.configuration(t.Context(), w))
			state, _, err = s.store.Load(t.Context(), w.id)
			must(t, err)
			if w.configError != nil || w.configTip != validTip || state.PauseReason(queue.Config{}) != "" {
				t.Fatal("configuration did not recover at the same valid tip")
			}
		})
	}
}

func TestUnreadableDefaultBaseTipPausesAndRecovers(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	must(t, s.configuration(t.Context(), w))
	tip := w.configTip
	w.Git = &configReadGit{Git: f, failRef: "main"}
	if err := s.configuration(t.Context(), w); err == nil {
		t.Fatal("unreadable tip accepted")
	}
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if w.configError == nil || w.configTip != "" || state.BaseSHA != tip || state.PauseReason(queue.Config{}) != "" {
		t.Fatalf("unreadable default/base tip did not pause: worker=%+v state=%+v", w, state)
	}
	w.Git = f
	must(t, s.configuration(t.Context(), w))
	state, _, err = s.store.Load(t.Context(), w.id)
	must(t, err)
	if w.configError != nil || w.configTip != tip || state.PauseReason(queue.Config{}) != "" {
		t.Fatal("default/base tip did not recover at the same commit")
	}
}

func TestDefaultBranchRenameReevaluatesOmittedBase(t *testing.T) {
	for _, deleteOld := range []bool{false, true} {
		t.Run(fmt.Sprintf("delete-old=%v", deleteOld), func(t *testing.T) {
			s, w, f, _ := fixture(t, repositoryConfig)
			must(t, s.configuration(t.Context(), w))
			tip := w.configTip
			// Both branch names point at one commit: the selector default still changed.
			must(t, f.PushRef(repo, "trunk", w.configTip))
			f.SetDefaultBranch(repo, "trunk")
			if deleteOld {
				must(t, f.DeleteRef(t.Context(), repo, "main"))
			}
			readErr := s.configuration(t.Context(), w)
			if deleteOld && readErr == nil || !deleteOld && readErr != nil {
				t.Fatalf("read result=%v", readErr)
			}
			state, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			if w.configError == nil || w.configBranch != "" || state.PauseReason(queue.Config{}) != map[bool]string{true: "", false: "invalid-config"}[deleteOld] {
				t.Fatal("old base retained omitted-base policy after default branch rename")
			}
			must(t, f.SetRef(repo, "main", tip))
			f.SetDefaultBranch(repo, "main")
			must(t, s.configuration(t.Context(), w))
			if w.configError != nil || w.configBranch != "main" {
				t.Fatal("restoring default branch did not recover configuration")
			}
		})
	}
}

func TestOverlappingQueuePausesOnlyItsBase(t *testing.T) {
	contents := releaseConfig + "  - name: v1\n    base: release/v1\n    merge_when: [check-success = ci]\n"
	s, w, f, _ := releaseFixture(t, contents)
	must(t, s.configuration(t.Context(), w))
	var diagnostics config.Errors
	if !errors.As(w.configError, &diagnostics) || len(diagnostics) != 2 || diagnostics[0].Path != "queues[0].base" || diagnostics[1].Path != "queues[2].base" {
		t.Fatalf("overlap = %v", w.configError)
	}
	target := w.Target
	target.Key.BaseBranch = "main"
	other, err := New(t.Context(), s.store, []Target{target}, s.options)
	must(t, err)
	must(t, other.configuration(t.Context(), other.queues[0]))
	if other.queues[0].configError != nil || other.queues[0].cfg.Name != "default" {
		t.Fatalf("unaffected main queue failed: %v", other.queues[0].configError)
	}
	// Removing the overlap resumes the existing release queue without a rekey.
	id := w.id
	_, err = f.PushBase(repo, "main", "remove overlap", map[string]string{".openmerge.yml": releaseConfig})
	must(t, err)
	must(t, s.configuration(t.Context(), w))
	if w.configError != nil || w.id != id {
		t.Fatal("correcting overlap did not restore the existing queue")
	}
}

func TestRepositoryDeleteHeadBranchOverridesServeDefault(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, setting string
		flag, deleted bool
	}{
		{"inherited true", "", true, true},
		{"inherited false", "", false, false},
		{"repo false overrides true", "delete_head_branch: false\n", true, false},
		{"repo true overrides false", "delete_head_branch: true\n", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				// Load an explicit setting first, then reload the test's policy so
				// omission must discard the previous override as well as inherit.
				s, w, f, _ := fixture(t, repositoryConfig+"delete_head_branch: true\n")
				s.options.DisableDeleteHeadBranch = !tc.flag
				must(t, s.configuration(t.Context(), w))
				_, err := f.PushBase(repo, "main", "reload cleanup policy", map[string]string{".openmerge.yml": repositoryConfig + tc.setting})
				must(t, err)
				pr, err := f.PullRequest(t.Context(), repo, 1)
				must(t, err)
				f.AddComment(repo, 1, "writer", "@openmerge queue")
				must(t, s.cycle(t.Context(), w))
				state, _, err := s.store.Load(t.Context(), w.id)
				must(t, err)
				if state.Active == nil || state.Active.Phase != queue.Testing {
					t.Fatal("PR did not reach testing")
				}
				tested := state.Active.TestedSHA
				f.SetCheckRuns(repo, tested, []forge.CheckRun{{ID: 1, Name: "ci", Source: "app:1", State: forge.Success, HeadBranch: state.Active.TestRef}})
				time.Sleep(2 * time.Millisecond)
				must(t, s.cycle(t.Context(), w))
				state, _, err = s.store.Load(t.Context(), w.id)
				must(t, err)
				if state.BaseSHA != tested || state.Active != nil {
					t.Fatal("PR did not land")
				}
				_, err = f.ReadRef(t.Context(), repo, pr.HeadBranch)
				if (err != nil) != tc.deleted {
					t.Fatalf("head deletion = %v, want %v", err, tc.deleted)
				}
			})
		})
	}
}

func TestLegacyWindowUpgradeUsesHistoricalPolicyBeforeClamp(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		oldDepth, newDepth int
		idle               bool
	}{
		{"unchanged idle", 8, 8, true}, {"unchanged active", 8, 8, false},
		{"clamp idle", 8, 4, true}, {"clamp active", 8, 4, false},
		{"higher ceiling", 8, 12, true}, {"serial to speculative", 1, 4, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			contents := repositoryConfig + fmt.Sprintf("    speculation:\n      max_depth: %d\n", tc.oldDepth)
			s, w, f, _ := fixture(t, contents)
			oldTip, err := f.ReadRef(t.Context(), repo, "main")
			must(t, err)
			cfg, _, err := config.Load([]byte(contents))
			must(t, err)
			old := cfg.Queues[0]
			events := []queue.Event{queue.BaseMoved{SHA: oldTip}, queue.ConfigApplied{Commit: oldTip, Digest: old.Digest(true)}}
			if !tc.idle {
				pr, err := f.PullRequest(t.Context(), repo, 1)
				must(t, err)
				events = append(events, queue.PRObserved{PR: 1, HeadSHA: pr.HeadSHA, Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: time.Now()}, queue.ChainedBatchStarted{BatchID: "b1", BaseSHA: oldTip, PRs: []queue.BatchPR{{PR: 1, HeadSHA: pr.HeadSHA}}, Position: 1, At: time.Now()})
			}
			_, err = s.store.Append(t.Context(), w.id, events, "")
			must(t, err)
			want := min(tc.oldDepth, tc.newDepth)
			changed := tc.oldDepth != tc.newDepth
			if changed {
				_, err = f.PushBase(repo, "main", "new policy", map[string]string{".openmerge.yml": repositoryConfig + fmt.Sprintf("    speculation:\n      max_depth: %d\n", tc.newDepth)})
				must(t, err)
			}
			must(t, s.configuration(t.Context(), w))
			state, revision, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			if state.Window(w.cfg.QueueConfig()) != want || !state.WindowInitialized() {
				t.Fatalf("window=%d want=%d initialized=%t", state.Window(w.cfg.QueueConfig()), want, state.WindowInitialized())
			}
			if !changed && !tc.idle && (state.Active == nil || state.Active.ID != "b1" || state.Active.InvalidationReason != "") {
				t.Fatal("migration discarded admitted work")
			}
			restarted, err := New(t.Context(), s.store, []Target{w.Target}, s.options)
			must(t, err)
			must(t, restarted.configuration(t.Context(), restarted.queues[0]))
			state, after, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			if after != revision || state.Window(w.cfg.QueueConfig()) != want {
				t.Fatal("restart repeated migration or reset window")
			}
		})
	}
}

func TestLegacyWindowUpgradeRejectsUnrecoverableHistoricalPolicy(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(fmt.Sprint("digest mismatch=", mismatch), func(t *testing.T) {
			s, w, f, _ := fixture(t, repositoryConfig+"    speculation: {max_depth: 4}\n")
			tip, err := f.ReadRef(t.Context(), repo, "main")
			must(t, err)
			commit := strings.Repeat("f", 40)
			if mismatch {
				commit = tip
			}
			digest := strings.Repeat("b", 64)
			_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.BaseMoved{SHA: tip}, queue.ConfigApplied{Commit: commit, Digest: digest}}, "")
			must(t, err)
			if err := s.configuration(t.Context(), w); err == nil {
				t.Fatal("invented a window without matching historical policy")
			}
			state, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			if state.ConfigDigest != digest || state.WindowInitialized() {
				t.Fatal("failed migration published the replacement policy")
			}
		})
	}
}
