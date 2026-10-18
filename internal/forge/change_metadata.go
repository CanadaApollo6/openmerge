package forge

import (
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/changepolicy"
)

// ChangeMetadata contains only facts pinned before CI, never approval receipts.
// It is not part of the queue action wire format. Production enables this input
// only when PR 11b wires queues with approvals to recorded classifications.
type ChangeMetadata struct {
	ChangeID string
	Revision uint64
	Labels   []string
}

// Trailers returns the adjacent authoritative pair in ADR 0007's spelling.
func (m *ChangeMetadata) Trailers() ([]string, error) {
	if m == nil {
		return nil, nil
	}
	if len(m.ChangeID) != 69 || !strings.HasPrefix(m.ChangeID, "chg1_") || strings.ToLower(m.ChangeID) != m.ChangeID || m.Revision == 0 {
		return nil, fmt.Errorf("invalid build change identity")
	}
	if _, err := hex.DecodeString(m.ChangeID[5:]); err != nil {
		return nil, fmt.Errorf("invalid build change identity: %w", err)
	}
	labels := slices.Clone(m.Labels)
	for _, label := range labels {
		if !changepolicy.ValidClassificationLabel(label) || strings.ContainsAny(label, ",\x00\r\n") {
			return nil, fmt.Errorf("invalid build classification label %q", label)
		}
	}
	slices.Sort(labels)
	labels = slices.Compact(labels)
	value := strings.Join(labels, ",")
	if value == "" {
		value = "none"
	}
	key := fmt.Sprintf("%s@%d", m.ChangeID, m.Revision)
	return []string{"OpenMerge-Change: " + key, "OpenMerge-Classification: " + key + " " + value}, nil
}

// ValidateBuildMetadata rejects partially supplied or duplicate member metadata.
func ValidateBuildMetadata(prs []BuildPR) error {
	anyMetadata := false
	for _, pr := range prs {
		anyMetadata = anyMetadata || pr.Metadata != nil
	}
	if !anyMetadata {
		return nil
	}
	seen := make(map[string]bool)
	for _, pr := range prs {
		if pr.Metadata == nil {
			return fmt.Errorf("missing build change metadata for PR %d", pr.Number)
		}
		if _, err := pr.Metadata.Trailers(); err != nil {
			return err
		}
		if seen[pr.Metadata.ChangeID] {
			return fmt.Errorf("duplicate build change identity")
		}
		seen[pr.Metadata.ChangeID] = true
	}
	return nil
}
