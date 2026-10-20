package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	gh "github.com/google/go-github/v93/github"
)

// AdminWriteRestrictionReader owns only an operator token. It cannot construct
// App JWTs or installation credentials and its transport rejects all writes.
type AdminWriteRestrictionReader struct{ token, api string }

// NewAdminWriteRestrictionReader creates a GET-only operator-token reader.
func NewAdminWriteRestrictionReader(token string) (*AdminWriteRestrictionReader, error) {
	return newAdminWriteRestrictionReader(token, "https://api.github.com/")
}
func newAdminWriteRestrictionReader(token, api string) (*AdminWriteRestrictionReader, error) {
	if token == "" {
		return nil, errors.New("GITHUB_TOKEN requires an admin token with Administration read (organization-owner visibility may be needed)")
	}
	if _, err := baseURL(api); err != nil {
		return nil, err
	}
	return &AdminWriteRestrictionReader{token: token, api: api}, nil
}

type readOnlyTransport struct{ base http.RoundTripper }

func (t readOnlyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet {
		return nil, errors.New("authority verification permits GET only")
	}
	return t.base.RoundTrip(r)
}

// ObserveWriteRestriction acquires the complete admin-visible policy inventory.
func (r *AdminWriteRestrictionReader) ObserveWriteRestriction(ctx context.Context, in forge.WriteRestrictionInput) (authority.Observation, error) {
	empty := forge.EmptyWriteRestrictionObservation()
	if err := forge.ValidateWriteRestrictionInput(in); err != nil {
		return empty, err
	}
	appID, err := strconv.ParseInt(in.AppID, 10, 64)
	if err != nil || appID <= 0 {
		return empty, errors.New("invalid App identity")
	}
	api, err := setupClient(r.api, r.token)
	if err != nil {
		return empty, err
	}
	api.http.Transport = readOnlyTransport{api.http.Transport}
	base := api.base.String()
	client, err := gh.NewClient(gh.WithHTTPClient(api.http), gh.WithURLs(&base, nil))
	if err != nil {
		return empty, err
	}
	driver := &Driver{app: &App{id: appID}, installation: in.Binding.InstallationID, client: client}
	o, err := driver.ObserveWriteRestriction(ctx, in)
	if err != nil {
		return o, err
	}
	return observeClassicInventory(ctx, api, in, o)
}

// ObserveAuthorityInventory extends the PR 2 reader for the dormant runtime
// observer without changing the acquisition-only contract used by old callers.
func (d *Driver) ObserveAuthorityInventory(ctx context.Context, in forge.WriteRestrictionInput) (authority.Observation, error) {
	o, err := d.ObserveWriteRestriction(ctx, in)
	if err != nil {
		return o, err
	}
	base, err := url.Parse(d.client.BaseURL())
	if err != nil {
		return o, err
	}
	api := &setupAPI{base: base, http: d.client.Client()}
	return observeClassicInventory(ctx, api, in, o)
}

func observeClassicInventory(ctx context.Context, api *setupAPI, in forge.WriteRestrictionInput, o authority.Observation) (authority.Observation, error) {
	empty := forge.EmptyWriteRestrictionObservation()
	// Classic protection participates in the fingerprint even when it is not a
	// managed write gate. Normalize every policy leaf (including unknown fields)
	// so changed parameters cannot silently reuse a draft pass.
	for _, ref := range in.ExpectedRefs {
		branch := strings.TrimPrefix(ref, "refs/heads/")
		var fields map[string]json.RawMessage
		endpoint := "repos/" + in.Repo.Path + "/branches/" + url.PathEscape(branch) + "/protection"
		err := api.request(ctx, http.MethodGet, endpoint, nil, &fields)
		if err != nil {
			var response *setupAPIError
			if errors.As(err, &response) && response.status.StatusCode == 404 && response.message == "Branch not protected" {
				continue
			}
			o.Inventory = authority.ReadFailure
			return o, err
		}
		if fields == nil {
			return empty, errors.New("missing classic protection body")
		}
		if raw, ok := fields["required_signatures"]; !ok || !jsonPresent(raw) {
			var signatures json.RawMessage
			if err := api.request(ctx, http.MethodGet, endpoint+"/required_signatures", nil, &signatures); err != nil {
				o.Inventory = authority.ReadFailure
				return o, err
			}
			fields["required_signatures"] = signatures
		}
		encoded, _ := json.Marshal(fields)
		var body map[string]any
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		if err := decoder.Decode(&body); err != nil {
			return empty, errors.New("invalid classic protection inventory")
		}
		// API URLs are location metadata, not policy parameters. All other leaves
		// are retained, without recording arbitrary response bodies on the wire.
		removeProtectionURLs(body)
		raw, err := marshalClassicParameters(body)
		if err != nil {
			return empty, err
		}
		actors, visibility := classicBypassActors(body)
		p := authority.Protection{Source: "classic_protection", ID: branch, Enforcement: "active", Target: "branch", TargetsVisibility: authority.Visible, Includes: []string{ref}, Excludes: []string{}, Conditions: []authority.Parameter{}, RulesVisibility: authority.Visible, Rules: []authority.Rule{{Kind: "classic_inventory", Parameters: raw}}, BypassVisibility: visibility, BypassActors: actors}
		o.Protections = append(o.Protections, p)
	}
	return o, nil
}

func removeProtectionURLs(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			if k == "url" || k == "contexts_url" {
				delete(x, k)
			} else {
				removeProtectionURLs(v)
			}
		}
	case []any:
		for _, v := range x {
			removeProtectionURLs(v)
		}
	}
}

func marshalClassicParameters(body map[string]any) ([]authority.Parameter, error) {
	// authorityParameters uses the same injective scalar/array path convention
	// as PR 2, retaining all classic policy rather than a lossy summary.
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("invalid classic inventory")
	}
	return authorityParameters(raw)
}

// A successful classic GET does not turn a missing/null configured bypass list
// into a visible empty list. The normalized policy remains retained separately.
func classicBypassActors(body map[string]any) ([]authority.BypassActor, authority.Visibility) {
	actors := []authority.BypassActor{}
	for _, field := range []string{"restrictions", "required_pull_request_reviews"} {
		value := body[field]
		if value == nil {
			continue
		}
		object, ok := value.(map[string]any)
		if !ok {
			return []authority.BypassActor{}, authority.Hidden
		}
		mode := "always"
		if field == "required_pull_request_reviews" {
			mode = "pull_request"
			object, ok = object["bypass_pull_request_allowances"].(map[string]any)
			if !ok {
				return []authority.BypassActor{}, authority.Hidden
			}
		}
		for _, group := range []struct{ field, kind string }{{"users", "user"}, {"teams", "team"}, {"apps", "app"}} {
			entries, ok := object[group.field].([]any)
			if !ok {
				return []authority.BypassActor{}, authority.Hidden
			}
			for _, entry := range entries {
				actor, ok := entry.(map[string]any)
				if !ok {
					return []authority.BypassActor{}, authority.Hidden
				}
				id, ok := actor["id"].(json.Number)
				if !ok {
					return []authority.BypassActor{}, authority.Hidden
				}
				number, err := strconv.ParseInt(id.String(), 10, 64)
				if err != nil || number <= 0 {
					return []authority.BypassActor{}, authority.Hidden
				}
				a := authority.BypassActor{Kind: group.kind, ID: id.String(), Mode: mode}
				if !slices.Contains(actors, a) {
					actors = append(actors, a)
				}
			}
		}
	}
	return actors, authority.Visible
}
