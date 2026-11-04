package status

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestLegacyRouteContracts(t *testing.T) {
	src := &source{journals: []store.Journal{journal(testingEvents()...)}}
	handlers := map[string]http.Handler{
		"static":  Handler(src, []store.QueueKey{key}, "secret", nil, ReadOptions{Discovery: func() Discovery { return Discovery{} }}),
		"dynamic": DynamicHandler(src, func() []store.QueueKey { return []store.QueueKey{key} }, "secret", nil, ReadOptions{Discovery: func() Discovery { return Discovery{} }}),
	}
	type result struct {
		status int
		code   string
	}
	// Recorded against origin/main at 2d3bd83. Keep parser and authentication order.
	cases := []struct {
		path                                                string
		get, unauthenticated, nonGET, unauthenticatedNonGET result
	}{
		{"/v1/discovery", result{200, ""}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/%64iscovery", result{200, ""}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/discovery/", result{404, "not-found"}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/discovery?unexpected=1", result{400, "invalid-request"}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/queues", result{200, ""}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/queues/", result{404, "not-found"}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/%71ueues", result{404, "not-found"}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/queues/owner/repo", result{200, ""}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/queues/owner/repo/", result{400, "invalid-request"}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/queues/owner/repo/main", result{200, ""}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/queues/owner/repo/main/", result{404, "not-found"}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/queues/owner/repo/%6dain", result{200, ""}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/queues/%6fwner/repo/main", result{200, ""}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/queues/owner/repo/main/prs/1", result{200, ""}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/queues/owner/repo/main/%70rs/1", result{200, ""}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/queues/owner/repo/main/prs/", result{400, "invalid-request"}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/queues/owner/repo/main/prs/1/", result{404, "not-found"}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/queues/owner/repo/main/nope/1", result{400, "invalid-request"}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/queues/owner/repo/main/prs/0", result{400, "invalid-request"}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/queues/owner/repo/main/prs/%31", result{200, ""}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/queues/owner/repo/main/prs/1?events=0", result{400, "invalid-request"}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/queues/owner/repo/main/changes/nope", result{400, "invalid-request"}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/queues/owner/repo/main/changes/nope/brief", result{400, "invalid-request"}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/queues/owner/repo/main/changes/nope/decisions", result{400, "invalid-request"}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/health", result{503, "unavailable"}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/%68ealth", result{404, "not-found"}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/health/", result{404, "not-found"}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/live", result{200, ""}, result{200, ""}, result{405, "method-not-allowed"}, result{405, "method-not-allowed"}},
		{"/v1/%6cive", result{404, "not-found"}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/live/", result{404, "not-found"}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/authority-completion", result{400, "invalid-request"}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/authority-completion/", result{404, "not-found"}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/%61uthority-completion", result{404, "not-found"}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v1/unknown", result{404, "not-found"}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/v2/queues", result{404, "not-found"}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
		{"/", result{404, "not-found"}, result{401, "unauthorized"}, result{405, "method-not-allowed"}, result{401, "unauthorized"}},
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			for _, tc := range cases {
				for _, variant := range []struct {
					method, token string
					want          result
				}{
					{"GET", "secret", tc.get}, {"GET", "", tc.unauthenticated}, {"GET", "wrong", tc.unauthenticated},
					{"POST", "secret", tc.nonGET}, {"HEAD", "secret", tc.nonGET}, {"DELETE", "secret", tc.nonGET}, {"PUT", "", tc.unauthenticatedNonGET},
				} {
					t.Run(variant.method+tc.path+"/"+variant.token, func(t *testing.T) {
						r := httptest.NewRequest(variant.method, tc.path, nil)
						if variant.token != "" {
							r.Header.Set("Authorization", "Bearer "+variant.token)
						}
						w := httptest.NewRecorder()
						h.ServeHTTP(w, r)
						var body struct {
							Error string `json:"error"`
						}
						if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
							t.Fatal(err)
						}
						got := result{w.Code, body.Error}
						if got != variant.want {
							t.Fatalf("got %+v, main returned %+v: %s", got, variant.want, w.Body)
						}
					})
				}
			}
		})
	}
}
