package gitops

import (
	"slices"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/provenance"
)

func TestProvenanceMapsRealOpenMergeBuilds(t *testing.T) {
	for _, method := range []forge.MergeMethod{forge.Merge, forge.Squash, forge.Rebase, forge.FastForward} {
		t.Run(string(method), func(t *testing.T) {
			f := newFixture(t)
			first := f.commit(f.base, "first\n\nOpenMerge-Replayed-From: "+strings.Repeat("f", 40), map[string]string{"first.txt": "first"})
			second := f.commit(first, "second\n\nAgent-Session: a:one", map[string]string{"second.txt": "second"})
			gitCommand(t, f.source, "", nil, "push", "--force", f.remote, second+":"+headRef(1))
			classifier, err := provenance.NewClassifier([]provenance.IdentityRule{
				{ID: "human", Mailbox: "fixture@localhost", Class: provenance.Human},
				{ID: "generated", Mailbox: "openmerge@localhost", Class: provenance.Agent},
			})
			must(t, err)
			req := provenance.InventoryRequest{Repository: testRepo.Path, Queue: "main", ChangeID: "pr-1", Revision: "r1", Generation: "g1", Head: second, PinnedBase: f.base, BuildBase: f.base}
			limits := provenance.DefaultInventoryLimits()
			inv := ReadProvenanceInventory(t.Context(), f.source, t.TempDir(), req, classifier, limits)
			expectInventoryIDs(t, inv, first, second)
			for _, original := range inv.Commits {
				if original.ObjectID == first && provenance.Classify(original.Declarations) != provenance.Human {
					t.Fatal("forged replay metadata changed original classification")
				}
			}
			prs := []forge.BuildPR{{Number: 1, HeadSHA: second}}
			tested := f.build(method, prs, "test-"+string(method))
			f.fetch(tested)
			observation := provenance.BuildObservation{ID: "test-1", Stage: "tested", Method: string(method), Base: f.base, Result: tested}
			if method == forge.FastForward {
				observation.Marker = tested
			}
			// The fixture captures trusted composition order, independently of all
			// contributor/generated replay trailer text. Production wiring must capture
			// these pairs as the composer creates each commit, rather than read trailers.
			if method == forge.Rebase {
				composed := strings.Fields(gitCommand(t, f.source, "", nil, "rev-list", "--reverse", "--first-parent", tested, "^"+f.base))
				if len(composed) != 2 {
					t.Fatalf("replays = %v", composed)
				}
				observation.Replays = []provenance.CommitMapping{{Original: first, Build: composed[0]}, {Original: second, Build: composed[1]}}
			}
			mapped := MapProvenanceBuild(t.Context(), f.source, t.TempDir(), inv, observation, limits)
			if !mapped.Complete || mapped.Request.Generation != req.Generation || mapped.Request.Head != req.Head || mapped.PolicyID != inv.PolicyID || len(mapped.Commits) != 2 {
				t.Fatalf("mapping = %+v", mapped)
			}
			for _, pair := range mapped.Commits {
				want := pair.Original
				switch method {
				case forge.Squash:
					want = tested
				case forge.Rebase:
					for _, replay := range observation.Replays {
						if replay.Original == pair.Original {
							want = replay.Build
						}
					}
				}
				if pair.Build != want {
					t.Fatalf("pair = %+v, want %s", pair, want)
				}
				if pair.Build == observation.Marker {
					t.Fatal("synthetic marker became contributor")
				}
			}
			// A speculative rebuild has the same pinned originals and declarations,
			// but distinct composition identity and generated commits.
			f.g = f.client(t, 2)
			rebuilt := f.build(method, prs, "rebuild-"+string(method))
			f.fetch(rebuilt)
			rebuiltObservation := observation
			rebuiltObservation.ID, rebuiltObservation.Result, rebuiltObservation.Stage = "landed-2", rebuilt, "landed"
			if method == forge.FastForward {
				rebuiltObservation.Marker = rebuilt
			}
			if method == forge.Rebase {
				composed := strings.Fields(gitCommand(t, f.source, "", nil, "rev-list", "--reverse", "--first-parent", rebuilt, "^"+f.base))
				rebuiltObservation.Replays = []provenance.CommitMapping{{Original: first, Build: composed[0]}, {Original: second, Build: composed[1]}}
			}
			rebuiltMapping := MapProvenanceBuild(t.Context(), f.source, t.TempDir(), inv, rebuiltObservation, limits)
			if !rebuiltMapping.Complete || rebuilt == tested || rebuiltMapping.Build.Stage != "landed" || rebuiltMapping.Request.Generation != mapped.Request.Generation {
				t.Fatalf("rebuild = %+v", rebuiltMapping)
			}
			if method == forge.Rebase || method == forge.Squash {
				if slices.Equal(rebuiltMapping.Commits, mapped.Commits) {
					t.Fatal("rebuild reused generated targets")
				}
			} else if !slices.Equal(rebuiltMapping.Commits, mapped.Commits) {
				t.Fatal("preserved original identity changed")
			}
		})
	}
}

