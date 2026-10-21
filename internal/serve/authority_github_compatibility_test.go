package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

const githubApprovalsConfig = "version: 1\nqueues:\n  - name: default\n    merge_when:\n      - check-success = ci\n    approvals:\n      default: any_writer\n"

// This exact source is also run as an overlay on the baseline checkout. Keep
// the legacy export below independent of the checked-in candidate goldens: the
// offline ADR verifier only compares the legacy GitHub projection bytes.
func TestAuthorityGithubProjectionBytes(t *testing.T) {
	if path := os.Getenv("ADR_GITHUB_PROJECTIONS"); path != "" {
		testSynctest(t, func(t *testing.T) {
			output := legacyGithubProjection(t)
			raw, err := json.Marshal(output)
			must(t, err)
			must(t, os.WriteFile(path, raw, 0600))
		})
		return
	}

	for _, tc := range []struct {
		name      string
		config    string
		golden    string
		approvals bool
	}{
		{name: "no-approvals", config: repositoryConfig, golden: "authority_github_no_approvals.json"},
		{name: "approvals-configured", config: githubApprovalsConfig, golden: "authority_github_approvals.json", approvals: true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// These fixtures deliberately remain serial. A Postgres-backed fixture
			// and its fake forge must be born and cleaned up in one synctest bubble.
			testSynctest(t, func(t *testing.T) {
				got := githubProjection(t, tc.config, tc.approvals)
				assertGithubProjectionGolden(t, tc.golden, got)
			})
		})
	}
}

func legacyGithubProjection(t *testing.T) map[string]string {
	t.Helper()
	s, w, f, _ := fixture(t, repositoryConfig)
	must(t, s.cycle(t.Context(), w))
	output := map[string]string{}
	for _, path := range []string{"/v1/queues", "/v1/health"} {
		output[path] = githubHTTP(t, s, w.Key, path)
	}
	summary, err := s.ledgerSummary(t.Context(), w, 1)
	must(t, err)
	output["author"] = summary
	// Export actual invalid-config journal and projections in both checkouts.
	// Keep the same legacy behavior whether authority work is present or not.
	_, err = f.PushBase(repo, "main", "invalid config", map[string]string{".openmerge.yml": "version: broken"})
	must(t, err)
	must(t, s.cycle(t.Context(), w))
	if w.configError == nil {
		t.Fatal("invalid config did not stop github work")
	}
	rows, err := s.store.EventsAfter(t.Context(), w.id, 0)
	must(t, err)
	events := []json.RawMessage{}
	for _, row := range rows {
		_, raw, err := queue.EncodeEvent(row.Event)
		must(t, err)
		events = append(events, json.RawMessage(raw))
	}
	raw, err := json.Marshal(events)
	must(t, err)
	output["invalid-config-journal"] = string(raw)
	output["invalid-config-api"] = githubHTTP(t, s, w.Key, "/v1/queues")
	output["invalid-config-health"] = githubHTTP(t, s, w.Key, "/v1/health")
	// An opted-in worker that has rolled back to github must retain the same
	// invalid-config diagnostics and behavior as this baseline too.
	rolledBack, rw, rf, _ := fixture(t, repositoryConfig)
	must(t, rolledBack.cycle(t.Context(), rw))
	rw.AuthorityRequired = true
	rw.resolveAuthorityLifecycle = func(context.Context, config.AuthorityCandidate, string, string) (authorityLifecycleResolution, error) {
		t.Fatal("invalid config invoked authority resolver")
		return authorityLifecycleResolution{}, nil
	}
	_, err = rf.PushBase(repo, "main", "invalid config", map[string]string{".openmerge.yml": "version: broken"})
	must(t, err)
	must(t, rolledBack.cycle(t.Context(), rw))
	rows, err = rolledBack.store.EventsAfter(t.Context(), rw.id, 0)
	must(t, err)
	events = nil
	for _, row := range rows {
		_, raw, err := queue.EncodeEvent(row.Event)
		must(t, err)
		events = append(events, json.RawMessage(raw))
	}
	raw, err = json.Marshal(events)
	must(t, err)
	output["github-opted-invalid-config-journal"] = string(raw)
	output["github-opted-invalid-config-api"] = githubHTTP(t, rolledBack, rw.Key, "/v1/queues")
	output["github-opted-invalid-config-health"] = githubHTTP(t, rolledBack, rw.Key, "/v1/health")

	return output
}

