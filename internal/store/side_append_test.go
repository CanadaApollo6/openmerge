package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/journalreader"
	pj "github.com/CanadaApollo6/openmerge/internal/provenance/journal"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

func sideInventory(t *testing.T) pj.Observation {
	t.Helper()
	h := readerObservation(t)
	h.Request.ChangeID = "chg1_" + strings.Repeat("1", 64)
	h.Digest = pj.InventoryDigest(h, h.Commits)
	return h
}
func sideObservation(h pj.Observation, key string) SideAppend {
	return SideAppend{Count: 1, DedupeKey: key, Build: func([]uint64) ([]journalreader.Event, error) { return []journalreader.Event{{Provenance: h}}, nil }}
}
func sideBundle(t *testing.T, h pj.Observation, attempt, key string, mutate func([]journalreader.Event)) SideAppend {
	t.Helper()
	raw, err := os.ReadFile("../provenance/journal/testdata/tested.json")
	must(t, err)
	return SideAppend{Count: 4, DedupeKey: key, Build: func(ids []uint64) ([]journalreader.Event, error) {
		e, err := pj.DecodeEvent(raw)
		if err != nil {
			return nil, err
		}
		tested := e.(pj.Landing)
		tested.AttemptID, tested.DecisionID = attempt, ids[1]
		member := &tested.Members[0]
		member.Request, member.Mapping.Request = h.Request, h.Request
		member.ObservationIDs = []uint64{ids[0]}
		member.InventoryDigest = h.Digest
		for i := range member.Layers {
			member.Layers[i].References = []uint64{ids[1]}
		}
		member.RecordDigest = pj.MemberDigest(*member)
		landed := tested
		// Detach slices through the canonical codec.
		_, b, err := pj.EncodeEvent(landed)
		if err != nil {
			return nil, err
		}
		e, err = pj.DecodeEvent(b)
		if err != nil {
			return nil, err
		}
		landed = e.(pj.Landing)
		landed.Stage = "landed"
		landed.Members[0].Mapping.Build.Stage = "landed"
		landed.Members[0].Mapping.Build.ID = "composition-L"
		landed.Members[0].RecordDigest = pj.MemberDigest(landed.Members[0])
		decision := queue.LandingDecisionRecorded{BatchID: tested.BatchID, AttemptID: attempt, TestedSHA: h.Request.Head, LandedSHA: h.Request.Head, Variant: "generated_remedy", Remedy: &queue.GeneratedRemedyDecision{ChangeID: queue.ChangeID(h.Request.ChangeID), Revision: 1, Head: h.Request.Head, Suspect: h.Request.Head, Evidence: []string{"fixture"}, OnRed: "revert"}}
		events := []journalreader.Event{{Provenance: h}, {Queue: decision}, {Provenance: tested}, {Provenance: landed}}
		if mutate != nil {
			mutate(events)
		}
		return events, nil
	}}
}

// Decisions are existing journal evidence, recorded outside AppendSide. The
// helper keeps the raw four-event builder for full replay/history fixtures.
func prepareSideBundle(t *testing.T, s *Store, q QueueID, request SideAppend) SideAppend {
	t.Helper()
	sample, err := request.Build([]uint64{1, 2, 3, 4})
	must(t, err)
	decisionID := appendEvents(t, s, q, sample[1].Queue)
	build := request.Build
	request.Count = 3
	request.Build = func(ids []uint64) ([]journalreader.Event, error) {
		events, err := build([]uint64{ids[0], uint64(decisionID), ids[1], ids[2]})
		if err != nil {
			return nil, err
		}
		return []journalreader.Event{events[0], events[2], events[3]}, nil
	}
	return request
}

func cursorVersion(t *testing.T, s *Store, q QueueID) (outsidePushProgress, int) {
	t.Helper()
	raw, err := s.Observation(t.Context(), q, outsidePushCursorKey)
	must(t, err)
	var progress outsidePushProgress
	must(t, json.Unmarshal(raw, &progress))
	var header struct{ Version int }
	must(t, json.Unmarshal(progress.State, &header))
	return progress, header.Version
}

