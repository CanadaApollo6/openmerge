package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// DecisionRecord joins retained evidence with the first settlement after its
// decision and before another attempt. An intended landing SHA is not an outcome.
type DecisionRecord struct {
	EventID           int64
	Evidence          LandingEvidence
	Outcome           string
	SettlementEventID int64
	LandedSHA         string
}

// ChangeDecisions selects only typed decision identities from migration 006's
// partial index. It never loads or decodes the full journal. Cursor is exclusive.
func (s *Store) ChangeDecisions(ctx context.Context, key QueueKey, id queue.ChangeID, revision uint64, after int64, limit int) ([]DecisionRecord, int64, bool, error) {
	if id.Validate() != nil || after < 0 || limit < 1 || limit > 100 {
		return nil, 0, false, fmt.Errorf("invalid decision query")
	}
	var q QueueID
	err := s.pool.QueryRow(ctx, `SELECT id FROM queues WHERE installation=$1 AND repository=$2 AND base=$3 AND lane=$4`, key.Installation, key.Repository, key.BaseBranch, key.Lane).Scan(&q)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	// Legacy IDs are derived from queue-local PR aliases. Only this narrow column
	// is read: unrelated events are never transferred or decoded.
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT (payload->'data'->>'pr')::bigint FROM events WHERE queue_id=$1 AND type IN ('pr_observed','change_revision_observed') ORDER BY 1 LIMIT 100001`, q)
	if err != nil {
		return nil, 0, false, err
	}
	prs, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		return nil, 0, false, err
	}
	if len(prs) > 100000 {
		return nil, 0, false, fmt.Errorf("queue alias reporting budget exceeded")
	}
	found := false
	for _, pr := range prs {
		canonical, e := key.ChangeID(pr)
		if e == nil && canonical == id {
			found = true
			break
		}
	}
	if !found {
		return nil, 0, false, nil
	}
	rows, err = s.pool.Query(ctx, changeDecisionPageSQL, q, string(id), fmt.Sprint(revision), after, limit+1)
	if err != nil {
		return nil, 0, false, err
	}
	type ref struct {
		id             int64
		batch, attempt string
	}
	refs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ref, error) {
		var r ref
		err := row.Scan(&r.id, &r.batch, &r.attempt)
		return r, err
	})
	if err != nil {
		return nil, 0, false, err
	}
	next := int64(0)
	if len(refs) > limit {
		refs = refs[:limit]
		next = refs[len(refs)-1].id
	}
	result := make([]DecisionRecord, 0, len(refs))
	for _, r := range refs {
		evidence, err := s.LandingDecisionEvidence(ctx, q, r.batch, r.attempt)
		if err != nil {
			return nil, 0, false, err
		}
		for _, e := range evidence {
			if e.DecisionEventID != r.id {
				continue
			}
			d := e.Decision
			matches := d.Approval != nil && d.Approval.Basis.ChangeID == id && (revision == 0 || uint64(d.Approval.Basis.Revision) == revision) || d.Remedy != nil && d.Remedy.ChangeID == id && (revision == 0 || uint64(d.Remedy.Revision) == revision)
			if !matches {
				continue
			}
			record := DecisionRecord{EventID: r.id, Evidence: e, Outcome: "unresolved"}
			// Batch IDs identify a composition; stop at a subsequent start so a later
			// attempt's settlement can never confirm this abandoned attempt.
			var kind string
			var raw []byte
			err = s.pool.QueryRow(ctx, `SELECT id,type,payload FROM events WHERE queue_id=$1 AND id>$2 AND type IN ('landed','landing_abandoned','landing_started') AND payload->'data'->>'batch_id'=$3 AND (type!='landing_started' OR payload->'data'->>'attempt_id'!=$4) AND (type!='landed' OR payload->'data'->>'new_base_sha'=$5) ORDER BY id LIMIT 1`, q, r.id, r.batch, r.attempt, d.LandedSHA).Scan(&record.SettlementEventID, &kind, &raw)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, 0, false, err
			}
			if kind == "landed" || kind == "landing_abandoned" {
				event, err := queue.DecodeEvent(raw)
				if err != nil {
					return nil, 0, false, err
				}
				if landed, ok := event.(queue.Landed); ok {
					record.Outcome = "landed"
					record.LandedSHA = landed.NewBaseSHA
				} else {
					record.Outcome = "abandoned"
				}
			} else {
				record.SettlementEventID = 0
			}
			result = append(result, record)
		}
	}
	return result, next, true, nil
}

const changeDecisionPageSQL = `SELECT id,payload->'data'->>'batch_id',payload->'data'->>'attempt_id' FROM events
 WHERE queue_id=$1 AND type='landing_decision_recorded' AND id>$4
 AND COALESCE(payload->'data'->'approval'->'basis'->>'change_id',payload->'data'->'remedy'->>'change_id')=$2
 AND ($3='0' OR payload->'data'->'approval'->'basis'->>'revision'=$3 OR payload->'data'->'remedy'->>'revision'=$3)
 ORDER BY id LIMIT $5`
