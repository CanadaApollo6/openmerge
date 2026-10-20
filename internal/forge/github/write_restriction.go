package github

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/ascii"
	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

const maxAuthorityItems = 1000

var _ forge.WriteRestrictionReader = (*Driver)(nil)

// ObserveWriteRestriction reads facts through the installation transport. It
// never substitutes a bypass actor or interprets ref coverage. Classic protection
// and installation grants remain independent reads, as in github-mode queues.
func (d *Driver) ObserveWriteRestriction(ctx context.Context, in forge.WriteRestrictionInput) (authority.Observation, error) {
	o := forge.EmptyWriteRestrictionObservation()
	if err := forge.ValidateWriteRestrictionInput(in); err != nil {
		return o, err
	}
	owner, repo, err := splitRepo(in.Repo)
	if err != nil {
		return o, err
	}
	if in.Binding.InstallationID != d.installation || in.AppID != strconv.FormatInt(d.app.id, 10) {
		return o, fmt.Errorf("authority installation or App binding mismatch: %w", forge.ErrInvalidInput)
	}
	if actor := in.Binding.ResolvedActor.RepositoryAdmin; actor != nil && actor.RoleID != "5" {
		return o, fmt.Errorf("unsupported GitHub repository-admin role: %w", forge.ErrIdentityMismatch)
	}
	// GitHub documents the built-in admin repository role as ID 5; unlike
	// custom roles it is not an installation or organization-specific ID.
	// https://github.com/integrations/terraform-provider-github/blob/main/docs/resources/repository_ruleset.md
	ctx = identityContext(ctx)
	info, _, err := d.client.Repositories.Get(ctx, owner, repo)
	if err != nil {
		return o, safeError(err)
	}
	if info.GetID() != in.Binding.RepositoryID || info.GetDefaultBranch() != in.Binding.DefaultBranch {
		return o, errors.New("authority repository identity or default branch changed")
	}
	var failures []error
	seen := map[int64]bool{}
	inventoryComplete := false
	inventoryInvalid := false
	for page := 1; page != 0; {
		var list []authorityRuleset
		response, err := d.readRules(ctx, fmt.Sprintf("repos/%s/rulesets?includes_parents=true&per_page=100&page=%d", in.Repo.Path, page), &list)
		if err != nil {
			failures = append(failures, err)
			break
		}
		if list == nil {
			failures = append(failures, errors.New("missing authority ruleset inventory"))
			break
		}
		invalid := false
		for _, entry := range list {
			if entry.ID <= 0 || seen[entry.ID] || len(seen) >= maxAuthorityItems {
				failures = append(failures, errors.New("invalid, repeated or excessive authority ruleset inventory"))
				invalid = true
				break
			}
			seen[entry.ID] = true
			var detail authorityRuleset
			_, readErr := d.readRules(ctx, fmt.Sprintf("repos/%s/rulesets/%d?includes_parents=true", in.Repo.Path, entry.ID), &detail)
			if readErr == nil && (detail.ID != entry.ID || detail.SourceType != entry.SourceType || !ascii.EqualFold(detail.Source, entry.Source) || detail.Enforcement != entry.Enforcement || entry.Target != "" && detail.Target != entry.Target) {
				readErr = errors.New("authority ruleset changed during detail read")
			}
			if readErr != nil {
				failures = append(failures, readErr)
				// List responses still establish the identity and enforcement of this
				// protection. Preserve that evidence even when its detail is inaccessible.
				detail = entry
				detail.Conditions, detail.Rules, detail.Bypass = nil, nil, nil
			}
			p, decodeErr := detail.protection(in.Repo.Path)
			if decodeErr != nil {
				failures = append(failures, decodeErr)
			}
			if p.ID == "" {
				inventoryInvalid = true
			}
			if p.ID != "" {
				if readErr != nil {
					p.TargetsVisibility, p.RulesVisibility, p.BypassVisibility = readVisibility(readErr), readVisibility(readErr), readVisibility(readErr)
				}
				o.Protections = append(o.Protections, p)
			}
		}
		if invalid {
			break
		}
		if response.NextPage == 0 {
			inventoryComplete = true
			break
		}
		if response.NextPage <= page || response.NextPage > 10 {
			failures = append(failures, errors.New("authority ruleset pagination did not advance or exceeded bound"))
			break
		}
		page = response.NextPage
	}
	if inventoryComplete && !inventoryInvalid {
		o.Inventory = authority.Visible
	}
	// Re-resolve the declared name through IdentityReader, then compare both
	// immutable IDs. A reassigned slug cannot satisfy the reviewed actor binding.
	if err := forge.CheckWriteRestrictionActor(ctx, d, in); err != nil {
		failures = append(failures, err)
		o.Inventory = authority.ReadFailure
	}
	slices.SortFunc(o.Protections, func(a, b authority.Protection) int { return cmp.Compare(a.Source+"/"+a.ID, b.Source+"/"+b.ID) })
	return o, errors.Join(failures...)
}

