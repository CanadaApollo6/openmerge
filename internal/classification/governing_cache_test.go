package classification

import (
	"strings"
	"testing"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
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

func TestGoverningCacheIncompleteThenCompleteSameBlob(t *testing.T) {
	cache := NewGoverningCache(8)
	owners := forge.GoverningFile{Path: ".github/CODEOWNERS", Present: true, Mode: "100644", ObjectID: strings.Repeat("a", 40), Coverage: forge.DiffCoverage{Completeness: forge.DiffIncomplete}}
	attrs := forge.GoverningFile{Path: ".gitattributes", Present: true, Mode: "100644", ObjectID: strings.Repeat("b", 40), Coverage: owners.Coverage}
	g := forge.GoverningFiles{SelectedCodeowners: owners.Path, Codeowners: []forge.GoverningFile{owners}, Attributes: []forge.GoverningFile{attrs}}
	cache.TreeDeriver(g, nil)
	if _, known := cache.GoverningOwners(g); known || cache.lru.Len() != 0 {
		t.Fatal("incomplete blobs entered parse cache")
	}
	g.Codeowners[0].Coverage.Completeness = forge.DiffComplete
	g.Codeowners[0].Content = "* @writer\n"
	g.Attributes[0].Coverage.Completeness = forge.DiffComplete
	g.Attributes[0].Content = "*.gen linguist-generated\n"
	d := cache.TreeDeriver(g, nil)
	got, known := cache.GoverningOwners(g)
	if !known || len(got) != 1 || got[0].Name != "writer" {
		t.Fatalf("complete owners poisoned: %+v %t", got, known)
	}
	if got := d.Generated("file.gen", g); got != cp.True {
		t.Fatalf("complete generated classification poisoned: %v", got)
	}
}
