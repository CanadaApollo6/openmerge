package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/forgetest"
)

func TestAdminVerificationReadOnlyAndClassicFingerprint(t *testing.T) {
	f, e := authorityHTTPEnv(t)
	e.SetTeam(forge.TeamIdentity{ID: 44, OrganizationID: 55, Organization: "acme", Slug: "platform"})
	old := f.override
	classic := false
	unknownPolicy := false
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer operator-only" {
			t.Errorf("non-operator credential: %s", r.Header.Get("Authorization"))
		}
		if r.Method != "GET" {
			t.Fatal("verification wrote GitHub")
		}
		if strings.Contains(r.URL.Path, "/protection") {
			if classic {
				fields := completeClassicFixture()
				fields["required_signatures"] = map[string]any{"enabled": false}
				fields["enforce_admins"] = map[string]any{"enabled": true}
				fields["required_linear_history"] = map[string]any{"enabled": true}
				fields["url"] = "https://ignored.example/protection"
				if unknownPolicy {
					fields["future_policy"] = map[string]any{"enabled": true}
				}
				f.json(w, fields)
			} else {
				f.apiError(w, 404, "Branch not protected")
			}
			return true
		}
		return old(w, r)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !f.override(w, r) {
			t.Errorf("unexpected verification read %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	reader, err := newAdminWriteRestrictionReader("operator-only", server.URL+"/api/v3/")
	must(t, err)
	input := forgetest.WriteRestrictionInput()
	first, err := reader.ObserveWriteRestriction(t.Context(), input)
	must(t, err)
	first, err = forge.InterpretWriteRestriction(input, first)
	must(t, err)
	fp, err := first.Fingerprint()
	must(t, err)
	classic = true
	second, err := reader.ObserveWriteRestriction(t.Context(), input)
	must(t, err)
	second, err = forge.InterpretWriteRestriction(input, second)
	must(t, err)
	fp2, err := second.Fingerprint()
	must(t, err)
	if fp == fp2 || len(second.Protections) <= len(first.Protections) {
		t.Fatal("classic policy missing from fingerprint")
	}
	unknownPolicy = true
	third, err := reader.ObserveWriteRestriction(t.Context(), input)
	must(t, err)
	third, err = forge.InterpretWriteRestriction(input, third)
	must(t, err)
	fp3, err := third.Fingerprint()
	must(t, err)
	if fp2 == fp3 {
		t.Fatal("shared classic read discarded unknown policy fields")
	}
	transport := readOnlyTransport{}
	req, _ := http.NewRequest(http.MethodPut, f.server.URL, nil)
	if _, err := transport.RoundTrip(req); err == nil {
		t.Fatal("admin reader accepted writes")
	}
}

func TestClassicHiddenBypassCannotBindDraft(t *testing.T) {
	for _, raw := range []string{
		`{"required_pull_request_reviews":{}}`,
		`{"required_pull_request_reviews":{"bypass_pull_request_allowances":null}}`,
		`{"restrictions":{"users":[],"teams":[],"apps":null}}`,
	} {
		var body map[string]any
		must(t, json.Unmarshal([]byte(raw), &body))
		actors, visibility := classicBypassActors(body)
		if visibility != authority.Hidden || len(actors) != 0 {
			t.Fatal("hidden classic bypass claimed complete visibility")
		}
	}
}

func TestAuthorityInventorySharesCompleteClassicRead(t *testing.T) {
	for _, kind := range []string{"partial", "null signatures", "missing signatures", "bad signatures", "generic 404", "unprotected"} {
		t.Run(kind, func(t *testing.T) {
			f := rulesAPI(t)
			signatureReads := 0
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if strings.HasSuffix(r.URL.Path, "/required_signatures") {
					signatureReads++
					if kind == "bad signatures" {
						f.json(w, map[string]any{})
					} else {
						f.json(w, map[string]any{"enabled": false})
					}
					return true
				}
				if strings.HasSuffix(r.URL.Path, "/protection") {
					fields := completeClassicFixture()
					switch kind {
					case "partial":
						delete(fields, "restrictions")
					case "null signatures":
						fields["required_signatures"] = nil
					case "generic 404":
						f.apiError(w, 404, "Not Found")
						return true
					case "unprotected":
						f.apiError(w, 404, "Branch not protected")
						return true
					}
					f.json(w, fields)
					return true
				}
				return false
			}
			api, err := setupClient(f.server.URL+"/api/v3/", "secret-installation-classic-read")
			must(t, err)
			in := forgetest.WriteRestrictionInput()
			in.ExpectedRefs = []string{"refs/heads/main"}
			o, err := observeClassicInventory(context.Background(), api, in, forge.EmptyWriteRestrictionObservation())
			if kind == "missing signatures" || kind == "unprotected" {
				must(t, err)
				if kind == "missing signatures" && (signatureReads != 1 || len(o.Protections) != 1) {
					t.Fatal("incomplete signatures inventory")
				}
			} else if err == nil || o.Inventory != authority.ReadFailure {
				t.Fatal("partial classic read certified", o, err)
			}
			if kind == "null signatures" && signatureReads != 0 {
				t.Fatal("null signatures treated as omitted")
			}
		})
	}
}
