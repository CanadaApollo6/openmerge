package status

import (
	"fmt"
	"html"
	"strings"
	"time"

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
	id, err := j.Key.ChangeID(pr)
	if err != nil {
		return "", err
	}
	view, found, err := projectBrief(j, string(id), "reviewer", briefOptions{policy: func(s queue.State, b queue.ApprovalBasis) (brief.Fact, string) {
		f, e := s.RecordedBrief(policy, b)
		if e != nil {
			return brief.Fact{}, "policy_unavailable"
		}
		return f, ""
	}})
	if err != nil || !found {
		return "", err
	}
	q, entry, _ := Project(j, pr, 0, nil)
	text := "\n\nReviewer brief: " + changePath(j.Key, string(id)) + "/brief?audience=reviewer\nDigest: " + checkText(view.Digest, 128)
	// Safety warnings are emitted first and individually bounded, independent of
	// the row budget. Never truncate the final summary across a warning boundary.
	if view.Classification == nil {
		text += "\nWARNING: classification/inventory not recorded; do not treat this as a complete diff."
	} else if !view.Classification.InventoryComplete || !view.Classification.HunksComplete {
		text += "\nWARNING: incomplete inventory; inspect full recorded evidence before approval."
	}
	for _, w := range view.Warnings {
		text += "\nWARNING: " + checkText(w.Code, 128) + "; " + checkText(w.Clears, 192) + "; next: " + checkText(w.NextStep.Action, 128)
	}
	if view.Truncated {
		text += "\nWARNING: brief display truncated; read retained evidence."
	}
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
		if c.Accountability.Truncated {
			text += "\nWARNING: accountability display is incomplete; read retained evidence."
		}
		text += "\nAccountability: " + c.Accountability.Status
		for i, h := range c.Accountability.Humans {
			if i == 8 {
				text += " (more in full brief/status)"
				break
			}
			text += " " + checkText(h.ID, 100)
		}
		if len(c.WaitingReasons) > 0 {
			text += "\nWaiting at approval gate: " + strings.Join(c.WaitingReasons, ", ") + "; record required evidence for this exact head."
		}
	}
	if entry != nil {
		text += "\nRecorded queue state: " + checkText(entry.Status, 80) + "; gate: " + checkText(holdingGate(&q, *entry, queue.Config{}, time.Time{}), 100)
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
	}
	text += "\nNative reviews bind the head; native intent/brief receipt: not recorded."
	for i, s := range view.Stances {
		if i == 8 {
			text += "\nMore stance evidence in full brief."
			break
		}
		text += "\nEvidence: " + checkText(s.UserID, 100) + " " + checkText(s.Kind, 80) + " via " + checkText(s.Channel, 80) + " source " + checkText(s.SourceID, 100) + "; receipt brief: " + checkText(s.BriefDigest, 128)
	}
	return text, nil
}

func checkText(s string, limit int) string {
	r := []rune(s)
	if len(r) > limit {
		s = string(r[:limit]) + "…"
	}
	s = html.EscapeString(s)
	return strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "`", "\\`", "#", "\\#", "\n", " ", "\r", " ").Replace(s)
}
