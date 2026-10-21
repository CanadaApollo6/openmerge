package serve

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// This exact source is also run as an overlay on the baseline checkout. Keep
// full response bytes: no field allowlist or normalization hides a regression.
func TestAuthorityGithubProjectionBytes(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, _, _ := fixture(t, repositoryConfig)
		must(t, s.cycle(t.Context(), w))
		output := map[string]string{}
		for _, path := range []string{"/v1/queues", "/v1/health"} {
			response := httptest.NewRecorder()
			status.Handler(s, []store.QueueKey{w.Key}, "", nil).ServeHTTP(response, httptest.NewRequest("GET", path, nil))
			if response.Code != 200 {
				t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
			}
			output[path] = response.Body.String()
		}
		summary, err := s.ledgerSummary(t.Context(), w, 1)
		must(t, err)
		output["author"] = summary
		raw, err := json.Marshal(output)
		must(t, err)
		if path := os.Getenv("ADR_GITHUB_PROJECTIONS"); path != "" {
			must(t, os.WriteFile(path, raw, 0600))
		}
	})
}