func TestProvenanceBuildMappingRejectsUntrustedOrIncompleteMapping(t *testing.T) {
	f := newProvenanceFixture(t)
	original := f.commit("source\n\nOpenMerge-Replayed-From: "+f.root+"\n", []string{f.root})
	inv := f.read(f.request(original, f.root, f.root), provenance.DefaultInventoryLimits())
	replay := f.commit("replayed\n", []string{f.root})
	build := provenance.BuildObservation{ID: "b1", Stage: "tested", Method: "rebase", Base: f.root, Result: replay}
	limits := provenance.DefaultInventoryLimits()
	noPairs := MapProvenanceBuild(t.Context(), f.dir, f.scratch, inv, build, limits)
	if noPairs.Complete || noPairs.Diagnostic.Resource != "missing_replay" {
		t.Fatalf("contributor trailer supplied mapping: %+v", noPairs)
	}
	build.Replays = []provenance.CommitMapping{{Original: original, Build: replay}}
	good := MapProvenanceBuild(t.Context(), f.dir, f.scratch, inv, build, limits)
	if !good.Complete {
		t.Fatalf("trusted mapping failed: %+v", good)
	}
	for _, change := range []struct {
		name   string
		mutate func(*provenance.BuildObservation)
	}{
		{"duplicate original", func(b *provenance.BuildObservation) { b.Replays = append(slices.Clone(b.Replays), b.Replays[0]) }},
		{"unknown original", func(b *provenance.BuildObservation) {
			b.Replays = []provenance.CommitMapping{{Original: f.root, Build: replay}}
		}},
		{"unreachable target", func(b *provenance.BuildObservation) {
			b.Replays = []provenance.CommitMapping{{Original: original, Build: f.commit("unreachable\n", []string{f.root})}}
		}},
		{"reuse original", func(b *provenance.BuildObservation) {
			b.Replays = []provenance.CommitMapping{{Original: original, Build: original}}
		}},
		{"wrong build base", func(b *provenance.BuildObservation) { b.Base = original }},
		{"wrong method", func(b *provenance.BuildObservation) { b.Method = "guess" }},
		{"wrong marker", func(b *provenance.BuildObservation) { b.Marker = replay }},
	} {
		t.Run(change.name, func(t *testing.T) {
			changed := build
			change.mutate(&changed)
			got := MapProvenanceBuild(t.Context(), f.dir, f.scratch, inv, changed, limits)
			if got.Complete || got.Diagnostic.Outcome != provenance.InvalidInput || len(got.Commits) != 0 {
				t.Fatalf("bad mapping accepted: %+v", got)
			}
		})
	}
	inv.Complete = false
	if got := MapProvenanceBuild(t.Context(), f.dir, f.scratch, inv, build, limits); got.Complete || got.Diagnostic.Outcome != provenance.ReadPending {
		t.Fatalf("incomplete accepted: %+v", got)
	}
}

func TestProvenanceMappingKeepsAlreadyLandedOriginals(t *testing.T) {
	f := newProvenanceFixture(t)
	first := f.commit("first\n", []string{f.root})
	second := f.commit("second\n", []string{first})
	inv := f.read(f.request(second, f.root, first), provenance.DefaultInventoryLimits())
	expectInventoryIDs(t, inv, first, second)
	for _, method := range []string{"squash", "rebase"} {
		result := f.commit(method+"\n", []string{first})
		build := provenance.BuildObservation{ID: method, Stage: "tested", Method: method, Base: first, Result: result}
		if method == "rebase" {
			build.Replays = []provenance.CommitMapping{{Original: second, Build: result}}
		}
		mapped := MapProvenanceBuild(t.Context(), f.dir, f.scratch, inv, build, provenance.DefaultInventoryLimits())
		if !mapped.Complete {
			t.Fatal(mapped)
		}
		for _, pair := range mapped.Commits {
			if pair.Original == first && pair.Build != first {
				t.Fatal("landed obligation was remapped")
			}
			if pair.Original == second && pair.Build != result {
				t.Fatal("new original was not mapped")
			}
		}
	}
}

func TestProvenanceBatchMappingsRemainPerMember(t *testing.T) {
	for _, method := range []forge.MergeMethod{forge.Merge, forge.Squash, forge.Rebase} {
		t.Run(string(method), func(t *testing.T) {
			f := newFixture(t)
			agent := f.commit(f.base, "agent member\n\nAgent-Session: a:one", map[string]string{"agent.txt": "agent"})
			gitCommand(t, f.source, "", nil, "push", "--force", f.remote, agent+":"+headRef(1))
			prs := []forge.BuildPR{{Number: 1, HeadSHA: agent}, f.prs[1]}
			top := f.build(method, prs, "batch-"+string(method))
			f.fetch(top)
			previous := gitCommand(t, f.source, "", nil, "rev-parse", top+"^")
			classifier, err := provenance.NewClassifier([]provenance.IdentityRule{{ID: "human", Mailbox: "fixture@localhost", Class: provenance.Human}})
			must(t, err)
			for i, pr := range prs {
				base, result := f.base, previous
				if i == 1 {
					base, result = previous, top
				}
				req := provenance.InventoryRequest{Repository: testRepo.Path, Queue: "main", ChangeID: string(rune('a' + i)), Revision: "r1", Generation: "batch-g1", Head: pr.HeadSHA, PinnedBase: f.base, BuildBase: base}
				limits := provenance.DefaultInventoryLimits()
				inv := ReadProvenanceInventory(t.Context(), f.source, t.TempDir(), req, classifier, limits)
				expectInventoryIDs(t, inv, pr.HeadSHA)
				class := provenance.Classify(inv.Commits[0].Declarations)
				if (i == 0 && class != provenance.Mixed) || (i == 1 && class != provenance.Human) {
					t.Fatalf("batch pooled classes: member %d = %s", i, class)
				}
				observation := provenance.BuildObservation{ID: "batch-member-" + req.ChangeID, Stage: "tested", Method: string(method), Base: base, Result: result}
				if method == forge.Rebase {
					observation.Replays = []provenance.CommitMapping{{Original: pr.HeadSHA, Build: result}}
				}
				mapping := MapProvenanceBuild(t.Context(), f.source, t.TempDir(), inv, observation, limits)
				if !mapping.Complete || len(mapping.Commits) != 1 || mapping.Commits[0].Original != pr.HeadSHA {
					t.Fatalf("member mapping = %+v", mapping)
				}
			}
		})
	}
}
