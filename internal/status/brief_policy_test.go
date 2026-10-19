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
	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

type historicalPolicySource struct {
	source
	data          []byte
	err           error
	requests      []BriefPolicyRequest
	defaultBranch *string
	governing     *forge.GoverningFiles
}

func (s *historicalPolicySource) BriefPolicy(_ context.Context, k store.QueueKey, r BriefPolicyRequest) (cp.Policy, error) {
	s.requests = append(s.requests, r)
	if s.err != nil {
		return cp.Policy{}, s.err
	}
	defaultBranch := k.BaseBranch
	if s.defaultBranch != nil {
		defaultBranch = *s.defaultBranch
	}
	r.Governing = s.governing
	return ReconstructBriefPolicy(s.data, k, defaultBranch, r)
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
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || (approvals && (v.Digest != brief.NotRecorded || v.Obligations != nil || !strings.Contains(w.Body.String(), "restore_recorded_policy"))) || (!approvals && (v.Digest == brief.NotRecorded || v.Obligations == nil || v.ApprovalsRequired == nil || *v.ApprovalsRequired || !strings.Contains(w.Body.String(), "approvals_not_required"))) {
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
			code = "policy_unavailable"
		}
		w := httptest.NewRecorder()
		Handler(s, []store.QueueKey{j.Key}, "", nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1/queues/Owner/Repo/release/changes/"+string(b.ChangeID)+"/brief?audience=agent", nil))
		var v brief.View
		if json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Digest != brief.NotRecorded || v.Obligations != nil || !strings.Contains(w.Body.String(), code) {
			t.Fatalf("bad failure %d %s", w.Code, w.Body)
		}
	}
}

func TestBriefPublicLoaderRejectsIncompleteCandidate(t *testing.T) {
	_, data, _, _ := policyJournal(t, true)
	if _, _, err := config.Load(append([]byte("version: 1\n"), data...)); err == nil {
		t.Fatal("incomplete config accepted")
	}
}

func TestBriefUnresolvableGroupIsUnavailable(t *testing.T) {
	j, _, _, _ := policyJournal(t, true)
	data := []byte("groups:\n  maintainers: [missing-user]\nqueues:\n  - name: release\n    approvals:\n      default: {group: maintainers}\n")
	req := BriefPolicyRequest{Config: j.Events[0].Event.(queue.ConfigApplied), Evidence: j.Events[2].Event.(queue.ChangeClassified).Evidence}
	_, err := ReconstructBriefPolicy(data, j.Key, j.Key.BaseBranch, req)
	var mismatch queue.BriefPolicyMismatchError
	if err == nil || errors.As(err, &mismatch) {
		t.Fatalf("unresolved identity reported as mismatch: %v", err)
	}
}

func TestR3LegacyEffectiveDigest(t *testing.T) {
	for _, explicit := range []string{"", "delete_head_branch: true\n", "delete_head_branch: false\n"} {
		for _, deletion := range []bool{true, false} {
			for _, forged := range []string{"", strings.Repeat("0", 64), strings.Repeat("f", 64)} {
				j, data, _, b := policyJournal(t, false)
				data = append([]byte(explicit), data...)
				cfg, _, err := config.Load(data)
				if err != nil {
					t.Fatal(err)
				}
				pin := j.Events[0].Event.(queue.ConfigApplied)
				pin.Digest = cfg.Queues[0].Digest(deletion)
				// Keep classification/accountability pins in agreement with the selected
				// deletion case, independently of the forged ConfigApplied pin.
				classified := j.Events[2].Event.(queue.ChangeClassified)
				classified.Evidence.Classification.Inputs.PolicyDigest = pin.Digest
				j.Events[2].Event = classified
				account := j.Events[3].Event.(queue.ChangeAccountabilityObserved)
				account.PolicyDigest = pin.Digest
				j.Events[3].Event = account
				if forged != "" {
					pin.Digest = forged
				}
				j.Events[0].Event = pin
				src := &historicalPolicySource{source: source{journals: []store.Journal{j}}, data: data}
				w := httptest.NewRecorder()
				Handler(src, []store.QueueKey{j.Key}, "", nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1/queues/Owner/Repo/release/changes/"+string(b.ChangeID)+"/brief?audience=operator", nil))
				var v brief.View
				if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
					t.Fatal(err)
				}
				match := forged == "" && (explicit == "" || deletion == *cfg.DeleteHeadBranch)
				if match {
					if v.Digest == brief.NotRecorded || v.Obligations == nil || len(v.Obligations) != 0 || v.ApprovalsRequired == nil || *v.ApprovalsRequired || !strings.Contains(w.Body.String(), "approvals not required by the recorded policy") {
						t.Fatalf("legacy match unavailable: %s", w.Body)
					}
				} else if v.Digest != brief.NotRecorded || v.Obligations != nil || !strings.Contains(w.Body.String(), "policy_digest_mismatch") {
					t.Fatalf("forged pin accepted: %s", w.Body)
				}
			}
		}
	}
}

