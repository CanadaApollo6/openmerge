package status

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/audit"

	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest/statusfixture"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// metamorphicPolicySource applies the queue window so derived holding gates
// are rendered exactly as a configured server would.
type metamorphicPolicySource struct {
	dependencyRenderSource
	cfg queue.Config
}

func (s metamorphicPolicySource) QueuePolicy(store.QueueKey) (queue.Config, bool) { return s.cfg, true }

func TestDependencyMetamorphicPrivacy(t *testing.T) {
	for _, c := range statusfixture.MetamorphicCases(testing.Short()) {
		t.Run(c.String(), func(t *testing.T) {
			baseline := map[string][]byte{}
			var worlds []statusfixture.MetamorphicWorld
			for _, world := range statusfixture.MetamorphicWorlds(testing.Short()) {
				m := statusfixture.Metamorphic(t, c, world)
				worlds = append(worlds, m)
				// Any authorized wait this old would be flagged; the unauthorized
				// audience must never see it.
				j, err := ProjectDependenciesAt(m.Journal, m.Binding, nil, statusfixture.StatusSources(), metamorphicClock(m.Journal))
				if err != nil {
					t.Fatal(err)
				}
				h := Handler(metamorphicPolicySource{dependencyRenderSource{journal: j}, statusfixture.MetamorphicPolicy(c.Depth)}, []store.QueueKey{j.Key}, "", nil)
				paths := []string{"/v1/queues", "/v1/queues/acme/client/main"}
				outputs := metamorphicExport(t, m)
				prs := m.PRs
				if m.SelfAckPR != 0 {
					prs = append(slices.Clone(prs), m.SelfAckPR)
					requireSelfAckView(t, h, m.SelfAckPR)
				}
				for _, pr := range prs {
					id, err := j.Key.ChangeID(pr)
					if err != nil {
						t.Fatal(err)
					}
					cp := changePath(j.Key, string(id))
					paths = append(paths, fmt.Sprintf("/v1/queues/acme/client/main/prs/%d?events=100", pr), cp, cp+"?revision=1", cp+"/decisions?limit=1&after=0")
					for _, audience := range []string{"reviewer", "agent", "operator"} {
						paths = append(paths, cp+"/brief?audience="+audience, cp+"/brief?audience="+audience+"&revision=1")
					}
				}
				for _, path := range paths {
					w := httptest.NewRecorder()
					h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
					wantCode := 200
					if strings.Contains(path, "/decisions") {
						wantCode = 503
					}
					if w.Code != wantCode {
						t.Fatalf("%s returned %d: %s", path, w.Code, w.Body)
					}
					outputs[path] = append([]byte(fmt.Sprintf("%d\n", w.Code)), w.Body.Bytes()...)
				}
				for _, path := range slices.Sorted(maps.Keys(outputs)) {
					got := outputs[path]
					key := m.Group + " " + path
					if !m.Compared() {
						continue
					}
					if want, ok := baseline[key]; ok {
						if !bytes.Equal(want, got) {
							t.Fatalf("world %d %s differs %s", world, path, statusfixture.Diff(want, got))
						}
					} else {
						baseline[key] = bytes.Clone(got)
					}
				}
			}
			statusfixture.RequireComparedWorlds(t, c, worlds)
		})
	}
}

// metamorphicClock flags every authorized wait older than a minute.
func metamorphicClock(j store.Journal) DependencyOptions {
	return DependencyOptions{Now: j.Events[0].RecordedAt.Add(48 * time.Hour), WaitAge: time.Minute}
}

// metamorphicExport renders the unauthorized scoped export and every cursor
// page of it. A private-only tail must not change a byte.
func metamorphicExport(t *testing.T, m statusfixture.MetamorphicWorld) map[string][]byte {
	t.Helper()
	manifest, stream, err := DependencyExportBytes(m.Journal, m.Binding, nil, statusfixture.StatusSources())
	if err != nil {
		t.Fatal(err)
	}
	tail := m.Journal
	tail.Events = append(slices.Clone(tail.Events), m.Tail...)
	tailManifest, tailStream, err := DependencyExportBytes(tail, m.Binding, nil, statusfixture.StatusSources())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(manifest, tailManifest) || !bytes.Equal(stream, tailStream) {
		t.Fatalf("private tail changed the export %s", statusfixture.Diff(stream, tailStream))
	}
	out := map[string][]byte{"export:manifest": manifest, "export:records": stream}
	records, err := audit.DecodeDependencyExport(manifest, stream)
	if err != nil {
		t.Fatalf("export does not verify: %v", err)
	}
	for _, limit := range []int{1, 2, 100} {
		var pages []audit.DependencyPage
		for after := int64(0); ; {
			page, err := audit.PageDependencyRecords(records, after, limit)
			if err != nil {
				t.Fatal(err)
			}
			pages = append(pages, page)
			if page.Next == 0 {
				break
			}
			after = page.Next
		}
		raw, err := json.Marshal(pages)
		if err != nil {
			t.Fatal(err)
		}
		out[fmt.Sprintf("export:pages:%d", limit)] = raw
	}
	return out
}

