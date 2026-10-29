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
}

func (c MetamorphicCase) String() string {
	return fmt.Sprintf("%s/provenance-%t/private-first-%t/cobatch-%t/pr-%d/depth-%d", c.Transition, c.Provenance, c.PrivateFirst, c.Cobatch, c.Target, c.Depth)
}

// MetamorphicCases is the explicit cross product. The short subset is fixed:
// every transition, every dimension value and every historical leak shape.
func MetamorphicCases(short bool) []MetamorphicCase {
	if short {
		// The first five carry every publication-side shape for the slower
		// database-backed serve test.
		return []MetamorphicCase{
			// Pending dequeue batch fields, check bypass and private provenance.
			{"requested", true, false, true, 3, 1},
			// Paused batch-derived detail and co-batched dependency-changed reasons.
			{"paused", true, true, true, 3, 4},
			// Building dependent predecessor with unbatched independent followers.
			{"head", false, true, false, 1, 1},
			// Unconfirmed landing batch metadata.
			{"landing", true, false, true, 3, 1},
			// Change-left-queue invalidation, dequeue and retained departed checks.
			{"closed-planned", true, true, true, 2, 4},
			{"closed", false, false, false, 3, 4},
			{"retargeted", true, false, true, 2, 1},
			{"head-planned", false, false, true, 3, 1},
			{"requested-planned", false, true, false, 3, 1},
			{"retargeted-planned", true, false, true, 1, 4},
			{"ci-passed", true, true, true, 3, 1},
			{"ci-failed", false, false, false, 2, 4},
			{"check-retry", true, false, true, 3, 1},
			{"landed", false, false, false, 2, 4},
		}
	}
	var cases []MetamorphicCase
	for _, transition := range MetamorphicTransitions {
		for _, provenance := range []bool{false, true} {
			for _, privateFirst := range []bool{false, true} {
				for _, cobatch := range []bool{false, true} {
					for _, target := range []int{1, 2, 3} {
						for _, depth := range []int{1, 4} {
							cases = append(cases, MetamorphicCase{transition, provenance, privateFirst, cobatch, target, depth})
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
	// Group names the public settlement facts. Only worlds in the same group
	// observe identical public histories and are compared.
	Group string
	// Tail is a private-only observation recorded after the journal.
	Tail []store.JournalEvent
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
	j, binding, source, grant, prs := multiJournal(t, v.code, v.phase, c.Depth, c.Cobatch, tested)
	b := replay(t, &j, binding)
	b.grant = grant
	at := j.Events[0].RecordedAt
	if v.rebuilt {
		b.privateObservation("landed", "dependency-landed")
		started := 1
		if c.Depth == 4 {
			started = 4
		}
		startBatches(b, c.Depth, c.Cobatch, false, started, 100)
	}
	if c.PrivateFirst {
		b.privateObservation(metamorphicNext(c, w))
	}
	head := func() *queue.Batch {
		batches := b.reader.Queue.Batches()
		if len(batches) == 0 || batches[0].Phase != queue.Testing || batches[0].InvalidationReason != "" {
			return nil
		}
		return batches[0]
	}
	if c.Provenance {
		for _, batch := range b.reader.Queue.Batches() {
			if batch.Phase == queue.Testing {
				b.add(queue.CheckProvenanceObserved{BatchID: batch.ID, TestedSHA: batch.TestedSHA, Checks: []queue.UnboundCheck{{Name: "ci", Source: "github-actions", HeadBranch: "other"}}})
				break
			}
		}
	}
	group := ""
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
		if batch := head(); batch != nil {
			b.add(queue.ChecksObserved{BatchID: batch.ID, TestedSHA: batch.TestedSHA, Runs: runs("failure"), Terminal: true, OriginalDeadline: at.Add(time.Hour), Timeout: time.Hour})
			b.add(queue.CheckRerunRequested{BatchID: batch.ID, TestedSHA: batch.TestedSHA, Runs: runs("failure"), OriginalDeadline: at.Add(time.Hour), At: at.Add(10 * time.Second), Deadline: at.Add(70 * time.Second), Timeout: time.Minute})
		}
	case "landing", "landed":
		if batch := head(); batch != nil {
			members := []string{}
			for _, pin := range batch.PRs {
				members = append(members, fmt.Sprint(pin.PR))
			}
			group = transition + ":" + strings.Join(members, ",")
			b.add(queue.ChecksObserved{BatchID: batch.ID, TestedSHA: batch.TestedSHA, Runs: runs("success"), Terminal: true, Success: true, OriginalDeadline: at.Add(time.Hour), Timeout: time.Hour})
			b.add(queue.ChecksCompleted{TestedSHA: batch.TestedSHA, Success: true})
			b.add(queue.LandingStarted{BatchID: batch.ID, AttemptID: "attempt-" + batch.ID, FenceToken: b.reader.Queue.Fence, At: at})
			if transition == "landed" {
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
	// Every queue exit is a public outcome, e.g. a failed solo batch dequeues B.
	for _, pr := range prs {
		if !slices.ContainsFunc(b.reader.Queue.Entries, func(e queue.Entry) bool { return e.PR == pr }) {
			group += fmt.Sprintf(" left:%d", pr)
		}
	}
	world := MetamorphicWorld{Journal: j, Binding: binding, Source: source, Grant: grant, PRs: prs, Group: group}
	world.Journal.Events = slices.Clone(j.Events)
	n := len(j.Events)
	b.privateObservation(metamorphicNext(c, w+1))
	world.Tail = slices.Clone(j.Events[n:])
	return world
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
	id := b.add(last)
	b.add(dependency.DiagnosticRecorded{Namespace: b.binding.Namespace, ObservationEventID: id, Source: dependencytest.Request().Source.Alias, Evidence: dependency.ReadEvidence{Stage: dependency.WorkRead, Declaration: last.Declaration, Grant: b.grant, Observation: last, Code: code}})
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

// RequireComparedWorlds fails when a case compares nothing. A private read
// before the public transition may invalidate a batch before it can land, so
// only later private reads guarantee two landing worlds.
func RequireComparedWorlds(t testing.TB, c MetamorphicCase, groups map[string]int) {
	t.Helper()
	compared := false
	for group, n := range groups {
		compared = compared || n > 1
		if group != "" && !c.PrivateFirst && n < 2 {
			t.Fatalf("settlement group %q has %d world", group, n)
		}
	}
	landing := c.Transition == "landing" || c.Transition == "landed"
	if !compared || landing && !c.PrivateFirst && len(groups) < 2 {
		t.Fatalf("no comparable worlds: %v", groups)
	}
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
