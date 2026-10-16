package queue

import "testing"

func TestMetadataChangeInvalidatesOnlyActiveMemberPolicy(t *testing.T) {
	for _, phase := range []Phase{Building, Testing, Landing} {
		for _, pr := range []int{1, 2} {
			state := State{Active: &Batch{Phase: phase, PRs: []BatchPR{{PR: 1, HeadSHA: "head"}}, checksPassed: true, checksGeneration: 7}}
			after := Apply(state, PRMetadataObserved{PR: pr, HeadSHA: "head", Labels: []string{"changed"}, LabelsKnown: true})
			invalidated := phase == Testing && pr == 1
			if after.Active.checksPassed == invalidated || after.Active.needsRefresh != invalidated || invalidated && after.Active.checksGeneration != 8 || !invalidated && after.Active.checksGeneration != 7 {
				t.Fatalf("phase=%s PR=%d active=%+v", phase, pr, after.Active)
			}
			if !state.Active.checksPassed || state.Active.checksGeneration != 7 {
				t.Fatal("metadata fold mutated prior state")
			}
		}
	}
}
