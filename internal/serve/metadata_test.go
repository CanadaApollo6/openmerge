package serve

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestMetadataConditionsKeepUnknownUnderNegationAndCounts(t *testing.T) {
	for _, text := range []string{"label != blocked", "-label = blocked", "#label = 0", "files glob src/**", "files != secret", "#files = 0", "-conflict", "conflict = false"} {
		expression, err := condition.Parse(text)
		must(t, err)
		terminal, met, err := mergeResult(expression, forge.PullRequest{}, nil)
		must(t, err)
		if terminal || met {
			t.Errorf("missing metadata passed or became false: %s", text)
		}
		terminal, met, err = mergeResult(condition.Not(expression), forge.PullRequest{}, nil)
		must(t, err)
		if terminal || met {
			t.Errorf("NOT turned missing metadata into a decision: %s", text)
		}
	}
	pr := forge.PullRequest{LabelsKnown: true, FilesKnown: true, ConflictKnown: true}
	for _, text := range []string{"#label = 0", "files != secret", "-conflict"} {
		expression, err := condition.Parse(text)
		must(t, err)
		met, err := evaluate(expression, pr, nil)
		must(t, err)
		if !met {
			t.Errorf("observed empty/false was treated as unknown: %s", text)
		}
	}
}

func TestCoreConditionsHaveDurableInputsAndReconcileMergeability(t *testing.T) {
	cfg := strings.Replace(repositoryConfig, "    merge_when:", "    enter_when: [base = main, author = contributor, label = ready, files glob pr-*.txt, '-draft', '-conflict', '#approved-reviews-by >= 1']\n    merge_when:", 1)
	s, w, f, d := fixture(t, cfg)
	pr, err := f.PullRequest(t.Context(), repo, 1)
	must(t, err)
	must(t, f.Approve(repo, 1, forge.Approval{User: "writer", SHA: pr.HeadSHA}))
	must(t, f.SetPRConditions(repo, 1, []string{"ready"}, nil))
	must(t, s.reconcile(t.Context(), w, false))
	_, before, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	pulls := d.pulls
	// An unresolved GitHub mergeability value is polled even without an updated
	// summary. Merely re-reading unknown data does not append duplicate events.
	must(t, s.reconcile(t.Context(), w, false))
	_, after, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if d.pulls != pulls+1 || after != before {
		t.Fatal("unknown mergeability was not retried or duplicated unchanged evidence")
	}
	must(t, f.SetPRConditions(repo, 1, []string{"ready"}, new(false)))
	pr, err = s.observe(t.Context(), w, 1)
	must(t, err)
	met, _, err := s.entryEligibility(t.Context(), w, pr)
	must(t, err)
	if !met {
		t.Fatal("complete core metadata did not satisfy entry conditions")
	}
	records, err := s.store.EventsAfter(t.Context(), w.id, before)
	must(t, err)
	var metadata *queue.PRMetadataObserved
	for _, record := range records {
		if value, ok := record.Event.(queue.PRMetadataObserved); ok {
			metadata = &value
		}
	}
	if metadata == nil || !metadata.FilesKnown || !metadata.LabelsKnown || !metadata.ConflictKnown || metadata.Conflict || !reflect.DeepEqual(metadata.Files, []string{"pr-1.txt"}) || !reflect.DeepEqual(metadata.Labels, []string{"ready"}) || !reflect.DeepEqual(metadata.ApprovedReviewsBy, []string{"writer"}) {
		t.Fatalf("incomplete durable condition inputs: %+v", metadata)
	}
	must(t, f.SetPRConditions(repo, 1, []string{}, new(false)))
	pr, err = s.observe(t.Context(), w, 1)
	must(t, err)
	met, _, err = s.entryEligibility(t.Context(), w, pr)
	must(t, err)
	if met {
		t.Fatal("label removal did not revoke eligibility")
	}
}

