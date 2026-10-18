package status

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestBriefHTTPAuthScopeAndLegacy(t *testing.T) {
	branchKey := key
	branchKey.BaseBranch = "release/v1"
	id, _ := branchKey.ChangeID(1)
	otherID, _ := key.ChangeID(1)
	j := store.Journal{Key: branchKey, Seed: queue.State{BaseBranch: branchKey.BaseBranch}, Events: []store.JournalEvent{{Event: queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}}}}
	s := &source{journals: []store.Journal{j}}
	path := "/v1/queues/owner/repo/release%2Fv1/changes/" + string(id) + "/brief"
	h := Handler(s, []store.QueueKey{branchKey}, "secret", nil)
	for _, tc := range []struct {
		path, method, auth string
		code               int
	}{
		{path + "?audience=reviewer", "GET", "", 401},
		{path + "?audience=reviewer", "POST", "Bearer secret", 405},
		{path + "?audience=reviewer", "GET", "Bearer secret", 200},
		{path + "?audience=operator", "GET", "Bearer secret", 200},
		{path + "?audience=agent", "GET", "Bearer secret", 200},
		{path, "GET", "Bearer secret", 400},
		{path + "?audience=invalid", "GET", "Bearer secret", 400},
		{path + "?audience=agent&audience=agent", "GET", "Bearer secret", 400},
		{path + "?audience=agent&extra=1", "GET", "Bearer secret", 400},
		{strings.Replace(path, string(id), string(otherID), 1) + "?audience=agent", "GET", "Bearer secret", 404},
	} {
		t.Run(tc.path+tc.method+tc.auth, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.Header.Set("Authorization", tc.auth)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.code {
				t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
			}
			if tc.code == 200 {
				var v brief.View
				if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
					t.Fatal(err)
				}
				if v.ChangeID != string(id) || v.Digest != brief.NotRecorded || v.Classification != nil || v.Obligations != nil {
					t.Fatalf("invented legacy evidence: %+v", v)
				}
				if len(s.keys) != 1 || s.keys[0] != branchKey {
					t.Fatal("read escaped selected queue")
				}
			}
		})
	}
	duplicate := branchKey
	duplicate.Installation = "another"
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", path+"?audience=agent", nil)
	Handler(s, []store.QueueKey{branchKey, duplicate}, "", nil).ServeHTTP(w, r)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "ambiguous-queue") {
		t.Fatalf("lost ambiguity diagnostic: %s", w.Body)
	}
}

func TestBriefProjectsRecordedClassificationIntentAndNativeStance(t *testing.T) {
	k := store.QueueKey{Installation: "7", Repository: "Owner/Repo", BaseBranch: "release"}
	id, _ := k.ChangeID(12)
	j := store.Journal{Key: k}
	rev := queue.ChangeRevisionObserved{PR: 12, ChangeID: id, Revision: 1, HeadSHA: strings.Repeat("b", 40), Basis: queue.RevisionObservationBasis{BaseSHA: strings.Repeat("a", 40), At: time.Date(2026, 10, 18, 11, 0, 0, 0, time.UTC)}}
	j.Events = append(j.Events, store.JournalEvent{Event: rev})
	for _, name := range []string{"change_classified", "change_intent_observed", "review_stance_observed"} {
		raw, err := os.ReadFile("../queue/testdata/wire/v1/event_" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		event, err := queue.DecodeEvent(raw)
		if err != nil {
			t.Fatal(err)
		}
		j.Events = append(j.Events, store.JournalEvent{Event: event})
	}
	v, found, err := projectBrief(j, string(id), "operator")
	if err != nil || !found || v.Classification == nil || v.Intent == nil || len(v.Stances) != 1 {
		t.Fatalf("missing recorded facts: %+v %v", v, err)
	}
	if v.Intent.Title != "Title" || v.Classification.Files[0].NewPath != "docs/a.md" || v.Stances[0].BriefDigest != brief.NotRecorded || v.Stances[0].Channel != "native_review" {
		t.Fatal("recorded evidence changed")
	}
	if v.Digest != brief.NotRecorded || v.Obligations != nil {
		t.Fatal("manufactured unrecorded requirements/digest")
	}
	// A subsequent intent edit is projected from the log, with no forge/config read.
	edit := queue.Intent(queue.ApprovalBasis{ChangeID: id, Revision: 1, Head: rev.HeadSHA, ReadID: "next-read", At: rev.Basis.At.Add(2 * time.Hour)}, "edited", "body")
	j.Events = append(j.Events, store.JournalEvent{Event: edit})
	next, _, err := projectBrief(j, string(id), "reviewer")
	if err != nil || next.Intent.Title != "edited" || next.Stances[0].BriefDigest != brief.NotRecorded {
		t.Fatal("stale/inferred evidence")
	}
}

func TestOperatorBriefExplainsQueueBlockRecovery(t *testing.T) {
	id, _ := key.ChangeID(1)
	j := journal(queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, queue.BlockSourceBlocked{Source: queue.ConfigSource, Detail: "invalid governing configuration"})
	v, found, err := projectBrief(j, string(id), "operator")
	if err != nil || !found {
		t.Fatal(err)
	}
	for _, warning := range v.Warnings {
		if warning.Code == "queue_blocked" {
			if warning.NextStep.Action != "fix_governing_config" || warning.NextStep.Source != "config" || !strings.Contains(warning.Clears, "governing configuration") {
				t.Fatal("operator lacks actionable recovery")
			}
			return
		}
	}
	t.Fatal("recorded queue blocker missing")
}
