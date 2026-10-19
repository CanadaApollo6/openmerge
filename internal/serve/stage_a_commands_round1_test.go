package serve

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestStageACommandTransientWriteGrantIsRetried(t *testing.T) {
	t.Parallel()
	s, w, f := shadowFixture(t, 1, "main", "7", true)
	digest := shadowDigest(t, s, w)
	native := shadowNative(f, w, shadowComment(100, "@openmerge approve "+digest))
	f.SetNativeReviews(repo, 1, native)
	f.FailNext("WriteGrant", errors.New("temporary 503"))

	if err := s.commands(t.Context(), w, 1); err == nil || !strings.Contains(err.Error(), "temporary 503") {
		t.Fatalf("first command pass error = %v, want transient WriteGrant error", err)
	}
	if raw, err := s.store.Observation(t.Context(), w.id, "comment/100"); err != nil {
		t.Fatal(err)
	} else if len(raw) != 0 {
		t.Fatalf("transient WriteGrant failure consumed command: %s", raw)
	}

	if err := s.commands(t.Context(), w, 1); err != nil {
		t.Fatal(err)
	}
	state := shadowState(t, s, w)
	r, _ := state.PRRevision(1)
	if receipt := state.ReceiptRecords(r.ChangeID)["100"]; !receipt.Accepted {
		t.Fatalf("retry did not accept receipt: %+v", receipt)
	}
}

func TestStageAApprovalCommandsHonorQuotaWithoutConsumption(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		body func(string) string
	}{
		{name: "approve", body: func(digest string) string { return "@openmerge approve " + digest }},
		{name: "unapprove", body: func(string) string { return "@openmerge unapprove" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, w, f := shadowFixture(t, 1, "main", "7", true)
			body := tt.body(shadowDigest(t, s, w))
			f.SetNativeReviews(repo, 1, shadowNative(f, w, shadowComment(100, body)))
			before, err := s.store.Watermark(t.Context(), w.id)
			must(t, err)
			cursorBefore, err := s.store.Observation(t.Context(), w.id, "cursor/1")
			must(t, err)
			s.options.BackoffUntil = func() time.Time { return time.Now().Add(time.Hour) }

			err = s.commands(t.Context(), w, 1)
			var backoff *quotaBackoff
			if !errors.As(err, &backoff) {
				t.Fatalf("approval command error = %v, want quota backoff", err)
			}
			after, err := s.store.Watermark(t.Context(), w.id)
			must(t, err)
			if after != before {
				t.Fatalf("quota-blocked approval appended %d events", after-before)
			}
			cursorAfter, err := s.store.Observation(t.Context(), w.id, "cursor/1")
			must(t, err)
			if !reflect.DeepEqual(cursorAfter, cursorBefore) {
				t.Fatalf("quota-blocked approval advanced cursor: before=%q after=%q", cursorBefore, cursorAfter)
			}
			if raw, err := s.store.Observation(t.Context(), w.id, "comment/100"); err != nil {
				t.Fatal(err)
			} else if len(raw) != 0 {
				t.Fatalf("quota-blocked approval consumed comment: %s", raw)
			}
		})
	}
}

