package classification

import (
	"encoding/json"
	"strings"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func cloneGoverning(g forge.GoverningFiles) forge.GoverningFiles {
	raw, _ := json.Marshal(g)
	var clone forge.GoverningFiles
	_ = json.Unmarshal(raw, &clone)
	return clone
}

// Serialize before invoking Owners, so aliased mutations cannot alter the
// comparison baseline. Only ownership and diagnostic additions are permitted.
func protectedClassification(c cp.Classification) string {
	c.CodeOwners, c.PolicyOwners = nil, nil
	c.CodeOwnersKnown, c.PolicyOwnersKnown = false, false
	facts := func(f map[string]cp.Truth, top bool) map[string]cp.Truth {
		out := map[string]cp.Truth{}
		for k, v := range f {
			if !strings.HasPrefix(k, "owned:") && (!top || k != "unknown") {
				out[k] = v
			}
		}
		return out
	}
	reasons := func(rs []cp.LabelReasons) []cp.LabelReasons {
		var out []cp.LabelReasons
		for _, r := range rs {
			if !strings.HasPrefix(r.Label, "owned:") && r.Label != "unknown" && r.Label != "generated" {
				out = append(out, r)
			}
		}
		return out
	}
	c.Facts = facts(c.Facts, true)
	c.Reasons = reasons(c.Reasons)
	c.Files = append([]cp.FileEvidence(nil), c.Files...)
	for i := range c.Files {
		c.Files[i].Facts = facts(c.Files[i].Facts, false)
		c.Files[i].Reasons = reasons(c.Files[i].Reasons)
	}
	raw, _ := json.Marshal(c)
	return string(raw)
}

// Preserve existing diagnostic entries, including unknown/generated, while
// permitting Owners to append explanations. Count duplicates independently.
func protectedReasons(c cp.Classification) map[string]int {
	out := map[string]int{}
	add := func(scope int, rs []cp.LabelReasons) {
		for _, r := range rs {
			if strings.HasPrefix(r.Label, "owned:") {
				continue
			}
			raw, _ := json.Marshal(struct {
				Scope  int
				Reason cp.LabelReasons
			}{scope, r})
			out[string(raw)]++
		}
	}
	add(-1, c.Reasons)
	for i, f := range c.Files {
		add(i, f.Reasons)
	}
	return out
}
func reasonsPreserved(before map[string]int, after cp.Classification) bool {
	remaining := protectedReasons(after)
	for key, count := range before {
		if remaining[key] < count {
			return false
		}
	}
	return true
}