func TestOversizedFakePRKeepsAllFilePredicatesUnknown(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	base, err := f.ReadRef(t.Context(), repo, "main")
	must(t, err)
	files := map[string]string{}
	for i := range forge.MaxChangedFiles + 1 {
		files[fmt.Sprintf("src/%04d.go", i)] = "package example"
	}
	_, err = f.OpenPRWithFiles(repo, 2, base, true, files)
	must(t, err)
	pr, err := s.observe(t.Context(), w, 2)
	must(t, err)
	if pr.FilesKnown || len(pr.Files) != 0 {
		t.Fatal("oversized fake PR exposed a partial file list")
	}
	for _, text := range []string{"files glob src/**", "files != secret", "#files > 0"} {
		expression, err := condition.Parse(text)
		must(t, err)
		terminal, met, err := mergeResult(expression, pr, nil)
		must(t, err)
		if terminal || met {
			t.Errorf("truncated file predicate made a decision: %s", text)
		}
	}
}

func TestMergeMetadataRevalidatedBeforeLanding(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		cfg := strings.Replace(repositoryConfig, "      - check-success = ci", "      - check-success = ci\n      - label = ready\n      - files glob pr-*.txt\n      - -conflict", 1)
		s, w, f, _ := fixture(t, cfg)
		must(t, f.SetPRConditions(repo, 1, []string{"ready"}, new(false)))
		f.AddComment(repo, 1, "writer", "@openmerge queue")
		must(t, s.cycle(t.Context(), w))
		state, _, err := s.store.Load(t.Context(), w.id)
		must(t, err)
		if state.Active == nil || state.Active.TestedSHA == "" {
			t.Fatal("metadata-gated PR did not start testing")
		}
		tested := state.Active.TestedSHA
		f.SetCheckRuns(repo, tested, []forge.CheckRun{{ID: 1, Name: "ci", State: forge.Success}})
		// Leave the worker's summaries/cache unchanged: the fresh landing read must
		// discover a label removed after the passing policy snapshot was collected.
		gate := &labelRevocationDriver{Driver: w.Driver, revoke: func() { must(t, f.SetPRConditions(repo, 1, []string{}, new(false))) }}
		w.Driver = gate
		time.Sleep(2 * time.Millisecond)
		must(t, s.cycle(t.Context(), w))
		base, err := f.ReadRef(t.Context(), repo, "main")
		must(t, err)
		if base == tested {
			t.Fatal("stale metadata authorized landing after label revocation")
		}
	})
}

type labelRevocationDriver struct {
	forge.Driver
	revoke func()
}

func (d *labelRevocationDriver) CheckRuns(ctx context.Context, repo forge.RepoRef, sha string) ([]forge.CheckRun, error) {
	checks, err := d.Driver.CheckRuns(ctx, repo, sha)
	if d.revoke != nil {
		d.revoke()
		d.revoke = nil
	}
	return checks, err
}

type selectiveMetadataDriver struct {
	forge.Driver
	metadata, full int
}

func (d *selectiveMetadataDriver) PullRequestMetadata(ctx context.Context, repo forge.RepoRef, number int) (forge.PullRequest, error) {
	d.metadata++
	pr, err := d.Driver.PullRequest(ctx, repo, number)
	pr.Files, pr.FilesKnown = nil, false
	return pr, err
}

func (d *selectiveMetadataDriver) PullRequest(ctx context.Context, repo forge.RepoRef, number int) (forge.PullRequest, error) {
	d.full++
	return d.Driver.PullRequest(ctx, repo, number)
}

func TestObserveRequestsFilesOnlyForConditionsThatUseThem(t *testing.T) {
	for _, tc := range []struct {
		name, enter, merge string
		files              bool
	}{
		{"unused", "-draft", "check-success = ci", false},
		{"entry exclusion", "files != secret", "check-success = ci", true},
		{"merge count", "-draft", "#files > 0", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, w, _, _ := fixture(t, repositoryConfig)
			var err error
			w.cfg.EnterWhen, err = condition.Parse(tc.enter)
			must(t, err)
			w.cfg.MergeWhen, err = condition.Parse(tc.merge)
			must(t, err)
			d := &selectiveMetadataDriver{Driver: w.Driver}
			w.Driver = d
			pr, err := s.observe(t.Context(), w, 1)
			must(t, err)
			if pr.FilesKnown != tc.files || (d.full == 1) != tc.files || (d.metadata == 1) == tc.files {
				t.Fatalf("file observation not gated by policy: full=%d metadata=%d pr=%+v", d.full, d.metadata, pr)
			}
		})
	}
}
