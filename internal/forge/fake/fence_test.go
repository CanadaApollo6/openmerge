package fake

import (
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestFenceCommitsDifferForIdenticalCopies(t *testing.T) {
	for _, deterministic := range []bool{false, true} {
		f := New()
		if deterministic {
			f = NewDeterministic()
		}
		repo := forge.RepoRef{Path: "fence-copies"}
		base, initial := f.Init(repo, "main", "fence")
		r := f.repo(repo)
		for _, tested := range []string{"", base} {
			first, err := r.fence(initial, "same-attempt", tested)
			if err != nil {
				t.Fatal(err)
			}
			second, err := r.fence(initial, "same-attempt", tested)
			if err != nil {
				t.Fatal(err)
			}
			if first == second {
				t.Fatal("identical fence copies reused an object")
			}
		}
	}
}
