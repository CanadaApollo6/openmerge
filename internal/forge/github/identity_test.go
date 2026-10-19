package github

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/forgetest"
)

func TestIdentityContract(t *testing.T) {
	forgetest.RunIdentityReader(t, func(t *testing.T) forgetest.IdentityEnv {
		f := newAPI(t)
		users := map[string]forge.UserIdentity{}
		grants := map[int64]string{}
		var team forge.TeamIdentity
		var members []forge.UserIdentity
		userJSON := func(user forge.UserIdentity) map[string]any {
			kind := map[forge.AccountKind]string{forge.UserAccount: "User", forge.BotAccount: "Bot", forge.AppAccount: "App"}[user.Kind]
			return map[string]any{"id": user.ID, "login": user.Login, "type": kind}
		}
		f.override = func(w http.ResponseWriter, r *http.Request) bool {
			path := strings.TrimPrefix(r.URL.Path, "/api/v3/")
			switch {
			case strings.HasPrefix(path, "users/"):
				user, ok := users[strings.ToLower(strings.TrimPrefix(path, "users/"))]
				if !ok {
					f.apiError(w, 404, "Not Found")
				} else {
					f.json(w, userJSON(user))
				}
			case path == "orgs/acme/teams/platform":
				f.json(w, map[string]any{"id": team.ID, "slug": team.Slug, "organization": map[string]any{"id": team.OrganizationID, "login": team.Organization}})
			case path == "organizations/55/team/44/members":
				if r.URL.Query().Get("per_page") != "100" || r.URL.Query().Get("role") != "all" {
					t.Errorf("membership request must include all roles: %s", r.URL.RawQuery)
				}
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				start := (page - 1) * 2
				end := min(start+2, len(members))
				if end < len(members) {
					w.Header().Set("Link", fmt.Sprintf(`<%s%s?page=%d>; rel="next"`, f.server.URL, r.URL.Path, page+1))
				}
				result := []map[string]any{}
				for _, user := range members[start:end] {
					// GitHub documents child-team inclusion for this endpoint.
					// This fixture pins role=all and consumes the full returned set;
					// it does not independently test GitHub's hierarchy expansion.
					result = append(result, userJSON(user))
				}
				f.json(w, result)
			case strings.HasPrefix(path, "repos/acme/project/collaborators/") && strings.HasSuffix(path, "/permission"):
				login := strings.TrimSuffix(strings.TrimPrefix(path, "repos/acme/project/collaborators/"), "/permission")
				user := users[strings.ToLower(login)]
				permission, known := grants[user.ID]
				if !known {
					f.apiError(w, 404, "Not Found")
				} else {
					f.json(w, map[string]any{"user": userJSON(user), "permission": permission})
				}
			default:
				return false
			}
			return true
		}
		return forgetest.IdentityEnv{
			Reader: f.driver, Repo: testRepo,
			SetUser: func(user forge.UserIdentity) {
				f.mu.Lock()
				defer f.mu.Unlock()
				for login, old := range users {
					if old.ID == user.ID {
						delete(users, login)
					}
				}
				users[strings.ToLower(user.Login)] = user
			},
			SetGrant: func(id int64, permission string) {
				f.mu.Lock()
				defer f.mu.Unlock()
				grants[id] = permission
			},
			SetTeam: func(value forge.TeamIdentity, users []forge.UserIdentity) {
				f.mu.Lock()
				defer f.mu.Unlock()
				team, members = value, slices.Clone(users)
			},
		}
	})
}

