package audit

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

func TestReplayRejectsUnreachableDependencyCycle(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	rootID := insert(t, f.admin, 1, "2026-10-22T12:00:00Z")
	startID := closureEvent(t, f, "2026-10-22T14:00:00Z", landingStart("orphan"))
	landID := closureEvent(t, f, "2026-10-22T14:01:00Z", landingResult("orphan"))
	original := filepath.Join(t.TempDir(), "original")
	_, err := f.reader.Export(t.Context(), req(), original)
	must(t, err)
	artifact := buildReplayArtifact(t, f, original,
		[]sourceEvent{replayEvent(t, f, rootID), replayEvent(t, f, startID), replayEvent(t, f, landID)},
		[]string{"scope", "dependency", "dependency"})
	selections := readSelections(t, artifact)
	if len(selections) != 3 || !referencesSource(selections[1].References, "landed", landID) || !referencesSource(selections[2].References, "landing_started", startID) {
		t.Fatal("fixture did not create an unreachable two-node dependency cycle")
	}
	if _, err = f.reader.Replay(t.Context(), filepath.Join(artifact, "manifest.json"), filepath.Join(t.TempDir(), "replay")); err != errIntegrity {
		t.Fatalf("got %v want integrity rejection", err)
	}
}

func TestReplayRejectsNotRecordedReferenceWithSourceID(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	startID := closureEvent(t, f, "2026-10-22T12:00:00Z", landingStart("attempt"))
	dir := filepath.Join(t.TempDir(), "original")
	_, err := f.reader.Export(t.Context(), req(), dir)
	must(t, err)
	rewriteSelections(t, dir, func(selections []Selection) {
		for i := range selections[0].References {
			r := &selections[0].References[i]
			if r.Status == "not_recorded" {
				r.Source = &Source{QueueID: 1, ID: startID, Type: r.ExpectedType, Queue: selections[0].Source.Queue}
				return
			}
		}
		t.Fatal("fixture has no explicit historical absence")
	})
	if _, err = f.reader.Replay(t.Context(), filepath.Join(dir, "manifest.json"), filepath.Join(t.TempDir(), "replay")); err != errIntegrity {
		t.Fatalf("got %v want integrity rejection", err)
	}
}

func TestReplayRejectsResolvedSettlementRewrittenAsAbsent(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	closureEvent(t, f, "2026-10-22T12:10:00Z", landingStart("attempt"))
	closureEvent(t, f, "2026-10-22T12:20:00Z", landingResult("attempt"))
	request := req()
	dir := filepath.Join(t.TempDir(), "original")
	_, err := f.reader.Export(t.Context(), request, dir)
	must(t, err)
	rewriteSelections(t, dir, func(selections []Selection) {
		if len(selections) != 2 || selections[0].Role != "scope" || selections[1].Role != "scope" {
			t.Fatal("fixture must retain both settlement events as scope roots")
		}
		found := false
		for i := range selections[0].References {
			r := &selections[0].References[i]
			if r.ExpectedType == "landed" {
				if r.Status != "recorded" || r.Source == nil || r.Source.ID != selections[1].Source.ID {
					t.Fatal("fixture did not resolve the in-range landed source")
				}
				r.Status = "not_recorded"
				r.Source = nil
				found = true
			}
		}
		if !found {
			t.Fatal("fixture has no landed reference")
		}
		selections[0].References = normalizeReferences(selections[0].References)
	})
	if _, err = f.reader.Replay(t.Context(), filepath.Join(dir, "manifest.json"), filepath.Join(t.TempDir(), "replay")); err != errIntegrity {
		t.Fatalf("got %v want integrity rejection", err)
	}
}

func TestReplayUsesItsOwnReaderSnapshot(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	checks, _, _ := closureSharedChecks(t, f)
	checks.AttemptID = "attempt"
	closureEvent(t, f, "2026-10-22T12:00:00Z", checks)
	closureEvent(t, f, "2026-10-22T12:01:00Z", queue.LandingStarted{BatchID: checks.BatchID, AttemptID: checks.AttemptID})
	trace := &queryTrace{}
	r := traced(t, f, trace)
	dir := filepath.Join(t.TempDir(), "export")
	_, err := r.Export(t.Context(), req(), dir)
	must(t, err)
	_, err = r.Replay(t.Context(), filepath.Join(dir, "manifest.json"), filepath.Join(t.TempDir(), "replay"))
	must(t, err)
	trace.mu.Lock()
	defer trace.mu.Unlock()
	var snapshots int
	for _, query := range trace.queries {
		if query == "begin isolation level repeatable read read only" {
			snapshots++
		}
	}
	if snapshots != 2 {
		t.Fatalf("snapshot transactions=%d want export and replay transactions", snapshots)
	}
}

