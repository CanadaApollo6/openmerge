package forge

import "testing"

func TestReadGrantRequiresStableIdentityAndKnownReadLevel(t *testing.T) {
	for _, permission := range []string{"read", "triage", "write", "maintain", "admin"} {
		grant := WriteGrant{User: UserIdentity{ID: 11, Login: "alice"}, Permission: permission}
		if !grant.CanRead(11) || grant.CanRead(22) || grant.CanRead(0) {
			t.Fatalf("identity-bound %s grant failed: %+v", permission, grant)
		}
		if (permission == "read" || permission == "triage") && grant.CanWrite(11) {
			t.Fatal("read-level grant became writer authority")
		}
	}
	for _, permission := range []string{"", "none", "owner", "READ"} {
		if (WriteGrant{User: UserIdentity{ID: 11}, Permission: permission}).CanRead(11) {
			t.Fatal("unknown permission grants read", permission)
		}
	}
	if (WriteGrant{Permission: "admin"}).CanRead(0) {
		t.Fatal("missing actor/response identities grant read")
	}
}
