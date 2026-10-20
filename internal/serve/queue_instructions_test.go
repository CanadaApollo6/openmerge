package serve

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

type instructionDriver struct {
	forge.Driver
	writes, comments int
	fail             bool
}

func (d *instructionDriver) ReportStatus(ctx context.Context, r forge.RepoRef, pr int, head string, status forge.Status) error {
	d.writes++
	if d.fail {
		d.fail = false
		return errors.New("lost report")
	}
	return d.Driver.ReportStatus(ctx, r, pr, head, status)
}

func (d *instructionDriver) Comment(ctx context.Context, r forge.RepoRef, pr int, key, body string) error {
	d.comments++
	return d.Driver.Comment(ctx, r, pr, key, body)
}

func TestQueueInstructionsReportingPathsDedupeRetryRollback(t *testing.T) {
	for _, approval := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "approval"}[approval], func(t *testing.T) {
			var s *Server
			var w *worker
			if approval {
				s, w, _ = shadowFixture(t, 1, "main", "instructions", false)
			} else {
				s, w, _, _ = fixture(t, repositoryConfig)
				must(t, s.reconcile(t.Context(), w, true))
			}
			d := &instructionDriver{Driver: w.Driver}
			w.Driver = d
			raw, err := os.ReadFile("../queue/testdata/wire/v1/event_policy_authority_applied.json")
			must(t, err)
			event, err := queue.DecodeEvent(raw)
			must(t, err)
			pin := event.(queue.PolicyAuthorityApplied)
			state := shadowState(t, s, w)
			pin.EffectiveDigest = state.ConfigDigest
			pin.OpenMerge.Binding.EffectiveDigest = state.ConfigDigest
			appendPin := func(p queue.PolicyAuthorityApplied) {
				t.Helper()
				_, err := s.store.Append(t.Context(), w.id, []queue.Event{p}, "")
				must(t, err)
			}
			must(t, s.report(t.Context(), w))
			// Use the same input status to prove the whole preexisting observation,
			// including title/state and summary, survives activation and rollback.
			head := w.prs[1].HeadSHA
			input := forge.Status{State: forge.Queued, Summary: "Waiting for a test batch"}
			must(t, s.status(t.Context(), w, 1, head, input))
			before, err := s.store.Observation(t.Context(), w.id, "status/1")
			must(t, err)
			appendPin(pin)
			d.fail = true
			if err := s.status(t.Context(), w, 1, head, input); err == nil {
				t.Fatal("report failure ignored")
			}
			unchanged, err := s.store.Observation(t.Context(), w.id, "status/1")
			must(t, err)
			if string(before) != string(unchanged) {
				t.Fatal("failed write cached")
			}
			must(t, s.status(t.Context(), w, 1, head, input))
			rendered, err := s.store.Observation(t.Context(), w.id, "status/1")
			must(t, err)
			if strings.Count(string(rendered), "@openmerge queue") != 1 {
				t.Fatalf("missing/duplicate instruction: %s", rendered)
			}
			if approval && !strings.Contains(string(rendered), "Reviewer brief:") {
				t.Fatal("lost approval brief")
			}
			writes := d.writes
			for range 3 {
				must(t, s.status(t.Context(), w, 1, head, input))
			}
			if writes != d.writes {
				t.Fatal("unchanged cycles wrote checks")
			}
			// Both readiness-report paths also use the same single-check suffix.
			w.managed = true
			w.ready = false
			must(t, s.report(t.Context(), w))
			waiting, err := s.store.Observation(t.Context(), w.id, "status/1")
			must(t, err)
			must(t, s.reportWaiting(t.Context(), w))
			diagnostic, err := s.store.Observation(t.Context(), w.id, "status/1")
			must(t, err)
			if !reflect.DeepEqual(waiting, diagnostic) || strings.Count(string(waiting), "@openmerge queue") != 1 {
				t.Fatal("readiness paths differ")
			}
			pin.Authority = authority.GitHub
			pin.OpenMerge = nil
			appendPin(pin)
			must(t, s.status(t.Context(), w, 1, head, input))
			after, err := s.store.Observation(t.Context(), w.id, "status/1")
			must(t, err)
			if d.comments != 0 {
				t.Fatal("reporting posted a comment")
			}
			if string(before) != string(after) {
				t.Fatalf("legacy bytes changed after rollback:\n%s\n%s", before, after)
			}
		})
	}
}
