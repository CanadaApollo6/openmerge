package classification

import (
	"slices"
	"strings"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// OwnerResolver closes over fresh PR 3a IdentityReader observations, including
// complete team membership. It performs no I/O during classification. IDs use
// the same forge-scoped spelling as policy actors. Missing/stale observations
// must return an unresolved IdentitySet; email owners are never passed to it.
type OwnerResolver func(Owner) cp.IdentitySet

// TreeDeriver derives only from supplied default-branch governing probes.
// Resolve is injected; nil means no user/team identity observations are known.
type TreeDeriver struct{ Resolve OwnerResolver }

// Generated computes the attribute half, without host or head attribute reads.
func (TreeDeriver) Generated(p string, g forge.GoverningFiles) cp.Truth {
	return generatedAttribute(p, g)
}

// Owners emits exactly one code row per changed path/side and a separate row
// per policy path. Policy membership ignores emails when a real pool exists;
// code-owner membership requires the whole effective entry to resolve.
func (d TreeDeriver) Owners(c *cp.Classification, g forge.GoverningFiles) {
	source, known := selectedOwners(g)
	parsed := parseOwners(source.Content)
	known = known && parsed.valid
	c.CodeOwners = nil
	c.PolicyOwners = nil
	c.CodeOwnersKnown = c.InventoryComplete && known
	c.PolicyOwnersKnown = c.InventoryComplete && known
	diagnostic := func(label, why string) {
		c.Reasons = append(c.Reasons, cp.LabelReasons{Label: label, Reasons: []string{why}})
	}
	for _, why := range parsed.diagnostics {
		c.Facts["unknown"] = cp.True
		diagnostic("unknown", why)
	}
	for _, f := range g.Attributes {
		if !definite(f) {
			diagnostic("generated", f.Path+": incomplete attribute probe")
			continue
		}
		if f.Present {
			for _, why := range parseAttributes(f.Content).diagnostics {
				c.Facts["unknown"] = cp.True
				diagnostic("generated", f.Path+": "+why)
			}
		}
	}
	for _, token := range parsed.tokens {
		value := cp.Unknown
		if known && c.InventoryComplete {
			value = cp.False
		}
		c.Facts[token.Label()] = value
		for i := range c.Files {
			fileValue := cp.Unknown
			if known {
				fileValue = cp.False
			}
			c.Files[i].Facts[token.Label()] = fileValue
		}
	}
	cache := map[Owner]cp.IdentitySet{}
	resolve := func(owner Owner) cp.IdentitySet {
		if pool, ok := cache[owner]; ok {
			return pool
		}
		pool := cp.IdentitySet{Reason: "owner identity or team membership unavailable: " + owner.Name}
		if d.Resolve != nil {
			pool = d.Resolve(owner)
			pool.IDs = slices.Clone(pool.IDs)
		}
		validIDs := slices.DeleteFunc(slices.Clone(pool.IDs), func(id string) bool { return strings.TrimSpace(id) == "" })
		if len(validIDs) != len(pool.IDs) || len(validIDs) == 0 {
			pool.Resolved = false
			pool.Fresh = false
		}
		pool.IDs = validIDs
		cache[owner] = pool
		return pool
	}
	seenPolicy := map[string]bool{}
	for i := range c.Files {
		f := &c.Files[i]
		sides := []cp.CodeOwner{{Path: f.NewPath, Side: cp.NewSide}}
		if f.NewPath == "" {
			sides = []cp.CodeOwner{{Path: f.OldPath, Side: cp.OldSide}}
		} else if f.Renamed {
			sides = append(sides, cp.CodeOwner{Path: f.OldPath, Side: cp.OldSide})
		}
		for _, row := range sides {
			pool := cp.IdentitySet{Resolved: known, Fresh: known}
			entry := parsed.effective(row.Path)
			switch {
			case !known:
				row.Status = cp.OwnersUnresolved
				pool.Reason = "CODEOWNERS selection, content or probe coverage unknown"
			case entry == nil:
				row.Status = cp.OwnersUncovered
				pool.Reason = "no CODEOWNERS entry covers " + row.Path
			default:
				row.Status = cp.OwnersOwnerless
				pool.Reason = "CODEOWNERS entry intentionally has no owners"
			}
			hasEmail := false
			if entry != nil {
				row.LastMatchingEntry = entry.text
				if len(entry.owners) > 0 || len(entry.diagnostics) > 0 {
					pool.Reason = ""
					if len(entry.diagnostics) > 0 {
						pool.Resolved = false
						pool.Reason = strings.Join(entry.diagnostics, "; ")
					}
					for _, owner := range entry.owners {
						if known {
							c.Facts[owner.Label()] = cp.True
							f.Facts[owner.Label()] = cp.True
						}
						if owner.Kind == "email" {
							hasEmail = true
							continue
						}
						observed := resolve(owner)
						pool.IDs = append(pool.IDs, observed.IDs...)
						if !observed.Resolved || !observed.Fresh {
							pool.Resolved = false
							pool.Fresh = false
							pool.Reason += "; unresolved owner " + owner.Name + ": " + observed.Reason
						}
					}
					slices.Sort(pool.IDs)
					pool.IDs = slices.Compact(pool.IDs)
					if len(pool.IDs) == 0 {
						pool.Resolved = false
						pool.Reason += "; no resolvable owners"
					}
					row.Status = cp.OwnersResolved
					if !pool.Resolved || !pool.Fresh || hasEmail {
						row.Status = cp.OwnersUnresolved
					}
				}
			}
			if !known {
				pool.Resolved = false
				pool.Fresh = false
				row.Status = cp.OwnersUnresolved
			}
			row.Eligible = pool
			if hasEmail {
				row.Eligible.Resolved = false
				row.Eligible.Reason += "; email owners cannot resolve to approvers"
			}
			if row.Status == cp.OwnersUnresolved {
				c.CodeOwnersKnown = false
			}
			c.CodeOwners = append(c.CodeOwners, row)
			if f.Facts["policy"] == cp.True && !seenPolicy[row.Path] {
				seenPolicy[row.Path] = true
				if len(pool.IDs) == 0 {
					pool.Resolved = false
				}
				if !pool.Resolved || !pool.Fresh {
					c.PolicyOwnersKnown = false
				}
				c.PolicyOwners = append(c.PolicyOwners, cp.PolicyOwner{Path: row.Path, Eligible: pool})
			}
		}
	}
}
