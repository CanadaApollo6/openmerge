package serve

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestRestartChecksAfterReadiness(t *testing.T) {
	t.Parallel()
	for _, queued := range []bool{false, true} {
		t.Run(map[bool]string{false: "unqueued", true: "queued"}[queued], func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				s, d := newDiscoveryFixture(t)
				w := s.workers()[0]
				_, err := d.f.PushBase(repo, "main", "window four", map[string]string{".openmerge.yml": repositoryConfig + "    speculation: {min_depth: 4, max_depth: 4}\n"})
				must(t, err)
				must(t, s.Refresh(t.Context()))
				head, err := d.f.ReadRef(t.Context(), repo, "main")
				must(t, err)
				pr, err := d.f.OpenPR(repo, 1, head, true)
				must(t, err)
				head = pr.HeadSHA
				if queued {
					d.f.SetCanWrite(repo, "writer", true)
					d.f.AddComment(repo, 1, "writer", "@openmerge queue")
				}
				must(t, s.cycle(t.Context(), w))
				before := d.f.Statuses(repo, 1)[head]
				restarted, err := NewDiscovery(t.Context(), s.store, d.source(), s.options)
				must(t, err)
				current := restarted.workers()[0]
				must(t, restarted.resolveTarget(t.Context(), current))
				writes := &instructionDriver{Driver: current.Driver}
				current.Driver = writes
				// NewDiscovery returns before its first refresh: these calls pin
				// both sides of startup validation without racing a timer.
				for range 3 {
					must(t, restarted.cycle(t.Context(), current))
				}
				if writes.writes != 0 {
					t.Errorf("startup published %d checks before validation", writes.writes)
				}
				must(t, restarted.Refresh(t.Context()))
				must(t, restarted.cycle(t.Context(), current))
				if !current.ready {
					t.Fatal("restart did not become ready")
				}
				after := d.f.Statuses(repo, 1)[head]
				if !queued {
					if after != before {
						t.Fatalf("restart changed unqueued check: before=%+v after=%+v", before, after)
					}
				} else if after.State != forge.Testing || !strings.Contains(after.Summary, "Queue: window: 4;") {
					t.Fatalf("queued check did not recover its real state: %+v", after)
				}
				readyWrites := writes.writes
				for range 3 {
					must(t, restarted.cycle(t.Context(), current))
				}
				if writes.writes != readyWrites {
					t.Fatalf("unchanged ready cycles republished checks: %d -> %d", readyWrites, writes.writes)
				}
			})
		})
	}
}

func TestStartupReportingWithPolicyObservations(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	must(t, s.reconcile(t.Context(), w, true))
	w.managed, w.ready = true, false
	writes := &instructionDriver{Driver: w.Driver}
	w.Driver = writes
	for range 3 {
		must(t, s.report(t.Context(), w))
	}
	if writes.writes != 0 {
		t.Fatalf("unvalidated worker with PR observations published %d checks", writes.writes)
	}
}

func TestStartupDiscoveryFailuresPublishOnce(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"installations", "noncanonical installation", "installation binding", "repositories", "missing repository listing", "basis"} {
		t.Run(boundary, func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				s, d := newDiscoveryFixture(t)
				head, err := d.f.ReadRef(t.Context(), repo, "main")
				must(t, err)
				pr, err := d.f.OpenPR(repo, 1, head, true)
				must(t, err)
				d.f.SetCanWrite(repo, "writer", true)
				d.f.AddComment(repo, 1, "writer", "@openmerge queue")
				must(t, s.cycle(t.Context(), s.workers()[0]))
				if check := d.f.Statuses(repo, 1)[pr.HeadSHA]; check.State != forge.Testing {
					t.Fatalf("fixture did not publish testing: %+v", check)
				}
				restarted, err := NewDiscovery(t.Context(), s.store, d.source(), s.options)
				must(t, err)
				w := restarted.workers()[0]
				must(t, restarted.resolveTarget(t.Context(), w))
				writes := &instructionDriver{Driver: w.Driver}
				w.Driver = writes
				must(t, restarted.cycle(t.Context(), w))
				if writes.writes != 0 {
					t.Fatalf("startup wrote before discovery: %d", writes.writes)
				}
				failure := errors.New("discovery unavailable")
				for range 3 {
					ctx, cancel := context.WithCancel(t.Context())
					switch boundary {
					case "installations":
						d.appErr = failure
					case "noncanonical installation":
						d.installations[0].ID = "07"
					case "installation binding":
						restarted.discovery.App = "42"
						restarted.discovery.Installations = func(context.Context) ([]Installation, error) {
							cancel()
							return d.installations, nil
						}
						failure = context.Canceled
					case "repositories":
						d.listErr = failure
					case "missing repository listing":
						d.installations[0].Repositories = nil
					case "basis":
						// Cancel only after the complete repository listing so the
						// stored access-basis read fails, before refreshRepository.
						d.installations[0].Repositories = func(context.Context) ([]Repository, error) {
							cancel()
							return d.repos, nil
						}
						failure = context.Canceled
					}
					err := restarted.Refresh(ctx)
					cancel()
					switch boundary {
					case "noncanonical installation":
						if err == nil || !strings.Contains(err.Error(), "noncanonical installation ID") {
							t.Fatalf("refresh did not reject installation ID: %v", err)
						}
					case "missing repository listing":
						if err == nil || !strings.Contains(err.Error(), "installation requires repository listing") {
							t.Fatalf("refresh did not reject missing listing: %v", err)
						}
					default:
						if !errors.Is(err, failure) {
							t.Fatalf("refresh did not reach %s failure: %v", boundary, err)
						}
					}
					must(t, restarted.cycle(t.Context(), w))
					check := d.f.Statuses(repo, 1)[pr.HeadSHA]
					if writes.writes != 1 || check.State != forge.Paused || check.Reason != "validation-pending" {
						t.Fatalf("failed %s discovery left stale check or repeated writes: writes=%d check=%+v", boundary, writes.writes, check)
					}
					if strings.Contains(check.Summary, "window:") || strings.Contains(check.Summary, "batch size:") {
						t.Fatalf("failed discovery advertised unvalidated policy: %+v", check)
					}
				}
			})
		})
	}
}