func TestIdentityAccountKindsAndInvalidResolution(t *testing.T) {
	f := newAPI(t)
	for _, tt := range []struct {
		body string
		kind forge.AccountKind
		bad  bool
	}{
		{`{"id":11,"login":"alice","type":"User"}`, forge.UserAccount, false},
		{`{"id":11,"login":"coder[bot]","type":"Bot"}`, forge.BotAccount, false},
		{`{"id":11,"login":"app","type":"App"}`, forge.AppAccount, false},
		{`{"id":11,"login":"alice","type":"FutureType"}`, forge.UnknownAccount, false},
		{`{"id":11,"login":"alice"}`, forge.UnknownAccount, false},
		{`{"login":"alice","type":"User"}`, "", true},
		{`{"id":11,"type":"User"}`, "", true},
	} {
		f.override = func(w http.ResponseWriter, r *http.Request) bool {
			if !strings.HasPrefix(r.URL.Path, "/api/v3/users/") {
				return false
			}
			_, _ = fmt.Fprint(w, tt.body)
			return true
		}
		user, err := f.driver.ResolveUser(t.Context(), "alice")
		if (err != nil) != tt.bad || !tt.bad && user.Kind != tt.kind {
			t.Fatalf("body=%s: %+v, %v", tt.body, user, err)
		}
	}
	for _, login := range []string{"", "../alice", "alice?x", "alice%2fbob", "alice\n"} {
		if _, err := f.driver.ResolveUser(t.Context(), login); err == nil {
			t.Fatalf("invalid login %q resolved", login)
		}
	}
	for _, team := range []string{"acme", "acme/", "acme/a/b", "acme/../platform", "@acme/platform"} {
		if _, err := f.driver.ResolveTeam(t.Context(), team); err == nil {
			t.Fatalf("invalid team %q resolved", team)
		}
	}
}

func TestIdentityGrantRevalidationNeverUsesStalePermission(t *testing.T) {
	f := newAPI(t)
	author := forge.UserIdentity{ID: 11, Login: "alice"}
	calls, status, responseID := 0, 200, int64(11)
	permission := "write"
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/collaborators/alice/permission") {
			return false
		}
		calls++
		if status != 200 {
			f.apiError(w, status, "failed")
		} else {
			_, _ = fmt.Fprintf(w, `{"permission":%q,"user":{"id":%d,"login":"alice","type":"User"}}`, permission, responseID)
		}
		return true
	}
	grant, err := f.driver.WriteGrant(t.Context(), testRepo, author)
	must(t, err)
	if !grant.CanWrite(author.ID) {
		t.Fatal(grant)
	}
	grant, err = f.driver.WriteGrant(t.Context(), testRepo, author)
	must(t, err)
	if !grant.CanWrite(author.ID) || calls != 2 || f.notModified != 1 {
		t.Fatal("cached grant authorized without a round trip")
	}
	for _, code := range []int{403, 404, 500} {
		status = code
		grant, err = f.driver.WriteGrant(t.Context(), testRepo, author)
		if err == nil || grant.CanWrite(author.ID) {
			t.Fatalf("HTTP %d reused a cached grant: %+v, %v", code, grant, err)
		}
		if code == 404 && !errors.Is(err, forge.ErrIdentityUnavailable) {
			t.Fatalf("HTTP 404 must be unknown: %v", err)
		}
	}
	status, responseID = 200, 0
	grant, err = f.driver.WriteGrant(t.Context(), testRepo, author)
	if !errors.Is(err, forge.ErrIdentityMismatch) || grant.CanWrite(author.ID) {
		t.Fatalf("missing response ID authorized author: %+v, %v", grant, err)
	}
	status, responseID, permission = 200, 11, "read"
	grant, err = f.driver.WriteGrant(t.Context(), testRepo, author)
	must(t, err)
	if grant.CanWrite(author.ID) {
		t.Fatal("revoked writer authorized")
	}
}

func TestTeamMembershipIncompleteIsUnknown(t *testing.T) {
	for _, mode := range []string{"forbidden", "not-found", "later-page-error", "invalid-id", "cycle", "limit"} {
		t.Run(mode, func(t *testing.T) {
			f := newAPI(t)
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if strings.Contains(r.URL.Path, "/teams/") {
					f.apiError(w, 404, "team inaccessible")
					return true
				}
				if !strings.HasSuffix(r.URL.Path, "/team/44/members") {
					return false
				}
				page := r.URL.Query().Get("page")
				switch {
				case mode == "forbidden":
					f.apiError(w, 403, "members read not installed")
				case mode == "not-found":
					f.apiError(w, 404, "team inaccessible")
				case mode == "later-page-error" && page == "2":
					f.apiError(w, 500, "unavailable")
				case mode == "invalid-id":
					_, _ = fmt.Fprint(w, `[{"login":"alice"}]`)
				default:
					next := "2"
					switch mode {
					case "cycle":
						next = "1"
					case "limit":
						next = "101"
					}
					w.Header().Set("Link", fmt.Sprintf(`<%s%s?page=%s>; rel="next"`, f.server.URL, r.URL.Path, next))
					_, _ = fmt.Fprint(w, `[{"id":11,"login":"alice","type":"User"}]`)
				}
				return true
			}
			members, err := f.driver.TeamMembers(t.Context(), forge.TeamIdentity{ID: 44, OrganizationID: 55})
			if err == nil || members != nil {
				t.Fatalf("incomplete membership returned usable result: %+v, %v", members, err)
			}
			if _, err := f.driver.ResolveTeam(t.Context(), "acme/platform"); err == nil {
				t.Fatal("inaccessible team resolved")
			}
		})
	}
}

