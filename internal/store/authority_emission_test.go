package store

import (
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"net"
	"os"
	"strings"
	"testing"
	"testing/synctest"
)

func TestAuthorityEmissionLiveBackupWatermark(t *testing.T) {
	_, err := net.LookupHost("localhost")
	must(t, err)
	synctest.Test(t, func(t *testing.T) {
		s := newStore(t)
		id, err := s.EnsureQueue(t.Context(), QueueKey{Installation: "7", Repository: "owner/repo", BaseBranch: "main"})
		must(t, err)
		appendEvents(t, s, id, queue.BaseMoved{SHA: "base"})
		events, err := s.EventsAfter(t.Context(), id, 0)
		must(t, err)
		watermark := events[len(events)-1].ID
		must(t, s.CheckAuthorityEmissionBackup(t.Context(), watermark))
		appendEvents(t, s, id, queue.BaseMoved{SHA: "outside"})
		if err := s.CheckAuthorityEmissionBackup(t.Context(), watermark); err == nil || !strings.Contains(err.Error(), "watermark") {
			t.Fatalf("stale live backup accepted: %v", err)
		}
		// A real retained boundary means subsequent compatible emissions use the
		// original tested pre-emission backup, rather than inventing a newer one.
		raw, err := os.ReadFile("../queue/testdata/wire/v1/event_write_restriction_verified.json")
		must(t, err)
		event, err := queue.DecodeEvent(raw)
		must(t, err)
		appendEvents(t, s, id, event)
		must(t, s.CheckAuthorityEmissionBackup(t.Context(), watermark))
	})
}
