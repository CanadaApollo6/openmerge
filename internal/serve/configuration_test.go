package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	refs   []string
	onRead func()
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
	if !strings.Contains(output.String(), `"msg":"queue not yet served"`) || !strings.Contains(output.String(), `"queue":"default","base":"main"`) || len(s.queues) != 1 {
		t.Fatalf("other bases were not logged/excluded: %s", &output)
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
	for _, failure := range []string{"metadata", "tip", "file", "missing", "invalid"} {
		t.Run(failure, func(t *testing.T) {
			s, w, f, _ := releaseFixture(t, releaseConfig)
			must(t, s.configuration(t.Context(), w))
			validTip := w.configTip
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
			must(t, s.configuration(t.Context(), w))
			state, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			if w.configError == nil || w.configTip != "" || state.PauseReason(queue.Config{}) != "invalid-config" {
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
	must(t, s.configuration(t.Context(), w))
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if w.configError == nil || w.configTip != "" || state.BaseSHA != tip || state.PauseReason(queue.Config{}) != "invalid-config" {
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
	s, w, f, _ := fixture(t, repositoryConfig)
	must(t, s.configuration(t.Context(), w))
	// Both branch names point at one commit: the selector default still changed.
	must(t, f.PushRef(repo, "trunk", w.configTip))
	f.SetDefaultBranch(repo, "trunk")
	must(t, s.configuration(t.Context(), w))
	if w.configError == nil || !strings.Contains(w.configError.Error(), "no queue matches") {
		t.Fatal("old base retained omitted-base policy after default branch rename")
	}
	f.SetDefaultBranch(repo, "main")
	must(t, s.configuration(t.Context(), w))
	if w.configError != nil || w.configBranch != "main" {
		t.Fatal("restoring default branch did not recover configuration")
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
	}
}
