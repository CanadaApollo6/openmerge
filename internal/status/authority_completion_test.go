package status

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/store"
)

type completionSource struct {
	calls int
	err   error
}

func (*completionSource) Journals(context.Context, []store.QueueKey) ([]store.Journal, error) {
	return nil, errors.New("must use repository API")
}
func (s *completionSource) AuthorityCompletion(_ context.Context, request store.AuthorityCompletionRequest) (store.AuthorityCompletion, error) {
	s.calls++
	return store.AuthorityCompletion{Request: request, Complete: false, Queues: []store.AuthorityQueueCompletion{{Base: "main", Reason: "registration_missing"}}}, s.err
}

func TestAuthorityCompletionEndpoint(t *testing.T) {
	source := &completionSource{}
	h := Handler(source, nil, "secret", nil)
	for _, tc := range []struct {
		path, token, method string
		want                int
	}{
		{"/v1/authority-completion?installation=7&repo=owner/repo&digest=" + strings.Repeat("a", 64), "secret", "GET", 200},
		{"/v1/authority-completion", "", "GET", 401},
		{"/v1/authority-completion?unknown=1", "secret", "GET", 400},
		{"/v1/authority-completion?repo=a&repo=b", "secret", "GET", 400},
		{"/v1/authority-completion?targets=invalid", "secret", "GET", 400},
		{"/v1/authority-completion", "secret", "POST", 405},
	} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, nil)
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		h.ServeHTTP(rr, req)
		if rr.Code != tc.want {
			t.Fatalf("%s: %d %s", tc.path, rr.Code, rr.Body.String())
		}
		if tc.want == 200 && !strings.Contains(rr.Body.String(), `"complete":false`) {
			t.Fatal(rr.Body.String())
		}
	}
	if source.calls != 1 {
		t.Fatal(source.calls)
	}
	source.err = errors.New("token=do-not-expose")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/authority-completion?installation=7&repo=owner/repo&digest="+strings.Repeat("a", 64), nil)
	req.Header.Set("Authorization", "Bearer secret")
	h.ServeHTTP(rr, req)
	if rr.Code != 503 || strings.Contains(rr.Body.String(), "do-not-expose") {
		t.Fatal(rr.Body.String())
	}
}
