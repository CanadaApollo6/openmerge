package github

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestMembersReadMissingInstalledGrantDoesNotBlockLegacyQueues(t *testing.T) {
	f := newAPI(t)
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/access_tokens") {
			return false
		}
		token := f.object("installation_token")
		// The installed grant deliberately omits members, independently of the
		// new setup manifest. Every legacy permission is present.
		token["token"], token["expires_at"] = "secret-installation-members", f.clock.Add(time.Hour).Format(time.RFC3339)
		w.WriteHeader(201)
		f.json(w, token)
		return true
	}
	problems, warnings, err := f.driver.ValidateInstallationPermissions(t.Context())
	must(t, err)
	if len(problems) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], "members: read") {
		t.Fatalf("missing organization grant blocked unrelated queues: %v %v", problems, warnings)
	}
	allowed, err := f.driver.CanWrite(t.Context(), testRepo, "writer")
	must(t, err)
	if !allowed {
		t.Fatal("legacy permission check changed")
	}
}

func TestRenameOrReassignmentBetweenCommentAndGrantReads(t *testing.T) {
	for _, tt := range []struct {
		user string
		ok   bool
	}{
		{`null`, false},
		{`{"login":"alice"}`, false},
		{`{"id":22,"login":"alice"}`, false},
		{`{"id":11,"login":"alice-renamed","type":"User"}`, true},
	} {
		t.Run(tt.user, func(t *testing.T) {
			f := newAPI(t)
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path == "/api/v3/repos/acme/project/issues/1/comments" {
					_, _ = fmt.Fprint(w, `[{"id":77,"body":"approval command","user":{"id":11,"login":"alice","type":"User"}}]`)
					return true
				}
				if !strings.HasSuffix(r.URL.Path, "/permission") {
					return false
				}
				_, _ = fmt.Fprintf(w, `{"permission":"write","user":%s}`, tt.user)
				return true
			}
			// Read a real HTTP comment fixture before the permission read. The
			// legacy Comment view omits author ID; PR 3b will own its richer DTO.
			comments, _, err := f.driver.client.Issues.ListComments(t.Context(), "acme", "project", 1, nil)
			must(t, err)
			if len(comments) != 1 || comments[0].GetUser().GetID() != 11 {
				t.Fatalf("comment author fixture: %+v", comments)
			}
			author := userIdentity(comments[0].GetUser())
			grant, err := f.driver.WriteGrant(t.Context(), testRepo, author)
			if (err == nil) != tt.ok || grant.CanWrite(11) != tt.ok {
				t.Fatalf("identity-bound grant: %+v, %v", grant, err)
			}
			legacy, err := f.driver.CanWrite(t.Context(), testRepo, "alice")
			must(t, err)
			if !legacy {
				t.Fatal("legacy CanWrite must retain bool-only semantics")
			}
		})
	}
}

func TestMembershipPinsTeamIDAcrossSlugReassignment(t *testing.T) {
	f := newAPI(t)
	teamID := 44
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/api/v3/orgs/acme/teams/platform":
			_, _ = fmt.Fprintf(w, `{"id":%d,"slug":"platform","organization":{"id":55,"login":"acme"}}`, teamID)
		case "/api/v3/organizations/55/team/44/members":
			_, _ = fmt.Fprint(w, `[{"id":11,"login":"alice","type":"User"}]`)
		case "/api/v3/organizations/55/team/66/members":
			_, _ = fmt.Fprint(w, `[{"id":22,"login":"bob","type":"User"}]`)
		default:
			return false
		}
		return true
	}
	old, err := f.driver.ResolveTeam(t.Context(), "acme/platform")
	must(t, err)
	teamID = 66
	current, err := f.driver.ResolveTeam(t.Context(), "acme/platform")
	must(t, err)
	for _, tt := range []struct {
		team forge.TeamIdentity
		id   int64
	}{{old, 11}, {current, 22}} {
		members, err := f.driver.TeamMembers(t.Context(), tt.team)
		must(t, err)
		if len(members) != 1 || members[0].ID != tt.id {
			t.Fatalf("reassigned slug changed stable team membership: %+v", members)
		}
	}
}
