package classification

import (
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestGoverningCacheBoundsParsedBlobsAndPreservesAbsence(t *testing.T) {
	cache := NewGoverningCache(1)
	ownerBlob := strings.Repeat("a", 40)
	attrsBlob := strings.Repeat("b", 40)
	owners := forge.GoverningFiles{
		SelectedCodeowners: "CODEOWNERS",
		Codeowners: []forge.GoverningFile{
			{Path: ".github/CODEOWNERS", Coverage: forge.DiffCoverage{Completeness: forge.DiffComplete}},
			{Path: "CODEOWNERS", Present: true, Mode: "100644", ObjectID: ownerBlob, Content: "* @writer\n", Coverage: forge.DiffCoverage{Completeness: forge.DiffComplete}},
			{Path: "docs/CODEOWNERS", Coverage: forge.DiffCoverage{Completeness: forge.DiffComplete}},
		},
		Attributes: []forge.GoverningFile{{Path: ".gitattributes", Present: true, Mode: "100644", ObjectID: attrsBlob, Content: "*.gen linguist-generated\n", Coverage: forge.DiffCoverage{Completeness: forge.DiffComplete}}},
	}
	parsed, known := cache.GoverningOwners(owners)
	if !known || len(parsed) != 1 || parsed[0].Name != "writer" {
		t.Fatalf("cached owners = %#v, known=%t", parsed, known)
	}
	cache.TreeDeriver(owners, nil)
	if cache.lru.Len() != 1 || len(cache.entries) != 1 {
		t.Fatalf("parsed cache exceeded LRU capacity: len=%d entries=%d", cache.lru.Len(), len(cache.entries))
	}

	absent := owners
	absent.SelectedCodeowners = ""
	absent.Codeowners = []forge.GoverningFile{
		{Path: ".github/CODEOWNERS", Coverage: forge.DiffCoverage{Completeness: forge.DiffComplete}},
		{Path: "CODEOWNERS", Coverage: forge.DiffCoverage{Completeness: forge.DiffComplete}},
		{Path: "docs/CODEOWNERS", Coverage: forge.DiffCoverage{Completeness: forge.DiffComplete}},
	}
	parsed, known = cache.GoverningOwners(absent)
	if !known || len(parsed) != 0 {
		t.Fatalf("complete owner absence = %#v, known=%t", parsed, known)
	}
}
