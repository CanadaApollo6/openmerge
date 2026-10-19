package status

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestChangeShowHTTPRevisionScopeAndAuth(t *testing.T) {
	j, id, b, rev := briefRegressionBase(t)
	next := rev
	next.Predecessor = 1
	next.Revision = 2
	next.HeadSHA = strings.Repeat("d", 40)
	briefAppend(&j, next)
	briefAppend(&j, queue.PRObserved{PR: 1, HeadSHA: next.HeadSHA, Trusted: true, Eligible: true})
	h := Handler(&source{journals: []store.Journal{j}}, []store.QueueKey{key}, "secret", nil)
	path := changePath(key, string(id))
	other := key
	other.BaseBranch = "other"
	otherID, _ := other.ChangeID(1)
	for _, tc := range []struct {
		suffix, token, method string
		code                  int
	}{
		{"", "", "GET", 401}, {"", "secret", "POST", 405}, {"", "secret", "GET", 200}, {"?revision=1", "secret", "GET", 200}, {"?revision=3", "secret", "GET", 404}, {"?revision=0", "secret", "GET", 400}, {"?revision=1&revision=2", "secret", "GET", 400}, {"?after=1", "secret", "GET", 400},
	} {
		r := httptest.NewRequest(tc.method, path+tc.suffix, nil)
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatalf("%s: %d %s", tc.suffix, w.Code, w.Body)
		}
		if tc.code == 200 {
			var v ChangeResponse
			if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
				t.Fatal(err)
			}
			expected := uint64(2)
			if tc.suffix == "?revision=1" {
				expected = 1
			}
			if v.PR.Change.ID != string(id) || v.PR.Change.Revision != expected || v.PR.Change.Accountability.Status != brief.NotRecorded {
				t.Fatalf("bad change: %+v", v.PR.Change)
			}
			if expected == 1 && v.PR.HeadSHA != b.Head {
				t.Fatal("historical head overwritten")
			}
		}
	}
	for _, bad := range []string{strings.Replace(path, string(id), string(otherID), 1), strings.Replace(path, string(id), "bad", 1)} {
		r := httptest.NewRequest("GET", bad, nil)
		r.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 404 && w.Code != 400 {
			t.Fatal(w.Code, w.Body)
		}
	}
}

type decisionTestSource struct {
	calls    int
	revision uint64
	after    int64
	limit    int
}

func (s *decisionTestSource) Journals(context.Context, []store.QueueKey) ([]store.Journal, error) {
	panic("decision read must never replay journal")
}
func (s *decisionTestSource) ChangeDecisions(_ context.Context, k store.QueueKey, id queue.ChangeID, revision uint64, after int64, limit int) ([]store.DecisionRecord, int64, bool, error) {
	s.calls++
	s.revision, s.after, s.limit = revision, after, limit
	canonical, _ := k.ChangeID(1)
	if id != canonical {
		return nil, 0, false, nil
	}
	return []store.DecisionRecord{{EventID: 12, Outcome: "abandoned", Evidence: store.LandingEvidence{Decision: queue.LandingDecisionRecorded{BatchID: "b", AttemptID: "attempt", TestedSHA: "test", LandedSHA: "intended", Variant: "generated_remedy", Remedy: &queue.GeneratedRemedyDecision{ChangeID: id, Revision: 2}}}}}, 12, true, nil
}

func TestChangeDecisionsBoundedReadOnlyHTTP(t *testing.T) {
	s := &decisionTestSource{}
	h := Handler(s, []store.QueueKey{key}, "secret", nil)
	id, _ := key.ChangeID(1)
	path := changePath(key, string(id)) + "/decisions"
	for _, tc := range []struct {
		query string
		code  int
	}{{"?revision=2&after=4&limit=1", 200}, {"?limit=101", 400}, {"?limit=0", 400}, {"?after=-1", 400}, {"?revision=0", 400}, {"?limit=1&limit=2", 400}, {"?unknown=1", 400}} {
		r := httptest.NewRequest("GET", path+tc.query, nil)
		r.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatal(tc.query, w.Code, w.Body)
		}
		if tc.code == 200 {
			var v DecisionsResponse
			_ = json.Unmarshal(w.Body.Bytes(), &v)
			if s.revision != 2 || s.after != 4 || s.limit != 1 || len(v.Decisions) != 1 || v.NextCursor != 12 || v.Decisions[0].Outcome != "abandoned" || v.Decisions[0].LandedSHA != brief.NotRecorded || v.Decisions[0].AttemptID != "attempt" {
				t.Fatal(v)
			}
		}
	}
	if s.calls != 1 {
		t.Fatal("invalid requests reached evidence reader")
	}
}

func TestChangeResponseSizeLimit(t *testing.T) {
	w := httptest.NewRecorder()
	writeBoundedChange(w, strings.Repeat("x", brief.MaxViewBytes+1))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}

func TestReviewerCheckLegacyGolden(t *testing.T) {
	j := journal(append(ledgerChain(), identified("b1", 1, 1, "success"))...)
	suffix, err := ReviewerCheckSummary(j, 1, cp.Policy{})
	if err != nil || suffix != "" {
		t.Fatalf("legacy check changed: %q %v", suffix, err)
	}
	q, e, _ := Project(j, 1, 3, nil)
	ApplyPolicy(&q, e, queue.Config{SpeculationMaxDepth: 4, BatchMaxSize: 8}, at)
	got := "\n\nQueue: " + AdaptiveSummary(&q) + LedgerSummary(e) + suffix
	want, err := os.ReadFile("testdata/legacy-check.txt")
	if err != nil || got != string(want) {
		t.Fatalf("legacy check golden changed: %q; %v", got, err)
	}
}

func TestReviewerCheckWarningsEscapingAndBounds(t *testing.T) {
	j, _, b, _ := briefRegressionBase(t)
	briefAppend(&j, queue.Intent(b, "<script> [link](evil) "+strings.Repeat("z", 100000), "body"))
	policy := cp.Policy{Approvals: &cp.Approvals{Default: cp.Requirement{Selector: cp.Human, Count: 1}}}
	text, err := ReviewerCheckSummary(j, 1, policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"WARNING: classification/inventory not recorded", "Reviewer brief:", "Digest:", "Requirements: not recorded", "Native reviews bind the head"} {
		if !strings.Contains(text, want) {
			t.Fatal("missing", want, text)
		}
	}
	if len(text) > 16000 || strings.Contains(text, "<script>") || strings.Contains(text, "[link](evil)") {
		t.Fatal("unsafe or unbounded check", text)
	}
}

func TestDecisionViewDoesNotInferLanding(t *testing.T) {
	for _, outcome := range []string{"unresolved", "abandoned", "landed"} {
		v := decisionView(store.DecisionRecord{Outcome: outcome, LandedSHA: "confirmed", Evidence: store.LandingEvidence{Decision: queue.LandingDecisionRecorded{LandedSHA: "intended"}}}, diagnostic.NewSanitizer())
		if outcome != "landed" && v.LandedSHA != brief.NotRecorded || outcome == "landed" && v.LandedSHA != "confirmed" {
			t.Fatal(v)
		}
	}
}
