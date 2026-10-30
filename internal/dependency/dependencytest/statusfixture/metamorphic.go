package statusfixture

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// MetamorphicTransitions are the public observations applied to every world.
// "-planned" variants also execute the real planner's invalidation and dequeue
// follow-through. CI, retry and landing stages advance whichever batch the
// private history started; worlds without one record no batch progress.
var MetamorphicTransitions = []string{
	"head", "closed", "requested", "retargeted", "paused",
	"head-planned", "closed-planned", "requested-planned", "retargeted-planned",
	"ci-passed", "ci-failed", "check-retry", "landing", "landed",
}

// MetamorphicCase is one public observation history. Worlds vary only private
// dependency state and the batch progress it permits.
type MetamorphicCase struct {
	Transition   string
	Provenance   bool
	PrivateFirst bool
	Cobatch      bool
	Target       int
	Depth        int
	// SelfAck opts the queue into single-maintainer self-acknowledgement with
	// two writers and, after the public transition, records a completed public
	// self-acknowledgement for SelfAckPR, after private-dependent prefixes.
	SelfAck bool
}

// SelfAckPR is the observed, never-queued change that self-acknowledges.
const SelfAckPR = 7

func (c MetamorphicCase) String() string {
	name := fmt.Sprintf("%s/provenance-%t/private-first-%t/cobatch-%t/pr-%d/depth-%d", c.Transition, c.Provenance, c.PrivateFirst, c.Cobatch, c.Target, c.Depth)
	if c.SelfAck {
		name += "/self-ack"
	}
	return name
}

// MetamorphicCases is the explicit cross product. The short subset is fixed:
// every transition, every dimension value and every historical leak shape.
// Every short case applies its transition in at least one compared world.
func MetamorphicCases(short bool) []MetamorphicCase {
	if short {
		// The first five carry every publication-side shape for the slower
		// database-backed serve test, including a CI outcome.
		return []MetamorphicCase{
			// Pending dequeue batch fields, check bypass and private provenance.
			{"requested", true, false, true, 3, 1, false},
			// Paused batch-derived detail and co-batched dependency-changed reasons.
			{"paused", true, true, true, 3, 4, false},
			// Passing CI: landing-preflight gate against an unbatched world.
			{"ci-passed", false, false, true, 3, 1, false},
			// Unconfirmed landing: status, gate and batch metadata.
			{"landing", true, false, true, 3, 1, false},
			// Change-left-queue invalidation, dequeue and retained departed checks.
			{"closed-planned", true, true, true, 2, 4, false},
			// Building dependent predecessor with unbatched independent followers.
			{"head", false, true, false, 1, 1, false},
			{"closed", false, false, false, 3, 4, false},
			{"retargeted", true, false, true, 2, 1, false},
			{"head-planned", false, false, true, 3, 1, false},
			{"requested-planned", false, true, false, 3, 1, false},
			{"retargeted-planned", true, false, true, 1, 4, false},
			// A failed multi-PR batch: provisional in a chain, bisected alone.
			{"ci-failed", false, false, true, 2, 4, false},
			{"ci-failed", true, false, true, 1, 1, false},
			// A failed retry holds unbatched followers at predecessor-checks.
			{"check-retry", true, false, false, 3, 1, false},
			{"landed", false, false, false, 2, 4, false},
			// Self-acknowledgement after private-dependent prefixes: journal IDs,
			// recording times, positions and the warning must stay public.
			{"requested", true, false, true, 3, 1, true},
			{"paused", true, true, true, 3, 4, true},
			{"landed", false, false, false, 2, 4, true},
		}
	}
	var cases []MetamorphicCase
	for _, transition := range MetamorphicTransitions {
		for _, provenance := range []bool{false, true} {
			for _, privateFirst := range []bool{false, true} {
				for _, cobatch := range []bool{false, true} {
					for _, target := range []int{1, 2, 3} {
						for _, depth := range []int{1, 4} {
							cases = append(cases, MetamorphicCase{transition, provenance, privateFirst, cobatch, target, depth, false})
							// Self-acknowledgement is independent of the target.
							if target == 2 {
								cases = append(cases, MetamorphicCase{transition, provenance, privateFirst, cobatch, target, depth, true})
							}
						}
					}
				}
			}
		}
	}
	return cases
}

