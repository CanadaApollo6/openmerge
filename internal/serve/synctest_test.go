package serve

import (
	"net"
	"testing"
	"testing/synctest"
	"time"
)

// testSynctest owns the entire fixture lifetime. Create fixtures in f using its
// inner t: databaseConnection registers Close with that t, whose cleanups run
// inside the bubble before synctest waits for background goroutines to exit.
// Stop workers before pool cleanup (defer or a later t.Cleanup).
// Use this for manually driven cycles too: real database latency must not
// repeatedly expire the millisecond fixture polling interval within one turn.
// Never capture a store/server created outside f: puddle's WaitGroups cannot
// cross bubble boundaries, even when ordinary queries appear to work.
func testSynctest(t *testing.T, f func(*testing.T)) {
	t.Helper()
	// The process-wide resolver may start goroutines that outlive a bubble.
	_, err := net.LookupHost("localhost")
	must(t, err)
	synctest.Test(t, f)
}

func TestSynctestPoolDestroy(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		db := database(t)
		// Force puddle's asynchronous destruction path. Opening this fixture outside
		// the bubble made Close abort the test binary with a WaitGroup fatal error.
		db.Close()
	})
}

func TestSynctestDatabaseSchemasUnique(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		_, first := databaseConnection(t)
		_, second := databaseConnection(t)
		var firstSchema, secondSchema string
		must(t, first.QueryRow(t.Context(), "SELECT current_schema()").Scan(&firstSchema))
		must(t, second.QueryRow(t.Context(), "SELECT current_schema()").Scan(&secondSchema))
		if firstSchema == secondSchema {
			t.Fatal("same virtual instant reused a scratch schema")
		}
	})
}

// The millisecond fixture is deliberately fast, but a slow race-instrumented
// refresh must not make another refresh due in the same worker turn. Keep I/O
// on real PostgreSQL while advancing the polling clock only at test boundaries.
func TestSynctestCheckPollingIsClockDriven(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, f, d := fixture(t, repositoryConfig)
		f.AddComment(repo, 1, "writer", "@openmerge queue")
		must(t, s.cycle(t.Context(), w))
		before := d.checks
		if before != 1 {
			t.Fatalf("initial turn read checks %d times, want 1", before)
		}
		for range 2 {
			must(t, s.cycle(t.Context(), w))
		}
		if d.checks != before {
			t.Fatalf("unchanged clock repeated check reads: %d -> %d", before, d.checks)
		}
		time.Sleep(s.options.PollInterval)
		must(t, s.cycle(t.Context(), w))
		if d.checks != before+1 {
			t.Fatalf("one polling interval read checks %d times, want %d", d.checks, before+1)
		}
	})
}
