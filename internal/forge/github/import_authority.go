package github

import (
	"context"
	"fmt"
	"net/http"
	"slices"
)

// A nil principal set means unreadable or unrecognized membership, not empty.
func (c *setupAPI) importPrincipals(ctx context.Context, r *ProtectionReport, rules []Ruleset, p *importClassicProtection) {
	if r.principals == nil {
		r.principals = map[string][]string{}
	}
	actors := []BypassActor{}
	for _, rs := range rules {
		if slices.ContainsFunc(rs.Rules, func(rule RepositoryRule) bool { return rule.Type == "update" && strictFenceUpdate(rule) }) {
			for _, a := range rs.BypassActors {
				if writerBypass(a) {
					actors = append(actors, a)
				}
			}
		}
	}
	if p != nil && p.Restrictions != nil {
		for _, a := range p.Restrictions.Teams {
			actors = append(actors, BypassActor{ActorType: "Team", ActorID: new(a.ID)})
		}
	}
	for _, a := range actors {
		key := actorKey(a)
		if _, read := r.principals[key]; read || a.ActorType != "Team" {
			continue
		}
		r.principals[key] = nil
		if a.ActorID == nil {
			continue
		}
		members := []string{}
		complete := true
		for page := 1; ; page++ {
			var entries []classicIdentity
			next, err := c.page(ctx, http.MethodGet, fmt.Sprintf("teams/%d/members?per_page=100&page=%d", *a.ActorID, page), nil, &entries)
			if err != nil || entries == nil {
				complete = false
				break
			}
			for _, entry := range entries {
				if entry.ID <= 0 {
					complete = false
					break
				}
				members = append(members, fmt.Sprintf("User:%d", entry.ID))
			}
			if !complete || !next {
				break
			}
		}
		if complete {
			slices.Sort(members)
			r.principals[key] = slices.Compact(members)
		}
	}
}

func (r *ProtectionReport) actorPrincipals(a BypassActor) ([]string, bool) {
	key := actorKey(a)
	if a.ActorID != nil && (a.ActorType == "User" || a.ActorType == "Integration") {
		return []string{key}, true
	}
	members, ok := r.principals[key]
	return members, ok && members != nil
}

func (r *ProtectionReport) principalIntersection(base string, gates []Ruleset, p *importClassicProtection) {
	var sets []map[string]bool
	unresolved := false
	appendGate := func(actors []BypassActor) {
		members := map[string]bool{}
		for _, a := range actors {
			principals, known := r.actorPrincipals(a)
			if !known {
				unresolved = true
			}
			for _, principal := range principals {
				members[principal] = true
			}
		}
		sets = append(sets, members)
	}
	for _, gate := range gates {
		var actors []BypassActor
		for _, a := range gate.BypassActors {
			if writerBypass(a) {
				actors = append(actors, a)
			}
		}
		appendGate(actors)
	}
	if p != nil && p.Restrictions != nil {
		var actors []BypassActor
		for _, group := range []struct {
			kind    string
			entries []classicIdentity
		}{{"User", p.Restrictions.Users}, {"Team", p.Restrictions.Teams}, {"Integration", p.Restrictions.Apps}} {
			for _, entry := range group.entries {
				actors = append(actors, BypassActor{ActorType: group.kind, ActorID: new(entry.ID)})
			}
		}
		appendGate(actors)
	}
	if unresolved {
		r.add(base, "authority", "unresolved-memberships", "unsupported", true, "Unknown effective authority: team/role membership is unreadable or unrecognized. To verify at switch: resolve effective principals across every ruleset and classic restriction; actor-ID intersection cannot exclude overlapping writers.", "# Resolve overlapping memberships; acceptance cannot waive authority verification.\n")
	}
	if len(sets) == 0 {
		return
	}
	var effective []string
	for principal := range sets[0] {
		if slices.ContainsFunc(sets[1:], func(set map[string]bool) bool { return !set[principal] }) {
			continue
		}
		effective = append(effective, principal)
	}
	slices.Sort(effective)
	for _, principal := range effective {
		if len(principal) < 5 || principal[:5] != "User:" {
			continue
		}
		r.add(base, "authority", "principal-"+principal, "equivalent", true, "Effective principal "+principal+" can pass every applicable ruleset and classic push restriction through resolved memberships. Verify App/break-glass authority and route other writers through the queue.", "# Resolve effective principal "+principal+" before switch; acceptance cannot waive this.\n")
	}
}
