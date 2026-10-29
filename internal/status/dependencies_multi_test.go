package status

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest/statusfixture"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestDependencyMultiQueuePrivacyGoldens(t *testing.T) {
	codes := []string{"missing", "dependency-unresolved", "dependency-waiting", "dependency-closed", "dependency-stale", "dependency-landed", "dependency-unavailable", "dependency-out-of-scope"}
	for _, depth := range []int{1, 4} {
		for _, cobatch := range []bool{false, true} {
			t.Run(fmt.Sprintf("depth-%d/cobatch-%t", depth, cobatch), func(t *testing.T) {
				baseline := map[string][]byte{}
				for _, code := range codes {
					for _, phase := range []string{"queued", "building", "testing", "invalidating", "discarded"} {
						t.Run(code+"/"+phase, func(t *testing.T) {
							j, b, ref, g, prs := statusfixture.MultiJournal(t, code, phase, depth, cobatch)
							denied := g
							denied.Allowed = false
							for _, audience := range [][]dependency.Grant{nil, {denied}} {
								pj, err := ProjectDependencies(j, b, map[string][]dependency.Grant{ref: audience}, statusfixture.StatusSources())
								if err != nil {
									t.Fatal(err)
								}
								h := Handler(dependencyRenderSource{journal: pj}, []store.QueueKey{j.Key}, "", nil)
								equal := func(key string, raw []byte) {
									t.Helper()
									if want, ok := baseline[key]; ok {
										if !bytes.Equal(want, raw) {
											t.Fatalf("%s leaked private %s/%s:\nwant %s\ngot %s", key, code, phase, want, raw)
										}
									} else {
										baseline[key] = bytes.Clone(raw)
									}
								}
								paths := []string{"/v1/queues", "/v1/queues/acme/client/main"}
								for _, pr := range prs {
									id, err := j.Key.ChangeID(pr)
									if err != nil {
										t.Fatal(err)
									}
									cp := changePath(j.Key, string(id))
									paths = append(paths, fmt.Sprintf("/v1/queues/acme/client/main/prs/%d?events=100", pr), cp, cp+"?revision=1", cp+"/decisions?limit=1&after=0")
									for _, a := range []string{"reviewer", "agent", "operator"} {
										paths = append(paths, cp+"/brief?audience="+a, cp+"/brief?audience="+a+"&revision=1")
									}
									q, e, why := Project(pj, pr, 100, nil)
									if e == nil {
										t.Fatal("missing entry", pr)
									}
									ApplyPolicy(&q, e, queue.Config{MergeMethod: queue.Merge, SpeculationMaxDepth: depth}, j.Events[0].RecordedAt)
									raw, err := json.Marshal(Response{Version: Version, Queue: &q, PR: e, Why: why})
									if err != nil {
										t.Fatal(err)
									}
									equal(fmt.Sprintf("policy/pr-%d", pr), raw)
									check := DependencyCheckStatus(&q, e, forge.Status{State: forge.Testing, Reason: phase, Summary: code})
									check.Summary += "\n\nCurrent state: " + WaitingCheckText(&q, *e, nil) + LedgerSummary(e) + "\n\nQueue: " + AdaptiveSummary(&q) + QueueInstructions(&q)
									raw, err = json.Marshal(check)
									if err != nil {
										t.Fatal(err)
									}
									equal(fmt.Sprintf("check/pr-%d", pr), raw)
									ledger := q.LedgerEntry(pr)
									if ledger == nil {
										t.Fatal("missing ledger")
									}
									equal(fmt.Sprintf("ledger/pr-%d", pr), []byte(WaitingCheckText(&q, *ledger, nil)+LedgerSummary(ledger)))
									if pr == 2 {
										v, found, err := projectBrief(pj, string(id), "reviewer")
										if err != nil || !found {
											t.Fatal(found, err)
										}
										raw, _ := json.Marshal(v)
										if !bytes.Contains(raw, []byte(`"code":"dependencies"`)) {
											t.Fatal("current brief omitted dependency warning", string(raw))
										}
										if !strings.Contains(why.Summary, "Dependency cannot be verified") {
											t.Fatal("inconsistent explanation", why.Summary)
										}
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
										t.Fatalf("%s: %d %s", path, w.Code, w.Body)
									}
									equal(path, append([]byte(fmt.Sprintf("%d\n", w.Code)), w.Body.Bytes()...))
								}
							}
						})
					}
				}
				digests := map[string]string{}
				for key, raw := range baseline {
					digests[key] = fmt.Sprintf("%x", sha256.Sum256(raw))
				}
				raw, err := json.Marshal(digests)
				if err != nil {
					t.Fatal(err)
				}
				reportingGolden(t, fmt.Sprintf("dependency-private-multi-depth-%d-cobatch-%t.json", depth, cobatch), raw)
			})
		}
	}
}