func TestLegacyAndStageACommandsHonorQuotaWithoutConsumption(t *testing.T) {
	t.Parallel()
	legacy, legacyWorker, legacyForge, _ := fixture(t, repositoryConfig)
	stage, stageWorker, stageForge := shadowFixture(t, 1, "main", "7", false)
	legacyForge.AddComment(repo, 1, "writer", "@openmerge queue")
	legacyComments := legacyForge.CommentRecords(repo, 1)
	if len(legacyComments) == 0 {
		t.Fatal("quota parity comment was not added")
	}
	stageForge.SetNativeReviews(repo, 1, shadowNative(stageForge, stageWorker,
		shadowComment(legacyComments[0].ID, "@openmerge queue")))
	legacyBefore, err := legacy.store.Watermark(t.Context(), legacyWorker.id)
	must(t, err)
	stageBefore, err := stage.store.Watermark(t.Context(), stageWorker.id)
	must(t, err)
	legacyCursorBefore, err := legacy.store.Observation(t.Context(), legacyWorker.id, "cursor/1")
	must(t, err)
	stageCursorBefore, err := stage.store.Observation(t.Context(), stageWorker.id, "cursor/1")
	must(t, err)
	legacy.options.BackoffUntil = func() time.Time { return time.Now().Add(time.Hour) }
	stage.options.BackoffUntil = func() time.Time { return time.Now().Add(time.Hour) }

	legacyErr := legacy.commands(t.Context(), legacyWorker, 1)
	stageErr := stage.commands(t.Context(), stageWorker, 1)
	for name, err := range map[string]error{"legacy": legacyErr, "stage-a": stageErr} {
		var backoff *quotaBackoff
		if !errors.As(err, &backoff) {
			t.Fatalf("%s command error = %v, want quota backoff", name, err)
		}
	}
	legacyAfter, err := legacy.store.Watermark(t.Context(), legacyWorker.id)
	must(t, err)
	stageAfter, err := stage.store.Watermark(t.Context(), stageWorker.id)
	must(t, err)
	if legacyAfter != legacyBefore || stageAfter != stageBefore {
		t.Fatalf("quota-blocked command appended events: legacy %d, stage-a %d", legacyAfter-legacyBefore, stageAfter-stageBefore)
	}
	legacyCursorAfter, err := legacy.store.Observation(t.Context(), legacyWorker.id, "cursor/1")
	must(t, err)
	stageCursorAfter, err := stage.store.Observation(t.Context(), stageWorker.id, "cursor/1")
	must(t, err)
	if !reflect.DeepEqual(legacyCursorAfter, legacyCursorBefore) || !reflect.DeepEqual(stageCursorAfter, stageCursorBefore) {
		t.Fatalf("quota-blocked command advanced cursor: legacy %q -> %q, stage-a %q -> %q", legacyCursorBefore, legacyCursorAfter, stageCursorBefore, stageCursorAfter)
	}
	for name, pair := range map[string]struct {
		s *Server
		w *worker
	}{"legacy": {legacy, legacyWorker}, "stage-a": {stage, stageWorker}} {
		raw, err := pair.s.store.Observation(t.Context(), pair.w.id, "comment/"+fmt.Sprint(legacyComments[0].ID))
		must(t, err)
		if len(raw) != 0 {
			t.Fatalf("%s quota-blocked command consumed comment: %s", name, raw)
		}
	}
}

