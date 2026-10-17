package status

import (
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// A refresh/reopen pair is one observation. Only adjacent equal observations
// of the same batch and SHA collapse; state changes remain separate evidence.
func appendEvidence(rows []Event, row Event, event, previous queue.Event, before queue.State) []Event {
	refresh, ok := event.(queue.ChecksRefreshed)
	reopen, paired := previous.(queue.ChecksReopened)
	if !ok || !paired || len(rows) == 0 || rows[len(rows)-1].Type != "checks_reopened" || refresh.TestedSHA != reopen.TestedSHA || !refresh.At.Equal(reopen.At) ||
		before.Batch(refresh.BatchID) == nil || before.Batch(refresh.BatchID).TestedSHA != refresh.TestedSHA {
		return append(rows, row)
	}
	pair := rows[len(rows)-1]
	rows = rows[:len(rows)-1]
	pair.Type = "checks_reopened+checks_refreshed"
	pair.Count, pair.EndID, pair.Until = 1, row.ID, &row.At
	pair.pairKey = refresh.BatchID + "/" + refresh.TestedSHA + "/" + pair.Summary
	if len(rows) > 0 && rows[len(rows)-1].pairKey == pair.pairKey {
		old := rows[len(rows)-1]
		pair.ID, pair.At, pair.Count = old.ID, old.At, old.Count+1
		rows = rows[:len(rows)-1]
	}
	return append(rows, pair)
}
