package github

import (
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
	rules := f.setupRules != nil && (path == "repos/acme/project" || strings.HasPrefix(path, "repos/acme/project/rulesets"))
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
		f.json(w, map[string]any{"id": 42, "slug": "openmerge-test", "client_id": "Iv1.test", "pem": f.manifestPEM, "webhook_secret": "setup-webhook-secret", "client_secret": "unused-oauth-secret"})
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
			list = append(list, map[string]any{"id": id, "name": f.setupRules[id]["name"]})
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
	if r.Method != "GET" || f.setupRules[id] == nil {
		f.apiError(w, 404, "Not Found")
	} else {
		f.json(w, f.setupRules[id])
	}
	return true
}
