package authority

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestActorValidation(t *testing.T) {
	validTeam := Actor{Team: "Smart-Data-Ohio/break-glass"}
	validAdmin := Actor{RepositoryAdmin: true}
	if err := validTeam.Validate(); err != nil {
		t.Fatalf("team: %v", err)
	}
	if err := validAdmin.Validate(); err != nil {
		t.Fatalf("repository admin: %v", err)
	}
	for _, actor := range []Actor{
		{},
		{Team: "Smart-Data-Ohio/break-glass", RepositoryAdmin: true},
		{Team: "Smart-Data-Ohio"},
		{Team: "Smart Data Ohio/owners"},
		{Team: "Smart-Data-Ohio/break-glass/extra"},
		{Team: "Smart-Data-Ohio/openmerge[bot]"},
	} {
		if err := actor.Validate(); err == nil {
			t.Fatalf("accepted invalid actor %+v", actor)
		}
	}
}

func TestResolvedActorAndBindingValidation(t *testing.T) {
	team := ResolvedTeam{OrganizationID: "1301", TeamID: "42"}
	admin := ResolvedRepositoryAdmin{RoleID: "repository-admin-role"}
	for _, binding := range []ActorBinding{
		{Declared: Actor{Team: "Smart-Data-Ohio/break-glass"}, Resolved: ResolvedActor{Team: &team}},
		{Declared: Actor{RepositoryAdmin: true}, Resolved: ResolvedActor{RepositoryAdmin: &admin}},
	} {
		if err := binding.Validate(); err != nil {
			t.Fatalf("valid binding: %v", err)
		}
	}
	for _, binding := range []ActorBinding{
		{Declared: Actor{Team: "Smart-Data-Ohio/break-glass"}, Resolved: ResolvedActor{RepositoryAdmin: &admin}},
		{Declared: Actor{RepositoryAdmin: true}, Resolved: ResolvedActor{Team: &team}},
		{Declared: Actor{RepositoryAdmin: true}, Resolved: ResolvedActor{}},
		{Declared: Actor{RepositoryAdmin: true}, Resolved: ResolvedActor{RepositoryAdmin: &ResolvedRepositoryAdmin{}}},
		{Declared: Actor{RepositoryAdmin: true}, Resolved: ResolvedActor{Team: &team, RepositoryAdmin: &admin}},
	} {
		if err := binding.Validate(); err == nil {
			t.Fatalf("accepted invalid binding %+v", binding)
		}
	}
}

func TestActorWireJSONIsStrict(t *testing.T) {
	for _, tc := range []struct {
		data string
		want Actor
	}{
		{`{"team":"Smart-Data-Ohio/break-glass"}`, Actor{Team: "Smart-Data-Ohio/break-glass"}},
		{`{"repository_admin":true}`, Actor{RepositoryAdmin: true}},
	} {
		var actor Actor
		if err := json.Unmarshal([]byte(tc.data), &actor); err != nil {
			t.Fatalf("valid actor %s: %v", tc.data, err)
		}
		if !reflect.DeepEqual(actor, tc.want) {
			t.Fatalf("decoded actor = %#v, want %#v", actor, tc.want)
		}
	}
	for _, data := range []string{
		`null`,
		`{}`,
		`{"team":null}`,
		`{"team":null,"repository_admin":true}`,
		`{"team":"","repository_admin":true}`,
		`{"repository_admin":null}`,
		`{"repository_admin":false}`,
		`{"team":"Smart-Data-Ohio/break-glass","repository_admin":false}`,
		`{"team":"Smart-Data-Ohio/break-glass","repository_admin":true}`,
		`{"user":"alice"}`,
		`{"bot":"openmerge[bot]"}`,
		`{"team":"Smart-Data-Ohio/break-glass","unknown":true}`,
		`{"team":"Smart-Data-Ohio/break-glass","team":"other/break-glass"}`,
	} {
		var actor Actor
		if err := json.Unmarshal([]byte(data), &actor); err == nil {
			t.Fatalf("accepted invalid actor JSON %s", data)
		}
	}
	encoded, err := json.Marshal(Actor{Team: "Smart-Data-Ohio/break-glass"})
	if err != nil || string(encoded) != `{"team":"Smart-Data-Ohio/break-glass"}` {
		t.Fatalf("team JSON = %s, %v", encoded, err)
	}
	encoded, err = json.Marshal(Actor{RepositoryAdmin: true})
	if err != nil || string(encoded) != `{"repository_admin":true}` {
		t.Fatalf("repository-admin JSON = %s, %v", encoded, err)
	}
}

func TestResolvedBypassIdentity(t *testing.T) {
	for _, tc := range []struct {
		resolved ResolvedActor
		want     BypassActor
	}{
		{ResolvedActor{Team: &ResolvedTeam{OrganizationID: "org", TeamID: "team"}}, BypassActor{Kind: "team", ID: "team", Mode: "always"}},
		{ResolvedActor{RepositoryAdmin: &ResolvedRepositoryAdmin{RoleID: "adapter-role"}}, BypassActor{Kind: "repository_role", ID: "adapter-role", Mode: "always"}},
	} {
		got, err := tc.resolved.BypassIdentity()
		if err != nil || got != tc.want {
			t.Fatal("resolved bypass identity", got, err)
		}
	}
}
