package status

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestR4TimeoutRerun(t *testing.T) {
	for _, rerun := range []bool{false, true} {
		old := identified("b1", 1, 1, "failure")
		old.Runs[0].Name = "lint"
		events := append(ledgerChain(), old, queue.BatchFailed{BatchID: "b1", Reason: queue.ChecksTimeout}, queue.Dequeued{PR: 1, AdmissionID: 1, Reason: queue.ChecksTimeout})
		if rerun {
			newer := identified("b1", 2, 1, "success")
			newer.Runs[0].Name = "lint"
			events = append(events, newer)
		}
		j := journal(events...)
		id, _ := j.Key.ChangeID(1)
		v, _ := briefHTTPView(t, j, id, nil)
		repairs := 0
		for _, w := range v.Warnings {
			if w.NextStep.Action == "fix_check" && w.NextStep.Name == "lint" {
				repairs++
			}
		}
		if rerun && repairs != 0 || !rerun && repairs != 2 {
			t.Fatalf("rerun=%t repairs=%d warnings=%+v", rerun, repairs, v.Warnings)
		}
		if rerun && (len(v.Tests) != 2 || v.Tests[0].Status != "failure" || v.Tests[1].Status != "success") {
			t.Fatalf("history lost: %+v", v.Tests)
		}
	}
}

func TestR4SupersessionIndependentOfWaste(t *testing.T) {
	for _, reason := range []string{"", "rerun", "timeout", "checks-failed", "checks-timeout", "confirmed failure (bisection)", "base-moved", "config-changed", "dequeue", "closed", "superseded by a new head", "eligibility or trust changed", "cancelled: timeout"} {
		t.Run(reason, func(t *testing.T) {
			for _, sameID := range []bool{false, true} {
				old := Run{BatchID: "b1", Identity: &queue.CheckRun{ID: 1, Attempt: 1}, WastedReason: reason, producerKey: "lint"}
				newer := Run{BatchID: "b1", Identity: &queue.CheckRun{ID: 2, Attempt: 1}, WastedReason: reason, producerKey: "lint"}
				if sameID {
					newer.Identity.ID = 1
					newer.Identity.Attempt = 2
				}
				for _, runs := range [][]Run{{old, newer}, {newer, old}} {
					if currentBriefRun(old, runs) {
						t.Fatal("superseded attempt current")
					}
				}
			}
		})
	}
}

func TestR4VersionedApprovalRecovery(t *testing.T) {
	j, data, _, b := policyJournal(t, true)
	data = append([]byte("version: 1\n"), data...)
	src := &historicalPolicySource{source: source{journals: []store.Journal{j}}, data: data}
	w := httptest.NewRecorder()
	Handler(src, []store.QueueKey{j.Key}, "", nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1/queues/Owner/Repo/release/changes/"+string(b.ChangeID)+"/brief?audience=operator", nil))
	if !strings.Contains(w.Body.String(), "implement_11b_policy_composition") || strings.Contains(w.Body.String(), "restore_recorded_policy") {
		t.Fatal(w.Body.String())
	}
}

func TestR4CheckStepNameBound(t *testing.T) {
	const secret = "opaque-installation-secret-987654"
	old := identified("b1", 1, 1, "failure")
	old.Runs[0].Name = strings.Repeat("&", 336) + secret + strings.Repeat("x", 4096)
	j := journal(append(ledgerChain(), old, queue.Dequeued{PR: 1, AdmissionID: 1, Reason: queue.ChecksFailed})...)
	id, _ := j.Key.ChangeID(1)
	v, body := briefHTTPView(t, j, id, diagnostic.NewSanitizer(secret))
	if strings.Contains(body, "opaque-installation-") {
		t.Fatal("secret leaked")
	}
	found := false
	for _, w := range v.Warnings {
		if w.NextStep.Action == "fix_check" {
			found = true
			raw, _ := json.Marshal(w.NextStep.Name)
			if len(raw) > 2050 {
				t.Fatalf("unbounded name: %d", len(raw))
			}
		}
	}
	if !found || !v.Truncated {
		t.Fatalf("missing bounded repair: %+v", v)
	}
}

func TestR4UnknownDefaultBranchHTTP(t *testing.T) {
	j, data, _, b := policyJournal(t, false)
	unknown := ""
	src := &historicalPolicySource{source: source{journals: []store.Journal{j}}, data: data, defaultBranch: &unknown}
	w := httptest.NewRecorder()
	Handler(src, []store.QueueKey{j.Key}, "", nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1/queues/Owner/Repo/release/changes/"+string(b.ChangeID)+"/brief?audience=agent", nil))
	var v brief.View
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Digest != brief.NotRecorded || v.Obligations != nil || !strings.Contains(w.Body.String(), "policy_unavailable") {
		t.Fatal(w.Body.String())
	}
}
