package serve

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/gitops"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestAuthorityAcceptanceIntegratedObjectRefusal(t *testing.T) {
	for _, kind := range []string{"intermediate", "marker", "fence"} {
		t.Run(kind, func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				method := queue.Rebase
				if kind == "marker" {
					method = queue.FastForward
				}
				f := acceptanceGitFixture(t, method, 0, func(e *authority.Observation) {
					pattern := "OpenMerge-Batch"
					refs := []string{"refs/heads/openmerge/queue/**/*"}
					if kind == "marker" {
						pattern = "source"
					}
					if kind == "fence" {
						pattern = "initial"
						refs = []string{"refs/heads/openmerge/fence/main"}
					}
					e.Protections = append(e.Protections, authority.Protection{
						ID: "acceptance-content", Source: "repository_ruleset", Target: "branch", Enforcement: "active",
						TargetsVisibility: authority.Visible, RulesVisibility: authority.Visible, BypassVisibility: authority.Visible,
						Includes: refs, Excludes: []string{}, Conditions: []authority.Parameter{}, BypassActors: []authority.BypassActor{},
						Rules: []authority.Rule{{Kind: "commit_message_pattern", Parameters: []authority.Parameter{
							{Name: "operator", Values: []string{`"contains"`}},
							{Name: "pattern", Values: []string{`"` + pattern + `"`}},
						}}},
					})
				})
				s, w := f.s, f.w
				cfg := w.plannerConfig(s.options.PollInterval)
				before := f.git("", "ls-remote", "--refs", f.remote)
				_, watermark, err := s.store.Load(t.Context(), w.id)
				must(t, err)
				build := claimLandingChecksAction(t, s, w, cfg, func(a queue.Action) bool { _, ok := a.(queue.BuildTestRef); return ok })

				err = s.execute(t.Context(), w, build)
				if kind == "fence" {
					must(t, err)
					batch := lifecycleState(t, s, w).BatchForPR(1)
					f.driver.SetCheckRuns(w.repo(), batch.TestedSHA, []forge.CheckRun{{ID: 902, Name: "ci", State: forge.Success, HeadBranch: batch.TestRef}})
					_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.ChecksCompleted{TestedSHA: batch.TestedSHA, Success: true}}, "")
					must(t, err)
					before = f.git("", "ls-remote", "--refs", f.remote)
					_, watermark, err = s.store.Load(t.Context(), w.id)
					must(t, err)
					land := claimLandingChecksAction(t, s, w, cfg, func(a queue.Action) bool { _, ok := a.(queue.Land); return ok })
					err = s.execute(t.Context(), w, land)
				}
				if !errors.Is(err, forge.ErrInvalidInput) || !strings.Contains(err.Error(), "commit message rejected") {
					t.Fatalf("expected generated-object content rejection, got %v", err)
				}
				after := f.git("", "ls-remote", "--refs", f.remote)
				if before != after {
					t.Fatalf("refused %s changed remote advertised refs\nbefore:\n%s\nafter:\n%s", kind, before, after)
				}
				rows, readErr := s.store.EventsAfter(t.Context(), w.id, watermark)
				must(t, readErr)
				for _, row := range rows {
					switch row.Event.(type) {
					case queue.TestRefBuilt, queue.FastForwardRefBuilt, queue.Landed:
						t.Fatalf("refused object recorded success: %+v", row.Event)
					}
				}
				t.Logf("%s refused; remote main/fence/queue refs identical: %v", kind, err)
			})
		})
	}
}

