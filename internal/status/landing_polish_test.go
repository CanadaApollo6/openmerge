package status

import (
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func pendingPair(when time.Time) []queue.Event {
	return []queue.Event{queue.ChecksReopened{TestedSHA: "tested", At: when}, queue.ChecksRefreshed{BatchID: "b1", TestedSHA: "tested", At: when}}
}

func TestPendingEvidenceCollapsesWithoutInventingRevokedSuccess(t *testing.T) {
	events := testingEvents()
	for i := range 100 {
		events = append(events, pendingPair(at.Add(time.Duration(i)*30*time.Second))...)
	}
	j := journal(events...)
	for i := range j.Events {
		j.Events[i].RecordedAt = at.Add(time.Duration(i) * time.Second)
	}
	_, _, why := Project(j, 1, 2, nil)
	if len(why.Events) != 2 || why.Events[0].Type != "test_ref_built" {
		t.Fatalf("polling crowded out useful history: %+v", why.Events)
	}
	row := why.Events[1]
	if row.Summary != "CI still pending" || row.Count != 100 || row.ID != 6 || row.EndID != 205 || row.Until == nil || !row.At.Equal(j.Events[5].RecordedAt) || !row.Until.Equal(j.Events[204].RecordedAt) {
		t.Fatalf("bad pending range: %+v", row)
	}
	_, _, why = Project(j, 1, 1, nil)
	if len(why.Events) != 1 || why.Events[0].Count != 100 {
		t.Fatalf("limit truncated a group: %+v", why.Events)
	}
}

func TestReopenedSummariesFollowTheFold(t *testing.T) {
	for _, scenario := range []string{"success", "retained-success", "already-pending", "landing"} {
		t.Run(scenario, func(t *testing.T) {
			events := testingEvents()
			switch scenario {
			case "success", "landing":
				events = append(events, queue.ChecksCompleted{TestedSHA: "tested", Success: true})
			case "retained-success":
				events = append(admission(), queue.BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: []queue.BatchPR{{PR: 1, HeadSHA: "head"}}, At: at}, queue.ChecksCompleted{TestedSHA: "tested", Success: true}, queue.ChecksReopened{TestedSHA: "tested", At: at}, queue.TestRefBuilt{BatchID: "b1", TestedSHA: "tested", At: at, Method: queue.Squash})
			}
			if scenario == "landing" {
				events = append(events, queue.LandingStarted{BatchID: "b1", AttemptID: "attempt", FenceToken: "fence", At: at})
			}
			if scenario != "retained-success" {
				events = append(events, pendingPair(at)...)
			}
			_, _, why := Project(journal(events...), 1, 20, nil)
			revocations := 0
			for _, event := range why.Events {
				if strings.Contains(event.Summary, "previous success revoked") {
					revocations++
				}
			}
			want := 0
			if scenario == "success" || scenario == "retained-success" {
				want = 1
			}
			if revocations != want {
				t.Fatalf("invented/lost revocation: %+v", why.Events)
			}
			if scenario == "success" {
				events = append(events, pendingPair(at.Add(time.Minute))...)
				_, _, why = Project(journal(events...), 1, 2, nil)
				if len(why.Events) != 2 || why.Events[0].Summary != "CI reopened; previous success revoked" || why.Events[1].Summary != "CI still pending" {
					t.Fatalf("revocation collapsed into pending: %+v", why.Events)
				}
			}
		})
	}
}

func TestEvidenceGroupingPreservesInterruptionsAndSHAs(t *testing.T) {
	for _, interrupt := range []queue.Event{
		queue.RefreshRequested{},
		queue.ChecksCompleted{TestedSHA: "tested", Success: true},
		queue.ChecksReopened{TestedSHA: "tested", At: at.Add(time.Hour)},
		queue.ChecksRefreshed{BatchID: "b1", TestedSHA: "tested", At: at.Add(time.Hour)},
	} {
		events := append(testingEvents(), pendingPair(at)...)
		events = append(events, interrupt)
		events = append(events, pendingPair(at.Add(time.Minute))...)
		_, _, why := Project(journal(events...), 1, 20, nil)
		for _, row := range why.Events {
			if row.Count > 1 {
				t.Fatalf("collapsed across %T: %+v", interrupt, why.Events)
			}
		}
	}
	rows := []Event{{ID: 1, Type: "checks_reopened", Summary: "CI still pending"}}
	rows = appendEvidence(rows, Event{ID: 2}, queue.ChecksRefreshed{BatchID: "b1", TestedSHA: "other", At: at}, queue.ChecksReopened{TestedSHA: "tested", At: at}, queue.State{})
	if len(rows) != 2 {
		t.Fatal("collapsed mismatched SHAs")
	}
}
