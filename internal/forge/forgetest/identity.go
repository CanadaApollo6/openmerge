package forgetest

import (
	"errors"
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
		grant, err = e.Reader.WriteGrant(t.Context(), e.Repo, author)
		if !errors.Is(err, forge.ErrIdentityMismatch) || grant.CanWrite(author.ID) {
			t.Fatalf("missing response ID authorized: %+v, %v", grant, err)
		}
	})
}
