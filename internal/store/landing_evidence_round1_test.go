package store

import (
	"context"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestLandingEvidenceBoundaryQueryShapes(t *testing.T) {
	if landingDecisionEvidenceSQL != `SELECT id,payload FROM events WHERE queue_id=$1 AND type='landing_decision_recorded'
   AND payload->'data'->>'batch_id'=$2 AND payload->'data'->>'attempt_id'=$3 ORDER BY id LIMIT 129` {
		t.Fatal("unbounded decision evidence SQL changed")
	}
	if eligibilityReadEvidenceSQL != `SELECT id,payload FROM events WHERE queue_id=$1 AND type='approval_eligibility_observed'
     AND payload->'data'->'basis'->>'change_id'=$2 AND payload->'data'->'basis'->>'read_id'=$3
     AND payload->'data'->'basis'->>'revision'=$4 AND payload->'data'->'basis'->>'head'=$5 ORDER BY id LIMIT 2` {
		t.Fatal("unbounded eligibility evidence SQL changed")
	}
	for name, query := range map[string]string{
		"decision":    boundedLandingDecisionEvidenceSQL,
		"eligibility": boundedEligibilityReadEvidenceSQL,
	} {
		if !strings.Contains(query, "octet_length(payload::text)<=4194304") || !strings.Contains(query, "id<=") {
			t.Fatalf("%s boundary query lost payload or ID bound: %s", name, query)
		}
	}
	if _, err := LandingDecisionEvidenceTx(context.Background(), nil, 0, "", "attempt", nil); err == nil {
		t.Fatal("empty batch accepted")
	}
	if _, err := LandingDecisionEvidenceTx(context.Background(), nil, 0, "batch", "", nil); err == nil {
		t.Fatal("empty attempt accepted")
	}
}

func TestLandingDecisionEvidenceBoundedAndEligibilityCardinality(t *testing.T) {
	s := newStore(t)
	q, claim, c, d := decisionLand(t, s)
	must(t, commitDecision(t, s, claim, c, d))

	var decisionID, eligibilityID int64
	must(t, s.pool.QueryRow(t.Context(), `SELECT id FROM events WHERE queue_id=$1 AND type='landing_decision_recorded'`, q).Scan(&decisionID))
	must(t, s.pool.QueryRow(t.Context(), `SELECT id FROM events WHERE queue_id=$1 AND type='approval_eligibility_observed'`, q).Scan(&eligibilityID))
	tx, err := s.pool.Begin(t.Context())
	must(t, err)
	defer func() { _ = tx.Rollback(t.Context()) }()

	unbounded, err := LandingDecisionEvidenceTx(t.Context(), tx, q, c.Start.BatchID, c.Start.AttemptID, nil)
	must(t, err)
	if len(unbounded) != 1 || unbounded[0].DecisionEventID != decisionID || unbounded[0].EligibilityEventID != eligibilityID {
		t.Fatal("unbounded read changed behavior", unbounded)
	}
	zero := int64(0)
	bounded, err := LandingDecisionEvidenceTx(t.Context(), tx, q, c.Start.BatchID, c.Start.AttemptID, &zero)
	must(t, err)
	if len(bounded) != 0 {
		t.Fatal("bounded zero was treated as unbounded", bounded)
	}
	boundary := decisionID
	bounded, err = LandingDecisionEvidenceTx(t.Context(), tx, q, c.Start.BatchID, c.Start.AttemptID, &boundary)
	must(t, err)
	if len(bounded) != 1 || bounded[0].DecisionEventID != decisionID {
		t.Fatal("bounded read omitted retained decision", bounded)
	}
	if _, err := s.pool.Exec(t.Context(), `DELETE FROM events WHERE id=$1`, eligibilityID); err != nil {
		t.Fatal(err)
	}
	boundary = int64(^uint64(0) >> 1)
	if _, err := LandingDecisionEvidenceTx(t.Context(), tx, q, c.Start.BatchID, c.Start.AttemptID, &boundary); err == nil || !strings.Contains(err.Error(), "got 0") {
		t.Fatalf("missing eligibility read accepted: %v", err)
	}
	_, raw, err := queue.EncodeEvent(c.Fresh)
	must(t, err)
	for range 2 {
		if _, err := s.pool.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload) VALUES($1,'approval_eligibility_observed',$2::jsonb)`, q, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LandingDecisionEvidenceTx(t.Context(), tx, q, c.Start.BatchID, c.Start.AttemptID, &boundary); err == nil || !strings.Contains(err.Error(), "got 2") {
		t.Fatalf("duplicate eligibility reads accepted: %v", err)
	}
}

func TestLandingEvidencePayloadCapAtFourMiB(t *testing.T) {
	s := newStore(t)
	q, claim, c, d := decisionLand(t, s)
	must(t, commitDecision(t, s, claim, c, d))
	var id int64
	must(t, s.pool.QueryRow(t.Context(), `SELECT id FROM events WHERE queue_id=$1 AND type='landing_decision_recorded'`, q).Scan(&id))
	var padding int
	must(t, s.pool.QueryRow(t.Context(), `SELECT 4194304-octet_length(jsonb_set(payload,'{data,padding}',to_jsonb(''::text))::text) FROM events WHERE id=$1`, id).Scan(&padding))
	if padding < 0 {
		t.Fatal("event fixture exceeds payload cap")
	}
	check := func(wantPresent bool) {
		t.Helper()
		var gotID int64
		var raw []byte
		must(t, s.pool.QueryRow(t.Context(), boundedLandingDecisionEvidenceSQL, q, c.Start.BatchID, c.Start.AttemptID, id).Scan(&gotID, &raw))
		if gotID != id || (raw != nil) != wantPresent {
			t.Fatalf("payload cap result id=%d present=%t, want id=%d present=%t", gotID, raw != nil, id, wantPresent)
		}
		if wantPresent && len(raw) != 4194304 {
			t.Fatalf("payload at cap has %d bytes, want 4194304", len(raw))
		}
	}
	for _, n := range []int{padding, padding + 1} {
		_, err := s.pool.Exec(t.Context(), `UPDATE events SET payload=jsonb_set(payload,'{data,padding}',to_jsonb(repeat('x',$2))) WHERE id=$1`, id, n)
		must(t, err)
		check(n == padding)
	}
}