// MetamorphicWorld is one private history of a case.
type MetamorphicWorld struct {
	Journal store.Journal
	Binding dependency.JournalInitialized
	// Source and Grant authorize the complete source audience.
	Source string
	Grant  dependency.Grant
	PRs    []int
	// Group names the confirmed public settlement facts: members merged by a
	// confirmed Landed and members that left the queue because of a public
	// event on their own PR. Only worlds in the same group are compared.
	Group string
	// Applied reports that the public transition changed this world's queue.
	// CI, retry and landing transitions apply only to a started batch.
	Applied bool
	// Preempted reports that a private read before the transition discarded
	// every batch the transition could have advanced.
	Preempted bool
	// Uncompared names a queue exit caused by private state. Such a world is
	// rendered and checked, but it is never a comparison partner.
	Uncompared string
	// Tail is a private-only observation recorded after the journal.
	Tail []store.JournalEvent
	// SelfAckPR is the self-acknowledging change, or zero.
	SelfAckPR int
}

type metamorphicWorld struct {
	code, phase string
	rebuilt     bool
}

var metamorphicWorlds = []metamorphicWorld{
	{"dependency-waiting", "queued", false}, {"dependency-closed", "queued", false},
	{"dependency-stale", "queued", false}, {"dependency-unavailable", "queued", false},
	{"dependency-landed", "testing", false}, {"dependency-waiting", "invalidating", false},
	{"dependency-waiting", "discarded", false}, {"dependency-landed", "building", false},
	// A private flap discards the first attempt; a new composition reaches testing.
	{"dependency-waiting", "discarded", true},
}

// MetamorphicWorlds returns the private histories compared per case. Short
// runs keep the waiting baseline and every batched history; later private
// reads still rotate through all five private source states.
func MetamorphicWorlds(short bool) []int {
	if short {
		return []int{0, 4, 5, 6, 7, 8}
	}
	return []int{0, 1, 2, 3, 4, 5, 6, 7, 8}
}

// MetamorphicPolicy applies window gates so derived holds are rendered.
func MetamorphicPolicy(depth int) queue.Config {
	return queue.Config{BatchMaxSize: 3, MergeMethod: queue.Merge, SpeculationMaxDepth: depth}
}

func (c MetamorphicCase) index() int {
	n := slices.Index(MetamorphicTransitions, c.Transition) + c.Target + c.Depth
	for _, flag := range []bool{c.Provenance, c.PrivateFirst, c.Cobatch} {
		n *= 2
		if flag {
			n++
		}
	}
	return n
}

