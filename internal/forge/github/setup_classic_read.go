package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// readClassicProtection shares the complete wire read between rollback preflight
// and authority inventory, preserving unknown fields for the latter's fingerprint.
func (c *setupAPI) readClassicProtection(ctx context.Context, repo, ref string) (map[string]json.RawMessage, error) {
	endpoint := "repos/" + repo + "/branches/" + url.PathEscape(ref) + "/protection"
	var raw json.RawMessage
	if err := c.request(ctx, http.MethodGet, endpoint, nil, &raw); err != nil {
		var api *setupAPIError
		if errors.As(err, &api) && api.status.StatusCode == 404 && api.message == "Branch not protected" {
			return nil, nil
		}
		return nil, err
	}
	if err := completeClassicProtection(raw); err != nil {
		return nil, fmt.Errorf("incomplete classic protection response: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if _, present := fields["required_signatures"]; !present {
		var signatures json.RawMessage
		if err := c.request(ctx, http.MethodGet, endpoint+"/required_signatures", nil, &signatures); err != nil {
			return nil, err
		}
		if err := classicReadEnabled(signatures); err != nil {
			return nil, errors.New("incomplete required-signatures response")
		}
		fields["required_signatures"] = signatures
	}
	return fields, nil
}

// Validate the wire values before go-github can replace absent/null fields with
// permissive zero values. Only explicit null disables nullable protection kinds.
// Signatures alone may be omitted: setupClassicProtection reads their endpoint.
func completeClassicProtection(raw json.RawMessage) error {
	fields, err := classicReadObject(raw)
	if err != nil {
		return err
	}
	for _, key := range []string{"required_linear_history", "allow_force_pushes", "lock_branch"} {
		if err := classicReadEnabled(fields[key]); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	if signatures, ok := fields["required_signatures"]; ok {
		if err := classicReadEnabled(signatures); err != nil {
			return fmt.Errorf("required_signatures: %w", err)
		}
	}
	for _, key := range []string{"required_status_checks", "required_pull_request_reviews", "restrictions"} {
		value, present := fields[key]
		if !present {
			return fmt.Errorf("missing %s", key)
		}
		if string(value) == "null" {
			continue
		}
		object, err := classicReadObject(value)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		switch key {
		case "required_status_checks":
			var strict *bool
			var contexts *[]json.RawMessage
			var checks *[]json.RawMessage
			if json.Unmarshal(object["strict"], &strict) != nil || strict == nil ||
				json.Unmarshal(object["contexts"], &contexts) != nil || contexts == nil ||
				json.Unmarshal(object["checks"], &checks) != nil || checks == nil {
				return fmt.Errorf("%s: require strict boolean and contexts/checks arrays", key)
			}
			for _, context := range *contexts {
				var name *string
				if json.Unmarshal(context, &name) != nil || name == nil {
					return fmt.Errorf("%s: incomplete context", key)
				}
			}
			for _, check := range *checks {
				var item struct {
					Context *string `json:"context"`
				}
				if json.Unmarshal(check, &item) != nil || item.Context == nil {
					return fmt.Errorf("%s: incomplete check", key)
				}
			}
		case "required_pull_request_reviews":
			allowances, err := classicReadObject(object["bypass_pull_request_allowances"])
			if err != nil {
				return fmt.Errorf("%s.bypass_pull_request_allowances: %w", key, err)
			}
			if err := classicReadApps(allowances["apps"]); err != nil {
				return fmt.Errorf("%s.bypass_pull_request_allowances.apps: %w", key, err)
			}
		case "restrictions":
			if err := classicReadApps(object["apps"]); err != nil {
				return fmt.Errorf("%s.apps: %w", key, err)
			}
		}
	}
	return nil
}

func classicReadObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, fmt.Errorf("expected object")
	}
	return object, nil
}

func classicReadEnabled(raw json.RawMessage) error {
	var object struct {
		Enabled *bool `json:"enabled"`
	}
	if json.Unmarshal(raw, &object) != nil || object.Enabled == nil {
		return fmt.Errorf("expected enabled boolean")
	}
	return nil
}

func classicReadApps(raw json.RawMessage) error {
	var apps *[]*struct {
		ID *int64 `json:"id"`
	}
	if json.Unmarshal(raw, &apps) != nil || apps == nil {
		return fmt.Errorf("expected App array")
	}
	for _, app := range *apps {
		if app == nil || app.ID == nil || *app.ID <= 0 {
			return fmt.Errorf("expected positive App ID")
		}
	}
	return nil
}
