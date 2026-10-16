package serve

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestUnboundCheckProvenanceStatusAndTimeout(t *testing.T) {
	for _, branch := range []string{"", "foreign"} {
		t.Run("head_branch="+branch, func(t *testing.T) {
			s, w, f, _ := fixture(t, repositoryConfig)
			var output bytes.Buffer
			s.options.Logger = slog.New(slog.NewJSONHandler(&output, nil))
			f.AddComment(repo, 1, "writer", "@openmerge queue")
			must(t, s.cycle(t.Context(), w))
			state, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			tested, ref, head := state.Active.TestedSHA, state.Active.TestRef, state.Entries[0].HeadSHA
			f.SetCheckRuns(repo, tested, []forge.CheckRun{{ID: 1, Name: "ci", Source: "app:123", State: forge.Success, HeadBranch: branch}})
			time.Sleep(2 * time.Millisecond)
			must(t, s.cycle(t.Context(), w))
			observed := `head_branch="foreign"`
			if branch == "" {
				observed = "head_branch=null"
			}
			assertDetail := func(detail string) {
				t.Helper()
				for _, text := range []string{"unbound-check-provenance", `"ci"`, `"app:123"`, observed, ref} {
					if !strings.Contains(detail, text) {
						t.Fatalf("detail lacks %q: %s", text, detail)
					}
				}
			}
			assertDetail(f.Statuses(repo, 1)[head].Summary)
			if got := f.Statuses(repo, 1)[head]; got.State != forge.Blocked || got.Reason != "unbound-check-provenance" {
				t.Fatalf("PR did not surface provenance: %+v", got)
			}
			journals, err := s.store.Journals(t.Context(), []store.QueueKey{w.Key})
			must(t, err)
			_, entry, why := status.Project(journals[0], 1, 1, nil)
			if entry.Reason != "unbound-check-provenance" {
				t.Fatalf("API reason=%q", entry.Reason)
			}
			assertDetail(why.Summary)
			assertDetail(why.Events[0].Summary)
			found := false
			for _, row := range logRows(t, &output) {
				if row["reason"] == "unbound-check-provenance" {
					assertDetail(row["detail"].(string))
					found = true
				}
			}
			if !found {
				t.Fatal("missing provenance log")
			}
			// Reconstruct the server from the same isolated test journal.
			restarted, err := New(t.Context(), s.store, []Target{w.Target}, s.options)
			must(t, err)
			w = restarted.queues[0]
			must(t, restarted.report(t.Context(), w))
			assertDetail(f.Statuses(repo, 1)[head].Summary)
			must(t, restarted.cycle(t.Context(), w))
			w.cfg.CheckTimeout = time.Nanosecond
			time.Sleep(2 * time.Millisecond)
			must(t, restarted.cycle(t.Context(), w))
			state, _, err = s.store.Load(t.Context(), w.id)
			must(t, err)
			if state.Active != nil || len(state.Entries) != 0 {
				t.Fatalf("batch did not time out: %+v", state.Active)
			}
			got := f.Statuses(repo, 1)[head]
			if got.State != forge.Dequeued || got.Reason != string(queue.ChecksTimeout) {
				t.Fatalf("changed stable timeout outcome: %+v", got)
			}
			assertDetail(got.Summary)
			journals, err = s.store.Journals(t.Context(), []store.QueueKey{w.Key})
			must(t, err)
			_, entry, why = status.Project(journals[0], 1, 1, nil)
			if entry.LastDequeueReason != string(queue.ChecksTimeout) {
				t.Fatalf("API lost timeout reason: %+v", entry)
			}
			assertDetail(why.Summary)
			assertDetail(why.Events[0].Summary)
			found = false
			for _, row := range logRows(t, &output) {
				if row["msg"] == "dequeued" {
					assertDetail(row["detail"].(string))
					found = true
				}
			}
			if !found {
				t.Fatal("missing timeout dequeue log")
			}
		})
	}
}

func TestUnboundCheckProvenanceRecovers(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	f.AddComment(repo, 1, "writer", "@openmerge queue")
	must(t, s.cycle(t.Context(), w))
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	sha, ref, head := state.Active.TestedSHA, state.Active.TestRef, state.Entries[0].HeadSHA
	f.SetCheckRuns(repo, sha, []forge.CheckRun{{Name: "ci", Source: "app:123", State: forge.Success}})
	time.Sleep(2 * time.Millisecond)
	must(t, s.cycle(t.Context(), w))
	for _, checkState := range []forge.CheckState{forge.Pending, forge.Success} {
		f.SetCheckRuns(repo, sha, []forge.CheckRun{
			{ID: 1, Name: "ci", Source: "app:123", State: checkState, HeadBranch: ref},
			{ID: 999, Name: "ci", Source: "app:123", State: forge.Success},
		})
		time.Sleep(2 * time.Millisecond)
		must(t, s.cycle(t.Context(), w))
		got := f.Statuses(repo, 1)[head]
		if got.Reason != "" || strings.Contains(got.Summary, "unbound-check-provenance") {
			t.Fatalf("diagnostic survived bound CI: %+v", got)
		}
		journals, err := s.store.Journals(t.Context(), []store.QueueKey{w.Key})
		must(t, err)
		_, entry, why := status.Project(journals[0], 1, 1, nil)
		if entry.Reason != "" || strings.Contains(why.Summary, "unbound-check-provenance") {
			t.Fatalf("API retained stale diagnostic: %+v", why)
		}
		if checkState == forge.Pending && got.State != forge.Testing || checkState == forge.Success && got.State != forge.Merged {
			t.Fatalf("unexpected recovery state: %+v", got)
		}
	}
}

func TestUnboundCheckProvenanceCoexistsWithAmbiguity(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig+"      - check-success = lint\n")
	f.AddComment(repo, 1, "writer", "@openmerge queue")
	must(t, s.cycle(t.Context(), w))
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	f.SetCheckRuns(repo, state.Active.TestedSHA, []forge.CheckRun{
		{Name: "ci", Source: "app:123", State: forge.Success},
		{Name: "lint", Source: "app:123", State: forge.Success, HeadBranch: state.Active.TestRef},
		{Name: "lint", Source: "app:456", State: forge.Success, HeadBranch: state.Active.TestRef},
	})
	time.Sleep(2 * time.Millisecond)
	must(t, s.cycle(t.Context(), w))
	got := f.Statuses(repo, 1)[state.Entries[0].HeadSHA]
	for _, reason := range []string{"ambiguous-check-sources", "unbound-check-provenance"} {
		if !strings.Contains(got.Reason, reason) {
			t.Fatalf("concurrent diagnostic hidden: %+v", got)
		}
	}
	if !strings.Contains(got.Summary, `required check "ci"`) || !strings.Contains(got.Summary, `required check "lint"`) {
		t.Fatalf("concurrent check details hidden: %+v", got)
	}
}
