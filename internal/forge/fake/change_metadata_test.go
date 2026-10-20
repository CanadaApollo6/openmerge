package fake_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestFastForwardMemberMetadataBeforeCI(t *testing.T) {
	f, e := fixture(t)
	second, err := f.OpenPR(repo, 4, e.PRs[0].HeadSHA, true)
	must(t, err)
	prs := []forge.BuildPR{e.PRs[0], {Number: 4, HeadSHA: second.HeadSHA}}
	var pairs []string
	for i := range prs {
		prs[i].Metadata = &forge.ChangeMetadata{ChangeID: fmt.Sprintf("chg1_%064x", i+1), Revision: uint64(i + 1)}
		if i == 0 {
			prs[i].Metadata.Labels = []string{"policy", "docs"}
		}
		pair, err := prs[i].Metadata.Trailers()
		must(t, err)
		pairs = append(pairs, pair...)
	}
	tested, err := f.BuildTestRef(t.Context(), repo, forge.BuildRequest{Ref: "metadata", BaseSHA: e.BaseSHA, PRs: prs, Method: forge.FastForward, BatchID: "metadata"}, forge.PushOptions{})
	must(t, err)
	marker, err := f.Commit(repo, tested)
	must(t, err)
	want := strings.Join(append(pairs, "OpenMerge-Batch: metadata"), "\n")
	if !strings.HasSuffix(marker.Message, want) || forge.ParseBatchMessage(marker.Message) != "metadata" {
		t.Fatalf("member order/pairs: %s", marker.Message)
	}
	must(t, f.VerifyFastForward(t.Context(), repo, e.BaseSHA, tested, second.HeadSHA))
	// Metadata changes while CI runs do not change the marker or landing input.
	prs[0].Metadata.Labels = []string{"unknown"}
	_, err = f.Land(t.Context(), repo, forge.LandRequest{Base: e.Base, ExpectedBaseSHA: e.BaseSHA, TestedSHA: tested, Fence: e.FenceRef, ExpectedFence: e.FenceSHA, NextFenceID: "land/metadata", PRs: prs, FastForwardHeadSHA: second.HeadSHA}, forge.PushOptions{})
	must(t, err)
	after, err := f.Commit(repo, tested)
	must(t, err)
	landed, err := f.ReadRef(t.Context(), repo, e.Base)
	must(t, err)
	if after.Message != marker.Message || landed != second.HeadSHA {
		t.Fatal("metadata edit changed tested/landed identities")
	}
}
