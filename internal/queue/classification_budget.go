package queue

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// v8 limits are validated, never re-applied by truncation at persistence.
func validateRetainedHunks(c cp.Classification) error {
	count, rawBytes, escapedBytes, detailed := 0, 0, 0, 0
	if len(c.Files) > forge.MaxDiffFiles {
		return fmt.Errorf("classification file budget exceeded")
	}
	for _, f := range c.Files {
		lines, summaries := 0, 0
		for _, h := range f.Hunks.Hunks {
			count++
			rawBytes += len(h.Text)
			raw, _ := json.Marshal(h.Text)
			escapedBytes += len(raw) - 2
			lines += strings.Count(h.Text, "\n")
			if h.Text != "" && !strings.HasSuffix(h.Text, "\n") {
				lines++
			}
		}
		if lines > forge.MaxDiffHunkLinesPerFile {
			return fmt.Errorf("classification hunk line budget exceeded")
		}
		for _, r := range f.Hunks.Omitted {
			if r.Reason == string(forge.DiffOmittedRangeLimit) {
				summaries++
			} else {
				detailed++
			}
		}
		if summaries > 1 || len(f.Hunks.Omitted)-summaries > forge.MaxDiffOmittedRangesPerFile {
			return fmt.Errorf("classification omission budget exceeded")
		}
	}
	if count > forge.MaxDiffHunks || rawBytes > forge.MaxDiffHunkBytes || escapedBytes > forge.MaxDiffHunkEscapedBytes || detailed > forge.MaxDiffOmittedRanges {
		return fmt.Errorf("classification retained hunk budget exceeded")
	}
	return nil
}

// CheckClassificationAdmission lets 11a reject one change before journaling,
// preserving the previous recoverable queue. It never rewrites source evidence.
func (s State) CheckClassificationAdmission(e ChangeClassified) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if err := validateClassificationAdmission(e); err != nil {
		return err
	}
	next := Apply(s, e)
	i := e.Evidence.Classification.Inputs
	accepted, ok := next.Classification(ChangeID(i.ChangeID), ChangeRevision(i.Revision))
	if !ok || !reflect.DeepEqual(nonNullClassification(accepted), nonNullClassification(e)) {
		return fmt.Errorf("classification sequence rejected")
	}
	return checkSharedAdmissionBudget(next, ChangeID(i.ChangeID))
}

// Emission validation is stricter than historical decoding: existing snapshots
// retain their recorded versions, while new producers cannot assert aggregates
// or owner provenance that contradict the retained per-file evidence.
func validateClassificationAdmission(e ChangeClassified) error {
	c, g := e.Evidence.Classification, e.Evidence.Governing
	if err := cp.ValidateOwnerEvidence(c); err != nil {
		return err
	}
	for _, name := range []string{"docs", "generated", "policy"} {
		value := cp.Unknown
		if name == "policy" {
			if c.InventoryComplete {
				value = cp.False
			}
			for _, f := range c.Files {
				if f.Facts[name] == cp.True {
					value = cp.True
					break
				}
				if f.Facts[name] == cp.Unknown {
					value = cp.Unknown
				}
			}
		} else if c.InventoryComplete {
			value = cp.True
			for _, f := range c.Files {
				if f.Facts[name] == cp.False {
					value = cp.False
					break
				}
				if f.Facts[name] == cp.Unknown {
					value = cp.Unknown
				}
			}
		}
		if c.Facts[name] != value {
			return fmt.Errorf("classification aggregate %s contradicts files", name)
		}
	}
	selected := ""
	for _, f := range g.Codeowners {
		if f.Path == g.SelectedCodeowners && f.Present {
			selected = f.ObjectID
		}
	}
	if selected != c.Inputs.OwnersBlob {
		return fmt.Errorf("governing owners blob mismatch")
	}
	return nil
}
