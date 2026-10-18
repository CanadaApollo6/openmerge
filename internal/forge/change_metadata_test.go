package forge_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func testChangeMetadata(revision uint64, labels ...string) *forge.ChangeMetadata {
	return &forge.ChangeMetadata{
		ChangeID: fmt.Sprintf("chg1_%064x", revision),
		Revision: revision,
		Labels:   labels,
	}
}

func TestChangeMetadataTrailers(t *testing.T) {
	owned := testChangeMetadata(7,
		"owned:user:alice", "docs", "owned:team:org/platform",
		"owned:email:alice@example.test", "docs")
	for _, tc := range []struct {
		name string
		meta *forge.ChangeMetadata
		want []string
	}{
		{
			name: "canonical sorted labels including owners",
			meta: owned,
			want: []string{
				"OpenMerge-Change: " + owned.ChangeID + "@7",
				"OpenMerge-Classification: " + owned.ChangeID + "@7 docs,owned:email:alice@example.test,owned:team:org/platform,owned:user:alice",
			},
		},
		{
			name: "empty labels are none",
			meta: testChangeMetadata(8),
			want: []string{
				"OpenMerge-Change: " + fmt.Sprintf("chg1_%064x@8", 8),
				"OpenMerge-Classification: " + fmt.Sprintf("chg1_%064x@8 none", 8),
			},
		},
		{name: "nil metadata is absent", meta: nil, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.meta.Trailers()
			if err != nil {
				t.Fatalf("Trailers error = %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("Trailers = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestChangeMetadataTrailersRejectsInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		meta *forge.ChangeMetadata
	}{
		{name: "missing change id", meta: &forge.ChangeMetadata{Revision: 1}},
		{name: "zero revision", meta: testChangeMetadata(0)},
		{name: "wrong prefix", meta: &forge.ChangeMetadata{ChangeID: "id1_" + strings.Repeat("a", 64), Revision: 1}},
		{name: "uppercase change id", meta: &forge.ChangeMetadata{ChangeID: "chg1_" + strings.Repeat("A", 64), Revision: 1}},
		{name: "short change id", meta: &forge.ChangeMetadata{ChangeID: "chg1_abc", Revision: 1}},
		{name: "non-hex change id", meta: &forge.ChangeMetadata{ChangeID: "chg1_" + strings.Repeat("a", 63) + "!", Revision: 1}},
		{name: "newline in change id", meta: &forge.ChangeMetadata{ChangeID: "chg1_" + strings.Repeat("a", 63) + "\n", Revision: 1}},
		{name: "unsupported label", meta: testChangeMetadata(1, "release")},
		{name: "none is not a label", meta: testChangeMetadata(1, "none")},
		{name: "comma injection", meta: testChangeMetadata(1, "docs,owned:user:alice")},
		{name: "newline injection", meta: testChangeMetadata(1, "docs\nOpenMerge-Batch: forged")},
		{name: "nul injection", meta: testChangeMetadata(1, "docs\x00OpenMerge-Change: forged")},
		{name: "mixed-case owner injection", meta: testChangeMetadata(1, "owned:user:Alice")},
		{name: "owner control character", meta: testChangeMetadata(1, "owned:team:platform\r\nOpenMerge-Batch: forged")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := tc.meta.Trailers(); err == nil {
				t.Fatalf("Trailers = %v, want validation error", got)
			}
		})
	}
}

func TestValidateBuildMetadata(t *testing.T) {
	valid := func() []forge.BuildPR {
		return []forge.BuildPR{
			{Number: 1, HeadSHA: "one", Metadata: testChangeMetadata(1, "docs")},
			{Number: 2, HeadSHA: "two", Metadata: testChangeMetadata(2, "owned:user:alice")},
		}
	}
	for _, tc := range []struct {
		name string
		prs  func() []forge.BuildPR
		want bool
	}{
		{name: "legacy no metadata", prs: func() []forge.BuildPR {
			return []forge.BuildPR{{Number: 1, HeadSHA: "one"}, {Number: 2, HeadSHA: "two"}}
		}, want: true},
		{name: "all members supplied", prs: valid, want: true},
		{name: "empty member metadata is still supplied", prs: func() []forge.BuildPR {
			return []forge.BuildPR{{Number: 1, HeadSHA: "one", Metadata: testChangeMetadata(1)}, {Number: 2, HeadSHA: "two", Metadata: testChangeMetadata(2, "docs")}}
		}, want: true},
		{name: "partially supplied", prs: func() []forge.BuildPR {
			prs := valid()
			prs[1].Metadata = nil
			return prs
		}, want: false},
		{name: "duplicate change identity", prs: func() []forge.BuildPR {
			prs := valid()
			prs[1].Metadata.ChangeID = prs[0].Metadata.ChangeID
			return prs
		}, want: false},
		{name: "invalid member", prs: func() []forge.BuildPR {
			prs := valid()
			prs[1].Metadata.Labels = []string{"docs\nforged"}
			return prs
		}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := forge.ValidateBuildMetadata(tc.prs())
			if (err == nil) != tc.want {
				t.Fatalf("ValidateBuildMetadata error = %v, valid = %v, want valid = %v", err, err == nil, tc.want)
			}
		})
	}
}