func TestValidResolutionRequiresExactReferenceAndDependencySets(t *testing.T) {
	owner := Source{QueueID: 1, ID: 8, Type: "landing_started"}
	dependency := Source{QueueID: 1, ID: 7, Type: "landed"}
	ref := Reference{Kind: "settlement", Relation: "relied_on", Status: "recorded", Owner: EvidenceOwner{QueueID: 1, EventID: owner.ID}, ExpectedType: "landed", Identity: "batch", Source: &dependency}
	absence := Reference{Kind: "settlement", Relation: "relied_on", Status: "not_recorded", Owner: EvidenceOwner{QueueID: 1, EventID: owner.ID}, ExpectedType: "landing_abandoned", Identity: "batch"}
	dep := sourceEvent{source: dependency, payload: []byte(`{"type":"landed"}`)}
	other := sourceEvent{source: Source{QueueID: 1, ID: 6, Type: "landing_abandoned"}, payload: []byte(`{"type":"landing_abandoned"}`)}
	cases := []struct {
		name string
		refs []Reference
		deps []sourceEvent
		want bool
	}{
		{name: "matching recorded source", refs: []Reference{ref}, deps: []sourceEvent{dep}, want: true},
		{name: "explicit absence", refs: []Reference{absence}, want: true},
		{name: "unemitted recorded source", refs: []Reference{ref}},
		{name: "unreferenced emitted source", deps: []sourceEvent{dep}},
		{name: "extra emitted source", refs: []Reference{ref}, deps: []sourceEvent{dep, other}},
		{name: "wrong owner", refs: []Reference{{Kind: ref.Kind, Relation: ref.Relation, Status: ref.Status, Owner: EvidenceOwner{QueueID: 1, EventID: 9}, ExpectedType: ref.ExpectedType, Identity: ref.Identity, Source: ref.Source}}, deps: []sourceEvent{dep}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validResolution(owner, tc.refs, tc.deps); got != tc.want {
				t.Fatalf("validResolution=%t want %t", got, tc.want)
			}
		})
	}
}

func TestValidReferencesRejectsInventedAbsenceAndDuplicates(t *testing.T) {
	ref := Reference{Kind: "settlement", Relation: "relied_on", Status: "not_recorded", Owner: EvidenceOwner{QueueID: 1, EventID: 8}, ExpectedType: "landing_started", Identity: "batch"}
	if !validReferences([]Reference{ref}) {
		t.Fatal("valid historical absence rejected")
	}
	withSource := ref
	withSource.Source = &Source{QueueID: 1, ID: 7, Type: ref.ExpectedType}
	if validReferences([]Reference{withSource}) {
		t.Fatal("historical absence accepted a fabricated source ID")
	}
	if validReferences([]Reference{ref, ref}) {
		t.Fatal("adjacent duplicate references accepted")
	}
}

func TestSpoolRejectsConflictingDuplicateRecords(t *testing.T) {
	source := Source{QueueID: 1, ID: 5, Type: "history", Queue: Key{Installation: "i", Repository: "owner/repo", Base: "main"}}
	refA := Reference{Identity: "a"}
	refB := Reference{Identity: "b"}
	for _, tc := range []struct {
		name string
		a, b spoolRecord
	}{
		{name: "payload", a: spoolRecord{Source: source, Payload: json.RawMessage(`{"n":1}`), Role: "scope"}, b: spoolRecord{Source: source, Payload: json.RawMessage(`{"n":2}`), Role: "scope"}},
		{name: "references", a: spoolRecord{Source: source, Payload: json.RawMessage(`{}`), Role: "scope", References: []Reference{refA}}, b: spoolRecord{Source: source, Payload: json.RawMessage(`{}`), Role: "scope", References: []Reference{refB}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			input, output := filepath.Join(dir, "input"), filepath.Join(dir, "output")
			must(t, spoolFile(input, func(w io.Writer) error {
				if err := spoolWrite(w, tc.a); err != nil {
					return err
				}
				return spoolWrite(w, tc.b)
			}))
			if err := sortSpool(dir, input, output); err != errIntegrity {
				t.Fatalf("got %v want conflicting duplicate rejection", err)
			}
		})
	}
}