func TestR3PolicyRecovery(t *testing.T) {
	for _, tc := range []struct {
		err          error
		code, action string
	}{
		{errors.New("object missing"), "policy_unavailable", "restore_recorded_policy"},
		{diagnostic.BusyError("cache busy"), "policy_busy", "retry"},
		{nil, "policy_unavailable", "restore_recorded_policy"},
	} {
		j, data, _, b := policyJournal(t, true)
		src := &historicalPolicySource{source: source{journals: []store.Journal{j}}, data: data, err: tc.err}
		w := httptest.NewRecorder()
		Handler(src, []store.QueueKey{j.Key}, "", nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1/queues/Owner/Repo/release/changes/"+string(b.ChangeID)+"/brief?audience=operator", nil))
		var v brief.View
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, warning := range v.Warnings {
			if warning.Code == "not_recorded" {
				t.Fatal("policy reconstruction advised recording existing evidence")
			}
			if warning.Code == tc.code && warning.NextStep.Action == tc.action {
				found = true
				if tc.action == "restore_recorded_policy" && (!strings.Contains(warning.Clears, "operator-managed fetch or backup import") || !strings.Contains(warning.Clears, "default-branch config commit")) {
					t.Fatal("missing object recovery is not actionable")
				}

			}
		}
		if !found || v.Obligations != nil || v.Digest != brief.NotRecorded {
			t.Fatalf("bad recovery: %s", w.Body)
		}
	}
}

func TestApprovalBriefPinnedComposition(t *testing.T) {
	j, _, _, b := policyJournal(t, true)
	data := []byte("version: 1\nqueues:\n- name: release\n  merge_when: [check-success = ci]\n  approvals: {default: any_writer}\n")
	cfg, _, err := config.Load(data)
	if err != nil {
		t.Fatal(err)
	}
	var classified queue.ChangeClassified
	for _, row := range j.Events {
		if e, ok := row.Event.(queue.ChangeClassified); ok {
			classified = e
		}
	}
	g := classified.Evidence.Governing
	owners, attrs := policyContents(g)
	pin := &cp.PolicyPin{DefaultBranch: "release", Resolved: []cp.ResolvedIdentity{}, ClassifierVersion: classified.Evidence.Classification.Inputs.ClassifierVersion}
	component, err := cp.PolicyDigest(cp.DigestInputs{Repository: cfg.Policy, Queue: cfg.Policy.Queues[0], Resolved: pin.Resolved, Owners: owners, Attributes: attrs, ClassifierVersion: pin.ClassifierVersion})
	if err != nil {
		t.Fatal(err)
	}
	effective, err := cp.WithPolicyDigest(cfg.Queues[0].Digest(true), component)
	if err != nil {
		t.Fatal(err)
	}
	applied := queue.ConfigApplied{Commit: g.CommitSHA, Digest: effective, Window: &queue.WindowBounds{MinDepth: 1, MaxDepth: 1}, Policy: pin}
	classified.Evidence.Classification.Inputs.PolicyDigest = component
	for i, row := range j.Events {
		switch e := row.Event.(type) {
		case queue.ConfigApplied:
			if i == 0 {
				j.Events[i].Event = applied
			}
		case queue.ChangeClassified:
			j.Events[i].Event = classified
		case queue.ChangeAccountabilityObserved:
			e.PolicyDigest = component
			j.Events[i].Event = e
		}
	}
	req := BriefPolicyRequest{Config: applied, Evidence: classified.Evidence, Governing: &g}
	policy, err := ReconstructBriefPolicy(data, j.Key, "changed-default-branch", req)
	if err != nil || policy.Digest != component || policy.Approvals == nil {
		t.Fatalf("reconstruction: %+v %v", policy, err)
	}
	for _, mutation := range []string{"config", "component", "tree", "missing-pin", "missing-tree"} {
		t.Run(mutation, func(t *testing.T) {
			request := req
			request.Config = req.Config
			changed := append([]byte(nil), data...)
			switch mutation {
			case "config":
				request.Config.Digest = strings.Repeat("f", 64)
			case "component":
				request.Evidence.Classification.Inputs.PolicyDigest = strings.Repeat("e", 64)
			case "tree":
				changed = []byte(strings.ReplaceAll(string(data), "default: any_writer", "default: {any_writer: true, count: 2}"))
			case "missing-pin":
				request.Config.Policy = nil
			case "missing-tree":
				request.Governing = nil
			}
			_, err := ReconstructBriefPolicy(changed, j.Key, "release", request)
			var mismatch queue.BriefPolicyMismatchError
			if err == nil {
				t.Fatal("unverifiable composition accepted")
			}
			if !strings.HasPrefix(mutation, "missing") && !errors.As(err, &mismatch) {
				t.Fatalf("mismatch type: %v", err)
			}
		})
	}
	// The HTTP projection must carry the historical ConfigApplied pin, rather
	// than the newer policy at the end of this journal.
	src := &historicalPolicySource{source: source{journals: []store.Journal{j}}, data: data, governing: &g}
	w := httptest.NewRecorder()
	Handler(src, []store.QueueKey{j.Key}, "", nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1/queues/Owner/Repo/release/changes/"+string(b.ChangeID)+"/brief?audience=reviewer", nil))
	var view brief.View
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil || view.Digest == brief.NotRecorded || view.Obligations == nil || view.ApprovalsRequired == nil || !*view.ApprovalsRequired || strings.Contains(w.Body.String(), "policy_unavailable") {
		t.Fatalf("HTTP reconstruction: %d %s", w.Code, w.Body)
	}
	if len(src.requests) != 1 || src.requests[0].Config.Window == nil || *src.requests[0].Config.Window != *applied.Window {
		t.Fatal("historical ConfigApplied window bounds lost")
	}
	reportingGolden(t, "approval-brief.json", w.Body.Bytes())
	for _, path := range []string{
		changePath(j.Key, string(b.ChangeID)),
		"/v1/queues/Owner/Repo/release/prs/12",
	} {
		response := httptest.NewRecorder()
		Handler(src, []store.QueueKey{j.Key}, "", nil).ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		var result struct {
			PR Entry `json:"pr"`
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.PR.Change == nil || result.PR.Change.Evidence == nil || result.PR.Change.Evidence.Digest != view.Digest || result.PR.Change.Evidence.Obligations == nil {
			t.Fatalf("status reconstruction: %d %s", response.Code, response.Body)
		}
	}
	projection, _, _ := Project(j, 0, 0, nil)
	text, err := ReviewerCheckSummaryFromProjection(j, &projection, 12, policy, func(err error) {
		t.Errorf("reconstructed check policy unavailable: %v", err)
	})
	if err != nil || !strings.Contains(text, "Digest: "+view.Digest) || strings.Contains(text, "policy\\_unavailable") || strings.Contains(text, "Requirements: not recorded") {
		t.Fatalf("check reconstruction: %v %s", err, text)
	}
	checkJSON, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	reportingGolden(t, "approval-check.json", append(checkJSON, '\n'))
	src.data = []byte(strings.ReplaceAll(string(data), "default: any_writer", "default: {any_writer: true, count: 2}"))
	mismatchResponse := httptest.NewRecorder()
	Handler(src, []store.QueueKey{j.Key}, "", nil).ServeHTTP(mismatchResponse, httptest.NewRequest("GET", "/v1/queues/Owner/Repo/release/changes/"+string(b.ChangeID)+"/brief?audience=operator", nil))
	if !strings.Contains(mismatchResponse.Body.String(), "policy_digest_mismatch") || strings.Contains(mismatchResponse.Body.String(), "implement_11b") {
		t.Fatalf("HTTP mismatch: %s", mismatchResponse.Body)
	}
}