// #138 item 2 carries over to 9c-3. Keep safety strict while allowing its
// typed lease refusal to replace the current generic boundary-proof refusal.
func TestAuthorityAcceptanceBaseAdvanceBeforeBuild(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		// Observe actual Git dispatch independently of unchanged remote refs.
		gitPath, err := exec.LookPath("git")
		must(t, err)
		spyDir := t.TempDir()
		pushMarker := filepath.Join(spyDir, "push-dispatched")
		quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
		script := "#!/bin/sh\nfor arg in \"$@\"; do\nif [ \"$arg\" = push ]; then\n: > " + quote(pushMarker) + "\nfi\ndone\nexec " + quote(gitPath) + " \"$@\"\n"
		must(t, os.WriteFile(filepath.Join(spyDir, "git"), []byte(script), 0700))
		t.Setenv("PATH", spyDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		f := acceptanceGitFixture(t, queue.Merge, 0)
		s, w := f.s, f.w
		cfg := w.plannerConfig(s.options.PollInterval)
		build := claimLandingChecksAction(t, s, w, cfg, func(a queue.Action) bool { _, ok := a.(queue.BuildTestRef); return ok })
		tree := f.git("", "--git-dir="+f.remote, "rev-parse", f.base+"^{tree}")
		advanced := f.git("outside base advance\n", "--git-dir="+f.remote, "commit-tree", tree, "-p", f.base)
		var before string
		moved := false
		w.Git = &acceptanceAdvanceBeforeBuild{Repo: f.g, before: func() {
			if moved {
				t.Fatal("stale build unexpectedly retried")
			}
			moved = true
			f.git("", "--git-dir="+f.remote, "update-ref", "refs/heads/main", advanced, f.base)
			before = f.git("", "ls-remote", "--refs", f.remote)
		}}
		must(t, os.Remove(pushMarker))
		_, watermark, err := s.store.Load(t.Context(), w.id)
		must(t, err)
		err = s.execute(t.Context(), w, build)
		if !moved {
			t.Fatal("build did not reach synchronized base advance")
		}
		// 9c-3 may change the refusal type and failure/recovery journal shape.
		if err == nil || (!errors.Is(err, forge.ErrLeaseRejected) && !strings.Contains(err.Error(), "prepared boundary proof")) {
			t.Fatalf("expected boundary or typed lease refusal, got %v", err)
		}
		if _, statErr := os.Stat(pushMarker); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("stale-base build dispatched push past the guard: %v", statErr)
		}
		t.Logf("safe stale-base refusal: %v", err)
		after := f.git("", "ls-remote", "--refs", f.remote)
		if before != after {
			t.Fatal("stale-base build changed remote refs")
		}
		rows, err := s.store.EventsAfter(t.Context(), w.id, watermark)
		must(t, err)
		for _, row := range rows {
			switch row.Event.(type) {
			case queue.TestRefBuilt, queue.FastForwardRefBuilt, queue.LandingDecisionRecorded, queue.LandingChecksRecorded, queue.LandingStarted, queue.Landed:
				t.Fatalf("stale-base build recorded success: %+v", row.Event)
			}
		}
	})
}