func TestSpoolDeduplicatesAdjacentRecordsAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	input, output := filepath.Join(dir, "input"), filepath.Join(dir, "output")
	base := func(id int64) spoolRecord {
		return spoolRecord{Source: Source{QueueID: 1, ID: id, Type: "history", Queue: Key{Installation: "i", Repository: "owner/repo", Base: "main"}}, Payload: json.RawMessage(`{}`), Role: "dependency"}
	}
	shared := base(10000)
	must(t, spoolFile(input, func(w io.Writer) error {
		for i := 1; i < pageSize; i++ {
			if err := spoolWrite(w, base(int64(i))); err != nil {
				return err
			}
		}
		if err := spoolWrite(w, shared); err != nil {
			return err
		}
		return spoolWrite(w, shared)
	}))
	must(t, sortSpool(dir, input, output))
	c, err := openSpool(output)
	must(t, err)
	defer func() { _ = c.f.Close() }()
	count := 0
	for c.ok {
		count++
		if err = c.next(); err != nil {
			t.Fatal(err)
		}
	}
	if count != pageSize {
		t.Fatalf("sorted rows=%d want %d after adjacent duplicate merge", count, pageSize)
	}
}

func TestSourceLessUsesQueueIDForEqualKeys(t *testing.T) {
	key := Key{Installation: "i", Repository: "owner/repo", Base: "main"}
	a := Source{QueueID: 1, ID: 99, Queue: key}
	b := Source{QueueID: 2, ID: 1, Queue: key}
	if !sourceLess(a, b) || sourceLess(b, a) {
		t.Fatal("queue ID is not the stable tie breaker for equal queue keys")
	}
}

func buildReplayArtifact(t *testing.T, f *fixture, base string, events []sourceEvent, roles []string) string {
	t.Helper()
	if len(events) != len(roles) {
		t.Fatal("event and role counts differ")
	}
	var m Manifest
	must(t, json.Unmarshal(read(t, filepath.Join(base, "manifest.json")), &m))
	waters := readWatermarks(t, base)
	if len(waters) != 1 {
		t.Fatal("test fixture expects one queue watermark")
	}
	selections := make([]Selection, len(events))
	must(t, f.reader.snapshot(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		for i, event := range events {
			refs, deps, err := resolveEvidenceAt(ctx, tx, event, waters[0].ID)
			if err != nil {
				return err
			}
			if !validResolution(event.source, refs, deps) {
				return errIntegrity
			}
			selections[i], err = selectionFor(event, roles[i], refs)
			if err != nil {
				return err
			}
		}
		return nil
	}))
	dir := t.TempDir()
	var result Manifest
	must(t, build(dir, m.Request, func(emit func(sourceEvent, *Selection) error, water func(watermark) error) error {
		for _, w := range waters {
			if err := water(w); err != nil {
				return err
			}
		}
		for i, event := range events {
			if err := emit(event, &selections[i]); err != nil {
				return err
			}
		}
		return nil
	}, nil, &result))
	return dir
}

func replayEvent(t *testing.T, f *fixture, id int64) sourceEvent {
	t.Helper()
	var event sourceEvent
	var at time.Time
	if err := f.admin.QueryRow(t.Context(), `SELECT e.queue_id,q.installation,q.repository,q.base,q.lane,e.type,e.recorded_at,e.payload::text FROM events e JOIN queues q ON q.id=e.queue_id WHERE e.id=$1`, id).Scan(&event.source.QueueID, &event.source.Queue.Installation, &event.source.Queue.Repository, &event.source.Queue.Base, &event.source.Queue.Lane, &event.source.Type, &at, &event.payload); err != nil {
		t.Fatal(err)
	}
	event.source.ID = id
	event.source.RecordedAt = stamp(at)
	return event
}

func referencesSource(refs []Reference, expectedType string, id int64) bool {
	for _, ref := range refs {
		if ref.ExpectedType == expectedType && ref.Source != nil && ref.Source.ID == id {
			return true
		}
	}
	return false
}

func landingStart(attempt string) queue.LandingStarted {
	return queue.LandingStarted{BatchID: "orphan-cycle", AttemptID: attempt}
}

func landingResult(attempt string) queue.Landed {
	return queue.Landed{BatchID: "orphan-cycle", NewBaseSHA: "base-" + attempt, NewFence: "fence-" + attempt}
}
