package status

import (
	"fmt"
	"html"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// ReviewerCheckSummary is appended to the single existing check only for an
// opted-in queue. Legacy output is byte-for-byte unchanged. No forge reads occur.
func ReviewerCheckSummary(j store.Journal, pr int, policy cp.Policy) (string, error) {
	if policy.Approvals == nil {
		return "", nil
	}
	q, _, _ := Project(j, 0, 0, nil)
	return ReviewerCheckSummaryFromProjection(j, &q, pr, policy, nil)
}

// ReviewerCheckSummaryFromProjection reuses the ledger snapshot for a check write.
func ReviewerCheckSummaryFromProjection(j store.Journal, q *Queue, pr int, policy cp.Policy, warn func(error)) (string, error) {
	if policy.Approvals == nil {
		return "", nil
	}
	id, err := j.Key.ChangeID(pr)
	if err != nil {
		return "", err
	}
	view, found, err := projectBriefFromProjection(j, q, q.LedgerEntry(pr), string(id), "reviewer", briefOptions{policy: func(s queue.State, b queue.ApprovalBasis) (brief.Fact, string) {
		if facts, ok := s.ApprovalFacts(b.ChangeID); ok && facts.Intent != nil {
			b = facts.Intent.Basis
		}
		f, e := s.RecordedBrief(policy, b)
		if e != nil {
			if warn != nil {
				warn(e)
			}
			return brief.Fact{}, "policy_unavailable"
		}
		facts, _ := s.ApprovalFacts(b.ChangeID)
		f = recordedBriefProgress(f, policy, s, facts, q.eligibilityPins[b.ChangeID])
		return f, ""
	}})
	if err != nil || !found {
		return "", err
	}
	entry := q.LedgerEntry(pr)
	return reviewerCheckText(j.Key, string(id), view, entry), nil
}

func reviewerCheckText(key store.QueueKey, id string, view brief.View, entry *Entry) string {
	text := ""
	// Safety warnings are emitted first and individually bounded, independent of
	// the row budget. Never truncate the final summary across a warning boundary.
	if entry != nil && entry.Change != nil && entry.Change.Accountability.Truncated {
		text += "\nWARNING: accountability display is incomplete; read retained evidence."
	}
	if view.Classification == nil {
		text += "\nWARNING: classification/inventory not recorded; do not treat this as a complete diff."
	} else if !view.Classification.InventoryComplete || !view.Classification.HunksComplete {
		text += "\nWARNING: incomplete inventory; inspect full recorded evidence before approval."
	}
	warnings := slices.Clone(view.Warnings)
	slices.SortStableFunc(warnings, func(a, b brief.Warning) int {
		if strings.HasPrefix(a.Code, "policy_") != strings.HasPrefix(b.Code, "policy_") {
			if strings.HasPrefix(a.Code, "policy_") {
				return -1
			}
			return 1
		}
		return 0
	})
	for i, w := range warnings {
		if i == 8 {
			text += fmt.Sprintf("\nWARNING: +%d more warnings; read full brief.", len(view.Warnings)-i)
			break
		}
		text += checkWarning(w)
	}
	if view.Truncated {
		text += "\nWARNING: brief display truncated; read retained evidence."
	}
	// A very long queue path must never consume the safety-warning budget.
	briefURL := changePath(key, id) + "/brief?audience=reviewer"
	if len(briefURL) > 4096 {
		briefURL = "URL exceeds check display bound; use openmerge change brief with this queue's selectors."
	}
	text += "\n\nReviewer brief: " + briefURL + "\nDigest: " + checkText(view.Digest, 128)
	if view.Classification != nil {
		for i, l := range view.Classification.Labels {
			if i == 8 {
				text += "\nMore classification reasons in full brief."
				break
			}
			text += "\nClassification: " + checkText(l.Name, 80) + "=" + checkText(l.Truth, 20) + "; " + checkText(strings.Join(l.Reasons, ", "), 180)
		}
	}
	if view.Intent != nil {
		text += "\nClaimed intent: " + checkText(view.Intent.Title, 300)
	}
	if entry != nil && entry.Change != nil {
		c := entry.Change

		text += "\nAccountability: " + c.Accountability.Status
		for i, h := range c.Accountability.Humans {
			if i == 8 {
				text += " (more in full brief/status)"
				break
			}
			text += " " + checkText(h.ID, 100)
		}
		if len(c.WaitingReasons) > 0 {
			text += "\nWaiting at approval gate: " + checkReasons(c.WaitingReasons) + "; record required evidence for this exact head."
		}
	}
	if entry != nil {
		text += "\nRecorded queue state: " + checkText(entry.Status, 80) + "; gate: " + checkText(entry.Gate, 100)
	}
	if view.Obligations == nil {
		text += "\nRequirements: not recorded (policy unavailable)."
	}
	for i, r := range view.Obligations {
		if i == 8 {
			text += "\nMore requirements in full brief."
			break
		}
		text += fmt.Sprintf("\nRequirement %s: %s count=%d receipt=%t met=%t; next: %s", checkText(r.Path, 180), checkText(r.Selector, 80), r.Count, r.Receipt, r.Met, checkText(r.NextStep.Action, 100))
		if r.NextStep.Command != "" {
			text += " — post `" + checkText(r.NextStep.Command, 100) + "`"
		}
	}
	text += "\nNative reviews bind the head; native intent/brief receipt: "
	if len(view.Receipts) == 0 {
		text += "not recorded."
	} else {
		text += "recorded."
		for i, r := range view.Receipts {
			if i == 8 {
				text += fmt.Sprintf("\n+%d more receipts in full brief.", len(view.Receipts)-i)
				break
			}
			text += fmt.Sprintf("\nReceipt: %s accepted=%t binding=%s", checkText(r.SourceID, 100), r.Accepted, checkText(r.Binding, 80))
		}
	}
	for i, s := range view.Stances {
		if i == 8 {
			text += "\nMore stance evidence in full brief."
			break
		}
		text += "\nEvidence: " + checkText(s.UserID, 100) + " " + checkText(s.Kind, 80) + " via " + checkText(s.Channel, 80) + " source " + checkText(s.SourceID, 100) + "; receipt brief: " + checkText(s.BriefDigest, 128)
	}
	// Leave ample room for the existing queue summary within GitHub's 65,535
	// character limit. Warnings precede all optional rows and survive this bound.
	const maxCheckBytes = 12000
	if len(text) > maxCheckBytes {
		end := strings.LastIndex(text[:maxCheckBytes], "\n")
		text = text[:end] + "\n+more evidence in full brief."
	}
	return text
}

// Bound escaped bytes as well as runes so eight warnings leave space for the
// complete brief locator and digest before optional rows meet the 12 KB bound.
func checkWarning(w brief.Warning) string {
	line := func(code, detail, clears, action int) string {
		return "\nWARNING: " + checkText(w.Code, code) + "; " + checkText(w.Detail, detail) + "; " + checkText(w.Clears, clears) + "; next: " + checkText(w.NextStep.Action, action)
	}
	text := line(40, 80, 60, 40)
	if len(text) > 768 {
		text = line(32, 32, 24, 16)
	}
	return text
}

func checkReasons(reasons []string) string {
	rows := make([]string, 0, min(8, len(reasons)))
	for _, r := range reasons[:min(8, len(reasons))] {
		rows = append(rows, checkText(r, 160))
	}
	if len(reasons) > 8 {
		rows = append(rows, fmt.Sprintf("+%d more", len(reasons)-8))
	}
	return strings.Join(rows, ", ")
}

func checkText(s string, limit int) string {
	r := []rune(s)
	if len(r) > limit {
		s = string(r[:limit]) + "…"
	}
	s = html.EscapeString(s)
	return strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "`", "\\`", "#", "\\#", "\n", " ", "\r", " ").Replace(s)
}
