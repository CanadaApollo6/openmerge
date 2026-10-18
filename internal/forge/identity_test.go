package forge

import "testing"

func TestWriteGrantRequiresSameStableID(t *testing.T) {
	for _, permission := range []string{"admin", "maintain", "write", "read", "triage", "none", "", "custom"} {
		for _, id := range []int64{0, -1, 1, 2} {
			grant := WriteGrant{User: UserIdentity{ID: id}, Permission: permission}
			want := id == 1 && (permission == "admin" || permission == "maintain" || permission == "write")
			if grant.CanWrite(1) != want || grant.CanWrite(0) || grant.CanWrite(-1) {
				t.Fatalf("grant %+v authorized wrong identity or permission", grant)
			}
		}
	}
}
