package serve

import (
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestRestartChecksAfterReadiness(t *testing.T) {
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

func TestStartupValidationFailureStillReportsWithoutDefaultPolicy(t *testing.T) {
	for _, mode := range []string{"discovery", "repositories"} {
		t.Run(mode, func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				s, d := newDiscoveryFixture(t)
				head, err := d.f.ReadRef(t.Context(), repo, "main")
				must(t, err)
				pr, err := d.f.OpenPR(repo, 1, head, true)
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
			})
		})
	}
}
