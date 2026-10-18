package fake

import (
	"errors"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/forgetest"
)

func TestIdentityContract(t *testing.T) {
	forgetest.RunIdentityReader(t, func(_ *testing.T) forgetest.IdentityEnv {
		f := New()
		repo := forge.RepoRef{Path: "acme/project"}
		return forgetest.IdentityEnv{Reader: f, Repo: repo, SetUser: f.SetIdentity, SetTeam: f.SetTeam, SetGrant: func(id int64, permission string) { f.SetWriteGrant(repo, id, permission) }}
	})
}

func TestIdentityFailuresRemainUnknown(t *testing.T) {
	f := New()
	user := forge.UserIdentity{ID: 1, Login: "alice", Kind: forge.UserAccount}
	team := forge.TeamIdentity{ID: 2, OrganizationID: 3, Organization: "acme", Slug: "platform"}
	repo := forge.RepoRef{Path: "acme/project"}
	f.SetIdentity(user)
	f.SetTeam(team, []forge.UserIdentity{user})
	f.SetWriteGrant(repo, user.ID, "write")
	failure := errors.New("missing installed grant")
	f.FailNext("ResolveUser", failure)
	if _, err := f.ResolveUser(t.Context(), user.Login); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	f.FailNext("ResolveTeam", failure)
	if _, err := f.ResolveTeam(t.Context(), "acme/platform"); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	f.FailNext("TeamMembers", failure)
	if members, err := f.TeamMembers(t.Context(), team); !errors.Is(err, failure) || members != nil {
		t.Fatalf("unreadable membership became empty: %v, %v", members, err)
	}
	f.FailNext("WriteGrant", failure)
	if grant, err := f.WriteGrant(t.Context(), repo, user); !errors.Is(err, failure) || grant.CanWrite(user.ID) {
		t.Fatalf("failed read reused grant: %+v, %v", grant, err)
	}
	if _, err := f.ResolveTeam(t.Context(), "acme/inaccessible"); err == nil {
		t.Fatal("missing team resolved")
	}
	if grant, err := f.WriteGrant(t.Context(), forge.RepoRef{Path: "acme/other"}, user); !errors.Is(err, forge.ErrIdentityUnavailable) || grant.CanWrite(user.ID) {
		t.Fatalf("grant crossed repository boundary: %+v, %v", grant, err)
	}
}
