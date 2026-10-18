package status

import (
	"encoding/json"
	"github.com/CanadaApollo6/openmerge/internal/brief"
	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAstraR2SecretAndTrust(t *testing.T) {
	const secret = "opaque-installation-secret-987654"
	j := journal(queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: false}, queue.BlockSourceBlocked{Source: queue.ConfigSource, Detail: "diagnostic " + secret})
	id, _ := j.Key.ChangeID(1)
	v, body := briefHTTPView(t, j, id, diagnostic.NewSanitizer(secret))
	if strings.Contains(body, secret) {
		t.Fatal("secret leak")
	}
	for _, w := range v.Warnings {
		if w.Code == "untrusted_head" && w.Clears != "" && w.NextStep.Action == "trust_exact_head" {
			t.Log("opaque secret redacted; observation-only trust recovery preserved")
			return
		}
	}
	t.Fatal("missing trust recovery")
}
func TestAstraR2EffectiveDigestMismatch(t *testing.T) {
	for _, approvals := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "candidate"}[approvals], func(t *testing.T) {
			j, data, _, b := policyJournal(t, approvals)
			pin := j.Events[0].Event.(queue.ConfigApplied)
			pin.Digest = strings.Repeat("0", 64)
			j.Events[0].Event = pin
			s := &historicalPolicySource{source: source{journals: []store.Journal{j}}, data: data}
			w := httptest.NewRecorder()
			Handler(s, []store.QueueKey{j.Key}, "", nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1/queues/Owner/Repo/release/changes/"+string(b.ChangeID)+"/brief?audience=agent", nil))
			var v brief.View
			if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
				t.Fatal(err)
			}
			if v.Digest != brief.NotRecorded || v.Obligations != nil {
				t.Fatalf("mismatched ConfigApplied.Digest accepted: HTTP=%d digest=%s approvals_required=%v obligations=%d warnings=%+v", w.Code, v.Digest, v.ApprovalsRequired, len(v.Obligations), v.Warnings)
			}
		})
	}
}
func TestAstraR2SupersededCheckHTTP(t *testing.T) {
	old := identified("b1", 1, 1, "failure")
	old.Runs[0].Name = "lint"
	old.Runs[0].URL = "https://ci.example/lint/old"
	newer := identified("b1", 1, 2, "success")
	newer.Runs[0].Name = "lint"
	newer.Runs[0].URL = "https://ci.example/lint/new"
	failing := identified("b1", 3, 1, "failure")
	failing.Runs[0].Name = "unit"
	failing.Runs[0].URL = "https://ci.example/unit/current"
	newer.Success = false
	newer.Runs = append(newer.Runs, failing.Runs...)
	events := append(ledgerChain(), old, queue.CheckRerunRequested{BatchID: "b1", TestedSHA: "tested-b1", Runs: old.Runs, At: at, OriginalDeadline: at.Add(time.Hour), Deadline: at.Add(time.Hour), Timeout: time.Hour}, newer, queue.Dequeued{PR: 1, AdmissionID: 1, Reason: queue.ChecksFailed})
	j := journal(events...)
	id, _ := j.Key.ChangeID(1)
	v, _ := briefHTTPView(t, j, id, nil)
	t.Logf("progress=%+v tests=%+v", v.Progress, v.Tests)
	current := false
	for _, w := range v.Warnings {
		if w.NextStep.Action == "fix_check" {
			t.Logf("repair=%+v", w.NextStep)
			if w.NextStep.Name == "lint" {
				t.Error("superseded passing lint still has current fix_check action")
			}
			if w.NextStep.Name == "unit" {
				current = true
			}
		}
	}
	if !current {
		t.Fatal("reproduction lacks current unit failure")
	}
}

func TestAstraR2HandlerDoesNotMutateJournal(t *testing.T) {
	j, data, _, b := policyJournal(t, true)
	before, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	src := &historicalPolicySource{source: source{journals: []store.Journal{j}}, data: data}
	for _, aud := range []string{"reviewer", "operator", "agent"} {
		w := httptest.NewRecorder()
		Handler(src, []store.QueueKey{j.Key}, "", nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1/queues/Owner/Repo/release/changes/"+string(b.ChangeID)+"/brief?audience="+aud, nil))
		if w.Code != 200 {
			t.Fatalf("HTTP %d", w.Code)
		}
	}
	after, _ := json.Marshal(j)
	if string(before) != string(after) {
		t.Fatal("journal mutated")
	}
	t.Log("three audience requests leave supplied journal byte-identical")
}