// The second scope is still awaiting discovery, so a failure in the first
// must not end its silent startup window.
func TestStartupDiscoveryFailureScope(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"repositories", "basis", "installation binding", "missing repository listing"} {
		t.Run(boundary, func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				s, d := newDiscoveryFixture(t)
				other := forge.RepoRef{Path: "owner/other"}
				d.f.Init(other, "main", "openmerge/fence/main")
				_, err := d.f.PushBase(other, "main", "configured", map[string]string{".openmerge.yml": repositoryConfig})
				must(t, err)
				second := managedRepository(d.f, other.Path)
				if boundary != "basis" {
					target := second.Target
					second.Target = func(base string) Target {
						t := target(base)
						t.Key.Installation = "8"
						return t
					}
					d.installations = append(d.installations, Installation{ID: "8", Repositories: func(context.Context) ([]Repository, error) {
						return []Repository{second}, nil
					}})
				} else {
					d.repos = append(d.repos, second)
				}
				must(t, s.Refresh(t.Context()))
				if len(s.workers()) != 2 {
					t.Fatalf("want two workers, got %d", len(s.workers()))
				}
				heads := make(map[string]string)
				for _, w := range s.workers() {
					ref := forge.RepoRef{Path: w.Key.Repository}
					head, err := d.f.ReadRef(t.Context(), ref, "main")
					must(t, err)
					pr, err := d.f.OpenPR(ref, 1, head, true)
					must(t, err)
					heads[ref.Path] = pr.HeadSHA
					d.f.SetCanWrite(ref, "writer", true)
					d.f.AddComment(ref, 1, "writer", "@openmerge queue")
					must(t, s.cycle(t.Context(), w))
					if check := d.f.Statuses(ref, 1)[pr.HeadSHA]; check.State != forge.Testing {
						t.Fatalf("fixture did not publish testing for %s: %+v", ref.Path, check)
					}
				}
				before := d.f.Statuses(other, 1)[heads[other.Path]]
				restarted, err := NewDiscovery(t.Context(), s.store, d.source(), s.options)
				must(t, err)
				writes := make(map[string]*instructionDriver)
				for _, w := range restarted.workers() {
					must(t, restarted.resolveTarget(t.Context(), w))
					writes[w.Key.Repository] = &instructionDriver{Driver: w.Driver}
					w.Driver = writes[w.Key.Repository]
					must(t, restarted.cycle(t.Context(), w))
					if writes[w.Key.Repository].writes != 0 {
						t.Fatalf("startup wrote before discovery for %s", w.Key.Repository)
					}
				}
				for range 3 {
					ctx, cancel := context.WithCancel(t.Context())
					failure := errors.New("listing unavailable")
					// Installation B has not made its first attempt yet.
					restarted.discoveryBackoffs["8"] = time.Now().Add(time.Hour)
					switch boundary {
					case "repositories":
						d.listErr = failure
					case "installation binding":
						restarted.discovery.App = "42"
						restarted.discovery.Installations = func(context.Context) ([]Installation, error) {
							cancel()
							return d.installations, nil
						}
						failure = context.Canceled
					case "missing repository listing":
						d.installations[0].Repositories = nil
					case "basis":
						// B's descriptor is incomplete, so its validation has not
						// started. Keep both repositories in the complete listing.
						d.repos[1].Target = nil
						d.installations[0].Repositories = func(context.Context) ([]Repository, error) {
							cancel()
							return d.repos, nil
						}
						failure = context.Canceled
					}
					err := restarted.Refresh(ctx)
					cancel()
					if boundary == "missing repository listing" {
						if err == nil || !strings.Contains(err.Error(), "installation requires repository listing") {
							t.Fatalf("refresh did not reject missing listing: %v", err)
						}
					} else if !errors.Is(err, failure) {
						t.Fatalf("refresh did not reach %s failure: %v", boundary, err)
					}
					for _, w := range restarted.workers() {
						must(t, restarted.cycle(t.Context(), w))
					}
					check := d.f.Statuses(repo, 1)[heads[repo.Path]]
					if writes[repo.Path].writes != 1 || check.State != forge.Paused || check.Reason != "validation-pending" {
						t.Fatalf("affected scope did not publish once: writes=%d check=%+v", writes[repo.Path].writes, check)
					}
					if writes[other.Path].writes != 0 || d.f.Statuses(other, 1)[heads[other.Path]] != before {
						t.Fatalf("%s failure ended suppression for unrelated worker: writes=%d", boundary, writes[other.Path].writes)
					}
				}
			})
		})
	}
}

