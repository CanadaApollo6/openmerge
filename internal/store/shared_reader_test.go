package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	pj "github.com/CanadaApollo6/openmerge/internal/provenance/journal"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func insertReaderEvidence(t *testing.T, s *Store, q QueueID, e any) int64 {
	t.Helper()
	var kind string
	var raw []byte
	var err error
	switch v := e.(type) {
	case pj.Event:
		kind, raw, err = pj.EncodeEvent(v)
	case dependency.Event:
		kind, raw, err = dependency.EncodeEvent(v)
	default:
		t.Fatalf("unknown reader fixture %T", e)
	}
	must(t, err)
	var id int64
	err = s.pool.QueryRow(t.Context(), `INSERT INTO events(queue_id,type,payload) VALUES($1,$2,$3) RETURNING id`, q, kind, string(raw)).Scan(&id)
	must(t, err)
	return id
}
func readerObservation(t *testing.T) pj.Observation {
	t.Helper()
	raw, err := os.ReadFile("../provenance/journal/testdata/complete.json")
	must(t, err)
	e, err := pj.DecodeEvent(raw)
	must(t, err)
	return e.(pj.Observation)
}

func TestSharedReaderLiveLoadReportingAndCursorRestart(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	appendEvents(t, s, q, queue.PRObserved{PR: 2, HeadSHA: "head", Trusted: true, PendingConditions: "sign-off"}, queue.EnqueueRequested{PR: 2, At: epoch})
	legacy, _, err := s.Load(t.Context(), q)
	must(t, err)
	want, err := queue.EncodeSnapshot(legacy)
	must(t, err)
	h := readerObservation(t)
	a, b := h, h
	a.Chunks, b.Chunks, b.Index = 2, 2, 1
	a.Commits, b.Commits = h.Commits[:1], h.Commits[1:]
	insertReaderEvidence(t, s, q, a)
	// Persist a legacy cursor at a partial inventory cut. The restarted reader
	// reconstructs side state from immutable events rather than emitting v9.
	first, err := s.ReadOutsidePushAudit(t.Context(), q)
	must(t, err)
	committed, err := s.CommitOutsidePushAudit(t.Context(), q, first, nil)
	must(t, err)
	if !committed {
		t.Fatal("cursor did not commit")
	}
	var token [32]byte
	token[0] = 1
	init, err := dependency.NewJournalIdentity("openmerge-db-v2:sha256:"+strings.Repeat("a", 64), token, "github:https://github.com", "7")
	must(t, err)
	insertReaderEvidence(t, s, q, init)
	last := insertReaderEvidence(t, s, q, b)
	got, watermark, err := s.Load(t.Context(), q)
	must(t, err)
	raw, err := queue.EncodeSnapshot(got)
	must(t, err)
	if !bytes.Equal(want, raw) || watermark != last {
		t.Fatal("side evidence changed legacy state or watermark")
	}
	journals, err := s.Journals(t.Context(), []QueueKey{{Installation: "installation/1", Repository: "owner/repo", BaseBranch: "main"}})
	must(t, err)
	if len(journals) != 1 || len(journals[0].Events) != 5 {
		t.Fatalf("reporting omitted evidence: %+v", journals)
	}
	rows, err := s.EventsAfter(t.Context(), q, 0)
	must(t, err)
	if len(rows) != 5 {
		t.Fatal("operational scan lost positions")
	}
	resumed, err := s.ReadOutsidePushAudit(t.Context(), q)
	must(t, err)
	if resumed.CursorResetReason != "" || resumed.progress.ScannedThrough != last {
		t.Fatalf("cursor failed to resume: %+v", resumed)
	}
	var header struct{ Version int }
	must(t, json.Unmarshal(resumed.progress.State, &header))
	if header.Version >= 8 {
		t.Fatal("reader emitted a new snapshot version")
	}
	if !bytes.Equal(want, resumed.progress.State) {
		t.Fatal("legacy cursor queue bytes changed")
	}
	committed, err = s.CommitOutsidePushAudit(t.Context(), q, resumed, nil)
	must(t, err)
	if !committed {
		t.Fatal("resumed cursor did not commit")
	}
	again, err := s.ReadOutsidePushAudit(t.Context(), q)
	must(t, err)
	if !bytes.Equal(again.progress.State, want) || again.progress.ScannedThrough != last {
		t.Fatal("peer restart changed cursor state")
	}
}

func TestSharedReaderLiveRejectsUnresolvedLanding(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	h := readerObservation(t)
	id := insertReaderEvidence(t, s, q, h)
	raw, err := os.ReadFile("../provenance/journal/testdata/tested.json")
	must(t, err)
	event, err := pj.DecodeEvent(raw)
	must(t, err)
	l := event.(pj.Landing)
	l.DecisionID = uint64(id)
	l.Members[0].ObservationIDs = []uint64{uint64(id)}
	l.Members[0].RecordDigest = pj.MemberDigest(l.Members[0])
	landingID := insertReaderEvidence(t, s, q, l)
	if _, _, err := s.Load(t.Context(), q); err == nil {
		t.Fatal("live load accepted wrong-kind decision")
	} else if prefix := fmt.Sprintf("event %d:", landingID); strings.Count(err.Error(), prefix) != 1 {
		t.Fatalf("event context must appear exactly once: %v", err)
	}
	if _, err := s.ReportingJournals(t.Context(), []QueueKey{{Installation: "installation/1", Repository: "owner/repo", BaseBranch: "main"}}); err == nil {
		t.Fatal("reporting accepted wrong-kind decision")
	}
}

func TestSharedReaderCursorAcceptsV9WithoutWritingIt(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	h := readerObservation(t)
	id := insertReaderEvidence(t, s, q, h)
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	base, err := queue.EncodeSnapshot(state)
	must(t, err)
	_, payload, err := pj.EncodeEvent(h)
	must(t, err)
	v9, err := json.Marshal(struct {
		Version  int
		BaseKind string
		Base     json.RawMessage
		LastID   uint64
		Records  []pj.Record
	}{9, "queue", base, uint64(id), []pj.Record{{ID: uint64(id), Payload: payload}}})
	must(t, err)
	if _, err := queue.DecodeSnapshot(v9); err == nil {
		t.Fatal("old decoder accepted v9")
	}
	progress, err := json.Marshal(outsidePushProgress{ScannedThrough: id, State: v9})
	must(t, err)
	_, err = s.pool.Exec(t.Context(), `INSERT INTO observations(queue_id,key,value) VALUES($1,$2,$3)`, q, outsidePushCursorKey, progress)
	must(t, err)
	audit, err := s.ReadOutsidePushAudit(t.Context(), q)
	must(t, err)
	if audit.CursorResetReason != "" {
		t.Fatal(audit.CursorResetReason)
	}
	if !bytes.Equal(audit.progress.State, base) {
		t.Fatal("cursor wrote v9 instead of legacy queue bytes")
	}
}
