package fake

import (
	"fmt"
	"maps"
	"reflect"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestCompactTreesPreserveAllHistoricalContentsAndMarkers(t *testing.T) {
	repo := forge.RepoRef{Path: "compact/repo"}
	ordinary, compact := NewDeterministic(), NewDeterministic()
	compact.CompactTrees(repo)
	var shas []string
	pairs := make(map[string]string)
	a, af := ordinary.Init(repo, "main", "fence")
	b, bf := compact.Init(repo, "main", "fence")
	pairs[a] = b
	pairs[af] = bf
	for i := 0; i < 150; i++ {
		changes := map[string]string{fmt.Sprintf("file-%d", i): fmt.Sprint(i), "updated": "value"}
		a, err := ordinary.PushBase(repo, "main", "commit", changes)
		if err != nil {
			t.Fatal(err)
		}
		b, err := compact.PushBase(repo, "main", "commit", changes)
		if err != nil {
			t.Fatal(err)
		}
		pairs[a] = b
		shas = append(shas, a)
	}
	// A deletion must survive eviction too.
	var extra []string
	for _, f := range []*Forge{ordinary, compact} {
		r := f.repo(repo)
		parent := r.refs["main"]
		files := maps.Clone(r.files(parent))
		delete(files, "updated")
		sha := r.commit([]string{parent}, nil, "deletion", files)
		r.refs["main"] = sha
		for range 65 {
			r.refs["main"] = r.commit([]string{r.refs["main"]}, nil, "next")
		}
		extra = append(extra, sha, r.refs["main"])
	}
	pairs[extra[0]] = extra[2]
	pairs[extra[1]] = extra[3]
	shas = append(shas, extra[:2]...)
	for _, sha := range shas {
		want, err := ordinary.Commit(repo, sha)
		if err != nil {
			t.Fatal(err)
		}
		got, err := compact.Commit(repo, pairs[sha])
		if err != nil {
			t.Fatal(err)
		}
		if !maps.Equal(want.Files, got.Files) || !reflect.DeepEqual(want.PRs, got.PRs) || want.Message != got.Message || len(want.Parents) != len(got.Parents) {
			t.Fatalf("compaction lost historical tree %s", sha)
		}
		metadata, err := compact.CommitMetadata(repo, pairs[sha])
		if err != nil {
			t.Fatal(err)
		}
		got.Files = nil
		if !reflect.DeepEqual(got, metadata) {
			t.Fatal("metadata read changed graph or markers")
		}

	}
	n := 0
	for _, c := range compact.repo(repo).commits {
		if c.Files != nil {
			n++
		}
	}
	if n > 64 {
		t.Fatalf("retained %d full trees", n)
	}
}

func TestCompactBatchCommitsMatchReconstructedDeltas(t *testing.T) {
	for _, method := range []forge.MergeMethod{forge.Merge, forge.Squash, forge.Rebase} {
		t.Run(string(method), func(t *testing.T) {
			f := NewDeterministic()
			repo := forge.RepoRef{Path: "compact/build"}
			f.CompactTrees(repo)
			root, _ := f.Init(repo, "main", "fence")
			var prs []forge.BuildPR
			for pr := 1; pr <= 40; pr++ {
				p, err := f.OpenPR(repo, pr, root, true)
				if err != nil {
					t.Fatal(err)
				}
				p, err = f.PushPR(repo, pr, "second commit")
				if err != nil {
					t.Fatal(err)
				}
				prs = append(prs, forge.BuildPR{Number: pr, HeadSHA: p.HeadSHA})
			}
			base := root
			var tips []string
			for i := 0; i < len(prs); i += 4 {
				sha, err := f.BuildTestRef(t.Context(), repo, forge.BuildRequest{BatchID: fmt.Sprintf("b%d", i+1), BaseSHA: base, PRs: prs[i : i+4], Ref: fmt.Sprintf("openmerge/queue/main/b%d", i+1), Method: method}, forge.PushOptions{})
				if err != nil {
					t.Fatal(err)
				}
				base = sha
				tips = append(tips, sha)
			}
			for _, tip := range tips {
				c, err := f.Commit(repo, tip)
				if err != nil {
					t.Fatal(err)
				}
				d := f.repo(repo).treeDelta(c)
				if compactSHA(c, d) != tip {
					t.Fatal("prepared build hash differs from reconstructed full-tree delta")
				}
				for _, pr := range c.PRs {
					if !f.ContainsPR(repo, tip, pr) {
						t.Fatalf("lost content for PR %d", pr)
					}
				}
			}
		})
	}
}