// Metamorphic builds world w of case c through the real journal reader.
func Metamorphic(t testing.TB, c MetamorphicCase, w int) MetamorphicWorld {
	t.Helper()
	v := metamorphicWorlds[w]
	tested := 100
	if v.rebuilt {
		tested = 200
	}
	j, binding, source, grant, prs := multiJournal(t, v.code, v.phase, c.Depth, c.Cobatch, tested, c.SelfAck)
	b := replay(t, &j, binding)
	b.grant = grant
	// Every private read delays later records by a world-dependent amount, so
	// public records differ in recording time as well as journal ID.
	b.delay = time.Duration(w+1) * time.Minute
	at := j.Events[0].RecordedAt
	if v.rebuilt {
		b.privateObservation("landed", "dependency-landed")
		started := 1
		if c.Depth == 4 {
			started = 4
		}
		startBatches(b, c.Depth, c.Cobatch, false, started, 100)
	}
	head := func() *queue.Batch {
		batches := b.reader.Queue.Batches()
		if len(batches) == 0 || batches[0].Phase != queue.Testing || batches[0].InvalidationReason != "" {
			return nil
		}
		return batches[0]
	}
	world := MetamorphicWorld{Binding: binding, PRs: prs, Applied: true}
	if c.PrivateFirst {
		started := head() != nil
		b.privateObservation(metamorphicNext(c, w))
		world.Preempted = started && head() == nil
	}
	if c.Provenance {
		for _, batch := range b.reader.Queue.Batches() {
			if batch.Phase == queue.Testing {
				b.add(queue.CheckProvenanceObserved{BatchID: batch.ID, TestedSHA: batch.TestedSHA, Checks: []queue.UnboundCheck{{Name: "ci", Source: "github-actions", HeadBranch: "other"}}})
				break
			}
		}
	}
	var merged []int
	runs := func(state string) []queue.CheckRun {
		return []queue.CheckRun{{ID: 7, Attempt: 1, Name: "ci", Source: "app:1", State: state, URL: "https://github.test/acme/client/actions/runs/7"}}
	}
	transition, planned := strings.CutSuffix(c.Transition, "-planned")
	switch transition {
	case "head":
		b.add(queue.PRObserved{PR: c.Target, HeadSHA: strings.Repeat("9", 40), Eligible: true, Trusted: true})
	case "closed":
		b.add(queue.PRClosed{PR: c.Target})
	case "requested":
		b.add(queue.DequeueRequested{PR: c.Target})
	case "retargeted":
		b.add(queue.PRObserved{PR: c.Target, HeadSHA: fixtureHead(c.Target), Eligible: false, Trusted: true})
	case "paused":
		b.add(queue.PauseRequested{Reason: "operator", Detail: "public maintenance"})
	case "ci-passed", "ci-failed":
		world.Applied = head() != nil
		if batch := head(); batch != nil {
			success := transition == "ci-passed"
			state := "failure"
			if success {
				state = "success"
			}
			b.add(queue.ChecksObserved{BatchID: batch.ID, TestedSHA: batch.TestedSHA, Runs: runs(state), Terminal: true, Success: success, OriginalDeadline: at.Add(time.Hour), Timeout: time.Hour})
			b.add(queue.ChecksCompleted{TestedSHA: batch.TestedSHA, Success: success})
			planned = !success
		}
	case "check-retry":
		world.Applied = head() != nil
		if batch := head(); batch != nil {
			b.add(queue.ChecksObserved{BatchID: batch.ID, TestedSHA: batch.TestedSHA, Runs: runs("failure"), Terminal: true, OriginalDeadline: at.Add(time.Hour), Timeout: time.Hour})
			b.add(queue.CheckRerunRequested{BatchID: batch.ID, TestedSHA: batch.TestedSHA, Runs: runs("failure"), OriginalDeadline: at.Add(time.Hour), At: at.Add(10 * time.Second), Deadline: at.Add(70 * time.Second), Timeout: time.Minute})
		}
	case "landing", "landed":
		// LandingStarted precedes any forge call: only Landed is public.
		world.Applied = head() != nil
		if batch := head(); batch != nil {
			b.add(queue.ChecksObserved{BatchID: batch.ID, TestedSHA: batch.TestedSHA, Runs: runs("success"), Terminal: true, Success: true, OriginalDeadline: at.Add(time.Hour), Timeout: time.Hour})
			b.add(queue.ChecksCompleted{TestedSHA: batch.TestedSHA, Success: true})
			b.add(queue.LandingStarted{BatchID: batch.ID, AttemptID: "attempt-" + batch.ID, FenceToken: b.reader.Queue.Fence, At: at})
			if transition == "landed" {
				for _, pin := range batch.PRs {
					merged = append(merged, pin.PR)
				}
				b.add(queue.Landed{BatchID: batch.ID, NewBaseSHA: batch.TestedSHA, NewFence: "landed-fence"})
			}
		}
	default:
		t.Fatalf("unknown transition %q", c.Transition)
	}
	if planned {
		batched := b.reader.Queue.BatchForPR(c.Target) != nil
		first := len(j.Events)
		b.followThrough(c.Depth)
		if transition != "ci-failed" {
			// The real planner must settle the public transition: a speculative
			// chain records the invalidated suffix, then the target is dequeued.
			invalidated, dequeued := false, false
			for _, record := range j.Events[first:] {
				switch e := record.Event.(type) {
				case queue.BatchesInvalidated:
					invalidated = true
				case queue.Dequeued:
					dequeued = dequeued || e.PR == c.Target
				}
			}
			if !dequeued || batched && c.Depth == 4 && !invalidated {
				t.Fatalf("planner follow-through: batched=%t invalidated=%t dequeued=%t", batched, invalidated, dequeued)
			}
		}
	}
	if !c.PrivateFirst {
		b.privateObservation(metamorphicNext(c, w))
	}
	if c.SelfAck {
		b.selfAcknowledge(SelfAckPR)
		world.SelfAckPR = SelfAckPR
	}
	world.Group, world.Uncompared = metamorphicSettlement(c, j, b.reader.Queue, prs, merged)
	world.Journal, world.Source, world.Grant = j, source, grant
	world.Journal.Events = slices.Clone(j.Events)
	n := len(j.Events)
	b.privateObservation(metamorphicNext(c, w+1))
	world.Tail = slices.Clone(j.Events[n:])
	return world
}

