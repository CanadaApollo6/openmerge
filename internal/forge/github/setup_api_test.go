package github

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// Extend the existing GitHub fake with independent wire-schema objects. List
// responses deliberately omit policy details and use short pages.
func (f *apiFixture) handleSetup(w http.ResponseWriter, r *http.Request, path string) bool {
	conversion := strings.HasPrefix(path, "app-manifests/")
	rules := f.setupRules != nil && (path == "repos/acme/project" || strings.HasPrefix(path, "repos/acme/project/rulesets") || r.Header.Get("Authorization") == "Bearer setup-admin-secret" && (strings.HasPrefix(path, "repos/acme/project/branches") || strings.HasPrefix(path, "orgs/") || strings.HasPrefix(path, "repos/acme/project/git/") || strings.HasPrefix(path, "users/") || strings.HasPrefix(path, "repos/acme/project/collaborators/")))
	if !conversion && !rules {
		return false
	}
	if conversion {
		if r.Header.Get("Authorization") != "" {
			f.t.Error("manifest exchange must not send credentials")
		}
	} else if r.Header.Get("Authorization") != "Bearer setup-admin-secret" {
		f.t.Error("ruleset request missing admin token")
		f.apiError(w, 401, "Bad credentials")
		return true
	}
	if f.override != nil && f.override(w, r) {
		return true
	}
	if conversion {
		if path != "app-manifests/one-use-code/conversions" || r.Method != "POST" {
			f.apiError(w, 404, "Not Found")
			return true
		}
		w.WriteHeader(201)
		// GitHub returns a null webhook secret for an App created without a hook.
		var secret any = "setup-webhook-secret"
		if f.noHookSecret {
			secret = nil
		}
		f.json(w, map[string]any{"id": 42, "slug": "openmerge-test", "client_id": "Iv1.test", "pem": f.manifestPEM, "webhook_secret": secret, "client_secret": "unused-oauth-secret"})
		return true
	}
	if path == "repos/acme/project/branches" {
		f.json(w, []any{map[string]any{"name": "main", "commit": map[string]string{"sha": strings.Repeat("a", 40)}}})
		return true
	}
	if path == "repos/acme/project/git/ref/heads/main" {
		f.json(w, map[string]any{"object": map[string]string{"sha": strings.Repeat("a", 40), "type": "commit"}})
		return true
	}
	if path == "repos/acme/project/git/trees/"+strings.Repeat("a", 40) {
		f.json(w, map[string]any{"truncated": false, "tree": []any{
			map[string]any{"path": ".openmerge.yml", "type": "blob", "mode": "100644", "sha": strings.Repeat("b", 40)},
			map[string]any{"path": "CODEOWNERS", "type": "blob", "mode": "100644", "sha": strings.Repeat("c", 40)},
		}})
		return true
	}
	if strings.HasPrefix(path, "repos/acme/project/git/blobs/") {
		content := "version: 1\nqueues: [{name: main, merge_when: ['check-success = ci']}]\n"
		if strings.HasSuffix(path, strings.Repeat("c", 40)) {
			content = "* @writer\n"
		}
		f.json(w, map[string]any{"encoding": "base64", "size": len(content), "content": base64.StdEncoding.EncodeToString([]byte(content))})
		return true
	}
	if path == "users/writer" {
		f.json(w, map[string]any{"id": 21, "login": "writer", "type": "User"})
		return true
	}
	if path == "repos/acme/project/collaborators/writer/permission" {
		f.json(w, map[string]any{"permission": "write", "user": map[string]any{"id": 21, "login": "writer", "type": "User"}})
		return true
	}
	if strings.HasPrefix(path, "repos/acme/project/branches/") && strings.HasSuffix(path, "/protection") {
		f.apiError(w, 404, "Branch not protected")
		return true
	}
	if path == "orgs/acme/teams/owners" {
		f.json(w, map[string]any{"id": 98, "slug": "owners", "organization": map[string]any{"id": 55, "login": "acme"}})
		return true
	}
	if path == "repos/acme/project" {
		f.json(w, map[string]any{"default_branch": "main", "permissions": map[string]bool{"admin": true}})
		return true
	}
	if path == "repos/acme/project/rulesets" && r.Method == "GET" {
		if r.URL.Query().Get("includes_parents") != "true" {
			f.t.Error("ruleset inspection must include inherited policies")
		}
		ids := make([]int64, 0, len(f.setupRules))
		for id := range f.setupRules {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		list := []map[string]any{}
		for _, id := range ids {
			list = append(list, map[string]any{"id": id, "name": f.setupRules[id]["name"], "source_type": f.setupRules[id]["source_type"], "source": f.setupRules[id]["source"], "enforcement": f.setupRules[id]["enforcement"], "target": f.setupRules[id]["target"]})
		}
		f.json(w, paginate(f, r, w, list))
		return true
	}
	if r.Method == "POST" || r.Method == "PUT" {
		var patch map[string]any
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			f.t.Error(err)
			f.apiError(w, 422, "Invalid JSON")
			return true
		}
		if rawRules, ok := patch["rules"].([]any); ok {
			for _, raw := range rawRules {
				rule := raw.(map[string]any)
				if rule["type"] == "update" {
					params, ok := rule["parameters"].(map[string]any)
					if !ok || params["update_allows_fetch_and_merge"] != false {
						f.t.Error("fence update must explicitly forbid fetch-and-merge")
					}
					// Like GitHub, store no parameters when only the false default
					// was sent; validation must accept the omitted default.
					if len(params) == 1 {
						delete(rule, "parameters")
					}
				}
			}
		}
		id := f.id()
		if r.Method == "PUT" {
			id, _ = strconv.ParseInt(strings.TrimPrefix(path, "repos/acme/project/rulesets/"), 10, 64)
			if f.setupRules[id] == nil {
				f.apiError(w, 404, "Not Found")
				return true
			}
		} else {
			f.setupRules[id] = map[string]any{"id": id, "source_type": "Repository", "source": "acme/project"}
		}
		for key, value := range patch {
			f.setupRules[id][key] = value
		}
		f.setupWrites++
		if r.Method == "POST" {
			w.WriteHeader(201)
		}
		f.json(w, f.setupRules[id])
		return true
	}
	id, _ := strconv.ParseInt(strings.TrimPrefix(path, "repos/acme/project/rulesets/"), 10, 64)
	if r.Method == "DELETE" && f.setupRules[id] != nil {
		delete(f.setupRules, id)
		f.setupWrites++
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	if r.Method != "GET" || f.setupRules[id] == nil {
		f.apiError(w, 404, "Not Found")
	} else {
		f.json(w, f.setupRules[id])
	}
	return true
}
