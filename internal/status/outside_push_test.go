package status

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestOutsidePushStatusAPIBoundedEvidence(t *testing.T) {
	var events []queue.Event
	for i := range queue.OutsidePushLimit + 2 {
		events = append(events, queue.OutsidePushRecorded{PreviousSHA: fmt.Sprintf("old%d", i), NewSHA: fmt.Sprintf("new%d", i), AncestryKnown: true, Rewrite: i%2 == 1, VerificationBeforeLanding: "none", At: at})
	}
	s := &source{journals: []store.Journal{journal(events...)}}
	handler := Handler(s, []store.QueueKey{key}, "", nil)
	for _, path := range []string{"/v1/queues", "/v1/queues/owner/repo/main"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		var result Response
		if w.Code != 200 {
			t.Fatalf("status: %d %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		q := result.Queue
		if q == nil {
			q = &result.Queues[0]
		}
		pushes := q.OutsidePushes
		if len(pushes) != queue.OutsidePushLimit || pushes[0].EventID != 3 || pushes[0].PreviousSHA != "old2" || !pushes[len(pushes)-1].Rewrite {
			t.Fatalf("lost bounded evidence: %+v", pushes)
		}
		for _, p := range pushes {
			if p.HistoryKnown || p.VerificationBeforeLanding != "none" || p.At != at {
				t.Fatal("changed historical claims")
			}
		}
	}
}
