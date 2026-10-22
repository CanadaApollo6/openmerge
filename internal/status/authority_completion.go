package status

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/store"
)

// AuthorityCompletionSource is the repository-wide completion provider. The
// production server implements it; a Source without it answers 503, never a
// worker-key fold of per-queue receipts.
type AuthorityCompletionSource interface {
	AuthorityCompletion(context.Context, store.AuthorityCompletionRequest) (store.AuthorityCompletion, error)
}

// This independent endpoint preserves every existing GitHub formatter and byte.
func serveAuthorityCompletion(w http.ResponseWriter, r *http.Request, source Source) {
	query := r.URL.Query()
	for key, values := range query {
		if (key != "installation" && key != "repo" && key != "digest" && key != "targets") || len(values) != 1 {
			fail(w, 400, "invalid-request", "invalid completion query")
			return
		}
	}
	request := store.AuthorityCompletionRequest{Installation: query.Get("installation"), Repository: query.Get("repo"), Digest: query.Get("digest")}
	if raw := query.Get("targets"); raw != "" {
		if len(raw) > 1<<20 || json.Unmarshal([]byte(raw), &request.Targets) != nil {
			fail(w, 400, "invalid-request", "invalid completion targets")
			return
		}
	}
	if err := request.Validate(); err != nil {
		fail(w, 400, "invalid-request", "completion requires installation, repository and expected digests")
		return
	}
	provider, ok := source.(AuthorityCompletionSource)
	if !ok {
		fail(w, 503, "unavailable", "repository completion source unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	completion, err := provider.AuthorityCompletion(ctx, request)
	if err != nil {
		fail(w, 503, "unavailable", "repository completion unavailable; retry with complete discovery and expected digests")
		return
	}
	writeJSON(w, completion)
}
