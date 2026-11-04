package status

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
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
			if !strings.Contains(string(docs), read.Path) {
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
			// Removing a row must remove its route, even for special probes.
			original := reads
			defer func() { reads = original }()
			reads = nil
			w = httptest.NewRecorder()
			h.ServeHTTP(w, request)
			if w.Code != 404 {
				t.Fatalf("unregistered route served: %s: %d %s", path, w.Code, w.Body)
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
