package status

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// briefRoute reuses the existing selector rather than resolving an ID globally.
func briefRoute(r *http.Request, keys []store.QueueKey) ([]store.QueueKey, string, string, bool, error) {
	parts := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
	if len(parts) != 8 || parts[0] != "v1" || parts[1] != "queues" || parts[5] != "changes" || parts[7] != "brief" {
		return nil, "", "", false, nil
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(values) != 1 || len(values["audience"]) != 1 || !brief.ValidAudience(values.Get("audience")) {
		return nil, "", "", true, errBriefQuery
	}
	id, err := url.PathUnescape(parts[6])
	if err != nil || queue.ChangeID(id).Validate() != nil {
		return nil, "", "", true, errBriefQuery
	}
	selector := r.Clone(r.Context())
	u := *r.URL
	selector.URL = &u
	u.RawPath = "/" + strings.Join(parts[:5], "/")
	u.Path, _ = url.PathUnescape(u.RawPath)
	u.RawQuery = ""
	selected, _, _, err := route(selector, keys)
	return selected, id, values.Get("audience"), true, err
}

// projectBrief uses only committed journal evidence. Requirements are not yet
// persisted as a complete brief by Stage A ingestion, so it never manufactures
// them from today's config, nor offers a partial digest as a receipt target.
func projectBrief(j store.Journal, id, audience string) (brief.View, bool, error) {
	s := j.Seed
	candidates := map[int]bool{}
	for _, e := range s.Entries {
		candidates[e.PR] = true
	}
	for _, r := range j.Events {
		switch e := r.Event.(type) {
		case queue.PRObserved:
			candidates[e.PR] = true
		case queue.ChangeRevisionObserved:
			candidates[e.PR] = true
		}
		s = queue.Apply(s, r.Event)
	}
	selected := 0
	for pr := range candidates {
		canonical, err := j.Key.ChangeID(pr)
		if err == nil && string(canonical) == id {
			if _, ok := s.PRRevision(pr); ok {
				selected = pr
				break
			}
		}
	}
	if selected == 0 {
		return brief.View{}, false, nil
	}
	rev, _ := s.PRRevision(selected)
	f := brief.Fact{Presentation: &brief.Presentation{RequirementsMissing: true}}
	if c, ok := s.Classification(queue.ChangeID(id), rev.Revision); ok {
		recorded := c.Evidence.Classification
		f.Classification = &recorded
	}
	if a, ok := s.ApprovalFacts(queue.ChangeID(id)); ok {
		f.Presentation.Saturated = a.Saturated
		if a.Intent != nil && a.Intent.Basis.Revision == rev.Revision && a.Intent.Basis.Head == rev.HeadSHA {
			f.Intent = &brief.Intent{Recorded: true, Title: a.Intent.Title, Body: a.Intent.Body, Digest: a.Intent.Digest}
		}
		users := make([]string, 0, len(a.Latest))
		for user := range a.Latest {
			users = append(users, user)
		}
		slices.Sort(users)
		for _, user := range users {
			row := a.Sources[a.Latest[user]]
			stance := row.Event.Stance
			if row.Revision != rev.Revision {
				continue
			}
			evidence := brief.StanceEvidence{UserID: user, Kind: string(stance.Kind), Channel: string(stance.Source.Channel), SourceID: stance.Source.ID, Head: stance.Head, Revision: uint64(row.Revision), Time: stance.Source.Time, TimeKnown: stance.Source.TimeKnown, UpperBound: stance.Source.UpperTimeBound, Precision: string(stance.Source.Precision)}
			if stance.Receipt != nil {
				evidence.IntentDigest = stance.Receipt.IntentDigest
				evidence.BriefDigest = stance.Receipt.BriefDigest
			}
			f.Presentation.Stances = append(f.Presentation.Stances, evidence)
		}
	}
	q, entry, _ := Project(j, selected, 0, diagnostic.NewSanitizer())
	if entry != nil {
		f.Presentation.Progress = &brief.ProgressView{Position: entry.Position, Status: entry.Status, Reason: entry.Reason}
	}
	var runs []Run
	if entry != nil {
		runs = entry.RunLedger
	}
	for _, run := range runs {
		t := brief.TestEvidence{Status: run.Outcome, SHA: run.TestedSHA}
		if run.Identity != nil {
			t.Name = run.Identity.Name
			t.URL = run.Identity.URL
			if run.Identity.State != "" {
				t.Status = run.Identity.State
			}
		}
		f.Presentation.Tests = append(f.Presentation.Tests, t)
	}
	v, err := brief.Project(f, audience)
	v.ChangeID = id
	for _, block := range q.Blocks {
		clears, action := clearQueueBlock(block.Source)
		warning := brief.Warning{Code: "queue_blocked", NextStep: brief.Step{Action: action, Source: block.Source}}
		if audience != "agent" {
			warning.Detail = block.Reason + ": " + block.Detail
			warning.Clears = clears
		}
		v.Warnings = append(v.Warnings, warning)
	}
	return v, true, err
}

var errBriefQuery = errors.New("brief requires a canonical change ID and ?audience=reviewer|operator|agent")

func clearQueueBlock(source string) (string, string) {
	switch source {
	case "config":
		return "Fix the governing configuration and let reconciliation record a valid configuration.", "fix_governing_config"
	case "repository-removed":
		return "Restore installation access and let discovery record repository recovery.", "restore_repository_access"
	case "check-sources":
		return "Resolve ambiguous or untrusted required check producers and let reconciliation record the corrected sources.", "resolve_check_sources"
	case "landing-not-on-base":
		return "Verify the recorded landing against base history before an authorized operator clears this source.", "verify_landing_history"
	case "action-poisoned":
		return "Investigate the quarantined action before an authorized operator clears this source.", "investigate_quarantined_action"
	case "operator":
		return "An authorized operator must resume the queue or clear the operator block.", "operator_resume"
	case "build-failed":
		return "Resolve the recorded build failure and use the existing authenticated refresh path to reset its retry backoff.", "resolve_build_failure"
	default:
		return "Resolve the recorded governing rules or lifecycle condition and let its owning observer record recovery.", "resolve_queue_block"
	}
}
