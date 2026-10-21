package github

import (
	"encoding/base64"
	"errors"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"maps"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestAuthorityAcquisitionOperatorProtection(t *testing.T) {
	f := rulesAPI(t)
	c, err := setupClient(f.server.URL+"/api/v3/", "setup-admin-secret")
	must(t, err)
	_, err = c.operatorProtection(t.Context(), "acme/project", []string{"refs/heads/main"})
	if err != nil {
		t.Fatalf("%v: %v", err, errors.Unwrap(err))
	}
}

func TestAuthoritySetupAcquiresLandedEvidenceBeforeMutation(t *testing.T) {
	for _, failure := range []string{"invalid-config", "missing-owners", "unresolved-owners", "incomplete-tree", "missing-completeness", "wrong-object", "oversize", "hidden-protection", "read-secret", "none"} {
		t.Run(failure, func(t *testing.T) {
			f := rulesAPI(t)
			opts := authorityOptions(t)
			reads := 0
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				path := r.URL.Path
				if strings.Contains(path, "/git/trees/") {
					reads++
					if failure == "missing-completeness" {
						f.json(w, map[string]any{"tree": []any{}})
						return true
					}
					if failure == "incomplete-tree" {
						f.json(w, map[string]any{"truncated": true, "tree": []any{}})
						return true
					}
					if failure == "wrong-object" {
						f.json(w, map[string]any{"truncated": false, "tree": []any{map[string]string{"path": ".openmerge.yml", "type": "blob", "mode": "100644", "sha": "caller-value"}}})
						return true
					}
				}
				if strings.HasSuffix(path, "/git/blobs/"+strings.Repeat("b", 40)) && failure == "invalid-config" {
					f.json(w, map[string]any{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte("version: broken"))})
					return true
				}
				if strings.HasSuffix(path, "/git/blobs/"+strings.Repeat("c", 40)) {
					content := ""
					switch failure {
					case "missing-owners":
						content = "/README.md @writer\n"
					case "unresolved-owners":
						content = "* @missing\n"
					case "oversize":
						f.json(w, map[string]any{"size": forge.MaxGoverningBytes + 1, "encoding": "base64", "content": ""})
						return true
					case "read-secret":
						f.apiError(w, 503, "ghs_SECRET")
						return true
					}
					if content != "" {
						f.json(w, map[string]any{"encoding": "base64", "size": len(content), "content": base64.StdEncoding.EncodeToString([]byte(content))})
						return true
					}
				}
				if path == "/api/v3/users/missing" {
					f.apiError(w, 404, "not found")
					return true
				}
				// Legacy setup's rules() accepts this shape, but the typed reader must not.
				if strings.Contains(path, "/rulesets/") && failure == "hidden-protection" {
					raw := maps.Clone(f.setupRules[10])
					delete(raw, "rules")
					f.json(w, raw)
					return true
				}
				return false
			}
			out, err := runAuthority(t, f, opts)
			if failure == "none" {
				must(t, err)
				if reads == 0 || f.setupWrites != 2 {
					t.Fatal("landed acquisition bypassed")
				}
				return
			}
			if err == nil || f.setupWrites != 0 || strings.Contains(err.Error(), "ghs_SECRET") || strings.Contains(out, "ghs_SECRET") {
				t.Fatalf("failure did not prevent mutation safely: writes=%d err=%v", f.setupWrites, err)
			}
			if _, err := os.Stat(opts.Backup); !os.IsNotExist(err) {
				t.Fatal("failed acquisition wrote recovery artifact")
			}
		})
	}
}

func TestAuthoritySetupCannotNarrowLandedProtectionEvidence(t *testing.T) {
	f := rulesAPI(t)
	opts := authorityOptions(t) // Proposed file serves only main.
	releaseRead := false
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case strings.HasSuffix(r.URL.Path, "/git/blobs/"+strings.Repeat("b", 40)):
			content := "version: 1\nqueues:\n- name: main\n  merge_when: ['check-success = ci']\n- name: releases\n  base: release/**\n  merge_when: ['check-success = ci']\n"
			f.json(w, map[string]any{"size": len(content), "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(content))})
			return true
		case strings.HasSuffix(r.URL.Path, "/branches"):
			f.json(w, []any{map[string]any{"name": "main", "commit": map[string]string{"sha": strings.Repeat("a", 40)}}, map[string]any{"name": "release/v1", "commit": map[string]string{"sha": strings.Repeat("d", 40)}}})
			return true
		case strings.Contains(r.URL.Path, "/branches/release/v1/protection"):
			releaseRead = true
			f.apiError(w, 403, "ghs_SECRET")
			return true
		}
		return false
	}
	_, err := runAuthority(t, f, opts)
	if err == nil || !releaseRead || f.setupWrites != 0 || strings.Contains(err.Error(), "ghs_SECRET") {
		t.Fatal("proposed config narrowed governing protection evidence", err, releaseRead, f.setupWrites)
	}
}

func TestAuthorityVerifyAcquiresServerGoverningCommit(t *testing.T) {
	f := rulesAPI(t)
	reader, err := newAdminWriteRestrictionReader("setup-admin-secret", f.server.URL+"/api/v3/")
	must(t, err)
	must(t, reader.AcquireGoverning(t.Context(), "acme/project"))
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/git/ref/heads/main") {
			f.json(w, map[string]any{"object": map[string]string{"sha": "caller-supplied-tip", "type": "commit"}})
			return true
		}
		return false
	}
	if err := reader.AcquireGoverning(t.Context(), "acme/project"); err == nil {
		t.Fatal("verify accepted unresolved governing commit")
	}
}