func TestStageALegacyCommandsMatchLegacyPrimitives(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		body       string
		reply      bool
		wantEvents []queue.Event
	}{
		{name: "queue", body: "@openmerge queue", reply: true, wantEvents: []queue.Event{queue.EnqueueRequested{PR: 1}}},
		{name: "dequeue", body: "@openmerge dequeue", wantEvents: []queue.Event{queue.DequeueRequested{PR: 1}}},
		{name: "refresh", body: "@openmerge refresh", wantEvents: []queue.Event{
			queue.BlockSourceCleared{Source: queue.Operator, Basis: 1},
			queue.BlockSourceCleared{Source: queue.ActionPoison, Basis: 1},
			queue.BlockSourceCleared{Source: queue.BuildFailed, Basis: 1},
			queue.BlockSourceCleared{Source: queue.LandingNotOnBase, Basis: 1},
			queue.RefreshRequested{},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			legacy, legacyWorker, legacyForge, _ := fixture(t, repositoryConfig)
			stage, stageWorker, stageForge := shadowFixture(t, 1, "main", "7", false)
			_, err := legacy.observe(t.Context(), legacyWorker, 1)
			must(t, err)
			_, err = stage.observe(t.Context(), stageWorker, 1)
			must(t, err)
			seedCommandBlocks(t, legacy, legacyWorker, legacyForge)
			seedCommandBlocks(t, stage, stageWorker, stageForge)

			legacyBefore, err := legacy.store.Watermark(t.Context(), legacyWorker.id)
			must(t, err)
			stageBefore, err := stage.store.Watermark(t.Context(), stageWorker.id)
			must(t, err)
			legacyForge.AddComment(repo, 1, "writer", tt.body)
			legacyComments := legacyForge.CommentRecords(repo, 1)
			if len(legacyComments) == 0 {
				t.Fatal("legacy comment was not added")
			}
			stageForge.SetNativeReviews(repo, 1, shadowNative(stageForge, stageWorker, shadowComment(legacyComments[len(legacyComments)-1].ID, tt.body)))
			must(t, legacy.commands(t.Context(), legacyWorker, 1))
			must(t, stage.commands(t.Context(), stageWorker, 1))

			legacyEvents := normalizedCommandEvents(t, legacy.store, legacyWorker.id, legacyBefore)
			stageEvents := normalizedCommandEvents(t, stage.store, stageWorker.id, stageBefore)
			if !reflect.DeepEqual(legacyEvents, stageEvents) {
				t.Fatalf("event payloads differ: legacy=%#v stage-a=%#v", legacyEvents, stageEvents)
			}
			if !reflect.DeepEqual(legacyEvents, tt.wantEvents) {
				t.Fatalf("event payloads = %#v, want %#v", legacyEvents, tt.wantEvents)
			}
			legacyState := mustState(t, legacy, legacyWorker)
			stageState := mustState(t, stage, stageWorker)
			if !reflect.DeepEqual(commandBlockSnapshot(legacyState), commandBlockSnapshot(stageState)) {
				t.Fatalf("block payloads differ: legacy=%#v stage-a=%#v", commandBlockSnapshot(legacyState), commandBlockSnapshot(stageState))
			}
			if tt.name == "refresh" {
				for _, source := range []queue.BlockSource{queue.Operator, queue.ActionPoison, queue.BuildFailed, queue.LandingNotOnBase} {
					if legacyState.SourceBlocked(source) || stageState.SourceBlocked(source) {
						t.Fatalf("refresh left %s blocked: legacy=%v stage-a=%v", source, legacyState.SourceBlocked(source), stageState.SourceBlocked(source))
					}
				}
			}
			if got := hasQueueReply(legacyForge); got != tt.reply {
				t.Fatalf("legacy reply=%v, want %v", got, tt.reply)
			}
			if got := hasQueueReply(stageForge); got != tt.reply {
				t.Fatalf("stage-a reply=%v, want %v", got, tt.reply)
			}
		})
	}
}

func TestLegacyCommandWaitingStopsSharedPass(t *testing.T) {
	t.Parallel()
	legacy, legacyWorker, legacyForge, _ := fixture(t, repositoryConfig)
	stage, stageWorker, stageForge := shadowFixture(t, 1, "main", "7", false)
	legacyWorker.managed, legacyWorker.ready = true, false
	stageWorker.managed, stageWorker.ready = true, false
	legacyForge.AddComment(repo, 1, "writer", "@openmerge queue")
	legacyForge.AddComment(repo, 1, "writer", "@openmerge dequeue")
	legacyComments := legacyForge.CommentRecords(repo, 1)
	if len(legacyComments) < 2 {
		t.Fatal("waiting comments were not added")
	}
	stageForge.SetNativeReviews(repo, 1, shadowNative(stageForge, stageWorker,
		shadowComment(legacyComments[0].ID, "@openmerge queue"),
		shadowComment(legacyComments[1].ID, "@openmerge dequeue"),
	))
	legacyCursorBefore, err := legacy.store.Observation(t.Context(), legacyWorker.id, "cursor/1")
	must(t, err)
	stageCursorBefore, err := stage.store.Observation(t.Context(), stageWorker.id, "cursor/1")
	must(t, err)
	legacyBefore, err := legacy.store.Watermark(t.Context(), legacyWorker.id)
	must(t, err)
	stageBefore, err := stage.store.Watermark(t.Context(), stageWorker.id)
	must(t, err)
	must(t, legacy.commands(t.Context(), legacyWorker, 1))
	must(t, stage.commands(t.Context(), stageWorker, 1))

	for name, pair := range map[string]struct {
		s *Server
		w *worker
	}{"legacy": {legacy, legacyWorker}, "stage-a": {stage, stageWorker}} {
		s, w := pair.s, pair.w
		cursor, err := s.store.Observation(t.Context(), w.id, "cursor/1")
		must(t, err)
		wantCursor := legacyCursorBefore
		if name == "stage-a" {
			wantCursor = stageCursorBefore
		}
		if !reflect.DeepEqual(cursor, wantCursor) {
			t.Fatalf("%s cursor changed after waiting queue command: before=%q after=%q", name, wantCursor, cursor)
		}
		ignored, err := s.store.Observation(t.Context(), w.id, fmt.Sprintf("ignored/comment/%d", legacyComments[0].ID))
		must(t, err)
		if len(ignored) == 0 {
			t.Fatalf("%s waiting command was not recorded as ignored", name)
		}
		second, err := s.store.Observation(t.Context(), w.id, fmt.Sprintf("comment/%d", legacyComments[1].ID))
		must(t, err)
		if len(second) != 0 {
			t.Fatalf("%s processed a later comment after waiting command: %s", name, second)
		}
	}
	legacyEvents := normalizedCommandEvents(t, legacy.store, legacyWorker.id, legacyBefore)
	stageEvents := normalizedCommandEvents(t, stage.store, stageWorker.id, stageBefore)
	if !reflect.DeepEqual(legacyEvents, stageEvents) {
		t.Fatalf("waiting command events differ or were emitted: legacy=%#v stage-a=%#v", legacyEvents, stageEvents)
	}
	if hasQueueReply(legacyForge) || hasQueueReply(stageForge) {
		t.Fatal("waiting queue command received a queue acceptance reply")
	}
}

