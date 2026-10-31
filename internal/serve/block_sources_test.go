package serve

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

type defaultBranchFailure struct {
	forge.Driver
	fail bool
	hook func()
}

func (d *defaultBranchFailure) DefaultBranch(ctx context.Context, repo forge.RepoRef) (string, error) {
	if d.hook != nil {
		d.hook()
	}
	if d.fail {
		return "", errors.New("transient default branch failure")
	}
	return d.Driver.DefaultBranch(ctx, repo)
}

func TestConfigRecoveryPreservesSafetySources(t *testing.T) {
	t.Parallel()
	for _, legacy := range []bool{false, true} {
		for _, seed := range []string{"landing", "poison", "both"} {
			name := seed
			if legacy {
				name += "/legacy"
			}
			t.Run(name, func(t *testing.T) {
				s, w, f, _ := fixture(t, repositoryConfig)
				must(t, s.configuration(t.Context(), w))
				base, err := f.ReadRef(t.Context(), repo, "main")
				must(t, err)
				var seeds []queue.Event
				if seed != "poison" {
					if legacy {
						seeds = append(seeds, queue.PauseRequested{Reason: "landing-not-on-base", Detail: "restore tested SHA"})
					} else {
						seeds = append(seeds, queue.BlockSourceBlocked{Source: queue.LandingNotOnBase, Detail: "restore tested SHA", Subject: base})
					}
				}
				if seed != "landing" {
					if legacy {
						seeds = append(seeds, queue.ActionPoisoned{Key: "bad-action", Reason: "invalid payload"})
					} else {
						seeds = append(seeds, queue.BlockSourceBlocked{Source: queue.ActionPoison, Detail: "invalid payload", Subject: "bad-action"})
					}
				}
				_, err = s.store.Append(t.Context(), w.id, seeds, "")
				must(t, err)
				before, _, err := s.store.Load(t.Context(), w.id)
				must(t, err)
				d := &defaultBranchFailure{Driver: w.Driver, fail: true}
				w.Driver = d
				if err := s.cycle(t.Context(), w); err == nil {
					t.Fatal("transient read error swallowed")
				}
				d.fail = false
				must(t, s.cycle(t.Context(), w))
				state, _, err := s.store.Load(t.Context(), w.id)
				must(t, err)
				if state.SourceBlocked(queue.ConfigSource) || len(state.Blocks(queue.Config{})) != len(before.Blocks(queue.Config{})) {
					t.Fatalf("config recovery removed another source: %+v", state.Blocks(queue.Config{}))
				}
				for _, block := range before.Blocks(queue.Config{}) {
					if !state.SourceBlocked(block.Source) || state.BlockSourceDetail(block.Source) != block.Detail || state.BlockSourceRevision(block.Source) != block.Revision {
						t.Fatalf("source changed across recovery: %+v", block)
					}
				}
			})
		}
	}
}

func TestConfigPassCannotClearAnOverlappingFailure(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	must(t, s.block(t.Context(), w, queue.ConfigSource, "old failure", ""))
	w.Driver = &defaultBranchFailure{Driver: w.Driver, hook: func() {
		must(t, s.block(t.Context(), w, queue.ConfigSource, "newer failure", ""))
	}}
	must(t, s.configuration(t.Context(), w))
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.BlockSourceDetail(queue.ConfigSource) != "newer failure" {
		t.Fatal("stale config pass cleared a newer failure")
	}
}

