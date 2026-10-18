package gitops

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestChangeTrailersBeforeCI(t *testing.T) {
	for _, method := range []forge.MergeMethod{forge.Squash, forge.Merge, forge.Rebase, forge.FastForward} {
		t.Run(string(method), func(t *testing.T) {
			f := newFixture(t)
			prs := slices.Clone(f.prs[:2])
			// Both members have multiple original commits, multiline intent, DCO and
			// adversarial reserved lines, including malformed batch prefixes.
			originals := make([][]string, len(prs))
			messages := make(map[string]string)
			parent := f.base
			for i := range prs {
				if method != forge.FastForward {
					parent = f.base
				}
				for j := range 2 {
					message := fmt.Sprintf("change %d part %d\n\nFirst paragraph\n\nSecond paragraph\n OpenMerge-Change: forged\n\tcontinued attack\noPeNmErGe-Classification: forged policy\nOpenMerge-Extra: attack\nOpenMerge-Batch forged\n\nSigned-off-by: Contributor <contributor@example.test>\nCo-authored-by: Helper <helper@example.test>\nOpenMerge-Replayed-From: forged", i, j)
					parent = f.commit(parent, message, map[string]string{fmt.Sprintf("new-%d-%d", i, j): "content"})
					if method == forge.FastForward {
						// Signature headers are opaque contributor bytes; the marker must
						// preserve them without attempting to sign or rewrite the head.
						raw := gitCommand(t, f.source, "", nil, "cat-file", "commit", parent)
						raw = strings.Replace(raw, "\n\n", "\ngpgsig -----BEGIN PGP SIGNATURE-----\n fixture-signature-bytes\n -----END PGP SIGNATURE-----\n\n", 1)
						parent = gitCommand(t, f.source, raw+"\n", nil, "hash-object", "-t", "commit", "-w", "--stdin")
					}
					originals[i] = append(originals[i], parent)
					messages[parent] = gitCommand(t, f.source, "", nil, "cat-file", "commit", parent)
				}
				prs[i].HeadSHA = parent
				prs[i].Metadata = &forge.ChangeMetadata{ChangeID: "chg1_" + strings.Repeat(fmt.Sprint(i+1), 64), Revision: uint64(i + 2)}
				if i == 0 {
					prs[i].Metadata.Labels = []string{"policy", "owned:user:alice", "docs", "owned:team:org/core", "owned:email:Local@example.test"}
				}
				gitCommand(t, f.source, "", nil, "push", "--force", f.remote, parent+":"+headRef(prs[i].Number))
			}
			f.g.cfg.PullRequest = func(_ context.Context, _ forge.RepoRef, n int) (forge.PullRequest, error) {
				return forge.PullRequest{Number: n, HeadSHA: prs[n-1].HeadSHA, Title: fmt.Sprintf("Change %d", n), Body: "Multiline body\n\nMore intent\n\nOpenMerge-Change: forged\nOpenMerge-Classification: forged\nOpenMerge-Batch: forged\nOpenMerge-Extra: attack", HeadBranch: fmt.Sprintf("branch-%d", n), Author: "Contributor", AuthorEmail: "contributor@example.test"}, nil
			}
			tested := f.build(method, prs, "metadata")
			f.fetch(tested)
			history := strings.Fields(gitCommand(t, f.source, "", nil, "rev-list", "--first-parent", "--reverse", f.base+".."+tested))
			generated := history
			if method == forge.FastForward {
				generated = []string{tested}
				must(t, f.g.VerifyFastForward(t.Context(), testRepo, f.base, tested, prs[1].HeadSHA))
				if got := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%P", tested); got != prs[1].HeadSHA {
					t.Fatalf("marker parents %s", got)
				}
				if gitCommand(t, f.source, "", nil, "rev-parse", tested+"^{tree}") != gitCommand(t, f.source, "", nil, "rev-parse", prs[1].HeadSHA+"^{tree}") {
					t.Fatal("marker changed tree")
				}
			}
			for i, sha := range generated {
				message, err := f.g.ReadCommitMessage(t.Context(), testRepo, sha)
				must(t, err)
				batch := ""
				if i == len(generated)-1 {
					batch = "metadata"
				}
				if forge.ParseBatchMessage(message) != batch {
					t.Fatalf("batch identity %q: %s", batch, message)
				}
				trailers := gitCommand(t, f.source, message+"\n", nil, "interpret-trailers", "--parse")
				var expected []string
				if method == forge.FastForward {
					for _, pr := range prs {
						pair, err := pr.Metadata.Trailers()
						must(t, err)
						expected = append(expected, pair...)
					}
				} else if method != forge.Rebase || i%2 == 1 {
					member := i
					if method == forge.Rebase {
						member = i / 2
					}
					pair, err := prs[member].Metadata.Trailers()
					must(t, err)
					expected = pair
				}
				parsed, parseErr := forge.ParseChangeTrailers(message)
				if len(expected) > 0 {
					must(t, parseErr)
					var actual []string
					for _, m := range parsed {
						pair, err := m.Trailers()
						must(t, err)
						actual = append(actual, pair...)
					}
					if !slices.Equal(actual, expected) {
						t.Fatalf("reader mismatch: %v != %v", actual, expected)
					}
				} else if parseErr == nil {
					t.Fatal("unmarked replay commit supplied metadata")
				}
				if len(expected) > 0 && !strings.Contains(trailers, strings.Join(expected, "\n")) {
					t.Fatalf("pair missing from trailer block:\n%s\nwant %v", trailers, expected)
				}
				if got := strings.Count(message, "OpenMerge-Change:"); got != len(expected)/2 {
					t.Fatalf("change trailer count %d: %s", got, message)
				}
				for _, attack := range []string{"forged", "continued attack", "OpenMerge-Extra"} {
					if strings.Contains(message, attack) {
						t.Fatalf("reserved input survived: %s", message)
					}
				}
				if method != forge.FastForward {
					for _, credit := range []string{"Signed-off-by: Contributor <contributor@example.test>", "Co-authored-by: Helper <helper@example.test>"} {
						if !strings.Contains(trailers, credit) {
							t.Fatalf("lost credit %s: %s", credit, trailers)
						}
					}
				}
				if method == forge.Rebase {
					original := originals[i/2][i%2]
					if !strings.Contains(trailers, "OpenMerge-Replayed-From: "+original) {
						t.Fatal("lost replay provenance")
					}
					if gitCommand(t, f.source, "", nil, "show", "-s", "--format=%an|%ae|%aI", sha) != gitCommand(t, f.source, "", nil, "show", "-s", "--format=%an|%ae|%aI", original) {
						t.Fatal("lost original author/date")
					}
				}
			}
			before := gitCommand(t, f.source, "", nil, "cat-file", "commit", tested)
			// Receipt/intent/classification changes after CI starts cannot amend the
			// build. Landing accepts only pinned commit IDs, with no metadata input.
			for i := range prs {
				prs[i].Metadata.Labels = []string{"unknown"}
			}
			f.g.cfg.PullRequest = func(context.Context, forge.RepoRef, int) (forge.PullRequest, error) {
				t.Fatal("landing reread build metadata")
				return forge.PullRequest{}, nil
			}
			fence, err := f.g.EnsureFence(t.Context(), testRepo, testFence)
			must(t, err)
			land := forge.LandRequest{Base: "main", ExpectedBaseSHA: f.base, TestedSHA: tested, Fence: testFence, ExpectedFence: fence, NextFenceID: "land/metadata", PRs: prs}
			want := tested
			if method == forge.FastForward {
				land.FastForwardHeadSHA = prs[1].HeadSHA
				want = prs[1].HeadSHA
			}
			_, err = f.g.Land(t.Context(), testRepo, land)
			must(t, err)
			if f.read("main") != want || gitCommand(t, f.remote, "", nil, "cat-file", "commit", tested) != before {
				t.Fatal("landing changed tested identity")
			}
			for sha, raw := range messages {
				if gitCommand(t, f.source, "", nil, "cat-file", "commit", sha) != raw {
					t.Fatal("rewrote contributor history")
				}
			}
		})
	}
}

