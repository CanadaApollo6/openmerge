package forge

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
)

func TestWriteRestrictionInputBinding(t *testing.T) {
	raw, err := os.ReadFile("../queue/testdata/wire/v1/event_write_restriction_verified.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Data struct {
			Binding authority.PolicyBinding `json:"binding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	in := WriteRestrictionInput{Repo: RepoRef{Path: fixture.Data.Binding.Repository}, Binding: fixture.Data.Binding, BaseSelectors: []authority.BaseSelector{{DefaultBranch: true}, {Pattern: "release/**"}}, ExpectedRefs: []string{"refs/heads/main", "refs/heads/openmerge/fence/main"}, AppID: "5383059"}
	in.Binding.CoverageDigest, err = authority.CoverageDigest("main", in.BaseSelectors)
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*WriteRestrictionInput){
		func(in *WriteRestrictionInput) { in.Binding.MergeMethod = "unknown" },
		func(in *WriteRestrictionInput) { in.Repo.Path = "other/repo" },
		func(in *WriteRestrictionInput) { in.AppID = "" },
		func(in *WriteRestrictionInput) { in.BaseSelectors = []authority.BaseSelector{{Pattern: "other/**"}} },
		func(in *WriteRestrictionInput) { in.ExpectedRefs = []string{"main", "main"} },
		func(in *WriteRestrictionInput) { in.ExpectedRefs = []string{"refs/heads/main", "refs/heads/main"} },
		func(in *WriteRestrictionInput) { in.ExpectedRefs = []string{"main"} },
		func(in *WriteRestrictionInput) { in.ExpectedRefs = []string{"heads/main"} },
		func(in *WriteRestrictionInput) { in.ExpectedRefs = []string{"refs/tags/v1"} },
		func(in *WriteRestrictionInput) { in.ExpectedRefs = []string{"refs/heads/release/*"} },
		func(in *WriteRestrictionInput) { in.ExpectedRefs = []string{"refs/heads/main..next"} },
		func(in *WriteRestrictionInput) { in.ExpectedRefs = []string{"refs/heads/main\n"} },
		func(in *WriteRestrictionInput) { in.ExpectedRefs = []string{"refs/heads/main.lock"} },
		func(in *WriteRestrictionInput) { in.Binding.DefaultBranch = "bad..branch" },
		func(in *WriteRestrictionInput) { in.BaseSelectors = []authority.BaseSelector{{}} },
	} {
		bad := in
		change(&bad)
		if err := bad.Validate(); !errors.Is(err, ErrInvalidInput) {
			t.Fatal("invalid observation input did not wrap ErrInvalidInput", err)
		}
	}
}
