package forge_test

import (
	"os"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestInterpretWriteRestrictionVisibleHiddenAndDrift(t *testing.T) {
	raw, err := os.ReadFile("../queue/testdata/wire/v1/event_write_restriction_verified.json")
	if err != nil {
		t.Fatal(err)
	}
	event, err := queue.DecodeEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	v := event.(queue.WriteRestrictionVerified)
	selectors := []authority.BaseSelector{{DefaultBranch: true}, {Pattern: "release/**"}}
	v.Binding.CoverageDigest, err = authority.CoverageDigest("main", selectors)
	if err != nil {
		t.Fatal(err)
	}
	in := forge.WriteRestrictionInput{Repo: forge.RepoRef{Path: v.Binding.Repository}, Binding: v.Binding, AppID: "5383059", BaseSelectors: selectors, ExpectedRefs: []string{"refs/heads/main"}}
	for _, name := range []string{"pass", "hidden", "disabled", "wrong-parameter", "future-base-gap", "unexpected-actor", "unknown-actor"} {
		t.Run(name, func(t *testing.T) {
			o := authority.CloneObservation(v.Evidence)
			o.Findings = []authority.Finding{}
			switch name {
			case "hidden":
				for i := range o.Protections {
					o.Protections[i].BypassVisibility = authority.Hidden
					o.Protections[i].BypassActors = []authority.BypassActor{}
				}
			case "disabled":
				o.Protections[0].Enforcement = "disabled"
			case "wrong-parameter":
				o.Protections[0].Rules[1].Parameters[0].Values = []string{"true"}
			case "future-base-gap":
				o.Protections[0].Includes = []string{"refs/heads/main"}
			case "unexpected-actor":
				o.Protections[0].BypassActors = append(o.Protections[0].BypassActors, authority.BypassActor{Kind: "user", ID: "rogue", Mode: "always"})
			case "unknown-actor":
				o.Protections[0].BypassActors = append(o.Protections[0].BypassActors, authority.BypassActor{Kind: "unknown", RawKind: "Future", Mode: "always"})
			}
			result, err := forge.InterpretWriteRestriction(in, o)
			if err != nil {
				t.Fatal(err)
			}
			if (len(result.Findings) == 0) != (name == "pass" || name == "hidden") {
				t.Fatalf("findings %s: %+v", name, result.Findings)
			}
			if (name == "hidden") && result.SeeingComplete() {
				t.Fatal("hidden list claimed a seeing read")
			}
		})
	}
}
