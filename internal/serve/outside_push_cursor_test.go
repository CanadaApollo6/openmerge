package serve

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestOutsidePushCursorResetWarnsOnlyAfterCAS(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	must(t, s.cycle(t.Context(), w))
	tip, err := f.PushBase(repo, "main", "outside change", nil)
	must(t, err)
	_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.BaseMoved{SHA: tip}}, "")
	must(t, err)
	const key = "outside-push-audit/v2"
	future := func(version int) []byte { return fmt.Appendf(nil, `{"State":{"version":%d}}`, version) }
	must(t, s.store.Record(t.Context(), w.id, key, future(999)))
	var log bytes.Buffer
	s.options.Logger = slog.New(slog.NewTextHandler(&log, nil))
	version := 999
	s.options.OutsidePushHistory = func(ctx context.Context, _ store.QueueKey) (forge.OutsidePushHistory, error) {
		version++
		// Competing writer wins after the scan and before this pass's CAS.
		return f, s.store.Record(ctx, w.id, key, future(version))
	}
	for range 3 {
		must(t, s.auditOutsidePushes(t.Context(), w.id, w.Key))
		if strings.Contains(log.String(), "cursor unusable") {
			t.Fatal("failed CAS warned", log.String())
		}
	}
	s.options.OutsidePushHistory = nil
	must(t, s.auditOutsidePushes(t.Context(), w.id, w.Key))
	if strings.Count(log.String(), "cursor unusable") != 1 {
		t.Fatal("successful reset warning missing", log.String())
	}
	cursor, err := s.store.Observation(t.Context(), w.id, key)
	must(t, err)
	if bytes.Equal(cursor, future(version)) {
		t.Fatal("future-version derived cursor was not overwritten")
	}
	must(t, s.auditOutsidePushes(t.Context(), w.id, w.Key))
	if strings.Count(log.String(), "cursor unusable") != 1 {
		t.Fatal("repaired cursor warned again", log.String())
	}
}