func TestSideAppendValidatesBeforeAnyInsert(t *testing.T) {
	for name, mutate := range map[string]func([]journalreader.Event){
		"missing reference": func(events []journalreader.Event) {
			l := events[3].Provenance.(pj.Landing)
			l.DecisionID = 99999999
			events[3].Provenance = l
		},
		"wrong binding": func(events []journalreader.Event) {
			l := events[3].Provenance.(pj.Landing)
			l.AttemptID = "other"
			events[3].Provenance = l
		},
		"chunk order": func(events []journalreader.Event) {
			h := events[0].Provenance.(pj.Observation)
			h.Chunks = 2
			h.Index = 1
			events[0].Provenance = h
		},
		"member agreement": func(events []journalreader.Event) {
			l := events[3].Provenance.(pj.Landing)
			l.Members[0].InventoryDigest = strings.Repeat("f", 64)
			l.Members[0].RecordDigest = pj.MemberDigest(l.Members[0])
			events[3].Provenance = l
		},
		"queue transition": func(events []journalreader.Event) { events[1].Queue = queue.ResumeRequested{} },
		"ambiguous event":  func(events []journalreader.Event) { events[0].Queue = queue.BaseMoved{SHA: "base"} },
	} {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			q := ensureQueue(t, s, "")
			appendEvents(t, s, q, queue.BaseMoved{SHA: "base"})
			auditPass(t, s, q)
			before, err := s.Observation(t.Context(), q, outsidePushCursorKey)
			must(t, err)
			// A post-rollback row count alone would also pass a validate-after-
			// INSERT implementation. Reject any attempted insert in this schema
			// and require the replay/variant error to win before that trigger.
			request := sideBundle(t, sideInventory(t), "attempt", name, nil)
			if name != "queue transition" && name != "ambiguous event" {
				request = prepareSideBundle(t, s, q, request)
				build := request.Build
				request.Build = func(ids []uint64) ([]journalreader.Event, error) {
					events, err := build(ids)
					if err != nil {
						return nil, err
					}
					// Preserve the raw builder's positions for the invalid companion mutation.
					expanded := []journalreader.Event{events[0], {}, events[1], events[2]}
					mutate(expanded)
					return []journalreader.Event{expanded[0], expanded[2], expanded[3]}, nil
				}
			} else {
				request = sideBundle(t, sideInventory(t), "attempt", name, mutate)
			}
			baseline := count(t, s, `SELECT count(*) FROM events WHERE queue_id=$1`, q)
			_, err = s.pool.Exec(t.Context(), `CREATE FUNCTION forbid_candidate_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'unvalidated candidate reached INSERT'; END $$;
CREATE TRIGGER forbid_candidate BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION forbid_candidate_insert()`)
			must(t, err)
			if _, err := s.AppendSide(t.Context(), q, request); err == nil {
				t.Fatal("invalid bundle accepted")
			} else if strings.Contains(err.Error(), "unvalidated candidate reached INSERT") {
				t.Fatal("validation ran after an insert was attempted", err)
			}
			if got := count(t, s, `SELECT count(*) FROM events WHERE queue_id=$1`, q); got != baseline {
				t.Fatalf("rejected record reached journal: %d", got)
			}
			after, err := s.Observation(t.Context(), q, outsidePushCursorKey)
			must(t, err)
			if !bytes.Equal(before, after) {
				t.Fatal("rejected bundle changed cursor")
			}
			_, _, err = s.Load(t.Context(), q)
			must(t, err)
		})
	}
}

