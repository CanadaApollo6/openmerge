package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
)

type importReviews struct {
	Count     int             `json:"required_approving_review_count"`
	Stale     bool            `json:"dismiss_stale_reviews"`
	Owners    bool            `json:"require_code_owner_reviews"`
	LastPush  bool            `json:"require_last_push_approval"`
	Threads   bool            `json:"required_review_thread_resolution"`
	Dismissal json.RawMessage `json:"dismissal_restrictions"`
	Bypass    *classicActors  `json:"bypass_pull_request_allowances"`
}
type classicIdentity struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Slug  string `json:"slug"`
}
type classicActors struct {
	Users []classicIdentity `json:"users"`
	Teams []classicIdentity `json:"teams"`
	Apps  []classicIdentity `json:"apps"`
}
type importRequiredCheck struct {
	Context       string `json:"context"`
	AppID         *int64 `json:"app_id"`
	IntegrationID *int64 `json:"integration_id"`
}
type importStatusChecks struct {
	Unhandled    map[string]json.RawMessage `json:"-"`
	Strict       bool                       `json:"strict"`
	StrictPolicy bool                       `json:"strict_required_status_checks_policy"`
	Contexts     []string                   `json:"contexts"`
	Checks       []importRequiredCheck      `json:"checks"`
	Required     []importRequiredCheck      `json:"required_status_checks"`
}
type importToggle struct {
	Enabled bool `json:"enabled"`
}
type importClassicProtection struct {
	Unhandled    map[string]json.RawMessage `json:"-"`
	Reviews      *importReviews             `json:"required_pull_request_reviews"`
	Checks       *importStatusChecks        `json:"required_status_checks"`
	Threads      importToggle               `json:"required_conversation_resolution"`
	Signatures   *importToggle              `json:"required_signatures"`
	Linear       importToggle               `json:"required_linear_history"`
	Lock         importToggle               `json:"lock_branch"`
	ForcePushes  importToggle               `json:"allow_force_pushes"`
	Deletions    importToggle               `json:"allow_deletions"`
	Creations    importToggle               `json:"block_creations"`
	Admins       importToggle               `json:"enforce_admins"`
	Restrictions *classicActors             `json:"restrictions"`
}

// Preserve unknown API fields before typed decoding can discard them. URL fields
// are response metadata; every other unhandled field remains a named policy gap.
func importUnknownFields(data []byte, value any, metadata ...string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	typ := reflect.TypeOf(value)
	for i := 0; i < typ.NumField(); i++ {
		delete(fields, strings.Split(typ.Field(i).Tag.Get("json"), ",")[0])
	}
	for _, key := range metadata {
		delete(fields, key)
	}
	return fields, nil
}

func (p *importStatusChecks) UnmarshalJSON(data []byte) error {
	type plain importStatusChecks
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	unknown, err := importUnknownFields(data, decoded, "url", "contexts_url")
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if unknown == nil {
		unknown = map[string]json.RawMessage{}
	}
	for _, key := range []string{"checks", "required_status_checks"} {
		var checks []json.RawMessage
		if raw := fields[key]; raw != nil {
			if err := json.Unmarshal(raw, &checks); err != nil {
				return err
			}
		}
		for i, raw := range checks {
			fields, err := importUnknownFields(raw, importRequiredCheck{})
			if err != nil {
				return err
			}
			for field, value := range fields {
				unknown[fmt.Sprintf("%s[%d].%s", key, i, field)] = value
			}
		}
	}
	decoded.Unhandled = unknown
	*p = importStatusChecks(decoded)
	return nil
}

func (p *importClassicProtection) UnmarshalJSON(data []byte) error {
	type plain importClassicProtection
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	unknown, err := importUnknownFields(data, decoded, "url")
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if unknown == nil {
		unknown = map[string]json.RawMessage{}
	}
	for key, raw := range fields {
		var shape any
		switch key {
		case "required_pull_request_reviews":
			shape = importReviews{}
		case "required_conversation_resolution", "required_signatures", "required_linear_history", "lock_branch", "allow_force_pushes", "allow_deletions", "block_creations", "enforce_admins":
			shape = importToggle{}
		default:
			continue
		}
		fields, err := importUnknownFields(raw, shape, "url")
		if err != nil {
			return err
		}
		for field, value := range fields {
			unknown[key+"."+field] = value
		}
	}
	decoded.Unhandled = unknown
	*p = importClassicProtection(decoded)
	return nil
}

func (r *ProtectionReport) unhandledFields(base, source, prefix string, fields map[string]json.RawMessage) {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		r.unsupported(base, source, prefix+key, "Unhandled "+prefix+key+"="+string(fields[key])+"; no faithful translation.")
	}
}

