package serve

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	github "github.com/CanadaApollo6/openmerge/internal/forge/github"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// Keep non-identity operations on the fake; identity failures come through the
// actual installation transport, SDK and adapter using a loopback HTTP server.
type stageAHTTPIdentityDriver struct {
	*fake.Forge
	ids       forge.IdentityReader
	operation string
}

func (d stageAHTTPIdentityDriver) ResolveUser(ctx context.Context, login string) (forge.UserIdentity, error) {
	if d.operation == "user" {
		return d.ids.ResolveUser(ctx, login)
	}
	return d.Forge.ResolveUser(ctx, login)
}
func (d stageAHTTPIdentityDriver) ResolveTeam(ctx context.Context, name string) (forge.TeamIdentity, error) {
	if d.operation == "team" {
		return d.ids.ResolveTeam(ctx, name)
	}
	return d.Forge.ResolveTeam(ctx, name)
}
func (d stageAHTTPIdentityDriver) TeamMembers(ctx context.Context, team forge.TeamIdentity) ([]forge.UserIdentity, error) {
	if d.operation == "members" {
		return d.ids.TeamMembers(ctx, team)
	}
	return d.Forge.TeamMembers(ctx, team)
}

func TestStageAHTTPIdentityFailuresBlockOrRetry(t *testing.T) {
	t.Parallel()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	must(t, err)
	private := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	for _, operation := range []string{"user", "team", "members"} {
		for _, tc := range []struct {
			name                   string
			status                 int
			header, value, message string
			definite               bool
		}{
			{name: "not-found", status: 404, definite: true},
			{name: "forbidden", status: 403, definite: operation != "user"},
			{name: "retry-after", status: 403, header: "Retry-After", value: "60"},
			{name: "exhausted", status: 403, header: "X-RateLimit-Remaining", value: "0"},
			{name: "secondary", status: 403, message: "You have exceeded a secondary rate limit"},
			{name: "too-many", status: 429},
			{name: "server", status: 503},
			{name: "timeout", status: 200},
		} {
			t.Run(fmt.Sprintf("%s/%s", operation, tc.name), func(t *testing.T) {
				t.Parallel()
				// Both base positions share the schema, HTTP server and initialized
				// policy. Each failure is followed by a fully asserted recovery.
				s, w, f := shadowFixture(t, 1, "main", "7", false)
				owner := "writer"
				if operation != "user" {
					owner = "org/reviewers"
					f.SetTeam(forge.TeamIdentity{ID: 50, OrganizationID: 5, Organization: "org", Slug: "reviewers"}, []forge.UserIdentity{{ID: 21, Login: "writer", Kind: forge.UserAccount}})
				}
				_, err := f.PushBase(repo, "main", "owner", map[string]string{"CODEOWNERS": "* @" + owner})
				must(t, err)
				must(t, s.configuration(t.Context(), w))
				server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
					out.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case "/app":
						_, _ = fmt.Fprint(out, `{"id":42,"slug":"test"}`)
					case "/app/installations/7/access_tokens":
						_, _ = fmt.Fprintf(out, `{"token":"fixture-only","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
					default:
						if tc.name == "timeout" {
							<-r.Context().Done()
							return
						}
						if tc.header != "" {
							out.Header().Set(tc.header, tc.value)
						}
						out.WriteHeader(tc.status)
						_, _ = fmt.Fprintf(out, `{"message":%q}`, tc.message)
					}
				}))
				defer server.Close()

				for _, moved := range []bool{false, true} {
					t.Run(fmt.Sprintf("moved=%t", moved), func(t *testing.T) {
						if moved {
							_, err := f.PushBase(repo, "main", "move", nil)
							must(t, err)
						}
						// Only the timeout case needs a small deadline; other statuses
						// must not turn into incidental timeouts on small race runners.
						timeout := 5 * time.Second
						if tc.name == "timeout" {
							timeout = 50 * time.Millisecond
						}
						app, err := github.New(github.Config{AppID: 42, PrivateKey: private, APIBaseURL: server.URL + "/", HTTPClient: &http.Client{Timeout: timeout}})
						must(t, err)
						driver, err := app.Installation(t.Context(), 7)
						must(t, err)
						w.Driver = stageAHTTPIdentityDriver{Forge: f, ids: driver, operation: operation}
						err = s.configuration(t.Context(), w)
						if tc.definite && err != nil || !tc.definite && err == nil {
							t.Fatalf("configuration error=%v definite=%t", err, tc.definite)
						}
						if got := shadowState(t, s, w).SourceBlocked(queue.ConfigSource); got != tc.definite {
							t.Fatalf("block=%t definite=%t error=%v", got, tc.definite, w.configError)
						}
						if w.configError == nil {
							t.Fatal("missing config diagnostic")
						}
						// A fresh readable resolution clears a durable block at either base.
						w.Driver = f
						must(t, s.configuration(t.Context(), w))
						if shadowState(t, s, w).SourceBlocked(queue.ConfigSource) || w.configError != nil {
							t.Fatal("readable identity failed to recover")
						}
					})
				}
			})
		}
	}
}
