package store

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestSharedReaderInvalidAuditCursorRecovery(t *testing.T) {
	for name, raw := range map[string][]byte{
		"garbage snapshot":    []byte(`{"ScannedThrough":1,"State":{"garbage":true}}`),
		"non JSON":            []byte(`not-json`),
		"unsupported version": []byte(`{"ScannedThrough":1,"State":{"Version":999}}`),
	} {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			q, claim, c, d := decisionLand(t, s)
			want, watermark, err := s.Load(t.Context(), q)
			must(t, err)
			wantBytes, err := queue.EncodeSnapshot(want)
			must(t, err)
			_, err = s.pool.Exec(t.Context(), `INSERT INTO observations(queue_id,key,value) VALUES($1,$2,$3)`, q, outsidePushCursorKey, raw)
			must(t, err)
			got, gotWatermark, err := s.Load(t.Context(), q)
			must(t, err)
			gotBytes, err := queue.EncodeSnapshot(got)
			must(t, err)
			if !bytes.Equal(gotBytes, wantBytes) || gotWatermark != watermark {
				t.Fatal("cache fallback differs from full journal replay")
			}
			_, err = s.Plan(t.Context(), q, c.Config, c.Start.At)
			must(t, err)
			must(t, commitDecision(t, s, claim, c, d))
			request := sideObservation(sideInventory(t), "recovery")
			before := eventCount(t, s, q)
			if _, err := s.AppendSide(t.Context(), q, request); err == nil {
				t.Fatal("append accepted invalid audit cursor")
			}
			cursor, err := s.Observation(t.Context(), q, outsidePushCursorKey)
			must(t, err)
			if eventCount(t, s, q) != before || !bytes.Equal(cursor, raw) {
				t.Fatal("refused append changed journal or cursor")
			}
			audit, err := s.ReadOutsidePushAudit(t.Context(), q)
			must(t, err)
			if audit.CursorResetReason == "" {
				t.Fatal("audit did not report invalid cursor reset")
			}
			committed, err := s.CommitOutsidePushAudit(t.Context(), q, audit, nil)
			must(t, err)
			if !committed {
				t.Fatal("audit reset CAS failed")
			}
			_, err = s.AppendSide(t.Context(), q, request)
			must(t, err)
			// A fresh load also validates the append checkpoint and upgraded cursor.
			_, _, err = s.Load(t.Context(), q)
			must(t, err)
		})
	}
}

func TestSharedReaderInvalidJournalStillFailsAfterCursorFallback(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	_, err := s.pool.Exec(t.Context(), `INSERT INTO observations(queue_id,key,value) VALUES($1,$2,$3)`, q, outsidePushCursorKey, []byte(`not-json`))
	must(t, err)
	_, err = s.pool.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload) VALUES($1,$2,$3)`, q, "invalid", json.RawMessage(`{"Type":"invalid"}`))
	must(t, err)
	if _, _, err := s.Load(t.Context(), q); err == nil {
		t.Fatal("cursor fallback hid an invalid authoritative journal event")
	}
}