func TestCheckSourcesRecoverIndependentlyAcrossPRsAndRestart(t *testing.T) {
	t.Parallel()
	cfg := strings.Replace(repositoryConfig, "    merge_when:", "    enter_when: [check-success = admission]\n    merge_when:", 1)
	s, w, f, _ := fixture(t, cfg)
	base, err := f.ReadRef(t.Context(), repo, "main")
	must(t, err)
	_, err = f.OpenPR(repo, 2, base, true)
	must(t, err)
	for _, n := range []int{1, 2} {
		pr, err := f.PullRequest(t.Context(), repo, n)
		must(t, err)
		f.SetCheckRuns(repo, pr.HeadSHA, []forge.CheckRun{{ID: 1, Name: "admission", Source: "app:1", State: forge.Success}, {ID: 2, Name: "admission", Source: "app:2", State: forge.Success}})
	}
	must(t, s.block(t.Context(), w, queue.Operator, "operator hold", ""))
	must(t, s.block(t.Context(), w, queue.ActionPoison, "poison hold", "bad-action"))
	must(t, s.cycle(t.Context(), w))
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if !state.SourceBlocked(queue.CheckSources) || !state.SourceBlocked(queue.Operator) || !state.SourceBlocked(queue.ActionPoison) {
		t.Fatal("ambiguity overwrote another source")
	}
	pr, err := f.PullRequest(t.Context(), repo, 1)
	must(t, err)
	f.SetCheckRuns(repo, pr.HeadSHA, []forge.CheckRun{{ID: 3, Name: "admission", Source: "app:1", State: forge.Success}})
	must(t, s.cycle(t.Context(), w))
	state, _, err = s.store.Load(t.Context(), w.id)
	must(t, err)
	if !state.SourceBlocked(queue.CheckSources) {
		t.Fatal("one passing PR cleared another PR's ambiguity")
	}
	restarted, err := New(t.Context(), s.store, []Target{w.Target}, s.options)
	must(t, err)
	w = restarted.queues[0]
	pr, err = f.PullRequest(t.Context(), repo, 2)
	must(t, err)
	f.SetCheckRuns(repo, pr.HeadSHA, []forge.CheckRun{{ID: 3, Name: "admission", Source: "app:1", State: forge.Success}})
	must(t, restarted.cycle(t.Context(), w))
	state, _, err = s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.SourceBlocked(queue.CheckSources) || !state.SourceBlocked(queue.Operator) || !state.SourceBlocked(queue.ActionPoison) {
		t.Fatal("recovery failed or crossed sources")
	}
}

func TestRefreshClearsOnlyOperatorClearableSources(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	must(t, s.configuration(t.Context(), w))
	base, err := f.ReadRef(t.Context(), repo, "main")
	must(t, err)
	for _, source := range []queue.BlockSource{queue.ConfigSource, queue.CheckSources, queue.Operator, queue.ActionPoison, queue.LandingNotOnBase} {
		subject := ""
		if source == queue.ActionPoison {
			subject = "bad-action"
		}
		if source == queue.LandingNotOnBase {
			subject = base
		}
		must(t, s.block(t.Context(), w, source, "repair "+string(source), subject))
	}
	_, err = s.store.Append(t.Context(), w.id, []queue.Event{
		queue.RulesSourceBlocked{Source: queue.Rulesets, Problems: []string{"rulesets"}},
		queue.RulesSourceBlocked{Source: queue.ClassicProtection, Problems: []string{"classic"}},
		queue.RulesSourceBlocked{Source: queue.Permissions, Problems: []string{"permissions"}},
		queue.LifecycleChanged{State: "dormant", Detail: "missing base"},
	}, "")
	must(t, err)
	// An invalid config does not prevent explicitly clearing a separate source.
	w.configError = errors.New("config unavailable")
	pr, err := s.observe(t.Context(), w, 1)
	must(t, err)
	must(t, s.report(t.Context(), w))
	summary := f.Statuses(repo, 1)[pr.HeadSHA].Summary
	for _, want := range []string{
		"@openmerge refresh to clear: action-poisoned, operator",
		"@openmerge refresh clears landing-not-on-base only after fresh ancestry verification",
		"clear automatically when their conditions recover: lifecycle, config, check-sources, rulesets, classic_protection, permissions",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("missing %q in %s", want, summary)
		}
	}
	f.AddComment(repo, 1, "writer", "@openmerge refresh")
	must(t, s.commands(t.Context(), w, 1))
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	for _, block := range state.Blocks(queue.Config{}) {
		if block.Source.OperatorClearable() {
			t.Fatalf("refresh did not clear %+v", block)
		}
	}
	if len(state.Blocks(queue.Config{})) != 6 || !state.SourceBlocked(queue.ConfigSource) || !state.SourceBlocked(queue.CheckSources) {
		t.Fatalf("automatic sources changed: %+v", state.Blocks(queue.Config{}))
	}
}

