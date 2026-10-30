package statusfixture

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// fatalRecorder captures a Fatalf instead of failing the enclosing test.
type fatalRecorder struct {
	testing.TB
	fatal string
}

func (r *fatalRecorder) Fatalf(format string, args ...any) {
	r.fatal = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func TestMetamorphicSettlementAcceptsOnlyPublicAndDependentFailedExits(t *testing.T) {
	c := MetamorphicCase{Transition: "closed", Target: 3}
	for _, tc := range []struct {
		name              string
		pr                int
		reason            queue.DequeueReason
		group, uncompared string
		fatal             bool
	}{
		{name: "target public exit", pr: 3, reason: queue.Closed, group: "left:3:closed"},
		{name: "dependent checks-failed", pr: dependentPR, reason: queue.ChecksFailed, uncompared: "PR 2 left the queue (checks-failed)"},
		{name: "independent checks-failed", pr: 1, reason: queue.ChecksFailed, fatal: true},
		{name: "public reason on a non-target", pr: 1, reason: queue.Closed, fatal: true},
		{name: "dependent other private exit", pr: dependentPR, reason: queue.NotEligible, fatal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := store.Journal{Events: []store.JournalEvent{{ID: 1, Event: queue.Dequeued{PR: tc.pr, Reason: tc.reason}}}}
			var q queue.State
			for _, pr := range []int{1, 2, 3} {
				if pr != tc.pr {
					q.Entries = append(q.Entries, queue.Entry{PR: pr})
				}
			}
			r := &fatalRecorder{TB: t}
			var group, uncompared string
			done := make(chan struct{})
			go func() {
				defer close(done)
				group, uncompared = metamorphicSettlement(r, c, j, q, []int{1, 2, 3}, nil)
			}()
			<-done
			if tc.fatal != (r.fatal != "") || group != tc.group || uncompared != tc.uncompared {
				t.Fatalf("group %q uncompared %q fatal %q", group, uncompared, r.fatal)
			}
		})
	}
}