func TestStartupValidationFailureStillReportsWithoutDefaultPolicy(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"discovery", "repositories"} {
		t.Run(mode, func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				s, d := newDiscoveryFixture(t)
				head, err := d.f.ReadRef(t.Context(), repo, "main")
				must(t, err)
				pr, err := d.f.OpenPR(repo, 1, head, true)
				must(t, err)
				d.f.SetCanWrite(repo, "writer", true)
				d.f.AddComment(repo, 1, "writer", "@openmerge queue")
				must(t, s.cycle(t.Context(), s.workers()[0]))
				// The durable authority pin must keep queue instructions visible
				// even when the restarted worker cannot validate configuration.
				raw, err := os.ReadFile("../queue/testdata/wire/v1/event_policy_authority_applied.json")
				must(t, err)
				event, err := queue.DecodeEvent(raw)
				must(t, err)
				pin := event.(queue.PolicyAuthorityApplied)
				pin.EffectiveDigest = shadowState(t, s, s.workers()[0]).ConfigDigest
				pin.OpenMerge.Binding.EffectiveDigest = pin.EffectiveDigest
				_, err = s.store.Append(t.Context(), s.workers()[0].id, []queue.Event{pin}, "")
				must(t, err)
				_, err = d.f.PushBase(repo, "main", "invalid config", map[string]string{".openmerge.yml": "version: broken"})
				must(t, err)
				var restarted *Server
				if mode == "discovery" {
					restarted, err = NewDiscovery(t.Context(), s.store, d.source(), s.options)
					must(t, err)
					must(t, restarted.Refresh(t.Context()))
				} else {
					// This constructor attempts validation before returning, even
					// when the invalid repository config prevents per-base refresh.
					restarted, err = NewRepositories(t.Context(), s.store, d.repos, s.options)
					must(t, err)
				}
				w := restarted.workers()[0]
				must(t, restarted.resolveTarget(t.Context(), w))
				writes := &instructionDriver{Driver: w.Driver}
				w.Driver = writes
				for range 3 {
					must(t, restarted.cycle(t.Context(), w))
				}
				check := d.f.Statuses(repo, 1)[pr.HeadSHA]
				if writes.writes != 1 || check.State != forge.Paused || check.Reason != "validation-pending" {
					t.Fatalf("real pause missing or repeatedly published: writes=%d check=%+v", writes.writes, check)
				}
				if mode == "discovery" && !strings.Contains(check.Summary, "unsupported version") {
					t.Fatalf("lost configuration diagnostic: %+v", check)
				}
				if strings.Contains(check.Summary, "Queue:") || strings.Contains(check.Summary, "window:") || strings.Contains(check.Summary, "batch size:") {
					t.Fatalf("pause rendered a policy before validation: %+v", check)
				}
				stateText := "Current state: Waiting for queue readiness: validation pending"
				if mode == "discovery" {
					stateText = "Current state: Queue paused: invalid-config"
				}
				if !strings.Contains(check.Summary, stateText) || strings.Count(check.Summary, "@openmerge queue") != 1 {
					t.Fatalf("invalid configuration lost durable state or queue instructions: %+v", check)
				}
			})
		})
	}
}
