package store

import (
	"context"
	"slices"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// ApprovalRead fences one sequential acquisition with database commit order.
// It is local to the acquisition, never shared with another worker or read.
// Each successful locked append advances it only over that transaction's own
// events. A conflicting commit in any intervening window rejects the read.
type ApprovalRead struct {
	queue     QueueID
	watermark int64
	members   []string
	state     queue.State
}

type approvalReadKey struct{}

// BeginApprovalRead must precede every forge read contributing to the result.
func (s *Store) BeginApprovalRead(ctx context.Context, q QueueID, members []queue.ChangeID) (*ApprovalRead, error) {
	if current := approvalRead(ctx); current != nil {
		if current.queue != q {
			return nil, ErrLandingDecisionStale
		}
		for _, member := range members {
			if !slices.Contains(current.members, string(member)) {
				return nil, ErrLandingDecisionStale
			}
		}
		return current, nil
	}
	state, watermark, err := s.Load(ctx, q)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(members))
	for i, id := range members {
		ids[i] = string(id)
	}
	return &ApprovalRead{queue: q, watermark: watermark, members: ids, state: state}, nil
}

// Context retains the caller's cancellation while carrying this acquisition.
func (r *ApprovalRead) Context(ctx context.Context) context.Context {
	return context.WithValue(ctx, approvalReadKey{}, r)
}

func approvalRead(ctx context.Context) *ApprovalRead {
	r, _ := ctx.Value(approvalReadKey{}).(*ApprovalRead)
	return r
}

// The queue lock serializes event-ID allocation and commit for this queue.
// Check BEFORE dedupe and before any writes; never advance over another read's
// commit, even when its evidence subsequently returns to the same fingerprint.
func checkApprovalRead(ctx context.Context, tx pgx.Tx, q QueueID) error {
	r := approvalRead(ctx)
	if r == nil {
		return nil
	}
	if r.queue != q {
		return ErrLandingDecisionStale
	}
	// A rejection invalidates its whole composition and speculative suffix.
	// Retain ancestors of each member batch, not unrelated later compositions.
	var batches []string
	chain := r.state.Batches()
	for i, batch := range chain {
		for _, pr := range batch.PRs {
			revision, _ := r.state.PRRevision(pr.PR)
			if slices.Contains(r.members, string(revision.ChangeID)) {
				for _, ancestor := range chain[:i+1] {
					batches = append(batches, ancestor.ID)
				}
			}
		}
	}
	var changed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM events WHERE queue_id=$1 AND id>$2 AND (
 (type='config_applied' AND payload->'data'->>'digest' IS DISTINCT FROM $4) OR
 (type='base_moved' AND payload->'data'->>'sha' IS DISTINCT FROM $5) OR
 (type='approval_gate_rejected' AND (
 payload->'data'->>'batch_id' = ANY($6::text[]) OR
 EXISTS (SELECT FROM jsonb_array_elements(payload->'data'->'waiting') AS wait
 WHERE wait->'basis'->>'change_id' = ANY($3::text[])))) OR
 (type IN ('change_revision_observed','change_classified','change_accountability_observed',
 'change_intent_observed','review_stance_observed','approval_receipt_recorded',
 'approval_eligibility_observed','approval_inputs_changed') AND
 COALESCE(payload->'data'->'basis'->>'change_id', payload->'data'->>'change_id',
 payload->'data'->'evidence'->'Classification'->'Inputs'->>'ChangeID') = ANY($3::text[]))))`, q, r.watermark, r.members, r.state.ConfigDigest, r.state.BaseSHA, batches).Scan(&changed)
	if err != nil {
		return err
	}
	if changed {
		return ErrLandingDecisionStale
	}
	return nil
}

func advanceApprovalRead(ctx context.Context, watermark int64, events []queue.Event) {
	if r := approvalRead(ctx); r != nil && watermark > r.watermark {
		r.watermark = watermark
		for _, event := range events {
			r.state = queue.Apply(r.state, event)
		}
	}
}
