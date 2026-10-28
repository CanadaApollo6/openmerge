package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/gitops"
	"github.com/CanadaApollo6/openmerge/internal/journalreader"
	p "github.com/CanadaApollo6/openmerge/internal/provenance"
	pj "github.com/CanadaApollo6/openmerge/internal/provenance/journal"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func writerGit(t *testing.T, dir, input string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"--git-dir=" + dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=human@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=human@example.test", "GIT_AUTHOR_DATE=2026-10-18T12:00:00Z", "GIT_COMMITTER_DATE=2026-10-18T12:00:00Z")
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

type writerFixture struct {
	s         *Store
	q         QueueID
	claim     ClaimedAction
	c         landingCase
	input     ProvenanceStart
	decisions []queue.LandingDecisionRecorded
	dir       string
	fresh     []queue.ApprovalEligibilityObserved
}

func newWriterFixture(t *testing.T, method queue.MergeMethod) writerFixture {
	return newWriterBatchFixture(t, method, 1)
}

func newWriterBatchFixture(t *testing.T, method queue.MergeMethod, size int, speculative ...bool) writerFixture {
	t.Helper()
	s := newStore(t)
	dir := filepath.Join(t.TempDir(), "objects.git")
	writerGit(t, dir, "", "init", "--bare", "--template=", dir)
	tree := writerGit(t, dir, "", "mktree")
	base := writerGit(t, dir, "base\n", "commit-tree", tree)
	heads, results := make([]string, size), make([]string, size)
	replayed := make([][]p.CommitMapping, size)
	top := base
	for i := range size {
		parent := base
		if method == queue.FastForward && i > 0 {
			parent = heads[i-1]
		}
		heads[i] = writerGit(t, dir, fmt.Sprintf("agent %d\n\nAgent-Session: codex:writer\nEntire-Checkpoint: abcdef012345\n", i), "commit-tree", tree, "-p", parent)
		replayed[i] = []p.CommitMapping{}
		switch method {
		case queue.Merge:
			top = writerGit(t, dir, fmt.Sprintf("tested merge %d\n", i), "commit-tree", tree, "-p", top, "-p", heads[i])
		case queue.Squash:
			top = writerGit(t, dir, fmt.Sprintf("tested squash %d\n", i), "commit-tree", tree, "-p", top)
		case queue.Rebase:
			top = writerGit(t, dir, fmt.Sprintf("tested replay %d\n", i), "commit-tree", tree, "-p", top)
			replayed[i] = append(replayed[i], p.CommitMapping{Original: heads[i], Build: top})
		case queue.FastForward:
			top = heads[i]
			if i == size-1 {
				top = writerGit(t, dir, "test marker\n", "commit-tree", tree, "-p", top)
			}
		}
		results[i] = top
	}
	head, result := heads[0], results[size-1]
	raw, err := os.ReadFile("../queue/testdata/landing-start.json")
	must(t, err)
	var c landingCase
	must(t, json.Unmarshal(raw, &c))
	oldPolicy := c.Policy.Digest
	c.Policy.Approvals.Rules = nil // This fixture exercises native baseline + additive human pool.
	when, err := p.ParseCondition("#agent-commits > 0")
	must(t, err)
	opt, err := p.NewPolicy(c.Policy, []p.Rule{{When: when, Approvals: &cp.Requirement{Selector: cp.AnyWriter, Human: true, Count: 1}}}, nil, p.NewParserPolicy(writerGit(t, dir, "", "version")), p.DefaultInventoryLimits(), condition.All())
	must(t, err)
	c.Policy.Digest = opt.Digest()
	c.Config.ConfigDigest = strings.Repeat("7", 64)
	_, configRaw, err := queue.EncodeEvent(queue.ConfigApplied{Commit: base, Digest: strings.Repeat("7", 64)})
	must(t, err)
	c.Events = append([]json.RawMessage{configRaw}, c.Events...)
	// Rebind the frozen fixture to actual immutable objects and this opt-in policy.
	filtered := c.Events[:0]
	for _, raw := range c.Events {
		e, err := queue.DecodeEvent(raw)
		must(t, err)
		if _, receipt := e.(queue.ApprovalReceiptRecorded); receipt {
			continue
		}
		filtered = append(filtered, raw)
	}
	c.Events = filtered
	fresh := []queue.ApprovalEligibilityObserved{c.Fresh}
	for i := 1; i < size; i++ {
		id, err := (QueueKey{Installation: "7", Repository: "Owner/Repo", BaseBranch: "release"}).ChangeID(12 + i)
		must(t, err)
		rewrite := func(raw []byte) []byte {
			var compact bytes.Buffer
			must(t, json.Compact(&compact, raw))
			raw = compact.Bytes()
			raw = bytes.ReplaceAll(raw, []byte(c.Fresh.Basis.ChangeID), []byte(id))
			raw = bytes.ReplaceAll(raw, []byte(c.Fresh.Basis.Head), []byte(heads[i]))
			return bytes.ReplaceAll(raw, []byte(`"pr":12`), []byte(fmt.Sprintf(`"pr":%d`, 12+i)))
		}
		var events []json.RawMessage
		for _, raw := range c.Events {
			e, err := queue.DecodeEvent(raw)
			must(t, err)
			if b, ok := e.(queue.BatchStarted); ok {
				b.PRs = append(b.PRs, queue.BatchPR{PR: 12 + i, HeadSHA: heads[i]})
				_, raw, err = queue.EncodeEvent(b)
				must(t, err)
			}
			events = append(events, raw)
			switch e := e.(type) {
			case queue.ChangeRevisionObserved:
				if e.PR == 12 {
					events = append(events, rewrite(raw))
				}
			case queue.PRObserved:
				if e.PR == 12 {
					events = append(events, rewrite(raw))
				}
			case queue.EnqueueRequested:
				if e.PR == 12 {
					events = append(events, rewrite(raw))
				}
			case queue.ChangeClassified, queue.ChangeAccountabilityObserved, queue.ChangeIntentObserved, queue.ReviewStanceObserved:
				if bytes.Contains(raw, []byte(c.Fresh.Basis.ChangeID)) || bytes.Contains(raw, []byte(`"pr":12`)) {
					events = append(events, rewrite(raw))
				}
			}
		}
		c.Events = events
		c.Land.PRs = append(c.Land.PRs, queue.BatchPR{PR: 12 + i, HeadSHA: heads[i]})
		fraw, err := json.Marshal(c.Fresh)
		must(t, err)
		var f queue.ApprovalEligibilityObserved
		must(t, json.Unmarshal(rewrite(fraw), &f))
		fresh = append(fresh, f)
	}
	c.Land.Method = method
	c.Config.MergeMethod = method
	if method == queue.FastForward {
		events := []json.RawMessage{}
		for _, raw := range c.Events {
			e, err := queue.DecodeEvent(raw)
			must(t, err)
			if built, ok := e.(queue.TestRefBuilt); ok {
				_, selection, err := queue.EncodeEvent(queue.FastForwardBatchSelected{BatchID: built.BatchID, PRs: c.Land.PRs})
				must(t, err)
				events = append(events, selection)
				_, raw, err = queue.EncodeEvent(queue.FastForwardRefBuilt{BatchID: built.BatchID, TestedSHA: built.TestedSHA, HeadSHA: c.Land.PRs[len(c.Land.PRs)-1].HeadSHA, At: built.At})
				must(t, err)
			}
			events = append(events, raw)
		}
		c.Events = events
	} else {
		for i, raw := range c.Events {
			e, err := queue.DecodeEvent(raw)
			must(t, err)
			if built, ok := e.(queue.TestRefBuilt); ok {
				built.Method = method
				_, raw, err = queue.EncodeEvent(built)
				must(t, err)
				c.Events[i] = raw
			}
		}
	}
	if len(speculative) > 0 && speculative[0] {
		c.Config.SpeculationMinDepth, c.Config.SpeculationMaxDepth = 2, 2
		for i, raw := range c.Events {
			e, err := queue.DecodeEvent(raw)
			must(t, err)
			switch e := e.(type) {
			case queue.ConfigApplied:
				e.Window = &queue.WindowBounds{MinDepth: 2, MaxDepth: 2}
				_, raw, err = queue.EncodeEvent(e)
			case queue.BatchStarted:
				_, raw, err = queue.EncodeEvent(queue.ChainedBatchStarted{BatchID: e.BatchID, BaseSHA: e.BaseSHA, PRs: e.PRs, At: e.At, Position: 1})
			case queue.TestRefBuilt:
				_, raw, err = queue.EncodeEvent(queue.ChainedTestRefBuilt{BatchID: e.BatchID, TestedSHA: e.TestedSHA, LandingSHA: e.TestedSHA, Method: e.Method, At: e.At, Position: 1})
			}
			must(t, err)
			c.Events[i] = raw
		}
	}
	raw, err = json.Marshal(c)
	must(t, err)
	for old, value := range map[string]string{oldPolicy: opt.Digest(), strings.Repeat("a", 40): base, strings.Repeat("b", 40): head, strings.Repeat("e", 40): result} {
		raw = bytes.ReplaceAll(raw, []byte(old), []byte(value))
	}
	q, claim, c, _ := decisionLandInput(t, s, raw)
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	classifier, _, limits := opt.ReaderInputs()
	input := ProvenanceStart{Policy: opt, Expression: condition.All(), DefaultBranch: "main", Members: []ProvenanceMemberInput{}}
	fresh[0] = c.Fresh
	for i, f := range fresh {
		buildBase := base
		if i > 0 {
			buildBase = results[i-1]
		}
		excluded := []string{}
		if method == queue.FastForward {
			excluded = append(excluded, heads[:i]...)
			excluded = sortedStrings(excluded)
		}
		req := p.InventoryRequest{Repository: "Owner/Repo", Queue: "release/", ChangeID: string(f.Basis.ChangeID), Revision: "1", Generation: f.Basis.ReadID, Head: heads[i], PinnedBase: base, BuildBase: buildBase, Excluded: excluded}
		inv := gitops.ReadProvenanceInventory(t.Context(), dir, t.TempDir(), req, classifier, limits)
		if !inv.Complete {
			t.Fatalf("inventory: %+v", inv)
		}
		build := p.BuildObservation{ID: fmt.Sprintf("%s/%d", c.Start.AttemptID, i), Stage: "tested", Method: string(method), Base: buildBase, Result: results[i], Replays: replayed[i]}
		if method == queue.FastForward && i == size-1 {
			build.Marker = result
		}
		tested := gitops.MapProvenanceBuild(t.Context(), dir, "", inv, build, limits)
		landedResult := result
		if method == queue.FastForward {
			landedResult = heads[size-1]
		}
		lm, err := landedMapping(tested, landedResult, i == size-1)
		must(t, err)
		landed := gitops.MapProvenanceBuild(t.Context(), dir, "", inv, lm.Build, limits)
		input.Members = append(input.Members, ProvenanceMemberInput{Inventory: inv, Tested: tested, Landed: landed, Values: condition.Snapshot{}})
	}
	decisions, _, err := input.Decisions(state, c.Policy, c.Land, c.Start, fresh)
	must(t, err)
	return writerFixture{s: s, q: q, claim: claim, c: c, input: input, decisions: decisions, dir: dir, fresh: fresh}
}

func (f writerFixture) start(ctx context.Context) error {
	changes := make([]queue.ChangeID, len(f.fresh))
	for i, v := range f.fresh {
		changes[i] = v.Basis.ChangeID
	}
	read, err := f.s.BeginApprovalRead(ctx, f.q, changes)
	if err != nil {
		return err
	}
	return f.s.StartProvenanceLanding(read.Context(ctx), f.claim, f.c.Config, f.c.Policy, f.fresh, f.decisions, f.c.Start, f.input)
}

func TestProvenanceWritersRealGitTestedLandedAndReplay(t *testing.T) {
	for _, method := range []queue.MergeMethod{queue.Merge, queue.Squash, queue.Rebase, queue.FastForward} {
		t.Run(string(method), func(t *testing.T) {
			f := newWriterFixture(t, method)
			must(t, f.start(t.Context()))
			r, _, err := replayJournal(t.Context(), f.s.pool, f.q, queue.State{BaseBranch: "release", Fence: "fence"}, 0, true)
			must(t, err)
			if len(r.Provenance.Records()) != 2 {
				t.Fatal("missing atomic T", len(r.Provenance.Records()))
			}
			original := r.Provenance.Records()[1]
			event, err := pj.DecodeEvent(original.Payload)
			must(t, err)
			tested := event.(pj.Landing)
			if tested.Stage != "tested" || tested.Members[0].Layers[1].Truth != "true" || len(tested.Members[0].Layers[2].References) < 2 {
				t.Fatal("lost separate additive evidence", tested)
			}
			if tested.Members[0].Mapping.PolicyID != p.NewParserPolicy(writerGit(t, f.dir, "", "version")).Identity() {
				t.Fatal("Git identity missing")
			}
			landedID := appendEvents(t, f.s, f.q, queue.Landed{BatchID: f.c.Start.BatchID, NewBaseSHA: f.decisions[0].LandedSHA, NewFence: "next"})
			// Simulate a process loss after Landed and before L. Recovery must need no
			// object directory, transport, credentials, current refs or forge callback.
			must(t, os.RemoveAll(f.dir))
			must(t, f.s.RecoverProvenance(t.Context(), f.q))
			count := eventCount(t, f.s, f.q)
			must(t, f.s.RecoverProvenance(t.Context(), f.q))
			if eventCount(t, f.s, f.q) != count {
				t.Fatal("restart duplicated L")
			}
			r, _, err = replayJournal(t.Context(), f.s.pool, f.q, queue.State{BaseBranch: "release", Fence: "fence"}, 0, true)
			must(t, err)
			if len(r.Provenance.Records()) != 0 || len(r.Provenance.AwaitingLandings()) != 0 {
				t.Fatal("recovery did not retire T/L")
			}
			var payload []byte
			must(t, f.s.pool.QueryRow(t.Context(), `SELECT payload FROM events WHERE queue_id=$1 AND id>$2 AND type='landing_provenance_recorded'`, f.q, landedID).Scan(&payload))
			decoded, err := pj.DecodeEvent(payload)
			must(t, err)
			landed := decoded.(pj.Landing)
			if landed.Stage != "landed" || landed.Members[0].Mapping.Build.ID == tested.Members[0].Mapping.Build.ID || landed.Members[0].Mapping.Build.Result != f.decisions[0].LandedSHA {
				t.Fatal("L called T", landed)
			}
			if method == queue.FastForward && (landed.Members[0].Mapping.Build.Marker != "" || tested.Members[0].Mapping.Build.Marker == "") {
				t.Fatal("marker leaked to L")
			}
			// Checkpoint + every possible suffix is identical to full offline replay.
			rows, err := f.s.pool.Query(t.Context(), `SELECT id,payload FROM events WHERE queue_id=$1 ORDER BY id`, f.q)
			must(t, err)
			type row struct {
				id  uint64
				raw []byte
			}
			var records []row
			for rows.Next() {
				var v row
				must(t, rows.Scan(&v.id, &v.raw))
				records = append(records, v)
			}
			must(t, rows.Err())
			rows.Close()
			seed := queue.State{BaseBranch: "release", Fence: "fence"}
			full := journalreader.New(seed, dependency.JournalInitialized{}, func(id uint64) ([]byte, error) {
				for _, v := range records {
					if v.id == id {
						return v.raw, nil
					}
				}
				return nil, errors.New("missing")
			})
			lookup := func(id uint64) ([]byte, error) {
				for _, v := range records {
					if v.id == id {
						return v.raw, nil
					}
				}
				return nil, errors.New("missing")
			}
			for _, v := range records {
				_, err = full.Apply(v.id, v.raw)
				must(t, err)
			}
			want, err := full.CursorSnapshot()
			must(t, err)
			for cut := range len(records) + 1 {
				prefix := journalreader.New(seed, dependency.JournalInitialized{}, lookup)
				var through uint64
				for _, v := range records[:cut] {
					_, err = prefix.Apply(v.id, v.raw)
					must(t, err)
					through = v.id
				}
				snap, err := prefix.CursorSnapshot()
				must(t, err)
				resumed := journalreader.New(seed, dependency.JournalInitialized{}, lookup)
				must(t, resumed.LoadSnapshot(snap, through))
				for _, v := range records[cut:] {
					_, err = resumed.Apply(v.id, v.raw)
					must(t, err)
				}
				got, err := resumed.CursorSnapshot()
				must(t, err)
				if !bytes.Equal(got, want) {
					t.Fatalf("offline replay cut %d", cut)
				}
			}
		})
	}
}

func TestProvenanceStartMutationRefusalsAreAtomic(t *testing.T) {
	for _, mutation := range []string{"generation", "requirements", "parser", "mapping", "decision", "missing-activation", "namespace", "insert"} {
		t.Run(mutation, func(t *testing.T) {
			f := newWriterFixture(t, queue.Squash)
			before := eventCount(t, f.s, f.q)
			switch mutation {
			case "generation":
				f.input.Members[0].PreviousObservationID = 99
			case "requirements":
				f.fresh[0].Actors = map[string]cp.Actor{}
			case "parser":
				f.input.Members[0].Inventory.Policy = p.NewParserPolicy("git version 0")
			case "mapping":
				f.input.Members[0].Landed.Build.Result = f.c.Land.ExpectedBaseSHA
			case "decision":
				f.decisions[0].Approval.InputFingerprint = strings.Repeat("f", 64)
			case "missing-activation":
				f.input.Policy = nil
			case "namespace":
				m := &f.input.Members[0]
				m.Inventory.Request.Repository = "other/repo"
				m.Tested.Request = m.Inventory.Request
				m.Landed.Request = m.Inventory.Request
			case "insert":
				_, err := f.s.pool.Exec(t.Context(), `CREATE FUNCTION reject_writer() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='landing_provenance_recorded' THEN RAISE EXCEPTION 'forced writer failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_writer BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION reject_writer()`)
				must(t, err)
			}
			if err := f.start(t.Context()); err == nil {
				t.Fatal("mutated start admitted", mutation)
			}
			assertNoLandingSideEffects(t, f.s, f.q, f.claim, before)
			if count(t, f.s, `SELECT count(*) FROM observations WHERE queue_id=$1 AND key LIKE 'side-append/%'`, f.q) != 0 {
				t.Fatal("partial checkpoint")
			}
		})
	}
}

func TestProvenanceGenerationCASRace(t *testing.T) {
	f := newWriterFixture(t, queue.Squash)
	// Synchronize after the off-transaction read: a competing generation wins
	// under the queue lock while the prepared landing remains in flight.
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() {
		<-ready
		h, err := provenanceChunks(f.input.Members[0].Inventory, pj.PolicySource{DefaultBranch: "main", ReadRef: "refs/heads/main", Commit: f.c.Land.ExpectedBaseSHA, EffectiveDigest: f.c.Policy.Digest}, 0)
		if err == nil {
			h[0].Request.Generation = "competing-generation"
			h[0].Digest = pj.InventoryDigest(h[0], h[0].Commits)
			_, err = f.s.AppendSide(t.Context(), f.q, sideObservation(h[0], "competing-generation"))
		}
		done <- err
	}()
	close(ready)
	must(t, <-done)
	before := eventCount(t, f.s, f.q)
	if err := f.start(t.Context()); !errors.Is(err, pj.ErrBinding) {
		t.Fatal("stale generation survived", err)
	}
	assertNoLandingSideEffects(t, f.s, f.q, f.claim, before)
}

func TestProvenanceChunksCanonicalAndBounded(t *testing.T) {
	f := newWriterFixture(t, queue.Merge)
	inv := f.input.Members[0].Inventory
	inv.Commits = append(inv.Commits, inv.Commits[0])
	inv.Commits[1].ObjectID = strings.Repeat("f", 40)
	inv.Stable = sortedStrings([]string{inv.Commits[0].ObjectID, inv.Commits[1].ObjectID})
	// Reverse and duplicate declarations, as a reader can produce independent
	// trailer and identity sets. The writer owns canonical sorting/deduplication.
	inv.Commits[0].Declarations = append(inv.Commits[0].Declarations, inv.Commits[0].Declarations...)
	headers, err := provenanceChunks(inv, pj.PolicySource{DefaultBranch: "main", ReadRef: "refs/heads/main", Commit: inv.Request.PinnedBase, EffectiveDigest: f.c.Policy.Digest}, 0)
	must(t, err)
	var state pj.State
	for i, h := range headers {
		state, err = pj.Apply(state, uint64(i+1), h)
		must(t, err)
	}
	got, ok := state.Inventory(inv.Request)
	if !ok || len(got.Commits) != 2 || len(got.Commits[0].Declarations) != 2 {
		t.Fatal("noncanonical source inventory", got)
	}
	if !reflect.DeepEqual(inv.Commits[0].Declarations[0], inv.Commits[0].Declarations[2]) {
		t.Fatal("writer mutated source declarations")
	}
	t.Logf("canonical inventory: %d chunks", len(headers))
}

func TestProvenanceBatchEveryMemberAndIntermediateCompositions(t *testing.T) {
	for _, method := range []queue.MergeMethod{queue.Merge, queue.Squash, queue.Rebase, queue.FastForward} {
		t.Run(string(method), func(t *testing.T) {
			f := newWriterBatchFixture(t, method, 2)
			before := eventCount(t, f.s, f.q)
			valid := f.fresh[1]
			f.fresh[1].Actors = map[string]cp.Actor{}
			if err := f.start(t.Context()); err == nil {
				t.Fatal("secondary member borrowed approvals")
			}
			assertNoLandingSideEffects(t, f.s, f.q, f.claim, before)
			f.fresh[1] = valid
			must(t, f.start(t.Context()))
			r, _, err := replayJournal(t.Context(), f.s.pool, f.q, queue.State{BaseBranch: "release", Fence: "fence"}, 0, true)
			must(t, err)
			records := r.Provenance.Records()
			event, err := pj.DecodeEvent(records[len(records)-1].Payload)
			must(t, err)
			tested := event.(pj.Landing)
			if len(tested.Members) != 2 || tested.Members[0].Mapping.Build.Result == tested.Members[1].Mapping.Build.Result {
				t.Fatal("member compositions pooled")
			}
			for i, m := range tested.Members {
				if len(m.Mapping.Commits) != 1 || m.Mapping.Commits[0].Original != f.c.Land.PRs[i].HeadSHA || m.Request.ChangeID != string(f.fresh[i].Basis.ChangeID) {
					t.Fatal("member originals pooled", m)
				}
			}
			appendEvents(t, f.s, f.q, queue.Landed{BatchID: f.c.Start.BatchID, NewBaseSHA: f.decisions[0].LandedSHA, NewFence: "next"})
			must(t, f.s.RecoverProvenance(t.Context(), f.q))
		})
	}
}

func TestProvenanceRecoveryRebindsOnlySupersededHistoricalInventories(t *testing.T) {
	f := newWriterBatchFixture(t, queue.Rebase, 2)
	must(t, f.start(t.Context()))
	appendEvents(t, f.s, f.q, queue.Landed{BatchID: f.c.Start.BatchID, NewBaseSHA: f.decisions[0].LandedSHA, NewFence: "next"})
	r, _, err := replayJournal(t.Context(), f.s.pool, f.q, queue.State{BaseBranch: "release", Fence: "fence"}, 0, true)
	must(t, err)
	h := r.Provenance.Records()[0]
	event, err := pj.DecodeEvent(h.Payload)
	must(t, err)
	header := event.(pj.Observation)
	header.PreviousObservationID = h.ID
	header.Request.Generation = "newer-generation"
	header.Digest = pj.InventoryDigest(header, header.Commits)
	_, err = f.s.AppendSide(t.Context(), f.q, sideObservation(header, "superseding-historical"))
	must(t, err)
	must(t, os.RemoveAll(f.dir))
	must(t, f.s.RecoverProvenance(t.Context(), f.q))
	var raw []byte
	must(t, f.s.pool.QueryRow(t.Context(), `SELECT payload FROM events WHERE queue_id=$1 AND type='landing_provenance_recorded' ORDER BY id DESC LIMIT 1`, f.q).Scan(&raw))
	e, err := pj.DecodeEvent(raw)
	must(t, err)
	landed := e.(pj.Landing)
	if !strings.HasPrefix(landed.Members[0].Request.Generation, "recover/") || landed.Members[1].Request.Generation != f.fresh[1].Basis.ReadID {
		t.Fatal("unrelated inventories reconstructed", landed)
	}
	if landed.Members[0].Mapping.Commits[0].Original != f.c.Land.PRs[0].HeadSHA {
		t.Fatal("historical source changed")
	}
}

func TestProvenanceSpeculativeRebuildUsesNewGeneration(t *testing.T) {
	f := newWriterBatchFixture(t, queue.Squash, 1, true)
	must(t, f.start(t.Context()))
	priorDigest := f.decisions[0].Approval.Identity.BriefDigest
	appendEvents(t, f.s, f.q, queue.LandingAbandoned{BatchID: f.c.Start.BatchID, NewFence: "next"})
	must(t, f.s.Complete(t.Context(), f.claim, Done))
	state, _, err := f.s.Load(t.Context(), f.q)
	must(t, err)
	old := f.input.Members[0]
	previous, err := f.s.ProvenancePredecessors(t.Context(), f.q, []p.InventoryRequest{old.Inventory.Request})
	must(t, err)
	if previous[0] == 0 {
		t.Fatal("retired generation fence lost")
	}
	tree := writerGit(t, f.dir, "", "rev-parse", old.Inventory.Request.Head+"^{tree}")
	result := writerGit(t, f.dir, "rebuilt\n", "commit-tree", tree, "-p", state.BaseSHA)
	f.c.Land.BatchID = "b2"
	f.c.Land.TestedSHA = result
	f.c.Land.Ref = "openmerge/queue/release/b2"
	f.c.Land.FenceToken = "next"
	f.c.Start.BatchID = "b2"
	f.c.Start.AttemptID = "rebuilt-attempt"
	f.c.Start.FenceToken = "next"
	f.c.Start.At = f.c.Start.At.Add(time.Minute)
	appendEvents(t, f.s, f.q, queue.ChainedBatchStarted{BatchID: "b2", BaseSHA: state.BaseSHA, PRs: f.c.Land.PRs, At: f.c.Start.At, Position: 1}, queue.ChainedTestRefBuilt{BatchID: "b2", TestedSHA: result, LandingSHA: result, Method: queue.Squash, At: f.c.Start.At, Position: 1}, queue.ChecksCompleted{TestedSHA: result, Success: true})
	state, _, err = f.s.Load(t.Context(), f.q)
	must(t, err)
	if state.Active == nil || !state.Active.Speculative {
		t.Fatal("did not rebuild speculative batch")
	}
	f.fresh[0].Basis.ReadID = "rebuilt-read"
	f.fresh[0].Basis.At = f.c.Start.At
	m := &f.input.Members[0]
	m.Inventory.Request.Generation = "rebuilt-read"
	m.PreviousObservationID = previous[0]
	m.Tested.Request = m.Inventory.Request
	m.Tested.Build.ID = "rebuilt-attempt/0"
	m.Tested.Build.Result = result
	m.Tested = gitops.MapProvenanceBuild(t.Context(), f.dir, "", m.Inventory, m.Tested.Build, m.Inventory.Limits)
	m.Landed, err = landedMapping(m.Tested, result, true)
	must(t, err)
	f.decisions, _, err = f.input.Decisions(state, f.c.Policy, f.c.Land, f.c.Start, f.fresh)
	must(t, err)
	if f.decisions[0].Approval.Identity.BriefDigest != priorDigest {
		t.Fatal("composition rebuild churned declarations")
	}
	_, err = f.s.Plan(t.Context(), f.q, f.c.Config, f.c.Start.At)
	must(t, err)
	claims, err := f.s.ClaimQueue(t.Context(), f.q, 16, time.Minute, f.c.Start.At)
	must(t, err)
	found := false
	for _, claim := range claims {
		if land, ok := claim.Action.(queue.Land); ok && land.BatchID == "b2" {
			f.claim = claim
			found = true
		}
	}
	if !found {
		t.Fatal("rebuilt landing not planned", claims)
	}
	proceed, err := f.s.BeginConsequential(t.Context(), f.claim, f.c.Config, f.c.Start.At)
	must(t, err)
	if !proceed {
		t.Fatal("rebuilt landing declined")
	}
	must(t, f.start(t.Context()))
}

func TestProvenanceRepushAndBaseRewriteInvalidatePreparedStart(t *testing.T) {
	for _, kind := range []string{"repush", "base-rewrite"} {
		t.Run(kind, func(t *testing.T) {
			f := newWriterFixture(t, queue.Squash)
			old := f.input.Members[0].Inventory
			tree := writerGit(t, f.dir, "", "rev-parse", old.Request.Head+"^{tree}")
			req := old.Request
			req.Generation = "fresh-after-" + kind
			if kind == "repush" {
				req.Head = writerGit(t, f.dir, "repushed\n", "commit-tree", tree, "-p", old.Request.PinnedBase)
				req.Revision = "2"
				appendEvents(t, f.s, f.q, queue.ChangeRevisionObserved{PR: f.c.Land.PRs[0].PR, ChangeID: f.c.Fresh.Basis.ChangeID, Revision: 2, Predecessor: 1, HeadSHA: req.Head, Basis: queue.RevisionObservationBasis{BaseSHA: req.PinnedBase, At: f.c.Start.At.Add(time.Minute)}})
			} else {
				req.BuildBase = writerGit(t, f.dir, "rewritten base\n", "commit-tree", tree)
				appendEvents(t, f.s, f.q, queue.BaseMoved{SHA: req.BuildBase})
			}
			before := eventCount(t, f.s, f.q)
			if err := f.start(t.Context()); err == nil {
				t.Fatal("stale prepared landing survived", kind)
			}
			assertNoLandingSideEffects(t, f.s, f.q, f.claim, before)
			classifier, _, limits := f.input.Policy.ReaderInputs()
			inventory := gitops.ReadProvenanceInventory(t.Context(), f.dir, t.TempDir(), req, classifier, limits)
			if !inventory.Complete {
				t.Fatal("fresh inventory incomplete", inventory)
			}
			if kind == "repush" && inventory.Commits[0].ObjectID == old.Commits[0].ObjectID {
				t.Fatal("repush reused originals")
			}
			if kind == "base-rewrite" && (len(inventory.Additional) != 1 || inventory.Additional[0] != old.Request.PinnedBase) {
				t.Fatal("base rewrite erased added original", inventory)
			}
		})
	}
}

func TestProvenanceCompleteEmptyInventoryUsesArrayEncoding(t *testing.T) {
	f := newWriterFixture(t, queue.Merge)
	classifier, _, limits := f.input.Policy.ReaderInputs()
	req := f.input.Members[0].Inventory.Request
	req.Head, req.BuildBase = req.PinnedBase, req.PinnedBase
	inv := gitops.ReadProvenanceInventory(t.Context(), f.dir, t.TempDir(), req, classifier, limits)
	if !inv.Complete || len(inv.Commits) != 0 {
		t.Fatal("not an empty inventory", inv)
	}
	headers, err := provenanceChunks(inv, pj.PolicySource{DefaultBranch: "main", ReadRef: "refs/heads/main", Commit: req.PinnedBase, EffectiveDigest: f.c.Policy.Digest}, 0)
	must(t, err)
	state, err := pj.Apply(pj.State{}, 1, headers[0])
	must(t, err)
	complete, ok := state.Inventory(req)
	if !ok || complete.Commits == nil || len(complete.Commits) != 0 {
		t.Fatal("empty inventory did not round trip", complete)
	}
	mapping := gitops.MapProvenanceBuild(t.Context(), f.dir, "", inv, p.BuildObservation{ID: "empty", Stage: "landed", Method: "fast-forward", Base: req.BuildBase, Result: req.Head}, limits)
	if !mapping.Complete || mapping.Commits == nil || mapping.Build.Replays == nil {
		t.Fatal("empty mapping did not use arrays", mapping)
	}
}
