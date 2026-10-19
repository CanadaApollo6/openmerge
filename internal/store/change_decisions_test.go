package store

import (
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestChangeDecisionsIndexedReadbackAndSettlement(t *testing.T) {
	for _, settlement := range []string{"unresolved", "abandoned", "landed"} {
		t.Run(settlement, func(t *testing.T) {
			s := newStore(t)
			q, claim, c, d := decisionLand(t, s)
			must(t, commitDecision(t, s, claim, c, d))
			if settlement == "landed" {
				appendEvents(t, s, q, queue.Landed{BatchID: c.Start.BatchID, NewBaseSHA: c.Land.TestedSHA, NewFence: "settled"})
			}
			if settlement == "abandoned" {
				appendEvents(t, s, q, queue.LandingAbandoned{BatchID: c.Start.BatchID, NewFence: "abandoned"})
			}
			// Typed reads must ignore even undecodable unrelated journal payloads.
			_, err := s.pool.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload) VALUES ($1,'future_event','{"version":999}')`, q)
			must(t, err)
			var k QueueKey
			must(t, s.pool.QueryRow(t.Context(), `SELECT installation,repository,base,lane FROM queues WHERE id=$1`, q).Scan(&k.Installation, &k.Repository, &k.BaseBranch, &k.Lane))
			records, next, found, err := s.ChangeDecisions(t.Context(), k, c.Fresh.Basis.ChangeID, 0, 0, 1)
			must(t, err)
			if !found || len(records) != 1 || next != 0 || records[0].Outcome != settlement || records[0].Evidence.Eligibility.Basis.ReadID != d.Approval.EligibilityReadID || records[0].Evidence.Decision.AttemptID != c.Start.AttemptID {
				t.Fatalf("bad readback: %+v next=%d found=%t", records, next, found)
			}
			if settlement != "landed" && records[0].LandedSHA != "" {
				t.Fatal("invented landing")
			}
			_, _, found, err = s.ChangeDecisions(t.Context(), k, queue.ChangeID("chg1_"+strings.Repeat("0", 64)), 0, 0, 1)
			must(t, err)
			if found {
				t.Fatal("unknown change resolved")
			}
			rows, _, _, err := s.ChangeDecisions(t.Context(), k, c.Fresh.Basis.ChangeID, 999, 0, 1)
			must(t, err)
			if len(rows) != 0 {
				t.Fatal("revision filter ignored")
			}
			rows, _, _, err = s.ChangeDecisions(t.Context(), k, c.Fresh.Basis.ChangeID, 0, records[0].EventID, 1)
			must(t, err)
			if len(rows) != 0 {
				t.Fatal("exclusive cursor ignored")
			}
			k.BaseBranch = "another"
			_, _, found, err = s.ChangeDecisions(t.Context(), k, c.Fresh.Basis.ChangeID, 0, 0, 1)
			must(t, err)
			if found {
				t.Fatal("cross-queue ID resolved")
			}
		})
	}
}

func TestReportingIndexesExcludeUnrelatedHistory(t *testing.T) {
	s := newStore(t)
	q, claim, c, d := decisionLand(t, s)
	must(t, commitDecision(t, s, claim, c, d))
	_, err := s.pool.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload) SELECT $1,'future_event','{"version":999,"type":"future_event"}'::jsonb FROM generate_series(1,2000)`, q)
	must(t, err)
	_, err = s.pool.Exec(t.Context(), `ANALYZE events`)
	must(t, err)
	queries := []struct {
		index, sql string
		args       []any
	}{
		{"events_reporting_change_decision", changeDecisionPageSQL, []any{q, string(c.Fresh.Basis.ChangeID), "0", int64(0), 21}},
		{"events_reporting_pr_alias", `SELECT DISTINCT (payload->'data'->>'pr')::int FROM events WHERE queue_id=$1 AND type IN ('pr_observed','change_revision_observed') ORDER BY 1 LIMIT 100001`, []any{q}},
		{"events_reporting_settlement", `SELECT id FROM events WHERE queue_id=$1 AND type IN ('landed','landing_abandoned','landing_started') AND payload->'data'->>'batch_id'=$2 ORDER BY id LIMIT 1`, []any{q, c.Start.BatchID}},
	}
	for _, query := range queries {
		var raw []byte
		must(t, s.pool.QueryRow(t.Context(), "EXPLAIN (ANALYZE,FORMAT JSON) "+query.sql, query.args...).Scan(&raw))
		if !strings.Contains(string(raw), query.index) || strings.Contains(string(raw), `"Node Type": "Seq Scan"`) {
			t.Fatalf("reporting read walks unrelated history: %s", raw)
		}
	}
}

func TestChangeDecisionsPaginationDoesNotConfirmPriorAttempt(t *testing.T) {
	s := newStore(t)
	q, claim, c, d := decisionLand(t, s)
	must(t, commitDecision(t, s, claim, c, d))
	appendEvents(t, s, q, queue.LandingAbandoned{BatchID: c.Start.BatchID, NewFence: "abandoned"})
	// Insert immutable typed history to exercise read paging independently of
	// planner authorization. Both decisions retain their exact original read.
	d.AttemptID = "second-attempt"
	_, raw, err := queue.EncodeEvent(d)
	must(t, err)
	_, err = s.pool.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload) VALUES ($1,'landing_decision_recorded',$2::jsonb)`, q, string(raw))
	must(t, err)
	start := c.Start
	start.AttemptID = d.AttemptID
	_, raw, err = queue.EncodeEvent(start)
	must(t, err)
	_, err = s.pool.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload) VALUES ($1,'landing_started',$2::jsonb)`, q, string(raw))
	must(t, err)
	landed := queue.Landed{BatchID: c.Start.BatchID, NewBaseSHA: d.LandedSHA, NewFence: "second-landed"}
	_, raw, err = queue.EncodeEvent(landed)
	must(t, err)
	_, err = s.pool.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload) VALUES ($1,'landed',$2::jsonb)`, q, string(raw))
	must(t, err)
	var k QueueKey
	must(t, s.pool.QueryRow(t.Context(), `SELECT installation,repository,base,lane FROM queues WHERE id=$1`, q).Scan(&k.Installation, &k.Repository, &k.BaseBranch, &k.Lane))
	first, cursor, found, err := s.ChangeDecisions(t.Context(), k, c.Fresh.Basis.ChangeID, 0, 0, 1)
	must(t, err)
	if !found || len(first) != 1 || cursor == 0 || first[0].Outcome != "abandoned" || first[0].LandedSHA != "" {
		t.Fatalf("first attempt changed: %+v cursor=%d", first, cursor)
	}
	second, next, found, err := s.ChangeDecisions(t.Context(), k, c.Fresh.Basis.ChangeID, 0, cursor, 1)
	must(t, err)
	if !found || len(second) != 1 || next != 0 || second[0].Outcome != "landed" || second[0].Evidence.Decision.AttemptID != "second-attempt" || second[0].LandedSHA != d.LandedSHA || second[0].Evidence.EligibilityEventID == 0 || second[0].EventID != second[0].Evidence.DecisionEventID {
		t.Fatalf("second attempt lost: %+v", second)
	}
}