// During repository-wide propagation a GitHub worker can coexist with an
// adopted authority worker. Both Git clients below address the same bare origin.
func TestAuthorityAcceptanceMixedRealGit(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		f := acceptanceGitFixture(t, queue.Merge, 0)
		s := f.s
		main := lifecycleState(t, s, f.w)
		if pin := main.AuthorityPin(); pin == nil || !queue.AuthorityDraftAdopted(*pin) {
			t.Fatal("main lacks adopted authority")
		}
		_, mainBefore, err := s.store.Load(t.Context(), f.w.id)
		must(t, err)
		f.git("", "--git-dir="+f.remote, "update-ref", "refs/heads/release", f.base)
		now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
		g, err := gitops.New(t.Context(), gitops.Config{WorkDir: filepath.Join(t.TempDir(), "legacy-cache"), Now: func() time.Time { return now }, Remote: func(context.Context, forge.RepoRef) (string, gitops.Auth, error) { return f.remote, gitops.Auth{}, nil }, HeadRef: func(n int) string { return fmt.Sprintf("refs/pull/%d/head", n) }})
		must(t, err)
		key := f.w.Key
		key.BaseBranch = "release"
		driver := &countingDriver{Driver: f.driver}
		w, err := s.register(t.Context(), Target{Key: key, Driver: driver, Git: g, BotLogin: f.w.BotLogin})
		must(t, err)
		f.driver.SetCanWrite(w.repo(), "writer", true)
		_, before, err := s.store.Load(t.Context(), w.id)
		must(t, err)
		for i, user := range []string{"reader", "writer"} {
			_, err = s.handleLegacyCommand(t.Context(), w, 1, forge.Comment{ID: int64(900 + i), Author: user, Body: "@openmerge dequeue", CreatedAt: now})
			must(t, err)
		}
		if driver.permissions != 2 {
			t.Fatalf("mixed command permission reads=%d", driver.permissions)
		}
		recorded := []queue.Event{}
		originalRandom := rand.Reader
		defer func() { rand.Reader = originalRandom }()
		for _, method := range []queue.MergeMethod{queue.Merge, queue.Squash, queue.Rebase, queue.FastForward} {
			rand.Reader = bytes.NewReader(bytes.Repeat([]byte{0x5a}, 65536))
			batch := "mixed-legacy-" + string(method)
			ref := "openmerge/queue/release/" + batch
			opts := s.authorityPushOptions(t.Context(), w, forge.PushBuild, []forge.PushRefIntent{{Ref: "refs/heads/" + ref, BuildBaseSHA: f.base}})
			if !reflect.DeepEqual(opts, forge.PushOptions{}) {
				t.Fatal("GitHub worker inherited authority options")
			}
			x := executor.Executor{Git: g, Repo: w.repo(), Base: "release", AllowNoRequiredChecks: true, Now: func() time.Time { return now }, PreparePush: func(ctx context.Context, op forge.PushOperation, refs []forge.PushRefIntent) (forge.PushOptions, error) {
				return s.authorityPushOptions(ctx, w, op, refs), nil
			}}
			err = x.Execute(t.Context(), queue.BuildTestRef{BatchID: batch, Ref: ref, BaseSHA: f.base, PRs: f.pins, Method: method}, func(e queue.Event) error { recorded = append(recorded, e); return nil })
			must(t, err)
			actual := f.git("", "--git-dir="+f.remote, "rev-parse", "refs/heads/"+ref)
			legacyPRs := []forge.BuildPR{{Number: 1, HeadSHA: f.pins[0].HeadSHA}}
			rand.Reader = bytes.NewReader(bytes.Repeat([]byte{0x5a}, 65536))
			legacy, err := g.BuildTestRef(t.Context(), w.repo(), forge.BuildRequest{Ref: ref, BaseSHA: f.base, PRs: legacyPRs, Method: forge.MergeMethod(method), BatchID: batch}, forge.PushOptions{})
			must(t, err)
			if actual != legacy {
				t.Fatalf("%s mixed OID=%s legacy OID=%s", method, actual, legacy)
			}
		}
		rows, err := s.store.EventsAfter(t.Context(), w.id, before)
		must(t, err)
		if len(rows) != 1 {
			t.Fatalf("mixed GitHub command journal=%+v", rows)
		}
		if e, ok := rows[0].Event.(queue.DequeueRequested); !ok || e.PR != 1 {
			t.Fatalf("writer command changed: %+v", rows[0].Event)
		}
		for _, event := range recorded {
			switch event.(type) {
			case queue.BatchStarted, queue.TestRefBuilt, queue.FastForwardBatchSelected, queue.FastForwardRefBuilt:
			default:
				t.Fatalf("GitHub build emitted nonlegacy event: %+v", event)
			}
		}
		if len(recorded) != 9 {
			t.Fatalf("GitHub build event count=%d", len(recorded))
		}
		if lifecycleState(t, s, w).AuthorityPin() != nil {
			t.Fatal("GitHub queue acquired authority pin")
		}
		_, mainAfter, err := s.store.Load(t.Context(), f.w.id)
		must(t, err)
		if mainAfter != mainBefore {
			t.Fatal("legacy work changed authority queue journal")
		}
	})
}

// Move the origin only after execute's fresh trust/config read and immutable
// dispatch preparation, immediately before the real Git builder reads its base.
type acceptanceAdvanceBeforeBuild struct {
	*gitops.Repo
	before func()
}

func (g *acceptanceAdvanceBeforeBuild) BuildTestRef(ctx context.Context, repo forge.RepoRef, request forge.BuildRequest, options forge.PushOptions) (string, error) {
	g.before()
	return g.Repo.BuildTestRef(ctx, repo, request, options)
}