func TestExplicitRefreshResetsBuildRetriesAndBackoff(t *testing.T) {
	for _, failures := range []int{1, 3} {
		t.Run(fmt.Sprint(failures), func(t *testing.T) {
			s, w, f, _ := fixture(t, repositoryConfig)
			must(t, s.configuration(t.Context(), w))
			_, err := s.observe(t.Context(), w, 1)
			must(t, err)
			now := time.Now()
			_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.EnqueueRequested{PR: 1, At: now}}, "")
			must(t, err)
			cfg := w.plannerConfig(s.options.PollInterval)
			for range failures {
				state, _, err := s.store.Load(t.Context(), w.id)
				must(t, err)
				built := false
				for _, action := range queue.Plan(cfg, state, now.Add(time.Hour)) {
					if build, ok := action.(queue.BuildTestRef); ok {
						_, err = s.store.Append(t.Context(), w.id, []queue.Event{
							queue.BatchStarted{BatchID: build.BatchID, BaseSHA: build.BaseSHA, PRs: build.PRs, At: now},
							queue.TestRefBuildFailed{BatchID: build.BatchID, At: now},
						}, "")
						must(t, err)
						built = true
					}
				}
				if !built {
					t.Fatal("missing build to fail")
				}
			}
			before, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			if !before.SourceBlocked(queue.BuildFailed) {
				t.Fatal("missing build retry streak")
			}
			if failures == 3 {
				must(t, s.report(t.Context(), w))
				pr, err := f.PullRequest(t.Context(), repo, 1)
				must(t, err)
				if summary := f.Statuses(repo, 1)[pr.HeadSHA].Summary; !strings.Contains(summary, "@openmerge refresh to clear: build-failed") {
					t.Fatal(summary)
				}
			}
			f.AddComment(repo, 1, "writer", "@openmerge refresh")
			must(t, s.commands(t.Context(), w, 1))
			state, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			if state.SourceBlocked(queue.BuildFailed) || state.PauseReason(cfg) != "" {
				t.Fatal("refresh retained the build failure streak")
			}
			for _, action := range queue.Plan(cfg, state, now) {
				if _, ok := action.(queue.BuildTestRef); ok {
					return
				}
			}
			t.Fatal("refresh did not clear the build backoff")
		})
	}
}

func TestRefreshVerifiesLandingBeforeClearingAndRejectsStalePass(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	base, err := f.ReadRef(t.Context(), repo, "main")
	must(t, err)
	pr, err := f.PullRequest(t.Context(), repo, 1)
	must(t, err)
	must(t, s.block(t.Context(), w, queue.LandingNotOnBase, "missing landed commit", pr.HeadSHA))
	must(t, s.block(t.Context(), w, queue.Operator, "operator hold", ""))
	f.AddComment(repo, 1, "writer", "@openmerge refresh")
	must(t, s.commands(t.Context(), w, 1))
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if !state.SourceBlocked(queue.LandingNotOnBase) || state.SourceBlocked(queue.Operator) {
		t.Fatal("refresh skipped ancestry or failed to clear operator")
	}
	must(t, f.SetRef(repo, "main", pr.HeadSHA))
	clears, err := s.operatorClears(t.Context(), w)
	must(t, err)
	must(t, s.block(t.Context(), w, queue.LandingNotOnBase, "newer missing commit", base))
	_, err = s.store.Append(t.Context(), w.id, clears, "")
	must(t, err)
	state, _, err = s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.BlockSourceDetail(queue.LandingNotOnBase) != "newer missing commit" {
		t.Fatal("stale ancestry pass cleared a newer failure")
	}
	f.AddComment(repo, 1, "writer", "@openmerge refresh")
	must(t, s.commands(t.Context(), w, 1))
	state, _, err = s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.SourceBlocked(queue.LandingNotOnBase) {
		t.Fatal("verified landing did not clear")
	}
}

func TestSourceLogsIncludeSecondaryBlocksAndIgnoreStaleClears(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	var output bytes.Buffer
	s.options.Logger = slog.New(slog.NewJSONHandler(&output, nil))
	must(t, s.block(t.Context(), w, queue.ConfigSource, "config error", ""))
	must(t, s.block(t.Context(), w, queue.Operator, "operator hold", ""))
	_, err := s.store.Append(t.Context(), w.id, []queue.Event{queue.BlockSourceCleared{Source: queue.Operator, Basis: 0}, queue.BlockSourceCleared{Source: queue.Operator, Basis: 1}}, "")
	must(t, err)
	s.logCommitted(t.Context(), w)
	sets, clears := 0, 0
	for _, row := range logRows(t, &output) {
		if row["msg"] == "queue block set" {
			sets++
		}
		if row["msg"] == "queue block cleared" {
			clears++
			if row["source"] != "operator" {
				t.Fatal(row)
			}
		}
	}
	if sets != 2 || clears != 1 {
		t.Fatalf("set/clear log counts %d/%d: %s", sets, clears, &output)
	}
}

func TestServeNeverEmitsSharedPauseEvents(t *testing.T) {
	paths, err := filepath.Glob("*.go")
	must(t, err)
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		must(t, err)
		ast.Inspect(file, func(node ast.Node) bool {
			if sel, ok := node.(*ast.SelectorExpr); ok && (sel.Sel.Name == "PauseRequested" || sel.Sel.Name == "ResumeRequested") {
				t.Errorf("%s uses shared %s; emit an independently owned block source", path, sel.Sel.Name)
			}
			return true
		})
	}
	// Guard against silently passing if the scan ever stops finding source files.
	if _, err := os.Stat("reconcile.go"); err != nil {
		t.Fatal(err)
	}
}