func TestSideAppendCursorUpgradeAtomicAndRollback(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	appendEvents(t, s, q, queue.BaseMoved{SHA: "base"})
	a := auditPass(t, s, q)
	before, err := s.Observation(t.Context(), q, outsidePushCursorKey)
	must(t, err)
	// A DB-side assertion observes the exact transaction at each INSERT, and a
	// deliberate late failure proves that both the first insert and upgrade roll back.
	_, err = s.pool.Exec(t.Context(), `CREATE FUNCTION assert_side_cursor() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
 IF NEW.dedupe_key IN ('rollback','success') THEN
 IF NOT EXISTS(SELECT FROM observations WHERE queue_id=NEW.queue_id AND key='outside-push-audit/v2' AND convert_from(value,'UTF8')::jsonb->'State'->>'Version'='9') THEN RAISE EXCEPTION 'cursor not v9 at insert'; END IF;
 IF NEW.dedupe_key='rollback' THEN RAISE EXCEPTION 'injected late insert failure'; END IF;
 END IF;
 RETURN NEW;
 END $$;
 CREATE TRIGGER side_cursor BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION assert_side_cursor()`)
	must(t, err)
	h := sideInventory(t)
	request := prepareSideBundle(t, s, q, sideBundle(t, h, "attempt", "rollback", nil))
	if _, err := s.AppendSide(t.Context(), q, request); err == nil || !strings.Contains(err.Error(), "injected late insert failure") {
		t.Fatal("did not reach late failure", err)
	}
	after, err := s.Observation(t.Context(), q, outsidePushCursorKey)
	must(t, err)
	if !bytes.Equal(before, after) || count(t, s, `SELECT count(*) FROM events WHERE queue_id=$1`, q) != 2 {
		t.Fatal("transaction rollback leaked rows/cursor")
	}
	request.DedupeKey = "success"
	id, err := s.AppendSide(t.Context(), q, request)
	must(t, err)
	progress, version := cursorVersion(t, s, q)
	if version != 9 || progress.ScannedThrough != a.progress.ScannedThrough || progress.ObservedBase != a.progress.ObservedBase || progress.AuditedBaseMove != a.progress.AuditedBaseMove {
		t.Fatal("cursor upgrade skipped or changed audit bookkeeping")
	}
	got, err := s.AppendSide(t.Context(), q, request)
	must(t, err)
	if got != id || count(t, s, `SELECT count(*) FROM events WHERE queue_id=$1`, q) != 5 {
		t.Fatal("bundle retry duplicated evidence")
	}
	_, last, err := s.Load(t.Context(), q)
	must(t, err)
	if last != id {
		t.Fatal("replay watermark")
	}
	audit := auditPass(t, s, q)
	if audit.progress.ScannedThrough != id {
		t.Fatal("upgraded cursor skipped suffix")
	}
}

func TestSideAppendNoCursorAndDependencyBinding(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	var token [32]byte
	token[0] = 1
	init, err := dependency.NewJournalIdentity("openmerge-db-v2:sha256:"+strings.Repeat("a", 64), token, "github:https://github.com", "7")
	must(t, err)
	request := SideAppend{Count: 1, DedupeKey: "init", Binding: init, Build: func([]uint64) ([]journalreader.Event, error) { return []journalreader.Event{{Dependency: init}}, nil }}
	wrong := request
	var otherToken [32]byte
	otherToken[0] = 2
	wrong.Binding, err = dependency.NewJournalIdentity(init.DatabaseIdentity, otherToken, init.ForgeInstance, init.Installation)
	must(t, err)
	if _, err := s.AppendSide(t.Context(), q, wrong); err == nil {
		t.Fatal("unverified initialization accepted")
	}
	id, err := s.AppendSide(t.Context(), q, request)
	must(t, err)
	progress, version := cursorVersion(t, s, q)
	if version != 9 || progress.ScannedThrough != 0 {
		t.Fatal("first append did not create v9 at zero")
	}
	audit := auditPass(t, s, q)
	if audit.progress.ScannedThrough != id {
		t.Fatal("dependency suffix skipped")
	}
}

