package forge

import (
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
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

// ParseChangeTrailers reads the writer's canonical reserved suffix in the final
// paragraph. Contributor lines before that suffix need not be Git trailers.
// The writer strips every reserved line, so no other OpenMerge key is accepted.
func ParseChangeTrailers(message string) ([]*ChangeMetadata, error) {
	bad := func() ([]*ChangeMetadata, error) { return nil, fmt.Errorf("invalid tested change trailers") }
	message = strings.ReplaceAll(message, "\r\n", "\n")
	if strings.ContainsAny(message, "\x00\r") {
		return bad()
	}
	message = strings.TrimSuffix(message, "\n")
	boundary := strings.LastIndex(message, "\n\n")
	if boundary < 0 {
		return bad()
	}
	prefix, block := message[:boundary], message[boundary+2:]
	reserved := func(line string) bool {
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "openmerge-")
	}
	for line := range strings.SplitSeq(prefix, "\n") {
		if reserved(line) {
			return bad()
		}
	}
	lines := strings.Split(block, "\n")
	var result []*ChangeMetadata
	seen := map[string]bool{}
	started, replayed, batch := false, false, false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if !reserved(line) {
			// Git can leave trailing comments, but never continuation lines on an
			// authoritative key or contributor text after the reserved suffix.
			if started && !strings.HasPrefix(line, "#") {
				return bad()
			}
			continue
		}
		if batch {
			return bad()
		}
		if sha, ok := strings.CutPrefix(line, "OpenMerge-Replayed-From: "); ok {
			if started || !ValidDiffObjectID(sha) {
				return bad()
			}
			started, replayed = true, true
			if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "OpenMerge-Change: ") {
				return bad()
			}
			continue
		}
		if value, ok := strings.CutPrefix(line, "OpenMerge-Batch: "); ok {
			if len(result) == 0 || i != len(lines)-1 || !ValidBatchID(value) {
				return bad()
			}
			batch = true
			continue
		}
		id, ok := strings.CutPrefix(line, "OpenMerge-Change: ")
		if !ok || i+1 >= len(lines) {
			return bad()
		}
		if len(result) > 0 && (!strings.HasPrefix(message, "OpenMerge fast-forward test ") || replayed || !strings.HasPrefix(lines[i-1], "OpenMerge-Classification: ")) {
			return bad()
		}
		change, revision, ok := strings.Cut(id, "@")
		if !ok || seen[change] {
			return bad()
		}
		rev, err := strconv.ParseUint(revision, 10, 64)
		if err != nil {
			return bad()
		}
		labels, ok := strings.CutPrefix(lines[i+1], "OpenMerge-Classification: "+id+" ")
		if !ok {
			return bad()
		}
		m := &ChangeMetadata{ChangeID: change, Revision: rev, Labels: []string{}}
		if labels != "none" {
			m.Labels = strings.Split(labels, ",")
		}
		canonical, err := m.Trailers()
		if err != nil || canonical[0] != line || canonical[1] != lines[i+1] {
			return bad()
		}
		seen[change], started = true, true
		result = append(result, m)
		i++
	}
	if len(result) == 0 {
		return bad()
	}
	return result, nil
}
