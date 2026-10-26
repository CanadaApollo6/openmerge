package status

import (
	"os"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func instructionPin(t *testing.T) queue.PolicyAuthorityApplied {
	t.Helper()
	raw, err := os.ReadFile("../queue/testdata/wire/v1/event_policy_authority_applied.json")
	if err != nil {
		t.Fatal(err)
	}
	event, err := queue.DecodeEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	return event.(queue.PolicyAuthorityApplied)
}

func TestQueueInstructionsGoldensAndRollback(t *testing.T) {
	pin := instructionPin(t)
	for _, tc := range []struct {
		name   string
		events []queue.Event
	}{
		{"waiting", []queue.Event{queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}}},
		{"testing", ledgerChain()},
		{"blocked", []queue.Event{queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: false}, queue.EnqueueRequested{PR: 1, At: at}}},
		{"landed", append(admission(), queue.BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: []queue.BatchPR{{PR: 1, HeadSHA: "head"}}, At: at}, queue.TestRefBuilt{BatchID: "b1", TestedSHA: "tested", At: at, Method: queue.Merge}, queue.ChecksCompleted{TestedSHA: "tested", Success: true}, queue.LandingStarted{BatchID: "b1", AttemptID: "attempt", FenceToken: "fence", At: at}, queue.Landed{BatchID: "b1", NewBaseSHA: "tested", NewFence: "next"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := append([]queue.Event{queue.ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest}}, tc.events...)
			legacy, _, _ := Project(journal(events...), 0, 0, nil)
			if got := QueueInstructions(&legacy); got != "" {
				t.Fatalf("legacy: %q", got)
			}
			events = append(events, pin)
			q, _, _ := Project(journal(events...), 0, 0, nil)
			ApplyPolicy(&q, q.LedgerEntry(1), queue.Config{}, at)
			text := "Queue: " + AdaptiveSummary(&q) + LedgerSummary(q.LedgerEntry(1)) + QueueInstructions(&q)
			reportingGolden(t, "queue-instructions-"+tc.name+".txt", []byte(text))
			if strings.Count(text, "`@openmerge queue`") != 1 {
				t.Fatal(text)
			}
			// Replaying the same cycle does not change the text, including an
			// unverified authority pin (this message makes no verification claim).
			again, _, _ := Project(journal(events...), 0, 0, nil)
			if QueueInstructions(&again) != QueueInstructions(&q) {
				t.Fatal("unstable instruction")
			}
			rollback := pin
			rollback.Authority = authority.GitHub
			rollback.OpenMerge = nil
			events = append(events, rollback)
			q, _, _ = Project(journal(events...), 0, 0, nil)
			if got := QueueInstructions(&q); got != "" {
				t.Fatalf("rollback: %q", got)
			}
		})
	}
}

func TestQueueInstructionsBeforeApprovalBriefAndHostileNames(t *testing.T) {
	j, _, basis, rev := briefRegressionBase(t)
	pin := instructionPin(t)
	briefAppend(&j, queue.ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest})
	briefAppend(&j, pin)
	j.Key.Repository = strings.Repeat("<script>`@openmerge queue`\n", 1000)
	id, err := j.Key.ChangeID(1)
	if err != nil {
		t.Fatal(err)
	}
	rev.ChangeID = id
	basis.ChangeID = id
	j.Events[0].Event = rev
	j.Events[1].Event = queue.Intent(basis, strings.Repeat("<script>`untrusted`\n", 1000), "body")
	q, _, _ := Project(j, 0, 0, nil)
	instruction := QueueInstructions(&q)
	detail, err := ReviewerCheckSummaryFromProjection(j, &q, 1, cp.Policy{Approvals: &cp.Approvals{Default: cp.Requirement{Selector: cp.Human, Count: 1}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := instruction + detail
	if strings.Count(text, "`@openmerge queue`") != 1 || !strings.Contains(text, "Reviewer brief:") || strings.Index(text, "entire body") > strings.Index(text, "Reviewer brief:") || strings.Contains(text, "<script>") || len(text) > 12500 {
		t.Fatalf("incoherent or unbounded check: %s", text)
	}
	// The approval renderer remains independently byte-identical.
	if strings.Contains(detail, "This repository merges") {
		t.Fatal("instruction duplicated in brief")
	}
}

func TestQueueInstructionsRefreshPreservesTerminalSummary(t *testing.T) {
	pin := instructionPin(t)
	config := queue.ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest}
	active, _, _ := Project(journal(config, pin), 0, 0, nil)
	if got := active.state.AuthorityPin(); got == nil || got.Authority != authority.OpenMerge || QueueInstructions(&active) == "" {
		t.Fatal("expected an active OpenMerge authority pin and queue instructions")
	}
	pin.Authority = authority.GitHub
	pin.OpenMerge = nil
	rollback, _, _ := Project(journal(config, pin), 0, 0, nil)
	if got := rollback.state.AuthorityPin(); got == nil || got.Authority != authority.GitHub || QueueInstructions(&rollback) != "" {
		t.Fatal("expected an active GitHub rollback pin without queue instructions")
	}
	for _, warning := range []string{"", "\nWARNING: retained safety warning"} {
		before := "Dequeued: requested; exact head abc\n\nQueue: original ledger" + warning + "\n\nReviewer brief: url\nDigest: unchanged" + queueInstructionParagraph
		expected := "Dequeued: requested; exact head abc\n\nQueue: original ledger" + QueueInstructions(&active) + warning + "\n\nReviewer brief: url\nDigest: unchanged" + queueInstructionParagraph
		got := RefreshQueueInstructions(before, &active)
		if got != expected || RefreshQueueInstructions(got, &active) != got {
			t.Fatalf("activation altered summary: %q", got)
		}
		if got := RefreshQueueInstructions(got, &rollback); got != before {
			t.Fatalf("rollback altered summary: %q", got)
		}
	}
}