func githubProjection(t *testing.T, config string, approvals bool) map[string]string {
	t.Helper()
	s, w, f, _ := fixture(t, config)
	if approvals {
		// Approval queues need an explicitly known native-review snapshot. An
		// empty snapshot is the deterministic no-review GitHub observation.
		w.Driver = f
		writer := forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount}
		f.SetIdentity(writer)
		f.SetWriteGrant(repo, writer.ID, "write")
		contributor := forge.UserIdentity{ID: 12, Login: "contributor", Kind: forge.UserAccount}
		f.SetIdentity(contributor)
		f.SetWriteGrant(repo, contributor.ID, "write")
		f.SetNativeReviews(repo, 1, forge.NativeReviews{Comments: []forge.NativeComment{shadowComment(100, "@openmerge queue")}})
	} else {
		f.AddComment(repo, 1, "writer", "@openmerge queue")
	}
	must(t, s.cycle(t.Context(), w))

	pr, err := f.PullRequest(t.Context(), repo, 1)
	must(t, err)
	githubStatus, ok := f.Statuses(repo, 1)[pr.HeadSHA]
	if !ok {
		t.Fatal("cycle did not publish the GitHub status")
	}
	statusBytes, err := json.Marshal(struct {
		State   forge.StatusState `json:"state"`
		Reason  string            `json:"reason"`
		Summary string            `json:"summary"`
	}{githubStatus.State, githubStatus.Reason, githubStatus.Summary})
	must(t, err)

	id, err := w.Key.ChangeID(1)
	must(t, err)
	changePath := "/v1/queues/owner/repo/main/changes/" + url.PathEscape(string(id))
	output := map[string]string{
		"reviewer-check": githubStatus.Summary,
		"github-status":  string(statusBytes),
		"api":            githubHTTP(t, s, w.Key, "/v1/queues"),
		"health":         utcGithubHealth(t, githubHTTP(t, s, w.Key, "/v1/health")),
		"change":         githubHTTP(t, s, w.Key, changePath),
	}
	for _, audience := range []string{"reviewer", "operator", "agent"} {
		output["brief-"+audience] = githubHTTP(t, s, w.Key, changePath+"/brief?audience="+audience)
	}
	summary, err := s.ledgerSummary(t.Context(), w, 1)
	must(t, err)
	output["author"] = summary
	return output
}

// Normalize only health timestamp values: synctest fixes the instant, but
// time.Now retains the host location. Preserve every other byte (including JSON
// formatting) so the golden still catches changes outside timezone spelling.
func utcGithubHealth(t *testing.T, raw string) string {
	t.Helper()
	timestamps := regexp.MustCompile(`"(?:checked_at|started_at|last_success_at|cycle_started_at|last_progress_at|next_deadline)":"([^"]+)"`)
	return timestamps.ReplaceAllStringFunc(raw, func(field string) string {
		value := timestamps.FindStringSubmatch(field)[1]
		at, err := time.Parse(time.RFC3339Nano, value)
		must(t, err)
		return strings.Replace(field, value, at.UTC().Format(time.RFC3339Nano), 1)
	})
}

func githubHTTP(t *testing.T, source status.Source, key store.QueueKey, path string) string {
	t.Helper()
	response := httptest.NewRecorder()
	status.Handler(source, []store.QueueKey{key}, "", nil).ServeHTTP(response, httptest.NewRequest("GET", path, nil))
	if response.Code != 200 {
		t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
	}
	return response.Body.String()
}

func assertGithubProjectionGolden(t *testing.T, name string, got map[string]string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	wantBytes, err := os.ReadFile(path)
	must(t, err)
	want := map[string]string{}
	must(t, json.Unmarshal(wantBytes, &want))
	if !bytes.Equal(wantBytes, mustGithubProjectionJSON(t, want)) {
		t.Fatalf("projection golden %s is not canonical JSON", path)
	}
	if len(want) != len(got) {
		t.Fatalf("projection golden %s has keys %d, got %d", path, len(want), len(got))
	}
	for key, value := range got {
		if want[key] != value {
			t.Fatalf("projection golden %s field %q differs:\nwant:\n%s\ngot:\n%s", path, key, printableProjection(want[key]), printableProjection(value))
		}
	}
}

func mustGithubProjectionJSON(t *testing.T, value map[string]string) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	must(t, err)
	return append(raw, '\n')
}

func printableProjection(value string) string {
	return fmt.Sprintf("%q", value)
}