func readVisibility(err error) authority.Visibility {
	if permissionDenied(err) {
		return authority.Hidden
	}
	return authority.ReadFailure
}

// Raw fields distinguish missing/null from visible empty JSON collections.
// This decoder is separate from legacy Ruleset and ValidateRepositoryRules.
type authorityRuleset struct {
	ID          int64           `json:"id"`
	SourceType  string          `json:"source_type"`
	Source      string          `json:"source"`
	Target      string          `json:"target"`
	Enforcement string          `json:"enforcement"`
	UpdatedAt   string          `json:"updated_at"`
	Conditions  json.RawMessage `json:"conditions"`
	Rules       json.RawMessage `json:"rules"`
	Bypass      json.RawMessage `json:"bypass_actors"`
}

func (r authorityRuleset) protection(repo string) (authority.Protection, error) {
	p := authority.Protection{ID: strconv.FormatInt(r.ID, 10), Revision: r.UpdatedAt, Enforcement: r.Enforcement,
		TargetsVisibility: authority.Hidden, RulesVisibility: authority.Hidden, BypassVisibility: authority.Hidden,
		Includes: []string{}, Excludes: []string{}, Conditions: []authority.Parameter{}, Rules: []authority.Rule{}, BypassActors: []authority.BypassActor{}}
	switch r.SourceType {
	case "Repository":
		if !ascii.EqualFold(r.Source, repo) {
			return authority.Protection{}, errors.New("authority ruleset repository source mismatch")
		}
		p.Source = "repository_ruleset"
	case "Organization", "Enterprise":
		if r.Source == "" {
			return authority.Protection{}, errors.New("missing inherited authority source")
		}
		p.Source = "inherited_ruleset"
	default:
		return authority.Protection{}, errors.New("unknown authority ruleset source")
	}
	switch r.Enforcement {
	case "active", "evaluate", "disabled":
	default:
		return authority.Protection{}, errors.New("unknown authority enforcement")
	}
	var failures []error
	if r.UpdatedAt != "" {
		revision, err := time.Parse(time.RFC3339Nano, r.UpdatedAt)
		if err != nil {
			return authority.Protection{}, errors.New("invalid authority ruleset revision")
		}
		p.Revision = revision.UTC().Format(time.RFC3339Nano)
	}
	switch r.Target {
	case "branch", "tag", "push", "repository":
		p.Target = r.Target
	default:
		p.TargetsVisibility = authority.ReadFailure
		failures = append(failures, errors.New("missing or unknown authority target"))
	}
	if jsonPresent(r.Conditions) {
		var fields map[string]json.RawMessage
		err := json.Unmarshal(r.Conditions, &fields)
		conditions := []authority.Parameter{}
		if err == nil {
			// Ref selectors have dedicated fields; all other condition leaves
			// retain their relative JSON pointer paths.
			delete(fields, "ref_name")
			raw, e := json.Marshal(fields)
			if e == nil {
				conditions, e = authorityParameters(raw)
			}
			err = e
		}
		var refs struct {
			RefName *struct{ Include, Exclude []string } `json:"ref_name"`
		}
		if e := json.Unmarshal(r.Conditions, &refs); err == nil {
			err = e
		}
		if p.Target == "" || (r.Target == "branch" || r.Target == "tag") && refs.RefName == nil {
			err = errors.New("missing authority target")
		}
		if refs.RefName != nil && (refs.RefName.Include == nil || refs.RefName.Exclude == nil || len(refs.RefName.Include) > maxAuthorityItems || len(refs.RefName.Exclude) > maxAuthorityItems) {
			err = errors.New("incomplete authority ref conditions")
		}
		if err != nil {
			p.TargetsVisibility = authority.ReadFailure
			failures = append(failures, err)
		} else {
			p.TargetsVisibility = authority.Visible
			p.Conditions = conditions
			if refs.RefName != nil {
				p.Includes, p.Excludes = refs.RefName.Include, refs.RefName.Exclude
			}
		}
	}
	if jsonPresent(r.Rules) {
		var rules []RepositoryRule
		err := json.Unmarshal(r.Rules, &rules)
		if len(rules) > maxAuthorityItems {
			err = errors.New("too many authority rules")
			rules = nil
		}
		normalized := []authority.Rule{}
		for _, rule := range rules {
			params, e := authorityParameters(rule.Parameters)
			if e != nil {
				err = e
			}
			if rule.Type == "" {
				err = errors.New("missing authority rule kind")
			}
			normalized = append(normalized, authority.Rule{Kind: rule.Type, Parameters: params})
		}
		if err != nil {
			p.RulesVisibility = authority.ReadFailure
			failures = append(failures, err)
		} else {
			p.RulesVisibility, p.Rules = authority.Visible, normalized
		}
	}
	if jsonPresent(r.Bypass) {
		var actors []BypassActor
		err := json.Unmarshal(r.Bypass, &actors)
		if len(actors) > maxAuthorityItems {
			err = errors.New("too many authority bypass actors")
			actors = nil
		}
		normalized := []authority.BypassActor{}
		seen := map[authority.BypassActor]bool{}
		for _, actor := range actors {
			kind := map[string]string{"Integration": "app", "OrganizationAdmin": "organization_admin", "RepositoryRole": "repository_role", "Team": "team", "User": "user", "DeployKey": "deploy_key", "EnterpriseRole": "enterprise_role", "EnterpriseOwner": "enterprise_owner"}[actor.ActorType]
			a := authority.BypassActor{Kind: kind, Mode: actor.BypassMode}
			if kind == "" {
				a.Kind, a.RawKind = "unknown", actor.ActorType
			}
			switch a.Kind {
			case "organization_admin", "deploy_key", "enterprise_owner":
				// GitHub ignores IDs on these class-wide principals.
			case "unknown":
				// Preserve an optional unknown identity, but never silently turn
				// an invalid supplied ID into an absent one.
				if actor.ActorID != nil {
					if *actor.ActorID <= 0 {
						err = errors.New("invalid unknown authority bypass actor identity")
					} else {
						a.ID = strconv.FormatInt(*actor.ActorID, 10)
					}
				}
			case "app", "team", "repository_role", "user", "enterprise_role":
				if actor.ActorID != nil && *actor.ActorID > 0 {
					a.ID = strconv.FormatInt(*actor.ActorID, 10)
				}
			}
			if e := a.Validate(); e != nil {
				err = errors.New("unsupported or incomplete authority bypass actor")
			}
			if seen[a] {
				err = errors.New("repeated authority bypass actor")
			}
			seen[a] = true
			normalized = append(normalized, a)
		}
		if err != nil {
			p.BypassVisibility = authority.ReadFailure
			failures = append(failures, err)
		} else {
			p.BypassVisibility, p.BypassActors = authority.Visible, normalized
		}
	}
	return p, errors.Join(failures...)
}

