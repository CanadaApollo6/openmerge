package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// LandingEvidence is retained journal evidence for one member of an attempt.
// Eligibility is the exact observation referenced by the decision, not the
// latest projection. Generated remedies have no approval eligibility read.
type LandingEvidence struct {
	DecisionEventID    int64
	EligibilityEventID int64
	Decision           queue.LandingDecisionRecorded
	Eligibility        *queue.ApprovalEligibilityObserved
}

// LandingDecisionEvidence reads at most 128 member decisions and their pinned
// eligibility observations. These two event types must survive any compaction.
// SQL selects indexed decision/read identities before decoding, never replaying a journal.
func (s *Store) LandingDecisionEvidence(ctx context.Context, q QueueID, batch, attempt string) ([]LandingEvidence, error) {
	if batch == "" || attempt == "" {
		return nil, fmt.Errorf("batch and attempt required")
	}
	var result []LandingEvidence
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		result, err = LandingDecisionEvidenceTx(ctx, tx, q, batch, attempt)
		return err
	})
	return result, err
}

// LandingDecisionEvidenceTx reads the same retained evidence in the caller's
// snapshot. It opens no transaction and leaves transaction ownership to callers.
func LandingDecisionEvidenceTx(ctx context.Context, tx pgx.Tx, q QueueID, batch, attempt string, boundary ...int64) ([]LandingEvidence, error) {
	if batch == "" || attempt == "" {
		return nil, fmt.Errorf("batch and attempt required")
	}
	var result []LandingEvidence
	decisionSQL := landingDecisionEvidenceSQL
	decisionArgs := []any{q, batch, attempt}
	if len(boundary) > 0 {
		decisionSQL = strings.Replace(decisionSQL, "SELECT id,payload", "SELECT id,CASE WHEN octet_length(payload::text)<=4194304 THEN payload ELSE NULL END", 1)
		decisionSQL = strings.Replace(decisionSQL, " ORDER BY id", " AND id<=$4 ORDER BY id", 1)
		decisionArgs = append(decisionArgs, boundary[0])
	}
	rows, err := tx.Query(ctx, decisionSQL, decisionArgs...)
	if err != nil {
		return result, err
	}
	decisions, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (LandingEvidence, error) {
		var raw []byte
		var id int64
		if err := row.Scan(&id, &raw); err != nil {
			return LandingEvidence{}, err
		}
		event, err := queue.DecodeEvent(raw)
		if err != nil {
			return LandingEvidence{}, err
		}
		d, ok := event.(queue.LandingDecisionRecorded)
		if !ok {
			return LandingEvidence{}, fmt.Errorf("invalid landing decision event type")
		}
		return LandingEvidence{Decision: d, DecisionEventID: id}, nil
	})
	if err != nil {
		return result, err
	}
	if len(decisions) > 128 {
		return result, fmt.Errorf("landing decision member budget exceeded")
	}
	for _, evidence := range decisions {
		d := evidence.Decision
		if d.Approval != nil {
			basis := d.Approval.Basis
			eligibilitySQL := eligibilityReadEvidenceSQL
			eligibilityArgs := []any{q, string(basis.ChangeID), d.Approval.EligibilityReadID, fmt.Sprint(basis.Revision), basis.Head}
			if len(boundary) > 0 {
				eligibilitySQL = strings.Replace(eligibilitySQL, "SELECT id,payload", "SELECT id,CASE WHEN octet_length(payload::text)<=4194304 THEN payload ELSE NULL END", 1)
				eligibilitySQL = strings.Replace(eligibilitySQL, " ORDER BY id", " AND id<=$6 ORDER BY id", 1)
				eligibilityArgs = append(eligibilityArgs, boundary[0])
			}
			rows, err := tx.Query(ctx, eligibilitySQL, eligibilityArgs...)
			if err != nil {
				return result, err
			}
			reads, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (LandingEvidence, error) {
				var raw []byte
				var id int64
				if err := row.Scan(&id, &raw); err != nil {
					return LandingEvidence{}, err
				}
				event, err := queue.DecodeEvent(raw)
				if err != nil {
					return LandingEvidence{}, err
				}
				f, ok := event.(queue.ApprovalEligibilityObserved)
				if !ok {
					return LandingEvidence{}, fmt.Errorf("invalid eligibility event type")
				}
				return LandingEvidence{Eligibility: &f, EligibilityEventID: id}, nil
			})
			if err != nil {
				return result, err
			}
			if len(reads) != 1 {
				return result, fmt.Errorf("expected one retained eligibility observation, got %d", len(reads))
			}
			evidence.Eligibility = reads[0].Eligibility
			evidence.EligibilityEventID = reads[0].EligibilityEventID
		}
		result = append(result, evidence)
	}
	return result, nil
}

// Keep these expressions identical to migration 006's partial index keys.
const landingDecisionEvidenceSQL = `SELECT id,payload FROM events WHERE queue_id=$1 AND type='landing_decision_recorded'
   AND payload->'data'->>'batch_id'=$2 AND payload->'data'->>'attempt_id'=$3 ORDER BY id LIMIT 129`

const eligibilityReadEvidenceSQL = `SELECT id,payload FROM events WHERE queue_id=$1 AND type='approval_eligibility_observed'
     AND payload->'data'->'basis'->>'change_id'=$2 AND payload->'data'->'basis'->>'read_id'=$3
     AND payload->'data'->'basis'->>'revision'=$4 AND payload->'data'->'basis'->>'head'=$5 ORDER BY id LIMIT 2`
