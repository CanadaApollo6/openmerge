package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/audit"
	"github.com/jackc/pgx/v5"
)

// Expected edges come from the journal, not the resolver's exported references.
// The original owner connection also provides the independent oracle for restore.
func retentionClosure(t *testing.T, db *pgx.Conn, dir string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "selection.jsonl"))
	retentionMust(t, err)
	selected := map[int64]audit.Selection{}
	var queueID, watermark int64
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var row audit.Selection
		retentionMust(t, json.Unmarshal(line, &row))
		selected[row.Source.ID] = row
		if queueID != 0 && queueID != row.Source.QueueID {
			t.Fatal("closure fixture spans queues")
		}
		queueID = row.Source.QueueID
		watermark = max(watermark, row.Source.ID)
	}
	retentionMust(t, retentionReachable(selected))
	type settlement struct {
		id, start, decision, checks  int64
		kind, batch, attempt, tested string
	}
	// The latest preceding start identifies the actual settled attempt. Decision
	// and check inputs must independently agree on that same batch and attempt.
	rows, err := db.Query(t.Context(), `SELECT a.id,a.type,s.id,d.id,c.id,
 s.payload->'data'->>'batch_id',s.payload->'data'->>'attempt_id',c.payload->'data'->>'tested_sha'
 FROM events a JOIN events s ON s.queue_id=a.queue_id AND s.type='landing_started'
 AND s.id=(SELECT max(id) FROM events WHERE queue_id=a.queue_id AND type='landing_started' AND payload->'data'->>'batch_id'=a.payload->'data'->>'batch_id' AND id<a.id)
 JOIN events d ON d.queue_id=a.queue_id AND d.type='landing_decision_recorded' AND d.payload->'data'->>'batch_id'=s.payload->'data'->>'batch_id' AND d.payload->'data'->>'attempt_id'=s.payload->'data'->>'attempt_id'
 JOIN events c ON c.queue_id=a.queue_id AND c.type='landing_checks_recorded' AND c.payload->'data'->>'batch_id'=s.payload->'data'->>'batch_id' AND c.payload->'data'->>'attempt_id'=s.payload->'data'->>'attempt_id'
 WHERE a.queue_id=$1 AND a.id<=$2 AND a.type IN ('landed','landing_abandoned') ORDER BY a.id`, queueID, watermark)
	retentionMust(t, err)
	var settlements []settlement
	for rows.Next() {
		var v settlement
		retentionMust(t, rows.Scan(&v.id, &v.kind, &v.start, &v.decision, &v.checks, &v.batch, &v.attempt, &v.tested))
		settlements = append(settlements, v)
	}
	retentionMust(t, rows.Err())
	rows.Close()
	if len(settlements) != 2 || settlements[0].kind != "landing_abandoned" || settlements[1].kind != "landed" {
		t.Fatal("missing independent settlement oracle", settlements)
	}
	for _, v := range settlements {
		edge := func(owner, target int64, ownerKind, targetKind, kind string, identity []string) audit.Reference {
			t.Helper()
			o, ok := selected[owner]
			source, exists := selected[target]
			if !ok || !exists || o.Source.Type != ownerKind || source.Source.Type != targetKind {
				t.Fatalf("closure oracle missing %s or %s source", ownerKind, targetKind)
			}
			identityJSON, err := json.Marshal(identity)
			retentionMust(t, err)
			var matches []audit.Reference
			for _, ref := range o.References {
				if ref.ExpectedType == targetKind && ref.Identity == audit.Identity(identityJSON) {
					matches = append(matches, ref)
				}
			}
			if len(matches) != 1 {
				t.Fatalf("missing independent closure edge %s -> %s (%d -> %d)", ownerKind, targetKind, owner, target)
			}
			ref := matches[0]
			if ref.Source == nil || *ref.Source != source.Source || ref.Kind != kind || ref.Status != "recorded" || ref.Relation != "relied_on" || ref.Owner.QueueID != queueID || ref.Owner.EventID != owner || ref.Owner.BatchID != v.batch || ref.Owner.AttemptID != v.attempt {
				t.Fatalf("wrong independent closure edge %s -> %s: %+v", ownerKind, targetKind, ref)
			}
			if (ownerKind == "landing_decision_recorded" || ownerKind == "landing_checks_recorded") && ref.Owner.TestedSHA != v.tested {
				t.Fatal("wrong independent check owner SHA", ref)
			}
			return ref
		}
		edge(v.id, v.start, v.kind, "landing_started", "settlement", []string{v.batch})
		edge(v.decision, v.id, "landing_decision_recorded", v.kind, "settlement", []string{v.batch})
		edge(v.start, v.id, "landing_started", v.kind, "settlement", []string{v.batch})
		edge(v.start, v.decision, "landing_started", "landing_decision_recorded", "settlement", []string{v.batch, v.attempt})
		edge(v.decision, v.start, "landing_decision_recorded", "landing_started", "settlement", []string{v.batch, v.attempt})
		edge(v.decision, v.checks, "landing_decision_recorded", "landing_checks_recorded", "check", []string{v.batch, v.attempt})
		if v.kind != "landing_abandoned" {
			continue
		}
		// Every retry family is a pre-window dependency of the abandoned attempt's
		// check record. Pin exact IDs and run/generation metadata from raw rows.
		var requestGeneration uint64
		retentionMust(t, db.QueryRow(t.Context(), `SELECT (payload->'data'->>'generation')::bigint FROM events WHERE queue_id=$1 AND type='check_rerun_requested' AND id<$2 AND payload->'data'->>'batch_id'=$3 AND payload->'data'->>'tested_sha'=$4 ORDER BY id DESC LIMIT 1`, queueID, v.checks, v.batch, v.tested).Scan(&requestGeneration))
		for _, kind := range []string{"checks_observed", "check_rerun_requested", "check_rerun_dispatch_progress", "check_rerun_dispatched"} {
			var id int64
			retentionMust(t, db.QueryRow(t.Context(), `SELECT max(id) FROM events WHERE queue_id=$1 AND type=$2 AND id<$3 AND payload->'data'->>'batch_id'=$4 AND payload->'data'->>'tested_sha'=$5`, queueID, kind, v.checks, v.batch, v.tested).Scan(&id))
			ref := edge(v.checks, id, "landing_checks_recorded", kind, "check", []string{"app:15368", "ci"})
			if selected[id].Role != "dependency" {
				t.Fatal("retry source is not a narrow-window dependency", kind, id)
			}
			var generation uint64
			if kind == "checks_observed" {
				var runID, attempt uint64
				retentionMust(t, db.QueryRow(t.Context(), `SELECT (payload->'data'->'runs'->0->>'id')::bigint,(payload->'data'->'runs'->0->>'attempt')::bigint FROM events WHERE id=$1`, id).Scan(&runID, &attempt))
				if ref.RunID != runID || ref.Attempt != attempt {
					t.Fatal("wrong independent retry observation identity", ref)
				}
			} else {
				retentionMust(t, db.QueryRow(t.Context(), `SELECT (payload->'data'->>'generation')::bigint FROM events WHERE id=$1`, id).Scan(&generation))
				if ref.Generation != generation || generation != requestGeneration {
					t.Fatal("wrong independent retry generation", ref)
				}
			}
		}
	}
}