// publicDequeue lists the dequeue reasons whose cause is an event on the PR
// itself that the forge shows to every reader: closing it, a dequeue command
// comment, a retarget or a head push. A transition applies them to the same
// PR in every world, so they never partition worlds by private state.
var publicDequeue = map[queue.DequeueReason]bool{queue.Closed: true, queue.Requested: true, queue.NotEligible: true, queue.HeadChanged: true}

// metamorphicSettlement names the confirmed public settlement facts. A queue
// exit for any other reason, such as a failed composition that exists only
// because of private state, is not a public fact: the world is excluded from
// comparison rather than given a group of its own.
func metamorphicSettlement(c MetamorphicCase, j store.Journal, q queue.State, prs, merged []int) (string, string) {
	var group, uncompared []string
	for _, pr := range prs {
		if slices.ContainsFunc(q.Entries, func(e queue.Entry) bool { return e.PR == pr }) {
			continue
		}
		if slices.Contains(merged, pr) {
			group = append(group, fmt.Sprintf("merged:%d", pr))
			continue
		}
		reason := queue.DequeueReason("")
		for _, record := range j.Events {
			if e, ok := record.Event.(queue.Dequeued); ok && e.PR == pr {
				reason = e.Reason
			}
		}
		if pr == c.Target && publicDequeue[reason] {
			group = append(group, fmt.Sprintf("left:%d:%s", pr, reason))
		} else {
			uncompared = append(uncompared, fmt.Sprintf("PR %d left the queue (%s)", pr, reason))
		}
	}
	return strings.Join(group, " "), strings.Join(uncompared, "; ")
}

func metamorphicNext(c MetamorphicCase, w int) (string, string) {
	states := []struct{ result, code string }{
		{"waiting", "dependency-waiting"}, {"unavailable", "dependency-closed"},
		{"stale", "dependency-stale"}, {"landed", "dependency-landed"},
		{"unavailable", "dependency-unavailable"},
	}
	next := states[(w+c.index())%len(states)]
	return next.result, next.code
}

