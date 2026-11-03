package cidoctor

import (
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"reflect"
	"testing"
)

func TestWorkflowGraphStaticMatrix(t *testing.T) {
	r := Run{HeadSHA: "abc", Path: ".github/workflows/ci.yml"}
	g := workflowGraph(r, []byte(`jobs:
  test:
    strategy:
      matrix:
        shard: [one, two]
  lint:
    name: Lint
  check:
    needs: [test, lint]
`))
	if g == nil || !reflect.DeepEqual(g.Needs["check"], []string{"Lint", "test (one)", "test (two)"}) || g.HeadSHA != "abc" {
		t.Fatalf("graph %+v", g)
	}
	for _, data := range []string{
		"jobs: {test: {name: '${{ matrix.name }}'}}",
		"jobs: {test: {uses: './test.yml'}}",
		"jobs: {test: {needs: missing}}",
		"jobs: {test: {strategy: {matrix: '${{ fromJSON(needs.plan.outputs.matrix) }}'}}}",
		"jobs: {test: {strategy: {matrix: {include: [{shard: one}]}}}}",
		"jobs: {a: {name: test}, b: {name: test}}",
		"jobs: {a: {}, a: {needs: b}}",
	} {
		if workflowGraph(r, []byte(data)) != nil {
			t.Fatalf("guessed graph for %q", data)
		}
	}
}

func TestRequiredChecksPinnedPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, base, lane string
		files            map[string]string
		want             []string
	}{
		{"default", "main", "", map[string]string{".openmerge.yml": "version: 1\nqueues:\n  - name: default\n    merge_when: [check-success = check]\n"}, []string{"check"}},
		{"other base", "release", "", map[string]string{".openmerge.yml": "version: 1\nqueues:\n  - name: default\n    merge_when: [check-success = check]\n"}, nil},
		{"unknown lane", "main", "default", map[string]string{".openmerge.yml": "version: 1\nqueues:\n  - name: default\n    merge_when: [check-success = check]\n"}, nil},
		{"missing", "main", "", nil, nil},
		{"ambiguous files", "main", "", map[string]string{".openmerge.yml": "version: 1", ".openmerge.yaml": "version: 1"}, nil},
		{"invalid policy", "main", "", map[string]string{".openmerge.yml": "unparseable policy"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := requiredChecks(func(sha, path string) ([]byte, error) {
				if sha != "pinned-head" {
					t.Fatal("policy read from wrong revision")
				}
				if data, ok := tc.files[path]; ok {
					return []byte(data), nil
				}
				return nil, forge.ErrFileNotFound
			}, "pinned-head", tc.base, tc.lane, "main")
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("required checks %v, err %v", got, err)
			}
		})
	}
}