func retentionReachable(selected map[int64]audit.Selection) error {
	visited := map[int64]bool{}
	dependencies, roots := 0, 0
	var visit func(int64) error
	visit = func(id int64) error {
		if visited[id] {
			return nil
		}
		row, ok := selected[id]
		if !ok {
			return fmt.Errorf("recursive closure missing source %d", id)
		}
		visited[id] = true
		for _, ref := range row.References {
			if ref.Source == nil {
				continue
			}
			if source, ok := selected[ref.Source.ID]; !ok || source.Source != *ref.Source {
				return fmt.Errorf("recursive closure dangling reference %d", ref.Source.ID)
			}
			if err := visit(ref.Source.ID); err != nil {
				return err
			}
		}
		return nil
	}
	for id, row := range selected {
		if row.Role == "dependency" {
			dependencies++
		}
		if row.Role == "scope" {
			roots++
			if err := visit(id); err != nil {
				return err
			}
		}
	}
	if roots == 0 || dependencies == 0 {
		return fmt.Errorf("narrow closure requires scope and dependency rows")
	}
	if len(visited) != len(selected) {
		return fmt.Errorf("unreachable historical dependency")
	}
	return nil
}

func TestRetentionReachable(t *testing.T) {
	source := func(id int64) audit.Source { return audit.Source{ID: id, QueueID: 1} }
	row := func(id int64, role string, targets ...int64) audit.Selection {
		r := audit.Selection{Source: source(id), Role: role}
		for _, target := range targets {
			s := source(target)
			r.References = append(r.References, audit.Reference{Source: &s})
		}
		return r
	}
	for _, tc := range []struct {
		name  string
		rows  []audit.Selection
		valid bool
	}{
		{"recursive", []audit.Selection{row(1, "scope", 2), row(2, "dependency", 3), row(3, "dependency", 2)}, true},
		{"dangling", []audit.Selection{row(1, "scope", 2), row(2, "dependency", 3)}, false},
		{"orphan", []audit.Selection{row(1, "scope", 2), row(2, "dependency"), row(3, "dependency")}, false},
		{"all scope", []audit.Selection{row(1, "scope")}, false},
		{"no root", []audit.Selection{row(1, "dependency")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected := map[int64]audit.Selection{}
			for _, r := range tc.rows {
				selected[r.Source.ID] = r
			}
			if err := retentionReachable(selected); (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}