func seedCommandBlocks(t *testing.T, s *Server, w *worker, f *fake.Forge) {
	t.Helper()
	base, err := f.ReadRef(t.Context(), repo, w.Key.BaseBranch)
	must(t, err)
	for _, seed := range []struct {
		source  queue.BlockSource
		detail  string
		subject string
	}{
		{source: queue.Operator, detail: "operator hold"},
		{source: queue.ActionPoison, detail: "poison hold", subject: "bad-action"},
		{source: queue.BuildFailed, detail: "build hold"},
		{source: queue.LandingNotOnBase, detail: "landing hold", subject: base},
	} {
		must(t, s.block(t.Context(), w, seed.source, seed.detail, seed.subject))
	}
}

func normalizedCommandEvents(t *testing.T, db *store.Store, id store.QueueID, after int64) []queue.Event {
	t.Helper()
	records, err := db.EventsAfter(t.Context(), id, after)
	must(t, err)
	events := make([]queue.Event, 0, len(records))
	for _, record := range records {
		event := record.Event
		switch e := event.(type) {
		case queue.EnqueueRequested:
			e.At = time.Time{}
			event = e
		case queue.ChecksReopened:
			e.At = time.Time{}
			event = e
		case queue.ChecksRefreshed:
			e.At = time.Time{}
			event = e
		case queue.PRObserved:
			e.HeadSHA = "<head>"
			event = e
		case queue.PRMetadataObserved:
			e.HeadSHA = "<head>"
			event = e
		}
		events = append(events, event)
	}
	return events
}

type commandBlockState struct {
	blocked         bool
	detail, subject string
	revision        uint64
}

func commandBlockSnapshot(state queue.State) map[queue.BlockSource]commandBlockState {
	result := make(map[queue.BlockSource]commandBlockState)
	for _, source := range []queue.BlockSource{queue.Operator, queue.ActionPoison, queue.BuildFailed, queue.LandingNotOnBase} {
		subject := state.BlockSourceSubject(source)
		if source == queue.LandingNotOnBase && subject != "" {
			subject = "<landing-head>"
		}
		result[source] = commandBlockState{
			blocked:  state.SourceBlocked(source),
			detail:   state.BlockSourceDetail(source),
			subject:  subject,
			revision: state.BlockSourceRevision(source),
		}
	}
	return result
}

func hasQueueReply(f *fake.Forge) bool {
	for _, body := range f.Comments(repo, 1) {
		if strings.Contains(body, "Queue request accepted for exact SHA") {
			return true
		}
	}
	return false
}

func mustState(t *testing.T, s *Server, w *worker) queue.State {
	t.Helper()
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	return state
}
