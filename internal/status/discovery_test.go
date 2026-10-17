package status

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestDiscoveryEndpointsUseAuthenticationAndFreshSnapshots(t *testing.T) {
	now := time.Now().UTC()
	age := 125.0
	calls := 0
	read := func() Discovery {
		calls++
		return Discovery{LastSuccessfulSweep: &now, Repositories: []Repository{{Installation: "7", Name: "owner/not-configured", State: "not configured", LastSuccessfulSweep: &now, LastVerifiedDelivery: &now, LastDeliveryAgeSeconds: &age}}, Installations: []Installation{{ID: "7", WebhooksConfigured: true, WebhookHealthy: true, LastVerifiedDelivery: &now, Budget: Budget{Known: true, Limit: 1000, Remaining: 1000 - calls}}}}
	}
	handler := DynamicHandler(emptyDiscoverySource{}, func() []store.QueueKey { return nil }, "secret", nil, read)
	for _, path := range []string{"/v1/discovery", "/v1/queues"} {
		denied := httptest.NewRecorder()
		handler.ServeHTTP(denied, httptest.NewRequest(http.MethodGet, path, nil))
		if denied.Code != http.StatusUnauthorized || calls != 0 {
			t.Fatal("discovery bypassed authorization")
		}
	}
	for i, path := range []string{"/v1/discovery", "/v1/queues"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer secret")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		var body Response
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Discovery == nil || len(body.Discovery.Repositories) != 1 || body.Discovery.Installations[0].Budget.Remaining != 999-i {
			t.Fatalf("discovery response: %s", response.Body.String())
		}
		if !body.Discovery.Installations[0].WebhookHealthy || !body.Discovery.Installations[0].WebhooksConfigured || body.Discovery.Installations[0].LastVerifiedDelivery == nil || body.Discovery.Repositories[0].LastDeliveryAgeSeconds == nil || *body.Discovery.Repositories[0].LastDeliveryAgeSeconds != age {
			t.Fatalf("webhook visibility missing: %s", response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("discovery response cacheable")
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/discovery", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatal("discovery allowed mutation")
	}
}

type emptyDiscoverySource struct{}

func (emptyDiscoverySource) Journals(context.Context, []store.QueueKey) ([]store.Journal, error) {
	return nil, nil
}