func TestDependencyNotQueuedPrecedesDependencyHold(t *testing.T) {
	j, b, _, _ := statusfixture.StatusJournal(t, "dependency-closed", 1)
	j, err := ProjectDependencies(j, b, nil, statusfixture.StatusSources())
	if err != nil {
		t.Fatal(err)
	}
	q, e, _ := Project(j, 2, 0, nil)
	e.Position = 0
	e.Status = "not-queued"
	if got := DeriveWaitingReason(&q, *e); got.Code != WaitingOperator {
		t.Fatal(got)
	}
}

func TestDependencyLedgerAttachesCurrentEvidence(t *testing.T) {
	j, b, ref, g := statusfixture.StatusJournal(t, "dependency-closed", 1)
	j, err := ProjectDependencies(j, b, map[string][]dependency.Grant{ref: {g}}, statusfixture.StatusSources())
	if err != nil {
		t.Fatal(err)
	}
	q, _, _ := Project(j, 0, 0, nil)
	// Ledger fallback is also used for observed changes outside the admitted queue.
	q.Entries[0].Dependency = nil
	attached := q.LedgerEntry(2)
	if attached == nil || attached.Dependency == nil {
		t.Fatal("admitted ledger omitted dependency evidence", attached)
	}
	q.Entries = nil
	e := q.known[2]
	e.Dependency = nil
	e.dependencyWaiting = false
	q.known[2] = e
	got := q.LedgerEntry(2)
	if got == nil || got.Dependency == nil || len(got.Dependency.Diagnostics) != 1 || got.Dependency.Diagnostics[0].Code != "dependency-closed" {
		t.Fatal(got)
	}
}

// Independent transitions must reveal the same response regardless of private progress.
func TestDependencyIndependentTransitionPrivacy(t *testing.T) {
	for _, depth := range []int{1, 4} {
		for _, transition := range []string{"head-changed", "closed", "dequeued", "retargeted"} {
			t.Run(fmt.Sprintf("depth-%d/%s", depth, transition), func(t *testing.T) {
				var baseline []byte
				for _, phase := range []string{"queued", "building", "testing", "invalidating", "discarded"} {
					j, b, _, _, _ := statusfixture.MultiJournal(t, "dependency-landed", phase, depth, true)
					var event queue.Event
					switch transition {
					case "head-changed":
						event = queue.PRObserved{PR: 3, HeadSHA: strings.Repeat("e", 40), Eligible: true, Trusted: true}
					case "closed":
						event = queue.PRClosed{PR: 3}
					case "dequeued":
						event = queue.DequeueRequested{PR: 3}
					case "retargeted":
						event = queue.PRObserved{PR: 3, HeadSHA: fmt.Sprintf("%040x", 13), Eligible: false, Trusted: true}
					}
					_, payload, err := queue.EncodeEvent(event)
					if err != nil {
						t.Fatal(err)
					}
					j.Events = append(j.Events, store.JournalEvent{ID: int64(len(j.Events) + 1), RecordedAt: j.Events[0].RecordedAt, Payload: payload, Event: event})
					j, err = ProjectDependencies(j, b, nil, statusfixture.StatusSources())
					if err != nil {
						t.Fatal(err)
					}
					q, e, _ := Project(j, 3, 0, nil)
					raw, err := json.Marshal(Response{Version: Version, Queue: &q, PR: e})
					if err != nil {
						t.Fatal(err)
					}
					if baseline == nil {
						baseline = raw
					} else if !bytes.Equal(baseline, raw) {
						t.Fatalf("%s leaked: want %s got %s", phase, baseline, raw)
					}
				}
			})
		}
	}
}
