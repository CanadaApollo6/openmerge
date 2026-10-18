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
	next := Apply(s, e)
	i := e.Evidence.Classification.Inputs
	accepted, ok := next.Classification(ChangeID(i.ChangeID), ChangeRevision(i.Revision))
	if !ok || !reflect.DeepEqual(nonNullClassification(accepted), nonNullClassification(e)) {
		return fmt.Errorf("classification sequence rejected")
	}
	return checkSharedAdmissionBudget(next, ChangeID(i.ChangeID))
}