func (c *setupAPI) importClassic(ctx context.Context, repo, base string) (*importClassicProtection, error) {
	var p importClassicProtection
	endpoint := "repos/" + repo + "/branches/" + url.PathEscape(base) + "/protection"
	err := c.request(ctx, http.MethodGet, endpoint, nil, &p)
	if err != nil {
		var apiErr *setupAPIError
		if errors.As(err, &apiErr) && apiErr.status.StatusCode == 404 && apiErr.message == "Branch not protected" {
			return nil, nil
		}
		return nil, c.importError(err, "Administration read for classic branch protection on "+base)
	}
	if p.Signatures == nil {
		var sig importToggle
		if err := c.importGet(ctx, endpoint+"/required_signatures", "Administration read for required signatures", &sig); err != nil {
			return nil, err
		}
		p.Signatures = &sig
	}
	return &p, nil
}

func (r *ProtectionReport) reviews(base, source string, p importReviews, writers []string) {
	if r.queueDrafts == nil {
		r.queueDrafts = map[string]importQueueDraft{}
	}
	draft := r.queueDrafts[base]
	draft.Count = max(draft.Count, p.Count)
	draft.PullRequests = true
	draft.Owners = draft.Owners || p.Owners
	draft.Writers = writers
	r.queueDrafts[base] = draft
	r.add(base, source, "pull-requests", "equivalent", false, "Keep pull-request submission required. Approval policy uses eligible, distinct reviewers, excludes author/submitter/accountable humans and blocks eligible requests for changes; plain approved-reviews-by is not equivalent.", "# changes.pull_requests: required belongs in this base's reconciled queue fragment.\n")
	if p.Count > 0 {
		class, message := "stricter", "Head-bound approvals are stricter with stale-review dismissal off. The imported group snapshots current writers; future writers must be added."
		if p.Stale {
			class = "weaker"
			message = "GitHub stale-review dismissal can invalidate approval after a merge-base or diff change without a new head. OpenMerge can retain it; explicit acceptance is required. The imported group snapshots current writers."
		}
		r.add(base, source, "reviews", class, false, fmt.Sprintf("Require %d approvals. %s", p.Count, message), "# Approval count is included in the reconciled draft for this base.\n")
		if len(writers) == 0 {
			r.add(base, source, "reviewer-pool", "unsupported", false, "No eligible repository writers were returned; approval counts cannot be satisfied.", "# Resolve the eligible reviewer pool before using the approval draft.")
		}
	}
	if p.Owners {
		class := "equivalent"
		if p.Stale {
			class = "weaker"
		}
		r.add(base, source, "owner-review", class, false, fmt.Sprintf("Stale dismissal enabled=%t: stale owner approvals may survive merge-base/diff changes without a new head; acceptance is required when enabled. Require at least one eligible owner for every changed owned file, using CODEOWNERS at the governing default-branch commit. See the base comparison. Stage A must supply owner requirements; groups alone confer no path ownership.", p.Stale), "# Owner requirement is retained in the reconciled draft for this base.\n")
	}
	if p.LastPush {
		r.add(base, source, "last-push", "unsupported", false, "Last-push approval has no equivalent; excluding the submitter does not exclude a later pusher.", "# No equivalent for approval of the most recent reviewable push.")
	}
	if p.Threads {
		r.unsupported(base, source, "conversation-resolution", "Conversation resolution has no OpenMerge equivalent.")
	}
	if len(p.Dismissal) > 0 && string(p.Dismissal) != "null" && string(p.Dismissal) != "{}" {
		r.unsupported(base, source, "dismissal-restrictions", "Review dismissal actor restrictions have no equivalent in OpenMerge approval policy: "+string(p.Dismissal))
	}
}
func (r *ProtectionReport) unsupported(base, source, key, message string) {
	r.add(base, source, key, "unsupported", false, message, "# Retain for manual review: "+key+"; no equivalent configuration.\n")
}

