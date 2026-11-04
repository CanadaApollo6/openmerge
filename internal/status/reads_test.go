package status

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

type registrySource struct{ source }

func (*registrySource) Health(context.Context) (Health, error) {
	return Health{Version: Version, Healthy: true}, nil
}
func (*registrySource) AuthorityCompletion(context.Context, store.AuthorityCompletionRequest) (store.AuthorityCompletion, error) {
	return store.AuthorityCompletion{}, nil
}
func (*registrySource) ChangeDecisions(context.Context, store.QueueKey, queue.ChangeID, uint64, int64, int) ([]store.DecisionRecord, int64, bool, error) {
	return []store.DecisionRecord{}, 0, true, nil
}

// Keep this inventory independent of reads so deleting a row cannot delete its test.
var readRouteInventory = map[string]string{
	"reads":                "/v1",
	"discovery":            "/v1/discovery",
	"queues":               "/v1/queues",
	"repository_queues":    "/v1/queues/{owner}/{repo}",
	"queue":                "/v1/queues/{owner}/{repo}/{base}",
	"pr":                   "/v1/queues/{owner}/{repo}/{base}/prs/{n}",
	"change":               "/v1/queues/{owner}/{repo}/{base}/changes/{id}",
	"brief":                "/v1/queues/{owner}/{repo}/{base}/changes/{id}/brief",
	"decisions":            "/v1/queues/{owner}/{repo}/{base}/changes/{id}/decisions",
	"savings":              "/v1/queues/{owner}/{repo}/{base}/reports/savings",
	"health":               "/v1/health",
	"authority_completion": "/v1/authority-completion",
	"live":                 "/v1/live",
}

func TestReadRegistryInventory(t *testing.T) {
	got := map[string]string{}
	for _, read := range Reads() {
		got[read.Name] = read.Path
	}
	if !reflect.DeepEqual(got, readRouteInventory) {
		t.Fatalf("registry differs from route inventory: got %v, want %v", got, readRouteInventory)
	}
}

func TestReadRegistrySourcePaths(t *testing.T) {
	// Inspect handlers too. An early return can bypass every runtime registry check.
	files, err := parser.ParseDir(token.NewFileSet(), ".", func(info os.FileInfo) bool { return !strings.HasSuffix(info.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, path := range readRouteInventory {
		paths[path] = true
	}
	for _, pkg := range files {
		for name, file := range pkg.Files {
			ast.Inspect(file, func(node ast.Node) bool {
				lit, ok := node.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				if (value == "/v1" || strings.HasPrefix(value, "/v1/")) && !paths[strings.TrimSuffix(value, "/")] {
					t.Errorf("%s declares an API path outside the read inventory: %q", name, value)
				}
				return true
			})
		}
	}
}

func TestReadRegistryRoutesAndDocs(t *testing.T) {
	j, id, _, _ := briefRegressionBase(t)
	for i := range j.Events {
		j.Events[i].RecordedAt = at
	}
	src := &registrySource{source{journals: []store.Journal{j}}}
	h := DynamicHandler(src, func() []store.QueueKey { return []store.QueueKey{key} }, "secret", nil, func() Discovery { return Discovery{} })
	docs, err := os.ReadFile("../../docs/api.md")
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"owner": "owner", "repo": "repo", "base": "main", "n": "1", "id": string(id), "audience": "reviewer", "since": "2026-10-01T00:00:00Z", "installation": "7", "digest": strings.Repeat("a", 64)}
	names, paths := map[string]bool{}, map[string]bool{}
	for _, read := range Reads() {
		t.Run(read.Name, func(t *testing.T) {
			if read.Name == "" || names[read.Name] || paths[read.Path] || read.Description == "" {
				t.Fatalf("invalid or duplicate read: %+v", read)
			}
			names[read.Name], paths[read.Path] = true, true
			if !strings.Contains(string(docs), "`"+read.Path+"`") {
				t.Errorf("docs/api.md omits %s", read.Path)
			}
			path, query := read.Path, url.Values{}
			seen := map[string]bool{}
			for _, p := range read.Parameters {
				if seen[p.Name] || p.Description == "" || p.Type != "string" && p.Type != "integer" && p.Type != "number" {
					t.Fatalf("bad parameter %+v", p)
				}
				seen[p.Name] = true
				switch p.In {
				case "path":
					if !p.Required || !strings.Contains(path, "{"+p.Name+"}") {
						t.Fatalf("bad path parameter %+v", p)
					}
					path = strings.ReplaceAll(path, "{"+p.Name+"}", url.PathEscape(values[p.Name]))
				case "query":
					if p.Required {
						value := values[p.Name]
						if read.Name == "authority_completion" && p.Name == "repo" {
							value = key.Repository
						}
						query.Set(p.Name, value)
					}
				default:
					t.Fatalf("invalid parameter location %+v", p)
				}
			}
			if strings.Contains(path, "{") {
				t.Fatalf("path parameter missing from metadata: %s", path)
			}
			if read.Name == "authority_completion" {
				query.Set("digest", values["digest"])
			}
			if len(query) > 0 {
				path += "?" + query.Encode()
			}
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Header.Set("Authorization", "Bearer secret")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request)
			if w.Code != 200 {
				t.Fatalf("%s: %d %s", path, w.Code, w.Body)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(w.Header())
			}
		})
	}
}

func TestReadRegistryAuthenticationAndContract(t *testing.T) {
	h := Handler(&source{}, nil, "secret", nil)
	for _, path := range []string{"/v1", "/v1/queues/owner/repo/main/reports/savings?since=2026-10-01T00:00:00Z"} {
		for _, token := range []string{"", "wrong"} {
			r := httptest.NewRequest("GET", path, nil)
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 401 {
				t.Fatalf("%s token %q: %d", path, token, w.Code)
			}
		}
		for _, method := range []string{"POST", "HEAD", "PUT", "DELETE"} {
			r := httptest.NewRequest(method, path, nil)
			r.Header.Set("Authorization", "Bearer secret")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 405 || w.Header().Get("Allow") != "GET" {
				t.Fatal(w.Code, w.Header())
			}
		}
	}
	r := httptest.NewRequest("GET", "/v1", nil)
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var response struct {
		Version int    `json:"version"`
		Reads   []Read `json:"reads"`
	}
	if json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Version != Version || !reflect.DeepEqual(response.Reads, Reads()) {
		t.Fatal(w.Body)
	}
	for _, path := range []string{"/v1?unknown=1", "/v1?"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 || !strings.Contains(w.Body.String(), `"invalid-request"`) {
			t.Fatal(w.Code, w.Body)
		}
	}
}

type savingsDeadlineSource struct{ deadline time.Duration }

func (s *savingsDeadlineSource) Journals(ctx context.Context, _ []store.QueueKey) ([]store.Journal, error) {
	end, _ := ctx.Deadline()
	s.deadline = time.Until(end)
	return nil, context.DeadlineExceeded
}
