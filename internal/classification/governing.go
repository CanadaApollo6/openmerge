package classification

import (
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func definite(f forge.GoverningFile) bool {
	if f.Coverage.Completeness != forge.DiffComplete || len(f.Coverage.Reasons) != 0 {
		return false
	}
	if !f.Present {
		return f.ObjectID == "" && f.Mode == "" && f.Content == ""
	}
	return forge.ValidDiffObjectID(f.ObjectID) && (f.Mode == "100644" || f.Mode == "100755") && utf8.ValidString(f.Content) && !strings.ContainsRune(f.Content, 0)
}
func probe(files []forge.GoverningFile, p string) (forge.GoverningFile, bool) {
	var found forge.GoverningFile
	n := 0
	for _, f := range files {
		if f.Path == p {
			found = f
			n++
		}
	}
	return found, n == 1 && definite(found)
}
func selectedOwners(g forge.GoverningFiles) (forge.GoverningFile, bool) {
	for _, p := range []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"} {
		f, ok := probe(g.Codeowners, p)
		if !ok {
			return f, false
		}
		if f.Present {
			return f, g.SelectedCodeowners == p
		}
	}
	return forge.GoverningFile{}, g.SelectedCodeowners == ""
}
func attributePaths(p string) []string {
	paths := []string{".gitattributes"}
	for dir := path.Dir(p); dir != "." && dir != "/"; dir = path.Dir(dir) {
		paths = append(paths, dir+"/.gitattributes")
	}
	slices.SortFunc(paths, func(a, b string) int {
		if n := strings.Count(a, "/") - strings.Count(b, "/"); n != 0 {
			return n
		}
		return strings.Compare(a, b)
	})
	return paths
}
func attributesCovered(p string, g forge.GoverningFiles) bool {
	if p == "" || path.Clean(p) != p || strings.HasPrefix(p, "/") || p == ".." || strings.HasPrefix(p, "../") {
		return false
	}
	for _, attribute := range attributePaths(p) {
		if _, ok := probe(g.Attributes, attribute); !ok {
			return false
		}
	}
	return true
}

// ConfigBlob follows config.Find: exactly one readable candidate, no ambiguity.
func configBlob(g forge.GoverningFiles) (string, bool) {
	id := ""
	count := 0
	for _, p := range []string{".openmerge.yml", ".openmerge.yaml", ".github/openmerge.yml", ".github/openmerge.yaml"} {
		f, ok := probe(g.Config, p)
		if !ok {
			return "", false
		}
		if f.Present {
			count++
			id = f.ObjectID
		}
	}
	if count > 1 {
		return "", false
	}
	return id, true
}