// privateObservation records another read of private A and its typed
// diagnostic. The last observation is the causal predecessor even after
// invalidation.
func (b *journalBuilder) privateObservation(result, code string) {
	t := b.t
	t.Helper()
	var last dependency.Observed
	var prior uint64
	for _, record := range b.j.Events {
		if record.Event != nil {
			continue
		}
		e, err := dependency.DecodeEvent(record.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if observed, ok := e.(dependency.Observed); ok {
			last, prior = observed, uint64(record.ID)
		}
	}
	last.ExpectedPrior, last.ReadSequence = prior, last.ReadSequence+1
	last.Result = result
	b.offset += b.delay
	id := b.add(last)
	b.add(dependency.DiagnosticRecorded{Namespace: b.binding.Namespace, ObservationEventID: id, Source: dependencytest.Request().Source.Alias, Evidence: dependency.ReadEvidence{Stage: dependency.WorkRead, Declaration: last.Declaration, Grant: b.grant, Observation: last, Code: code}})
}

// selfAcknowledge records public consumer-repository history for an observed
// change: a declared edge, its removal by author 11 (a loosening hold) and 11's
// self-acknowledgement under the queue's single-maintainer opt-in.
func (b *journalBuilder) selfAcknowledge(pr int) {
	t := b.t
	t.Helper()
	b.add(queue.PRObserved{PR: pr, HeadSHA: fixtureHead(pr), Eligible: true, Trusted: true})
	id, err := b.j.Key.ChangeID(pr)
	if err != nil {
		t.Fatal(err)
	}
	decl := dependencytest.Request().Consumer
	decl.ChangeID, decl.Alias.Number = string(id), uint64(pr)
	first := b.add(decl)
	decl.Prior = dependency.Basis{EventID: first, Digest: decl.Digest}
	decl.Poll, decl.Edges = 2, []dependency.DeclaredEdge{}
	decl.Digest = dependency.DeclarationDigest(decl.Edges)
	decl.Hold = dependency.DeclarationHold{Held: true, Editors: []string{"11"}}
	held := b.add(decl)
	policy, policyID := b.policy()
	b.add(dependency.SelfAcknowledged{Acknowledged: dependency.Acknowledged{Namespace: b.binding.Namespace, Alias: decl.Alias, Declaration: dependency.Basis{EventID: held, Digest: decl.Digest}, Writer: dependency.Grant{SubjectID: "11", ReturnedID: "11", RepositoryID: decl.Alias.RepositoryID, ReadID: "self-ack", Fresh: true, Allowed: true}, Exclusions: []string{"11"}, PolicyEventID: policyID, GoverningCommit: policy.Commit, PolicyDigest: policy.Digest}, Overridden: []string{"11"}})
}

// policy returns the latest governing dependency policy companion.
func (b *journalBuilder) policy() (dependency.PolicyApplied, uint64) {
	for i := len(b.j.Events) - 1; i >= 0; i-- {
		if b.j.Events[i].Event != nil {
			continue
		}
		if e, err := dependency.DecodeEvent(b.j.Events[i].Payload); err == nil {
			if p, ok := e.(dependency.PolicyApplied); ok {
				return p, uint64(b.j.Events[i].ID)
			}
		}
	}
	b.t.Fatal("no dependency policy")
	return dependency.PolicyApplied{}, 0
}

// followThrough records the planner's own invalidation and dequeue decisions.
func (b *journalBuilder) followThrough(depth int) {
	b.t.Helper()
	for range 8 {
		progressed := false
		for _, action := range queue.Plan(MetamorphicPolicy(depth), b.reader.Queue, b.j.Events[0].RecordedAt) {
			switch a := action.(type) {
			case queue.InvalidateBatches:
				b.add(queue.BatchesInvalidated(a))
				progressed = true
			case queue.Dequeue:
				b.add(queue.Dequeued{PR: a.PR, AdmissionID: a.AdmissionID, Reason: a.Reason, BatchID: a.BatchID})
				progressed = true
			}
			if progressed {
				break
			}
		}
		if !progressed {
			return
		}
	}
	b.t.Fatal("planner follow-through did not settle")
}

// Compared reports whether the world is a comparison partner.
func (m MetamorphicWorld) Compared() bool { return m.Uncompared == "" }

// RequireComparedWorlds fails when a case is vacuous: the transition must
// apply in at least one world that is compared with another. Short cases never
// skip. The full product skips, by name, exactly two impossible shapes: a
// private read before the transition discarded every batch, or every world it
// applies to left the queue for a private cause or landed alone.
func RequireComparedWorlds(t testing.TB, c MetamorphicCase, worlds []MetamorphicWorld) {
	t.Helper()
	groups := map[string]int{}
	for _, m := range worlds {
		if m.Compared() {
			groups[m.Group]++
		}
	}
	applied := false
	var reasons []string
	for _, m := range worlds {
		if m.Group != "" && !c.PrivateFirst && m.Compared() && groups[m.Group] < 2 {
			t.Fatalf("settlement group %q has 1 world", m.Group)
		}
		switch {
		case !m.Applied:
		case !m.Compared():
			reasons = append(reasons, m.Uncompared)
		case groups[m.Group] < 2:
			reasons = append(reasons, "sole world in "+m.Group)
		default:
			applied = true
		}
	}
	if applied {
		return
	}
	// With no applied world at all, only a preempting private read explains it.
	if len(reasons) == 0 && slices.ContainsFunc(worlds, func(m MetamorphicWorld) bool { return m.Preempted }) {
		reasons = []string{"a private read before the transition discarded every batch"}
	}
	if testing.Short() || len(reasons) == 0 {
		t.Fatalf("%s applies in no compared world: groups %v", c.Transition, groups)
	}
	t.Skipf("impossible comparison: %s", strings.Join(reasons, "; "))
}

// Diff reports the first difference with bounded context.
func Diff(want, got []byte) string {
	i := 0
	for i < len(want) && i < len(got) && want[i] == got[i] {
		i++
	}
	window := func(b []byte) string {
		return string(b[max(0, i-160):min(len(b), i+160)])
	}
	return fmt.Sprintf("at byte %d:\nwant …%s…\ngot  …%s…", i, window(want), window(got))
}
