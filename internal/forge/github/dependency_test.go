package github

import (
	"errors"
	"fmt"
	dep "github.com/CanadaApollo6/openmerge/internal/dependency"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestDependencyObservationContract(t *testing.T) {
	dependencytest.Run(t, func(t *testing.T) dependencytest.Env {
		f := newAPI(t)
		var user forge.UserIdentity
		var permission string
		var source forge.DependencySource
		reads := 0
		failSource, failGrant := false, false
		f.override = func(w http.ResponseWriter, r *http.Request) bool {
			switch {
			case strings.HasSuffix(r.URL.Path, "/permission"):
				if failGrant {
					f.apiError(w, 403, "unreadable permission")
					return true
				}
				f.json(w, map[string]any{"permission": permission, "user": map[string]any{"id": user.ID, "login": user.Login, "type": "User"}})
			case r.URL.Path == "/api/v3/repos/acme/project/pulls/1":
				reads++
				if failSource {
					f.apiError(w, 503, "partial read failed")
					return true
				}
				state := "open"
				if source.Closed {
					state = "closed"
				}
				f.json(w, map[string]any{"number": source.Number, "state": state, "merged": source.Merged, "body": source.Body,
					"head": map[string]any{"sha": source.HeadSHA}, "base": map[string]any{"ref": source.Base, "sha": source.BaseSHA, "repo": map[string]any{"id": jsonRepoID(source.RepositoryID)}}})
			default:
				return false
			}
			return true
		}
		return dependencytest.Env{
			Reader: f.driver,
			SetGrant: func(value forge.UserIdentity, level string) {
				f.mu.Lock()
				defer f.mu.Unlock()
				user, permission = value, level
			},
			SetSource:   func(value forge.DependencySource) { f.mu.Lock(); defer f.mu.Unlock(); source = value },
			SourceReads: func() int { f.mu.Lock(); defer f.mu.Unlock(); return reads },
			FailGrant:   func() { f.mu.Lock(); defer f.mu.Unlock(); failGrant = true },
			FailSource:  func() { f.mu.Lock(); defer f.mu.Unlock(); failSource = true },
		}
	})
}

func jsonRepoID(value string) int64 {
	var id int64
	_, _ = fmt.Sscan(value, &id)
	return id
}

func TestDependencyReadsPreserveInstallationSettlementReserve(t *testing.T) {
	f := newAPI(t)
	r := dependencytest.Request()
	remaining := 102
	f.override = func(w http.ResponseWriter, request *http.Request) bool {
		path := request.URL.Path
		if strings.HasSuffix(path, "/access_tokens") {
			return false
		}
		remaining--
		w.Header().Set("X-RateLimit-Limit", "1000")
		w.Header().Set("X-RateLimit-Remaining", fmt.Sprint(remaining))
		switch {
		case strings.HasSuffix(path, "/permission"):
			f.json(w, map[string]any{"permission": "read", "user": map[string]any{"id": 11, "login": "alice", "type": "User"}})
		case strings.HasSuffix(path, "/pulls/1"):
			source := dependencytest.Source(r)
			f.json(w, map[string]any{"number": 1, "state": "open", "merged": false, "body": "",
				"head": map[string]any{"sha": source.HeadSHA}, "base": map[string]any{"ref": "main", "sha": source.BaseSHA, "repo": map[string]any{"id": 50}}})
		case path == "/api/v3/repos/acme/project":
			f.json(w, map[string]any{"default_branch": "main"})
		default:
			return false
		}
		return true
	}
	tr := f.driver.client.Client().Transport.(*transport)
	tr.mu.Lock()
	tr.budgetKnown = true
	tr.limit = 1000
	tr.remaining = 102
	tr.reset = epoch.Add(time.Hour)
	// Discovery/rules have already consumed their protected shares. Exactly the
	// final 10% remains protected after these two dependency requests.
	tr.spent[forge.DiscoveryRequest] = 10
	tr.spent[forge.RulesRequest] = 10
	tr.mu.Unlock()
	settlement := forge.WithRequestClass(t.Context(), forge.SettlementRequest)
	observation, err := dep.ObserveEdge(settlement, f.driver, r)
	must(t, err)
	if observation.Evidence().Observation.Result != "waiting" || f.driver.Budget().Remaining != 100 {
		t.Fatalf("two reads not charged: %+v %+v", observation.Evidence(), f.driver.Budget())
	}
	// Both direct adapters must defend the reserve even if passed settlement
	// priority by a future landing-start caller.
	_, err = f.driver.WriteGrant(settlement, testRepo, r.Declarer)
	var limited *RateLimitError
	if !errors.As(err, &limited) {
		t.Fatal("permission consumed settlement reserve", err)
	}
	_, err = f.driver.DependencySource(settlement, testRepo, 1)
	if !errors.As(err, &limited) {
		t.Fatal("graph consumed settlement reserve", err)
	}
	observation, err = dep.ObserveEdge(settlement, f.driver, r)
	must(t, err)
	if observation.Evidence().Observation.Result != "unavailable" || f.driver.Budget().Remaining != 100 {
		t.Fatal("budget failure reused success", observation.Evidence())
	}
	// A separate installation retains its own usable capacity.
	other, err := f.app.Installation(t.Context(), 8)
	must(t, err)
	_, err = other.DependencySource(t.Context(), testRepo, 1)
	must(t, err)
	if f.driver.Budget().Remaining != 100 {
		t.Fatal("other installation spent our reserve")
	}
	// Real settlement remains admitted while dependency reads are blocked.
	_, err = f.driver.DefaultBranch(settlement, testRepo)
	must(t, err)
	if f.driver.Budget().Remaining >= 100 {
		t.Fatal("settlement starved")
	}
}

func TestDependencyHTTPMissingFieldsFailClosed(t *testing.T) {
	for _, field := range []string{"number", "state", "merged", "body", "head", "base", "repository-id", "base-sha"} {
		t.Run(field, func(t *testing.T) {
			f := newAPI(t)
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if !strings.HasSuffix(r.URL.Path, "/pulls/1") {
					return false
				}
				source := dependencytest.Source(dependencytest.Request())
				body := map[string]any{"number": 1, "state": "open", "merged": false, "body": "", "head": map[string]any{"sha": source.HeadSHA}, "base": map[string]any{"ref": "main", "sha": source.BaseSHA, "repo": map[string]any{"id": 50}}}
				switch field {
				case "repository-id":
					body["base"].(map[string]any)["repo"] = nil
				case "base-sha":
					delete(body["base"].(map[string]any), "sha")
				default:
					delete(body, field)
				}
				f.json(w, body)
				return true
			}
			source, err := f.driver.DependencySource(t.Context(), testRepo, 1)
			if err == nil || source.Valid() {
				t.Fatal("partial HTTP read became a complete observation", source, err)
			}
		})
	}
}
