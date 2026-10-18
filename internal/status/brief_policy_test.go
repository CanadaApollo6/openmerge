package status

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

type historicalPolicySource struct {
	source
	data     []byte
	err      error
	requests []BriefPolicyRequest
}

func (s *historicalPolicySource) BriefPolicy(_ context.Context, k store.QueueKey, r BriefPolicyRequest) (cp.Policy, error) {
	s.requests = append(s.requests, r)
	if s.err != nil {
		return cp.Policy{}, s.err
	}
	return ReconstructBriefPolicy(s.data, k, r)
}

func policyJournal(t *testing.T, approvals bool) (store.Journal, []byte, cp.Policy, queue.ApprovalBasis) {
	t.Helper()
	k := store.QueueKey{Installation: "7", Repository: "Owner/Repo", BaseBranch: "release"}
	id, _ := k.ChangeID(12)
	raw, err := os.ReadFile("../queue/testdata/wire/v1/event_change_classified.json")
	if err != nil {
		t.Fatal(err)
	}
	event, err := queue.DecodeEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	classified := event.(queue.ChangeClassified)
	c := &classified.Evidence.Classification
	data := []byte("queues:\n  - name: release\n    approvals:\n      default: any_writer\n")
	repository, _, err := cp.ParseCandidate(data)
	if err != nil {
		t.Fatal(err)
	}
	g := classified.Evidence.Governing
	var owners cp.GoverningContent
	for _, f := range g.Codeowners {
		if f.Path == g.SelectedCodeowners {
			owners = cp.GoverningContent{Path: f.Path, Known: true, Present: f.Present, Content: f.Content}
		}
	}
	attrs := []cp.GoverningContent{}
	for _, f := range g.Attributes {
		attrs = append(attrs, cp.GoverningContent{Path: f.Path, Known: true, Present: f.Present, Content: f.Content})
	}
	digest, err := cp.PolicyDigest(cp.DigestInputs{Repository: repository, Queue: repository.Queues[0], Owners: owners, Attributes: attrs, ClassifierVersion: c.Inputs.ClassifierVersion})
	if err != nil {
		t.Fatal(err)
	}
	p := cp.Policy{Digest: digest, Approvals: repository.Queues[0].Approvals}
	if !approvals {
		data = []byte("version: 1\nqueues:\n  - name: release\n    merge_when: [check-success = ci]\n")
		cfg, _, err := config.Load(data)
		if err != nil {
			t.Fatal(err)
		}
		digest = cfg.Queues[0].Digest(true)
		p = cp.Policy{Digest: digest}
	}
	c.Inputs.PolicyDigest = digest
	b := queue.ApprovalBasis{ChangeID: id, Revision: 1, Head: c.Inputs.Head, ReadID: "read-1", At: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
	who := cp.Identity{ID: "github.com:user:1", Login: "author", Kind: cp.HumanAccount}
	configDigest := digest
	if approvals {
		configDigest = strings.Repeat("7", 64)
	} // Effective queue digest is distinct from its semantic policy component.
	events := []queue.Event{
		queue.ConfigApplied{Commit: c.Inputs.GoverningCommit, Digest: configDigest},
		queue.ChangeRevisionObserved{PR: 12, ChangeID: id, Revision: 1, HeadSHA: b.Head, Basis: queue.RevisionObservationBasis{BaseSHA: c.Inputs.Base, At: b.At.Add(-time.Hour)}},
		classified, queue.Accountability(b, who, who, nil, digest), queue.Intent(b, "title", "body"),
		// A newer config must never replace the historical classification policy.
		queue.ConfigApplied{Commit: strings.Repeat("9", 40), Digest: strings.Repeat("8", 64)},
	}
	j := store.Journal{Key: k}
	for _, e := range events {
		j.Events = append(j.Events, store.JournalEvent{Event: e})
	}
	return j, data, p, b
}

func TestBriefHTTPHistoricalPolicyRequiresEffectiveDigest(t *testing.T) {
	for _, approvals := range []bool{true, false} {
		j, data, _, b := policyJournal(t, approvals)
		s := &historicalPolicySource{source: source{journals: []store.Journal{j}}, data: data}
		w := httptest.NewRecorder()
		Handler(s, []store.QueueKey{j.Key}, "", nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1/queues/Owner/Repo/release/changes/"+string(b.ChangeID)+"/brief?audience=agent", nil))
		var v brief.View
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Digest != brief.NotRecorded || v.Obligations != nil || !strings.Contains(w.Body.String(), "policy_unavailable") {
			t.Fatalf("unverifiable effective config accepted: %d %s", w.Code, w.Body)
		}
		if len(s.requests) != 1 || s.requests[0].Config.Commit == strings.Repeat("9", 40) {
			t.Fatal("used current config")
		}
		for _, warning := range v.Warnings {
			if warning.Code == "policy_unavailable" && warning.NextStep.Action == "record_evidence" {
				t.Fatal("incorrect local policy recovery")
			}
		}
	}
}

func TestBriefHTTPPolicyUnavailableAndMismatch(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		j, data, _, b := policyJournal(t, true)
		s := &historicalPolicySource{source: source{journals: []store.Journal{j}}, data: data, err: errors.New("commit missing locally")}
		code := "policy_unavailable"
		if mismatch {
			s.err = nil
			s.data = []byte(strings.ReplaceAll(string(data), "any_writer", "{any_writer: true, count: 2}"))
			code = "policy_digest_mismatch"
		}
		w := httptest.NewRecorder()
		Handler(s, []store.QueueKey{j.Key}, "", nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1/queues/Owner/Repo/release/changes/"+string(b.ChangeID)+"/brief?audience=agent", nil))
		var v brief.View
		if json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Digest != brief.NotRecorded || v.Obligations != nil || !strings.Contains(w.Body.String(), code) {
			t.Fatalf("bad failure %d %s", w.Code, w.Body)
		}
	}
}

func TestBriefPublicLoaderStillRejectsCandidateKeys(t *testing.T) {
	_, data, _, _ := policyJournal(t, true)
	if _, _, err := config.Load(append([]byte("version: 1\n"), data...)); err == nil {
		t.Fatal("public approval keys activated")
	}
}

func TestBriefUnresolvableGroupIsUnavailable(t *testing.T) {
	j, _, _, _ := policyJournal(t, true)
	data := []byte("groups:\n  maintainers: [missing-user]\nqueues:\n  - name: release\n    approvals:\n      default: {group: maintainers}\n")
	req := BriefPolicyRequest{Config: j.Events[0].Event.(queue.ConfigApplied), Evidence: j.Events[2].Event.(queue.ChangeClassified).Evidence}
	_, err := ReconstructBriefPolicy(data, j.Key, req)
	var mismatch queue.BriefPolicyMismatchError
	if err == nil || errors.As(err, &mismatch) {
		t.Fatalf("unresolved identity reported as mismatch: %v", err)
	}
}