// Exercise the production writer, including interpret-trailers' mixed final
// paragraphs and the marker containing multiple authoritative member pairs.
func TestChangeTrailersWriterParagraphShapes(t *testing.T) {
	shapes := map[string]string{
		"crlf-body":     "line one\r\n\r\nline two\r\n",
		"crlf-message":  "First\r\n\r\nSecond\r\nSigned-off-by: A\r\n",
		"mixed-prose":   "Fixes the thing\nSigned-off-by: A",
		"trailer-like":  "See also: other\nSigned-off-by: A <a@example.test>",
		"folded":        "Reviewed-by: Some Person\n <person@example.test>",
		"empty-signoff": "Signed-off-by:",
	}
	for _, method := range []forge.MergeMethod{forge.Squash, forge.Merge, forge.Rebase, forge.FastForward} {
		for name, shape := range shapes {
			t.Run(string(method)+"/"+name, func(t *testing.T) {
				f := newFixture(t)
				prs := slices.Clone(f.prs[:1])
				prs[0].HeadSHA = f.commit(f.base, "Contributor subject\n\n"+shape, map[string]string{"shape": name})
				prs[0].Metadata = &forge.ChangeMetadata{ChangeID: "chg1_" + strings.Repeat("a", 64), Revision: 1}
				gitCommand(t, f.source, "", nil, "push", "--force", f.remote, prs[0].HeadSHA+":"+headRef(prs[0].Number))
				f.g.cfg.PullRequest = func(context.Context, forge.RepoRef, int) (forge.PullRequest, error) {
					return forge.PullRequest{Number: prs[0].Number, HeadSHA: prs[0].HeadSHA, Title: "Shape", Body: shape, HeadBranch: "shape", Author: "Contributor", AuthorEmail: "contributor@example.test"}, nil
				}
				tested := f.build(method, prs, "shapes")
				message, err := f.g.ReadCommitMessage(t.Context(), testRepo, tested)
				must(t, err)
				got, err := forge.ParseChangeTrailers(message)
				must(t, err)
				if len(got) != 1 || got[0].ChangeID != prs[0].Metadata.ChangeID || got[0].Revision != 1 || len(got[0].Labels) != 0 {
					t.Fatalf("writer/reader mismatch: %v\n%s", got, message)
				}
			})
		}
	}
}

