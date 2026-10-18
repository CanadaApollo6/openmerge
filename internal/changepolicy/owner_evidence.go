package changepolicy

import (
	"fmt"
	"strings"
)

type ownerPath struct {
	path string
	side OwnerSide
}

// ownerInventory cross-checks the two inventory projections before either can
// authorize an owner exemption. Ordinary edits use the new side; renames use
// both sides and deletions use the old side.
func ownerInventory(c Classification) (map[ownerPath]struct{}, error) {
	want := map[ownerPath]struct{}{}
	paths := map[string]bool{}
	for _, file := range c.Files {
		if file.OldPath == "" && file.NewPath == "" {
			return nil, fmt.Errorf("file has no path")
		}
		renamed := file.OldPath != "" && file.NewPath != "" && file.OldPath != file.NewPath
		if file.Renamed != renamed || renamed && !file.RenamePaired {
			return nil, fmt.Errorf("rename is not paired")
		}
		for _, path := range []string{file.OldPath, file.NewPath} {
			if path != "" {
				paths[path] = true
			}
		}
		rows := []ownerPath{{file.NewPath, NewSide}}
		if file.NewPath == "" {
			rows = []ownerPath{{file.OldPath, OldSide}}
		} else if file.Renamed {
			rows = append(rows, ownerPath{file.OldPath, OldSide})
		}
		for _, row := range rows {
			if _, exists := want[row]; exists {
				return nil, fmt.Errorf("duplicate inventory path/side")
			}
			want[row] = struct{}{}
		}
	}
	recordedPaths := map[string]bool{}
	for _, path := range c.Paths {
		if !paths[path] {
			return nil, fmt.Errorf("paths projection contains a path absent from files")
		}
		recordedPaths[path] = true
	}
	if len(recordedPaths) != len(paths) {
		return nil, fmt.Errorf("files contain a path absent from paths projection")
	}
	return want, nil
}

// entryFields inspects only the recorded pattern/owner fields, never matching
// CODEOWNERS patterns or deriving memberships. A trailing comment is not an owner.
func entryFields(entry string) []string {
	var fields []string
	var word strings.Builder
	for i := 0; i < len(entry); i++ {
		ch := entry[i]
		if ch == '\\' && i+1 < len(entry) {
			word.WriteByte(ch)
			i++
			word.WriteByte(entry[i])
			continue
		}
		if ch == '#' {
			break
		}
		if ch == ' ' || ch == '\t' || ch == '\r' {
			if word.Len() > 0 {
				fields = append(fields, word.String())
				word.Reset()
			}
		} else {
			word.WriteByte(ch)
		}
	}
	if word.Len() > 0 {
		fields = append(fields, word.String())
	}
	return fields
}

func codeOwnerEvidence(c Classification) error {
	seen := map[ownerPath]struct{}{}
	for _, owner := range c.CodeOwners {
		row := ownerPath{owner.Path, owner.Side}
		if owner.Path == "" || owner.Side != OldSide && owner.Side != NewSide {
			return fmt.Errorf("invalid code owner path/side")
		}
		if _, exists := seen[row]; exists {
			return fmt.Errorf("duplicate code owner path/side")
		}
		seen[row] = struct{}{}
		fields := entryFields(owner.LastMatchingEntry)
		switch owner.Status {
		case OwnersUncovered:
			if strings.TrimSpace(owner.LastMatchingEntry) != "" || len(owner.Eligible.IDs) != 0 {
				return fmt.Errorf("uncovered entry carries owners")
			}
		case OwnersOwnerless:
			if len(fields) != 1 || len(owner.Eligible.IDs) != 0 {
				return fmt.Errorf("ownerless entry lacks its pattern or carries owners")
			}
		case OwnersResolved:
			if len(fields) < 2 || len(owner.Eligible.IDs) == 0 {
				return fmt.Errorf("resolved entry lacks owners or IDs")
			}
			if c.CodeOwnersKnown && (!owner.Eligible.Resolved || !owner.Eligible.Fresh) {
				return fmt.Errorf("known code owners contain unresolved membership")
			}
		case OwnersUnresolved:
			// Partial evidence remains unresolved when a code-owner rule applies.
			if c.CodeOwnersKnown {
				return fmt.Errorf("known code owners contain unresolved entry")
			}
		default:
			return fmt.Errorf("unknown code owner status")
		}
		for _, id := range owner.Eligible.IDs {
			if id == "" {
				return fmt.Errorf("empty code owner ID")
			}
		}
	}
	if !c.InventoryComplete || !c.CodeOwnersKnown && len(seen) == 0 {
		return nil
	}
	want, err := ownerInventory(c)
	if err != nil {
		return err
	}
	for row := range seen {
		if _, exists := want[row]; !exists {
			return fmt.Errorf("code owner row absent from inventory")
		}
	}
	if c.CodeOwnersKnown && len(seen) != len(want) {
		return fmt.Errorf("missing code owner rows")
	}
	return nil
}

func policyOwnerEvidence(c Classification) error {
	seen := map[string]struct{}{}
	for _, owner := range c.PolicyOwners {
		if owner.Path == "" {
			return fmt.Errorf("empty policy owner path")
		}
		if _, exists := seen[owner.Path]; exists {
			return fmt.Errorf("duplicate policy owner path")
		}
		seen[owner.Path] = struct{}{}
		for _, id := range owner.Eligible.IDs {
			if id == "" {
				return fmt.Errorf("empty policy owner ID")
			}
		}
	}
	observedPolicy := false
	for _, file := range c.Files {
		observedPolicy = observedPolicy || file.Facts["policy"] == True
	}
	if !c.InventoryComplete || !c.PolicyOwnersKnown && len(seen) == 0 && !observedPolicy {
		return nil
	}
	if _, err := ownerInventory(c); err != nil {
		return err
	}
	want := map[string]struct{}{}
	for _, file := range c.Files {
		switch file.Facts["policy"] {
		case True:
			for _, path := range []string{file.OldPath, file.NewPath} {
				if path != "" {
					want[path] = struct{}{}
				}
			}
		case False:
		default:
			return fmt.Errorf("per-file policy classification unresolved")
		}
	}
	if c.Facts["policy"] != False && len(want) == 0 || c.Facts["policy"] == False && len(want) > 0 {
		return fmt.Errorf("policy label contradicts per-file policy paths")
	}
	for path := range seen {
		if _, exists := want[path]; !exists {
			return fmt.Errorf("policy owner row absent from policy inventory")
		}
	}
	if c.PolicyOwnersKnown && len(seen) != len(want) {
		return fmt.Errorf("missing policy owner rows")
	}
	return nil
}
