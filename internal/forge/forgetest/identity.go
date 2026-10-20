package forgetest

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// IdentityEnv configures isolated identities and ID-keyed permissions. SetUser
// replaces a login's holder and removes a user's old login on rename. SetTeam
// supplies effective membership including any inherited child-team members.
type IdentityEnv struct {
	Reader   forge.IdentityReader
	Repo     forge.RepoRef
	SetUser  func(forge.UserIdentity)
	SetGrant func(int64, string)
	SetTeam  func(forge.TeamIdentity, []forge.UserIdentity)
}

// RunIdentityReader is shared by the fake and HTTP GitHub implementations.
func RunIdentityReader(t *testing.T, newEnv func(*testing.T) IdentityEnv) {
	author := forge.UserIdentity{ID: 11, Login: "alice", Kind: forge.UserAccount}
	other := forge.UserIdentity{ID: 22, Login: "bob", Kind: forge.UserAccount}
	bot := forge.UserIdentity{ID: 33, Login: "coder[bot]", Kind: forge.BotAccount}
	team := forge.TeamIdentity{ID: 44, OrganizationID: 55, Organization: "acme", Slug: "platform"}
	setup := func(t *testing.T) IdentityEnv {
		e := newEnv(t)
		for _, user := range []forge.UserIdentity{author, other, bot} {
			e.SetUser(user)
		}
		e.SetGrant(author.ID, "write")
		e.SetGrant(other.ID, "admin")
		e.SetTeam(team, []forge.UserIdentity{bot, author, other, author})
		return e
	}

	t.Run("invalid-identities", func(t *testing.T) {
		e := setup(t)
		for _, login := range []string{"", "bad/login", "bad login", "bad.name", "bad\nlogin", "[bot]"} {
			// A malformed fixture must not bypass the same validation as the API.
			user := author
			user.Login = login
			e.SetUser(user)
			e.SetGrant(user.ID, "write")
			if _, err := e.Reader.ResolveUser(t.Context(), login); !errors.Is(err, forge.ErrIdentityUnavailable) {
				t.Fatalf("ResolveUser(%q)=%v", login, err)
			}
			if _, err := e.Reader.WriteGrant(t.Context(), e.Repo, user); !errors.Is(err, forge.ErrIdentityUnavailable) {
				t.Fatalf("WriteGrant(%q)=%v", login, err)
			}
		}
		for _, name := range []string{"", "org", "org/", "/team", "org/team/more", "org/bad.team"} {
			if _, err := e.Reader.ResolveTeam(t.Context(), name); !errors.Is(err, forge.ErrIdentityUnavailable) {
				t.Fatalf("ResolveTeam(%q)=%v", name, err)
			}
		}
		for _, ids := range []forge.TeamIdentity{{}, {ID: 1}, {OrganizationID: 1}, {ID: -1, OrganizationID: 1}} {
			if _, err := e.Reader.TeamMembers(t.Context(), ids); !errors.Is(err, forge.ErrIdentityUnavailable) {
				t.Fatalf("TeamMembers(%+v)=%v", ids, err)
			}
		}
	})
	t.Run("invalid-member-login", func(t *testing.T) {
		e := setup(t)
		member := author
		member.Login = "bad login"
		e.SetTeam(team, []forge.UserIdentity{member})
		if members, err := e.Reader.TeamMembers(t.Context(), team); !errors.Is(err, forge.ErrIdentityUnavailable) || members != nil {
			t.Fatalf("invalid membership=%v, %v", members, err)
		}
	})

	t.Run("membership-bound", func(t *testing.T) {
		e := setup(t)
		members := make([]forge.UserIdentity, forge.MaxTeamMembers+1)
		for i := range members {
			members[i] = forge.UserIdentity{ID: int64(i + 1), Login: fmt.Sprintf("member-%d", i+1), Kind: forge.UserAccount}
		}
		e.SetTeam(team, members[:forge.MaxTeamMembers])
		got, err := e.Reader.TeamMembers(t.Context(), team)
		if err != nil || len(got) != forge.MaxTeamMembers {
			t.Fatalf("maximum membership: len=%d err=%v", len(got), err)
		}
		e.SetTeam(team, members)
		if got, err := e.Reader.TeamMembers(t.Context(), team); !errors.Is(err, forge.ErrMembershipIncomplete) || got != nil {
			t.Fatalf("oversized membership: len=%d err=%v", len(got), err)
		}
	})
	t.Run("stable-identities-and-effective-membership", func(t *testing.T) {
		e := setup(t)
		for _, want := range []forge.UserIdentity{author, other, bot} {
			got, err := e.Reader.ResolveUser(t.Context(), want.Login)
			if err != nil || got != want {
				t.Fatalf("resolve user: %+v, %v; want %+v", got, err, want)
			}
		}
		gotTeam, err := e.Reader.ResolveTeam(t.Context(), "acme/platform")
		if err != nil || gotTeam != team {
			t.Fatalf("resolve team: %+v, %v", gotTeam, err)
		}
		members, err := e.Reader.TeamMembers(t.Context(), team)
		if err != nil || !reflect.DeepEqual(members, []forge.UserIdentity{author, other, bot}) {
			t.Fatalf("effective members must be complete, sorted, ID-deduped: %+v, %v", members, err)
		}
		members[0].Login = "mutated"
		e.SetTeam(team, []forge.UserIdentity{other})
		members, err = e.Reader.TeamMembers(t.Context(), team)
		if err != nil || !reflect.DeepEqual(members, []forge.UserIdentity{other}) {
			t.Fatalf("removed members persisted: %+v, %v", members, err)
		}
		e.SetTeam(team, nil)
		members, err = e.Reader.TeamMembers(t.Context(), team)
		if err != nil || members == nil || len(members) != 0 {
			t.Fatalf("known-empty membership: %+v, %v", members, err)
		}
	})
	t.Run("missing-grant-is-unknown", func(t *testing.T) {
		e := setup(t)
		grant, err := e.Reader.WriteGrant(t.Context(), e.Repo, bot)
		if !errors.Is(err, forge.ErrIdentityUnavailable) || grant.CanWrite(bot.ID) {
			t.Fatalf("missing grant must be unknown, not known-empty: %+v, %v", grant, err)
		}
	})
	t.Run("rename-or-reassignment-between-reads", func(t *testing.T) {
		e := setup(t)
		// This identity represents a review/comment author read before rename.
		observed, err := e.Reader.ResolveUser(t.Context(), author.Login)
		if err != nil {
			t.Fatal(err)
		}
		renamed := author
		renamed.Login = "alice-renamed"
		e.SetUser(renamed)
		impostor := other
		impostor.Login = author.Login
		e.SetUser(impostor)
		grant, err := e.Reader.WriteGrant(t.Context(), e.Repo, observed)
		if !errors.Is(err, forge.ErrIdentityMismatch) || grant.User.ID != other.ID || grant.CanWrite(observed.ID) {
			t.Fatalf("reassigned login lent permission to old author: %+v, %v", grant, err)
		}
		resolved, err := e.Reader.ResolveUser(t.Context(), renamed.Login)
		if err != nil || resolved.ID != observed.ID {
			t.Fatalf("rename lost stable identity: %+v, %v", resolved, err)
		}
		grant, err = e.Reader.WriteGrant(t.Context(), e.Repo, resolved)
		if err != nil || !grant.CanWrite(observed.ID) {
			t.Fatalf("renamed author cannot use own grant: %+v, %v", grant, err)
		}
	})
	t.Run("fresh-revocation-and-missing-identities", func(t *testing.T) {
		e := setup(t)
		grant, err := e.Reader.WriteGrant(t.Context(), e.Repo, author)
		if err != nil || !grant.CanWrite(author.ID) {
			t.Fatalf("initial grant: %+v, %v", grant, err)
		}
		e.SetGrant(author.ID, "read")
		grant, err = e.Reader.WriteGrant(t.Context(), e.Repo, author)
		if err != nil || grant.CanWrite(author.ID) {
			t.Fatalf("revoked grant persisted: %+v, %v", grant, err)
		}
		missing := author
		missing.ID = 0
		grant, err = e.Reader.WriteGrant(t.Context(), e.Repo, missing)
		if err == nil || grant.CanWrite(author.ID) {
			t.Fatalf("missing author ID authorized: %+v, %v", grant, err)
		}
		e.SetUser(missing)
		e.SetGrant(0, "write")
		grant, err = e.Reader.WriteGrant(t.Context(), e.Repo, author)
		if !errors.Is(err, forge.ErrIdentityMismatch) || grant.CanWrite(author.ID) {
			t.Fatalf("missing response ID authorized: %+v, %v", grant, err)
		}
	})
}