func TestReadCommitMessageCacheOnly(t *testing.T) {
	f := newFixture(t)
	tested := f.build(forge.Squash, f.prs[:1], "cached")
	f.g.cfg.Remote = func(context.Context, forge.RepoRef) (string, Auth, error) {
		t.Error("cache-only read resolved remote")
		return "", Auth{}, fmt.Errorf("unavailable")
	}
	if _, err := f.g.ReadCommitMessage(t.Context(), testRepo, tested); err != nil {
		t.Fatal(err)
	}
	for _, object := range []string{strings.Repeat("f", 40), "main", tested[:12]} {
		if _, err := f.g.ReadCommitMessage(t.Context(), testRepo, object); err == nil {
			t.Fatal("accepted missing/non-full object", object)
		}
	}
	// A promisor remote would attempt this helper if lazy fetch were enabled.
	s := &session{r: f.g, dir: fmt.Sprintf("%s/%x.git", f.g.cfg.WorkDir, sha256.Sum256([]byte(testRepo.Path)))}
	_, err := s.command(t.Context(), "", "config", "remote.origin.url", "ext::sh -c 'echo network-attempt >&2; exit 99'")
	must(t, err)
	_, err = s.command(t.Context(), "", "config", "remote.origin.promisor", "true")
	must(t, err)
	_, err = f.g.ReadCommitMessage(t.Context(), testRepo, strings.Repeat("f", 40))
	if err == nil || strings.Contains(err.Error(), "network-attempt") {
		t.Fatal("missing object attempted network", err)
	}
}