func (r *ProtectionReport) translateRule(base, source string, rule RepositoryRule, writers []string, evidence checkEvidence, workflows []importWorkflow) error {
	switch rule.Type {
	case "pull_request":
		var params map[string]json.RawMessage
		if err := json.Unmarshal(rule.Parameters, &params); err != nil {
			return err
		}
		if params == nil {
			return fmt.Errorf("missing pull_request parameters")
		}
		var reviews importReviews
		handled := map[string]any{
			"required_approving_review_count":   &reviews.Count,
			"dismiss_stale_reviews_on_push":     &reviews.Stale,
			"require_code_owner_review":         &reviews.Owners,
			"require_last_push_approval":        &reviews.LastPush,
			"required_review_thread_resolution": &reviews.Threads,
		}
		keys := make([]string, 0, len(params))
		for key := range params {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			raw := params[key]
			if target, ok := handled[key]; ok {
				if string(raw) == "null" {
					return fmt.Errorf("null pull_request parameter %s", key)
				}
				if err := json.Unmarshal(raw, target); err != nil {
					return err
				}
				continue
			}
			if key == "dismissal_restriction" {
				// Preserve the complete value, including future nested keys.
				r.unsupported(base, source, "dismissal-restrictions", "Review dismissal_restriction has no OpenMerge equivalent: "+string(raw))
				continue
			}
			reason := "Unhandled pull_request parameter " + key + "=" + string(raw) + "; no faithful translation."
			if key == "required_reviewers" {
				reason += " Per-file required teams use GitHub pattern/identity semantics; ADR 0007 groups do not confer ownership and the importer cannot prove a faithful mapping."
			}
			r.unsupported(base, source, "pull-request-parameter-"+key, reason)
		}
		if reviews.Count < 0 {
			return fmt.Errorf("negative approval count")
		}
		r.reviews(base, source, reviews, writers)
	case "required_status_checks":
		var checks importStatusChecks
		if err := json.Unmarshal(rule.Parameters, &checks); err != nil {
			return err
		}
		r.requiredChecks(base, source, checks, evidence, workflows)
	case "required_deployments":
		r.unsupported(base, source, "deployments", "Required deployments have no OpenMerge equivalent. Parameters: "+string(rule.Parameters))
	case "required_conversation_resolution":
		r.unsupported(base, source, "conversation-resolution", "Conversation resolution has no OpenMerge equivalent.")
	case "update":
		if !strictFenceUpdate(rule) {
			r.unsupported(base, source, "update_allows_fetch_and_merge", "update_allows_fetch_and_merge is enabled or unreadable; this update rule does not prove exclusive base write authority. Parameters: "+string(rule.Parameters))
		} else {
			r.add(base, source, "update", "equivalent", false, "Keep the GitHub update restriction with update_allows_fetch_and_merge=false; bypass inventory is separate from policy translation.", "# Keep GitHub update restrictions; update_allows_fetch_and_merge=false.")
		}
		if len(rule.Parameters) > 0 {
			fields, err := importUnknownFields(rule.Parameters, struct {
				Allow bool `json:"update_allows_fetch_and_merge"`
			}{})
			if err != nil {
				return err
			}
			r.unhandledFields(base, source, "update-parameter-", fields)
		}
	case "creation", "deletion", "non_fast_forward":
		r.add(base, source, rule.Type, "equivalent", false, "Keep write restrictions on GitHub; bypass inventory is separate from policy translation.", "# Keep GitHub "+rule.Type+" restrictions; do not translate into merge_when.")
	default:
		r.unsupported(base, source, rule.Type, "Content/unknown rule "+rule.Type+" parameters="+string(rule.Parameters)+" stays on GitHub and requires rule-aware validation for every generated ref and merge method before switch.")
	}
	return nil
}
func (r *ProtectionReport) translateClassic(base string, p *importClassicProtection, writers []string, evidence checkEvidence, workflows []importWorkflow) error {
	if p == nil {
		return nil
	}
	r.unhandledFields(base, "classic", "classic-field-", p.Unhandled)
	if p.Reviews != nil {
		r.reviews(base, "classic", *p.Reviews, writers)
		r.classicInventory(base, "classic-review-bypass", p.Reviews.Bypass)
	}
	if p.Checks != nil {
		r.requiredChecks(base, "classic", *p.Checks, evidence, workflows)
	}
	if p.Threads.Enabled {
		r.unsupported(base, "classic", "conversation-resolution", "Conversation resolution has no OpenMerge equivalent.")
	}
	if p.Signatures.Enabled {
		r.unsupported(base, "classic", "signatures", "Required signatures need rule-aware validation; generated commits and fence markers are unsigned.")
	}
	if p.Linear.Enabled {
		r.unsupported(base, "classic", "linear-history", "Linear history needs merge-method validation, including contributor merges under fast-forward.")
	}
	for _, toggle := range []struct {
		key   string
		value bool
	}{
		{"allow_force_pushes", p.ForcePushes.Enabled}, {"allow_deletions", p.Deletions.Enabled},
		{"block_creations", p.Creations.Enabled}, {"enforce_admins", p.Admins.Enabled},
	} {
		r.unsupported(base, "classic", toggle.key, fmt.Sprintf("Classic %s enabled=%t must be retained and verified on GitHub for the installed App; no translated policy guarantee.", toggle.key, toggle.value))
	}
	r.add(base, "classic", "lock_branch", "equivalent", p.Lock.Enabled, fmt.Sprintf("Classic lock_branch enabled=%t. A locked branch prevents the App from landing; unlock and verify at switch.", p.Lock.Enabled), "# Verify classic branch locking before switching.\n")
	r.classicInventory(base, "classic-push-restrictions", p.Restrictions)
	return nil
}

