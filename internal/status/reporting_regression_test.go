package status

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func reportingLegacyCostJournal() store.Journal {
	events := []queue.Event{queue.BaseMoved{SHA: "base"}}
	for i := 1; i <= 128; i++ {
		events = append(events, queue.PRObserved{PR: i, HeadSHA: "head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: i, At: at})
	}
	for i := 0; i < 512; i++ {
		events = append(events, queue.RefreshRequested{})
	}
	return journal(events...)
}

// Astra's 128-entry/512-refresh fixture allocated 20.4 MB on main and
// 3.60 GB before this fix. This budget permits at most twice main's bytes.
func TestLegacyReporting128EntriesAllocationBudget(t *testing.T) {
	j := reportingLegacyCostJournal()
	h := Handler(&source{journals: []store.Journal{j}}, []store.QueueKey{key}, "", nil)
	for _, path := range []string{"/v1/queues/owner/repo/main/prs/1", "/v1/queues"} {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		start := time.Now()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		elapsed := time.Since(start)
		runtime.ReadMemStats(&after)
		allocated := after.TotalAlloc - before.TotalAlloc
		t.Logf("%s: %s allocated=%d response=%d", path, elapsed, allocated, w.Body.Len())
		if w.Code != 200 || allocated > 40800000 {
			t.Fatalf("legacy status budget: code=%d allocated=%d", w.Code, allocated)
		}
		var result struct {
			Queue  *Queue  `json:"queue"`
			Queues []Queue `json:"queues"`
			PR     *Entry  `json:"pr"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		q := result.Queue
		if q == nil {
			q = &result.Queues[0]
		}
		if len(q.Entries) != 128 {
			t.Fatalf("entries=%d", len(q.Entries))
		}
		for _, e := range q.Entries {
			if e.Change == nil || e.Change.Evidence != nil {
				t.Fatal("legacy row requires only cheap metadata")
			}
		}
		if result.PR != nil && result.PR.Change.Evidence != nil {
			t.Fatal("legacy PR rendered Stage A brief")
		}
	}
}

func BenchmarkLegacyReporting128(b *testing.B) {
	j := reportingLegacyCostJournal()
	h := Handler(&source{journals: []store.Journal{j}}, []store.QueueKey{key}, "", nil)
	for _, path := range []string{"/v1/queues/owner/repo/main/prs/1", "/v1/queues"} {
		b.Run(path, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
				if w.Code != 200 {
					b.Fatal(w.Code)
				}
			}
		})
	}
}

func TestApprovalWaitOperationalGatePrecedence(t *testing.T) {
	for _, dequeue := range []bool{false, true} {
		t.Run(fmt.Sprint(dequeue), func(t *testing.T) {
			j, _, basis, _ := briefRegressionBase(t)
			j.Seed.BaseSHA = "base"
			briefAppend(&j, queue.EnqueueRequested{PR: 1, At: at})
			briefAppend(&j, queue.BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: []queue.BatchPR{{PR: 1, HeadSHA: basis.Head}}, At: at})
			briefAppend(&j, queue.TestRefBuilt{BatchID: "b1", TestedSHA: "tested", Method: queue.Merge, At: at})
			q, _, _ := Project(j, 0, 0, nil)
			briefAppend(&j, queue.ApprovalGateRejected{BatchID: "b1", TestedSHA: "tested", PRs: q.state.Active.PRs, Waiting: []queue.ApprovalWait{{Basis: basis, Fingerprint: q.state.ApprovalFingerprint(basis.ChangeID), Reasons: []string{"approval-required"}}}})
			want := "invalidation"
			if dequeue {
				briefAppend(&j, queue.BatchesInvalidated{BatchIDs: []string{"b1"}, Reason: "approval-wait"})
				briefAppend(&j, queue.DequeueRequested{PR: 1})
				want = "dequeue"
			}
			q, e, _ := Project(j, 1, 0, nil)
			ApplyPolicy(&q, e, queue.Config{}, at)
			if e.Gate != want || len(e.Change.WaitingReasons) == 0 {
				t.Fatalf("gate=%s waits=%v", e.Gate, e.Change.WaitingReasons)
			}
		})
	}
}

func TestChangeShowAndPRAliasEntryMatch(t *testing.T) {
	j, id, _, _ := briefRegressionBase(t)
	h := Handler(&source{journals: []store.Journal{j}}, []store.QueueKey{key}, "", nil)
	var entries [][]byte
	for _, path := range []string{changePath(key, string(id)), "/v1/queues/owner/repo/main/prs/1"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
		var result struct {
			PR json.RawMessage `json:"pr"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, result.PR)
	}
	if string(entries[0]) != string(entries[1]) {
		t.Fatalf("change and alias entries differ:\n%s\n%s", entries[0], entries[1])
	}
}

func TestReviewerCheckReceiptsWaitingAndWarningGolden(t *testing.T) {
	e := &Entry{Change: &ChangeReport{WaitingReasons: []string{strings.Repeat("<tag>*[bad]\n", 100)}}}
	for i := 0; i < 20; i++ {
		e.Change.WaitingReasons = append(e.Change.WaitingReasons, "approval-required")
	}
	v := brief.View{Digest: brief.NotRecorded, Receipts: []brief.ReceiptView{{SourceID: "receipt-1", Accepted: true, Binding: "current"}, {SourceID: "receipt-2", Binding: "rejected"}}}
	for i := 0; i < 50; i++ {
		v.Warnings = append(v.Warnings, brief.Warning{Code: "policy_unavailable", Detail: strings.Repeat("<warning>*", 1000)})
	}
	text := reviewerCheckText(key, "change", v, e)
	if len(text) > 12050 || strings.Contains(text, "<tag>") || strings.Contains(text, "receipt: not recorded") || !strings.Contains(text, "+42 more warnings") || !strings.Contains(text, "+13 more") {
		t.Fatal("unsafe check", text)
	}
	if strings.Index(text, "WARNING:") > strings.Index(text, "Accountability:") {
		t.Fatal("warning ordering")
	}
	reportingGolden(t, "reviewer-check-bounds.txt", []byte(text))
}

func TestChangeShowWithoutRevisionEvidenceIsNotFound(t *testing.T) {
	j := journal(queue.EnqueueRequested{PR: 1, At: at})
	id, _ := key.ChangeID(1)
	h := Handler(&source{journals: []store.Journal{j}}, []store.QueueKey{key}, "", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", changePath(key, string(id)), nil))
	if w.Code != 404 {
		t.Fatal(w.Code, w.Body)
	}
}

func TestReviewerCheckWarningsSurviveOversizedBriefURL(t *testing.T) {
	hugeKey := key
	hugeKey.BaseBranch = strings.Repeat("branch/", 12000)
	v := brief.View{Digest: brief.NotRecorded, Warnings: []brief.Warning{{Code: "policy_unavailable", Detail: "policy reconstruction failed"}}}
	text := reviewerCheckText(hugeKey, "change", v, nil)
	for _, want := range []string{"WARNING: classification/inventory not recorded", "WARNING: policy\\_unavailable", "Reviewer brief: URL exceeds check display bound", "Digest: not recorded"} {
		if !strings.Contains(text, want) {
			t.Fatalf("lost warning %q in bounded output: %s", want, text)
		}
	}
	if len(text) > 12050 {
		t.Fatal("check exceeds bound", len(text))
	}
}

func TestReviewerCheckUsesCurrentOperationalGate(t *testing.T) {
	q, entry, _ := Project(journal(admission()...), 1, 0, nil)
	ApplyPolicy(&q, entry, queue.Config{SpeculationMaxDepth: 4, BatchMaxSize: 8, BatchMaxWait: time.Hour}, at.Add(time.Minute))
	if entry.Gate != "max_wait" {
		t.Fatalf("gate=%s", entry.Gate)
	}
	text := reviewerCheckText(key, "change", brief.View{}, entry)
	if !strings.Contains(text, "gate: max\\_wait") {
		t.Fatalf("check lost applied gate: %s", text)
	}
}

func TestReviewerCheckBriefURLSurvivesRowTruncation(t *testing.T) {
	v := brief.View{Digest: strings.Repeat("a", 64), Classification: &brief.ClassificationView{}}
	for range 8 {
		v.Classification.Labels = append(v.Classification.Labels, brief.LabelView{Name: strings.Repeat("&", 80), Reasons: []string{strings.Repeat("&", 180)}})
		v.Obligations = append(v.Obligations, brief.ObligationView{Path: strings.Repeat("&", 180), Selector: strings.Repeat("&", 80)})
	}
	longKey := key
	longKey.BaseBranch = strings.Repeat("b", 3500)
	for range 8 {
		v.Warnings = append(v.Warnings, brief.Warning{Code: strings.Repeat("\"", 80), Detail: strings.Repeat("\"", 100), Clears: strings.Repeat("\"", 100), NextStep: brief.Step{Action: strings.Repeat("\"", 80)}})
	}
	text := reviewerCheckText(longKey, "change", v, nil)
	url := changePath(longKey, "change") + "/brief?audience=reviewer"
	if !strings.Contains(text, "+more evidence in full brief") || !strings.Contains(text, "Reviewer brief: "+url+"\nDigest: "+v.Digest) || len(text) > 12050 {
		t.Fatal("lost brief URL/digest or failed to bound rows", text)
	}
	if strings.Index(text, "WARNING:") > strings.Index(text, "Reviewer brief:") {
		t.Fatal("warnings must come first")
	}
}