func TestIdentityReadsPreserveSettlementBudget(t *testing.T) {
	f := newAPI(t)
	// Populate the installation token before setting the budget boundary.
	_, err := f.driver.CanWrite(t.Context(), testRepo, "writer")
	must(t, err)
	tr := f.driver.client.Client().Transport.(*transport)
	tr.mu.Lock()
	tr.budgetKnown, tr.limit, tr.remaining, tr.reset = true, 1000, 100, epoch.Add(time.Hour)
	tr.mu.Unlock()
	ctx := forge.WithRequestClass(t.Context(), forge.SettlementRequest)
	_, userErr := f.driver.ResolveUser(ctx, "alice")
	_, teamErr := f.driver.ResolveTeam(ctx, "acme/platform")
	_, membersErr := f.driver.TeamMembers(ctx, forge.TeamIdentity{ID: 44, OrganizationID: 55})
	grant, grantErr := f.driver.WriteGrant(ctx, testRepo, forge.UserIdentity{ID: 11, Login: "alice"})
	for _, err := range []error{userErr, teamErr, membersErr, grantErr} {
		var rate *RateLimitError
		if !errors.As(err, &rate) {
			t.Fatalf("identity read consumed reserved capacity: %v", err)
		}
	}
	if grant.CanWrite(11) || f.driver.Budget().Remaining != 100 {
		t.Fatal("identity reads consumed settlement reserve")
	}
	// Real settlement can still use its share through the same transport.
	allowed, err := f.driver.CanWrite(ctx, testRepo, "reader")
	must(t, err)
	if allowed || f.driver.Budget().Remaining != 99 {
		t.Fatal("settlement could not use reserved capacity")
	}
}

func TestIdentityRejectsUnicodeAliases(t *testing.T) {
	for _, name := range []string{"ſam", "Kam", "Kontributor", "Élodie", "élodie", "Ａam", "ıam", "İam"} {
		t.Run(name, func(t *testing.T) {
			f := newAPI(t)
			calls := 0
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				calls++
				if strings.Contains(r.URL.Path, "/teams/") {
					f.json(w, map[string]any{"id": 44, "slug": name, "organization": map[string]any{"id": 55, "login": "org"}})
				} else {
					f.json(w, map[string]any{"id": 11, "login": name, "type": "User"})
				}
				return true
			}
			if _, err := f.driver.ResolveUser(t.Context(), name); err == nil {
				t.Fatal("Unicode request accepted")
			}
			for _, team := range []string{name + "/team", "org/" + name} {
				if _, err := f.driver.ResolveTeam(t.Context(), team); err == nil {
					t.Fatal("Unicode team request accepted")
				}
			}
			if calls != 0 {
				t.Fatal("invalid identity reached API")
			}
			if _, err := f.driver.ResolveUser(t.Context(), "sam"); err == nil {
				t.Fatal("Unicode response accepted")
			}
			if _, err := f.driver.ResolveTeam(t.Context(), "org/sam"); err == nil {
				t.Fatal("Unicode team response accepted")
			}
		})
	}
}

func TestWriteGrantInvalidLoginDiagnostic(t *testing.T) {
	f := newAPI(t)
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/collaborators/alice/permission") {
			return false
		}
		_, _ = fmt.Fprint(w, `{"permission":"write","user":{"id":11,"login":"bad/login","type":"User"}}`)
		return true
	}
	_, err := f.driver.WriteGrant(t.Context(), testRepo, forge.UserIdentity{ID: 11, Login: "alice"})
	if !errors.Is(err, forge.ErrIdentityMismatch) || !strings.Contains(err.Error(), "invalid user login") || strings.Contains(err.Error(), "expected user 11, observed 11") {
		t.Fatal(err)
	}
}