func actorKey(a BypassActor) string {
	if a.ActorID == nil {
		return a.ActorType + ":null"
	}
	return fmt.Sprintf("%s:%d", a.ActorType, *a.ActorID)
}
func (r *ProtectionReport) inventoryBypass(base string, rs Ruleset) {
	for _, a := range rs.BypassActors {
		key := actorKey(a)
		r.add(base, fmt.Sprintf("ruleset:%d", rs.ID), "bypass-"+key+"-"+a.BypassMode, "equivalent", false, fmt.Sprintf("Bypass inventory: %s mode=%s; source=%s %s; enforcement=%s; applicability is evaluated separately. Bypassing this ruleset alone grants no authority over other applicable rules.", key, a.BypassMode, rs.SourceType, rs.Source, rs.Enforcement), "# Retain/review bypass actor "+key+"; an Integration/bot cannot be break-glass.")
	}
}
func (r *ProtectionReport) classicInventory(base, source string, actors *classicActors) {
	if actors == nil {
		return
	}
	for _, kind := range []struct {
		name    string
		entries []classicIdentity
	}{{"User", actors.Users}, {"Team", actors.Teams}, {"Integration", actors.Apps}} {
		for _, a := range kind.entries {
			r.add(base, source, fmt.Sprintf("actor-%s-%d", kind.name, a.ID), "equivalent", false, fmt.Sprintf("Classic actor inventory: %s:%d %s %s. Review bypass is not unrestricted push authority.", kind.name, a.ID, a.Login, a.Slug), "# Review this classic allowance when installing write restrictions.")
		}
	}
}

// PR-only bypass still permits a GitHub merge outside OpenMerge's queue.
// The App's atomic push needs always; other writers need only a merge route.
func writerBypass(actor BypassActor) bool {
	return actor.BypassMode == "always" || actor.BypassMode == "pull_request"
}

func (r *ProtectionReport) effectiveWriters(base string, rules []Ruleset, p *importClassicProtection) {
	var gates []Ruleset
	candidates := map[string]BypassActor{}
	for _, rs := range rules {
		if slices.ContainsFunc(rs.Rules, func(rule RepositoryRule) bool { return rule.Type == "update" && strictFenceUpdate(rule) }) {
			gates = append(gates, rs)
			for _, a := range rs.BypassActors {
				if writerBypass(a) {
					candidates[actorKey(a)] = a
				}
			}
		}
	}
	if p != nil && p.Lock.Enabled {
		return
	}
	if len(gates) == 0 {
		r.principalIntersection(base, gates, p)
		if p != nil && p.Restrictions != nil {
			for _, group := range []struct {
				kind    string
				entries []classicIdentity
			}{{"User", p.Restrictions.Users}, {"Team", p.Restrictions.Teams}, {"Integration", p.Restrictions.Apps}} {
				for _, actor := range group.entries {
					key := fmt.Sprintf("%s:%d", group.kind, actor.ID)
					r.add(base, "authority", "writer-"+key, "equivalent", true, "Effective classic push authority: "+key+" is permitted by the classic push restrictions and no ruleset update restriction excludes it. Resolve it as the OpenMerge App or declared team/admin break-glass; route every other writer through the queue. Bots cannot be break-glass.", "# Resolve effective writer "+key+" before switching.")
				}
			}
		} else {
			r.add(base, "authority", "other-writers", "equivalent", true, "Ordinary writers can still merge outside the queue under current protection. Resolve release jobs, version-bump CI, backport bots and human writers by installing exclusive base write restrictions. Review and status rules alone do not restrict who may merge.", "# Install exclusive openmerge-base write restrictions and resolve every other writer.")
		}
		return
	}
	r.principalIntersection(base, gates, p)
	keys := make([]string, 0, len(candidates))
	for key := range candidates {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		actor := candidates[key]
		effective := true
		for _, gate := range gates {
			if !slices.ContainsFunc(gate.BypassActors, func(other BypassActor) bool { return actorKey(other) == key && writerBypass(other) }) {
				effective = false
			}
		}
		if p != nil && p.Restrictions != nil {
			allowed := false
			for _, group := range []struct {
				kind string
				list []classicIdentity
			}{{"Team", p.Restrictions.Teams}, {"Integration", p.Restrictions.Apps}, {"User", p.Restrictions.Users}} {
				for _, entry := range group.list {
					if actor.ActorID != nil && group.kind == actor.ActorType && entry.ID == *actor.ActorID {
						allowed = true
					}
				}
			}
			if !allowed {
				effective = false
			}
		}
		if effective {
			r.add(base, "authority", "writer-"+key, "equivalent", true, "Effective write authority: "+key+" can merge past every applicable update restriction (including PR-only bypass routes). Verify it is the OpenMerge App, or declare exactly one team/repository-admin role as break-glass; route every other writer through the queue. Bots cannot be break-glass.", "# Resolve effective writer "+key+" before switching; acceptance cannot waive this.")
		}
	}
}