// requireSelfAckView keeps the self-acknowledgement cases non-vacuous: the
// distinct status and its nonblocking warning render for the unauthorized
// audience. Byte comparison, not this check, catches a leaked journal reference.
func requireSelfAckView(t *testing.T, h http.Handler, pr int) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", fmt.Sprintf("/v1/queues/acme/client/main/prs/%d", pr), nil))
	var body struct {
		PR struct {
			Dependency *WaitingReason `json:"dependency"`
		} `json:"pr"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	d := body.PR.Dependency
	if w.Code != 200 || d == nil || d.SelfAcknowledgement == nil || d.SelfAcknowledgement.Actor != "11" || len(d.Warnings) != 1 {
		t.Fatalf("self-acknowledgement view: %d %+v", w.Code, d)
	}
}

func TestDependencyEntryAllowlist(t *testing.T) {
	for _, status := range []string{"not-queued", "blocked", "waiting-entry", "queued", "building", "testing", "invalidating", "dequeue-pending", "paused", "landing", "merged", "closed", "dequeued"} {
		t.Run(status, func(t *testing.T) {
			ahead := 3
			e := Entry{PR: 3, HeadSHA: "public", Status: status, Reason: "public", Detail: "batch-private", BatchID: "private", BatchPosition: 4, BatchesAhead: &ahead, RunLedger: []Run{{BatchID: "private"}}, Landing: &LandingView{Outcome: "landed", SHA: "public", BatchID: "private", AttemptID: "private"}}
			maskDependencyEntry(&e)
			if e.BatchID != "" || e.BatchPosition != 0 || e.BatchesAhead != nil || len(e.RunLedger) != 0 || e.Landing.BatchID != "" || e.Landing.AttemptID != "" {
				t.Fatalf("batch references survived: %+v", e)
			}
			if status != "waiting-entry" && e.Detail != "" {
				t.Fatalf("batch detail survived: %+v", e)
			}
			if e.Landing.Outcome != "landed" || e.Landing.SHA != "public" {
				t.Fatal("lost public settlement", e)
			}
		})
	}
}

func TestDependencyPendingCheckKeepsReason(t *testing.T) {
	q := Queue{dependencyRedacted: true}
	e := Entry{Status: "dequeue-pending", Reason: "head-changed", Position: 1}
	got := DependencyCheckStatus(&q, &e, forge.Status{State: forge.Testing, Summary: "Batch b1"})
	if got.State != forge.Queued || got.Reason != "head-changed" || strings.Contains(got.Summary, "b1") {
		t.Fatal(got)
	}
}

func TestDependencyUnconfirmedLandingBatchPrivacy(t *testing.T) {
	for _, phase := range []string{"building", "testing", "invalidating", "landing"} {
		t.Run(phase, func(t *testing.T) {
			batch := &Batch{ID: "private", Phase: phase, TestRef: "private-ref", TestedSHA: "private-tested", LandingSHA: "private-landing", PRs: []BatchPR{{PR: 2, HeadSHA: "private-head"}}}
			q := Queue{Chain: []*Batch{batch}, ActiveBatch: batch, Invalidations: []queue.BatchesInvalidated{{Reason: "change-left-queue"}}}
			maskDependencyBatches(&q, store.Journal{DependencyRedacted: true})
			if q.ActiveBatch != nil || len(q.Chain) != 0 || len(q.Invalidations) != 0 {
				t.Fatalf("private batch presentation survived: %+v", q)
			}
			q = Queue{Chain: []*Batch{batch}, ActiveBatch: batch}
			maskDependencyBatches(&q, store.Journal{})
			if q.ActiveBatch != batch || len(q.Chain) != 1 {
				t.Fatal("inactive presentation changed")
			}
		})
	}
}