func jsonPresent(raw json.RawMessage) bool {
	return len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// Parameters retain scalar leaves at relative JSON pointer paths. Scalar arrays
// keep source order; object arrays use indices so nested associations survive.
func authorityParameters(raw json.RawMessage) ([]authority.Parameter, error) {
	result := []authority.Parameter{}
	if !jsonPresent(raw) {
		return result, nil
	}
	var values map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&values); err != nil || values == nil {
		return nil, errors.New("invalid authority parameter object")
	}
	scalar := func(value any) (string, bool) {
		switch value.(type) {
		case string, json.Number, bool, nil:
			// JSON quotes distinguish strings from null, booleans, numbers
			// and the empty-object marker, including in future parameters.
			raw, _ := json.Marshal(value)
			return string(raw), true
		}
		return "", false
	}
	appendParameter := func(path string, values []string) error {
		if len(result) >= maxAuthorityItems {
			return errors.New("too many authority parameters")
		}
		result = append(result, authority.Parameter{Name: path, Values: values})
		return nil
	}
	var walk func(string, any) error
	walk = func(path string, value any) error {
		if text, ok := scalar(value); ok {
			return appendParameter(path, []string{text})
		}
		switch v := value.(type) {
		case map[string]any:
			if len(v) > maxAuthorityItems {
				return errors.New("too many authority parameter fields")
			}
			if len(v) == 0 && path != "" {
				return appendParameter(path, []string{"{}"})
			}
			names := make([]string, 0, len(v))
			for name := range v {
				names = append(names, name)
			}
			slices.Sort(names)
			for _, name := range names {
				if name == "" {
					return errors.New("invalid authority parameter name")
				}
				segment := strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
				if path != "" {
					segment = path + "/" + segment
				}
				if err := walk(segment, v[name]); err != nil {
					return err
				}
			}
		case []any:
			if len(v) > maxAuthorityItems {
				return errors.New("too many authority parameter values")
			}
			texts := make([]string, 0, len(v))
			for _, item := range v {
				if text, ok := scalar(item); ok {
					texts = append(texts, text)
				}
			}
			if len(texts) == len(v) {
				return appendParameter(path, texts)
			}
			for i, item := range v {
				if err := walk(path+"/"+strconv.Itoa(i), item); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk("", values); err != nil {
		return nil, err
	}
	return result, nil
}
