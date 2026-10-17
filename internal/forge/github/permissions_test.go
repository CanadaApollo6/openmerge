package github

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestInstallationPermissionsUseActualGrantAndRenewWithToken(t *testing.T) {
	f := newAPI(t)
	grants := map[string]string{"contents": "read", "workflows": "read", "pull_requests": "read", "checks": "read", "actions": "read", "statuses": "", "issues": "", "metadata": ""}
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/access_tokens") {
			return false
		}
		token := f.object("installation_token")
		token["token"], token["expires_at"], token["permissions"] = "secret-installation-permissions", f.clock.Add(time.Hour).Format(time.RFC3339), grants
		w.WriteHeader(201)
		f.json(w, token)
		return true
	}
	problems, warnings, err := f.driver.ValidateInstallationPermissions(t.Context())
	must(t, err)
	if len(problems) != 2 || len(warnings) != 6 {
		t.Fatalf("landing vs other deficits: problems=%v warnings=%v", problems, warnings)
	}
	for _, name := range []string{"contents: write", "workflows: write"} {
		if !strings.Contains(strings.Join(problems, ";"), name) {
			t.Errorf("missing exact grant %s: %v", name, problems)
		}
	}
	for name := range grants {
		grants[name] = "write"
	}
	problems, warnings, err = f.driver.ValidateInstallationPermissions(t.Context())
	must(t, err)
	if len(problems) != 2 || len(warnings) != 6 {
		t.Fatal("grant changed before token renewal")
	}
	f.clock = f.clock.Add(56 * time.Minute)
	problems, warnings, err = f.driver.ValidateInstallationPermissions(t.Context())
	must(t, err)
	if len(problems)+len(warnings) != 0 {
		t.Fatalf("renewed grants not observed: %v %v", problems, warnings)
	}
}

func TestMissingPermissionsResponseIsNotAnImplicitGrant(t *testing.T) {
	f := newAPI(t)
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/access_tokens") {
			t.Errorf("unexpected policy read with missing landing permission: %s", r.URL.Path)
			return false
		}
		token := f.object("installation_token")
		token["token"], token["expires_at"] = "secret-installation-missing", f.clock.Add(time.Hour).Format(time.RFC3339)
		delete(token, "permissions")
		w.WriteHeader(201)
		f.json(w, token)
		return true
	}
	problems, warnings, err := f.driver.ValidateInstallationPermissions(t.Context())
	must(t, err)
	if len(problems) != 2 || len(warnings) != 6 {
		t.Fatalf("missing grant response silently accepted: %v %v", problems, warnings)
	}
}