func TestSideAppendStaleAuditCannotUndoUpgrade(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	appendEvents(t, s, q, queue.BaseMoved{SHA: "base"})
	auditPass(t, s, q)
	appendEvents(t, s, q, queue.PRObserved{PR: 2, HeadSHA: "head"})
	stale, err := s.ReadOutsidePushAudit(t.Context(), q)
	must(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	request := sideObservation(sideInventory(t), "side")
	build := request.Build
	request.Build = func(ids []uint64) ([]journalreader.Event, error) { close(entered); <-release; return build(ids) }
	writer := make(chan error, 1)
	go func() { _, err := s.AppendSide(t.Context(), q, request); writer <- err }()
	<-entered
	contender := openTestStore(t, s.pool.Config().ConnString())
	locked := make(chan struct{})
	var once sync.Once
	contender.beforeQueueLock = func() { once.Do(func() { close(locked) }) }
	type result struct {
		ok  bool
		err error
	}
	auditDone := make(chan result, 1)
	go func() {
		ok, err := contender.CommitOutsidePushAudit(t.Context(), q, stale, nil)
		auditDone <- result{ok, err}
	}()
	<-locked
	close(release)
	must(t, <-writer)
	resultValue := <-auditDone
	must(t, resultValue.err)
	if resultValue.ok {
		t.Fatal("stale audit overwrote upgraded cursor")
	}
	_, version := cursorVersion(t, s, q)
	if version != 9 {
		t.Fatal("cursor downgraded")
	}
	// Restart resumes the partial side projection and rejects a duplicate chunk.
	s = openTestStore(t, s.pool.Config().ConnString())
	auditPass(t, s, q)
	_, err = s.AppendSide(context.Background(), q, sideObservation(sideInventory(t), "duplicate"))
	if !errors.Is(err, pj.ErrBinding) {
		t.Fatal("restart lost side fence", err)
	}
}

func TestSideAppendConcurrentGenerationCAS(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	h := sideInventory(t)
	id, err := s.AppendSide(t.Context(), q, sideObservation(h, "initial"))
	must(t, err)
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		next := h
		next.PreviousObservationID = uint64(id)
		next.Request.Generation = fmt.Sprint("next-", i)
		next.Digest = pj.InventoryDigest(next, next.Commits)
		go func(i int) {
			<-start
			_, err := s.AppendSide(t.Context(), q, sideObservation(next, fmt.Sprint("key-", i)))
			results <- err
		}(i)
	}
	close(start)
	accepted := 0
	for range 2 {
		err := <-results
		if err == nil {
			accepted++
		} else if !errors.Is(err, pj.ErrBinding) {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatalf("CAS accepted %d contenders", accepted)
	}
}

func TestSideEvidenceLargeHistoryBoundedAuditAndReloadCost(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	// Each isolated schema has its own sequence. Populate a genuinely large prefix
	// in one statement; no live database or forge calls are involved.
	_, err := s.pool.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload) SELECT $1,'refresh_requested','{"version":1,"type":"refresh_requested","data":{}}'::jsonb FROM generate_series(1,260000)`, q)
	must(t, err)
	var last uint64
	must(t, s.pool.QueryRow(t.Context(), `SELECT max(id) FROM events WHERE queue_id=$1`, q).Scan(&last))
	reader := sharedReader(t.Context(), s.pool, q, queue.State{BaseBranch: "main"})
	candidates := map[uint64][]byte{}
	reader.WithLookup(func(id uint64) ([]byte, error) {
		b, ok := candidates[id]
		if !ok {
			return nil, fmt.Errorf("missing %d", id)
		}
		return b, nil
	})
	rows := make([][]any, 0, 8000)
	for i := 0; i < 2000; i++ {
		h := sideInventory(t)
		// Distinct changes ensure settlement also releases current inventories,
		// rather than relying only on same-change supersession.
		h.Request.ChangeID = "chg1_" + fmt.Sprintf("%064x", i)
		h.Request.Generation = fmt.Sprint("generation-", i)
		h.Digest = pj.InventoryDigest(h, h.Commits)
		request := sideBundle(t, h, fmt.Sprint("attempt-", i), fmt.Sprint("bundle-", i), nil)
		ids := []uint64{last + 1, last + 2, last + 3, last + 4}
		events, err := request.Build(ids)
		must(t, err)
		for j, e := range events {
			var kind string
			var raw []byte
			if e.Queue != nil {
				kind, raw, err = queue.EncodeEvent(e.Queue)
			} else {
				kind, raw, err = pj.EncodeEvent(e.Provenance)
			}
			must(t, err)
			candidates[ids[j]] = raw
			_, err = reader.Apply(ids[j], raw)
			must(t, err)
			rows = append(rows, []any{q, kind, string(raw)})
		}
		last += 4
	}
	// COPY uses the same sequence positions consumed by the test's builders.
	copied, err := s.pool.CopyFrom(t.Context(), []string{"events"}, []string{"queue_id", "type", "payload"}, pgx.CopyFromRows(rows))
	must(t, err)
	if copied != 8000 {
		t.Fatal("history fixture incomplete")
	}
	if len(reader.Provenance.Records()) != 0 {
		t.Fatal("settled history stayed in side projection")
	}
	reader.UpgradeCursor()
	raw, err := reader.CursorSnapshot()
	must(t, err)
	progress, err := json.Marshal(outsidePushProgress{ScannedThrough: int64(last), State: raw})
	must(t, err)
	_, err = s.pool.Exec(t.Context(), `INSERT INTO observations(queue_id,key,value) VALUES($1,$2,$3)`, q, outsidePushCursorKey, progress)
	must(t, err)
	_, err = s.pool.Exec(t.Context(), `ANALYZE events`)
	must(t, err)
	trace := traceAuditQueries(t, s)
	for pass := 0; pass < 3; pass++ {
		trace.reset()
		a := auditPass(t, s, q)
		trace.assertSuffix(t, int64(last), 0)
		if pass == 0 {
			query := trace.queries[0]
			var planRaw []byte
			must(t, s.pool.QueryRow(t.Context(), "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+query.SQL, query.Args...).Scan(&planRaw))
			var plan []struct {
				Plan struct {
					Hit  int `json:"Shared Hit Blocks"`
					Read int `json:"Shared Read Blocks"`
				}
				Execution float64 `json:"Execution Time"`
			}
			must(t, json.Unmarshal(planRaw, &plan))
			if len(plan) != 1 || plan[0].Plan.Hit+plan[0].Plan.Read > 16 {
				t.Fatalf("side-era idle query scanned historical buffers: %s", planRaw)
			}
			t.Logf("268000-row idle suffix plan: %d shared buffers, %.3f ms SQL execution", plan[0].Plan.Hit+plan[0].Plan.Read, plan[0].Execution)
		}
		if a.CursorResetReason != "" {
			t.Fatal(a.CursorResetReason)
		}
	}
	t.Logf("v9 load at 268000 rows: %d cursor bytes, 0 reference queries; each idle poll: 1 suffix query, 0 rows", len(raw))
	// Snapshot restore also keeps old attempt/generation duplicate fences.
	h := sideInventory(t)
	h.Request.ChangeID = "chg1_" + strings.Repeat("0", 64)
	h.Request.Generation = "generation-0"
	h.Digest = pj.InventoryDigest(h, h.Commits)
	if _, err := s.AppendSide(t.Context(), q, sideObservation(h, "old-generation")); !errors.Is(err, pj.ErrBinding) {
		t.Fatal("retired generation accepted", err)
	}
	// Inspect the source evidence directly: compaction does not alter or remove
	// the inventory, tested/landed pair, decision or closure references.
	if got := count(t, s, `SELECT count(*) FROM events WHERE queue_id=$1`, q); got != 268000 {
		t.Fatalf("audit evidence pruned: %d", got)
	}
	appendEvents(t, s, q, queue.PRObserved{PR: 2, HeadSHA: "head"})
	trace.reset()
	a := auditPass(t, s, q)
	trace.assertSuffix(t, int64(last), 1)
	trace.reset()
	auditPass(t, s, q)
	trace.assertSuffix(t, a.progress.ScannedThrough, 0)
}

func TestSidePendingV9ReloadHasFixedReferenceBudget(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	h := sideInventory(t)
	observationID, err := s.AppendSide(t.Context(), q, sideObservation(h, "inventory"))
	must(t, err)
	for i := 0; i < pj.MaxPendingLandings; i++ {
		request := sideBundle(t, h, fmt.Sprint("pending-", i), fmt.Sprint("pending-key-", i), nil)
		build := request.Build
		events, err := build([]uint64{uint64(observationID), 1, 2, 3})
		must(t, err)
		decisionID := appendEvents(t, s, q, events[1].Queue)
		request.Count = 1
		request.Build = func(ids []uint64) ([]journalreader.Event, error) {
			events, err := build([]uint64{uint64(observationID), uint64(decisionID), ids[0], ids[0] + 1})
			if err != nil {
				return nil, err
			}
			return events[2:3], nil
		}
		_, err = s.AppendSide(t.Context(), q, request)
		must(t, err)
	}
	auditPass(t, s, q)
	trace := traceAuditQueries(t, s)
	trace.reset()
	started := time.Now()
	a, err := s.ReadOutsidePushAudit(t.Context(), q)
	must(t, err)
	elapsed := time.Since(started)
	if a.CursorResetReason != "" {
		t.Fatal(a.CursorResetReason)
	}
	trace.mu.Lock()
	if len(trace.queries) != pj.MaxPendingLandings+1 || trace.rows != pj.MaxPendingLandings {
		t.Fatalf("reload query budget: %d queries/%d rows", len(trace.queries), trace.rows)
	}
	for _, query := range trace.queries[:len(trace.queries)-1] {
		if !strings.Contains(query.SQL, "queue_id=$1 AND id=$2") {
			t.Fatalf("reload scanned history: %s", query.SQL)
		}
	}
	trace.mu.Unlock()
	t.Logf("v9 with %d unfinished attempts: %d exact-ID queries + 1 empty suffix, %s reload", pj.MaxPendingLandings, pj.MaxPendingLandings, elapsed)
	// Preflight refuses another unfinished companion without poisoning replay.
	request := sideBundle(t, h, "pending-overflow", "pending-overflow", nil)
	build := request.Build
	events, err := build([]uint64{uint64(observationID), 1, 2, 3})
	must(t, err)
	decisionID := appendEvents(t, s, q, events[1].Queue)
	request.Count = 1
	request.Build = func(ids []uint64) ([]journalreader.Event, error) {
		events, err := build([]uint64{uint64(observationID), uint64(decisionID), ids[0], ids[0] + 1})
		if err != nil {
			return nil, err
		}
		return events[2:3], nil
	}
	before := count(t, s, `SELECT count(*) FROM events WHERE queue_id=$1`, q)
	if _, err := s.AppendSide(t.Context(), q, request); !errors.Is(err, pj.ErrLimit) {
		t.Fatal("pending overflow committed", err)
	}
	if count(t, s, `SELECT count(*) FROM events WHERE queue_id=$1`, q) != before {
		t.Fatal("preflight leaked candidate decision")
	}
	_, _, err = s.Load(t.Context(), q)
	must(t, err)
}

func TestSideDedupeRejectsDifferentBytesAndBundleLength(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	h := sideInventory(t)
	request := prepareSideBundle(t, s, q, sideBundle(t, h, "attempt", "dedupe", nil))
	_, err := s.AppendSide(t.Context(), q, request)
	must(t, err)
	for _, kind := range []string{"bytes", "length"} {
		t.Run(kind, func(t *testing.T) {
			changed := request
			build := request.Build
			if kind == "length" {
				changed.Count = 1
				changed.Build = func(ids []uint64) ([]journalreader.Event, error) {
					events, err := build([]uint64{ids[0], ids[0] + 1, ids[0] + 2})
					return events[:1], err
				}
			} else {
				changed.Build = func(ids []uint64) ([]journalreader.Event, error) {
					events, err := build(ids)
					if err != nil {
						return nil, err
					}
					obs := events[0].Provenance.(pj.Observation)
					obs.Request.Generation = "different"
					obs.Digest = pj.InventoryDigest(obs, obs.Commits)
					events[0].Provenance = obs
					return events, nil
				}
			}
			if _, err := s.AppendSide(t.Context(), q, changed); err == nil {
				t.Fatal("dedupe accepted different bundle")
			}
		})
	}
	if count(t, s, `SELECT count(*) FROM events WHERE queue_id=$1`, q) != 4 {
		t.Fatal("dedupe mismatch published rows")
	}
}

func TestSideAppendRefusesLandingDecision(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	request := sideBundle(t, sideInventory(t), "attempt", "decision-bypass", nil)
	if _, err := s.AppendSide(t.Context(), q, request); err == nil || !strings.Contains(err.Error(), "does not admit queue events") {
		t.Fatal("unvalidated decision admitted", err)
	}
	if count(t, s, `SELECT count(*) FROM events WHERE queue_id=$1`, q) != 0 {
		t.Fatal("partial decision bundle")
	}
}

func TestSideAppendAndLoadResumeCheckpointWithOnlySuffixRows(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	_, err := s.pool.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload) SELECT $1,'refresh_requested','{"version":1,"type":"refresh_requested","data":{}}'::jsonb FROM generate_series(1,2000)`, q)
	must(t, err)
	h := sideInventory(t)
	last, err := s.AppendSide(t.Context(), q, sideObservation(h, "initial"))
	must(t, err)
	trace := traceAuditQueries(t, s)
	for i := 0; i < 20; i++ {
		h.Request.Generation = fmt.Sprint("checkpoint-next-", i)
		h.PreviousObservationID = uint64(last)
		h.Digest = pj.InventoryDigest(h, h.Commits)
		trace.reset()
		last, err = s.AppendSide(t.Context(), q, sideObservation(h, fmt.Sprint("next-", i)))
		must(t, err)
		trace.mu.Lock()
		queries, rows := len(trace.queries), trace.rows
		suffix := 0
		for _, query := range trace.queries {
			if strings.Contains(query.SQL, "id>$2") {
				suffix++
				if query.Args[1].(int64) < 2000 {
					t.Fatal("append replayed historical prefix")
				}
			}
		}
		trace.mu.Unlock()
		if queries != 2 || rows != 0 || suffix != 1 {
			t.Fatalf("append read history: %d queries, %d rows, %d suffix pages", queries, rows, suffix)
		}
		trace.reset()
		_, watermark, err := s.Load(t.Context(), q)
		must(t, err)
		trace.mu.Lock()
		queries, rows = len(trace.queries), trace.rows
		trace.mu.Unlock()
		if watermark != last || queries != 1 || rows != 0 {
			t.Fatalf("load read history: %d queries/%d rows at %d", queries, rows, watermark)
		}
	}
	t.Log("2000-row prefix, 20 appends: each append 1 dedupe + 1 empty suffix query; each Load 1 empty suffix query, 0 historical rows")
}
