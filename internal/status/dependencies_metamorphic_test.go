package status

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest/statusfixture"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestDependencyMetamorphicPrivacy(t *testing.T) {
	seeds := 5
	if !testing.Short() {
		seeds = 40
	}
	for _, depth := range []int{1, 4} {
		for seed := 0; seed < seeds; seed++ {
			t.Run(fmt.Sprintf("depth-%d/seed-%d", depth, seed), func(t *testing.T) {
				baseline := map[string][]byte{}
				for world := 0; world < 7; world++ {
					j, b, prs := statusfixture.MetamorphicJournal(t, int64(seed), depth, world)
					j, err := ProjectDependencies(j, b, nil, statusfixture.StatusSources())
					if err != nil {
						t.Fatal(err)
					}
					h := Handler(dependencyRenderSource{journal: j}, []store.QueueKey{j.Key}, "", nil)
					paths := []string{"/v1/queues", "/v1/queues/acme/client/main"}
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
						got := append([]byte(fmt.Sprintf("%d\n", w.Code)), w.Body.Bytes()...)
						if want, ok := baseline[path]; ok {
							if !bytes.Equal(want, got) {
								t.Fatalf("world %d %s differs:\n%s\n%s", world, path, want, got)
							}
						} else {
							baseline[path] = bytes.Clone(got)
						}
					}
				}
			})
		}
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
