package status

import (
	"errors"
	"net/http"
	"net/url"
	"reflect"
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

// projectBrief joins committed journal evidence with the policy pinned at
// classification time. Unavailable requirements never offer a receipt digest.
func projectBrief(j store.Journal, id, audience string, options ...briefOptions) (brief.View, bool, error) {
	opt := briefOptions{}
	if len(options) > 0 {
		opt = options[0]
	}
	sanitizer := opt.sanitizer
	if sanitizer == nil {
		sanitizer = diagnostic.NewSanitizer()
	}
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
		f.Presentation.Stances = []brief.StanceEvidence{}
		if a.Intent != nil && a.Intent.Basis.Revision == rev.Revision && a.Intent.Basis.Head == rev.HeadSHA {
			f.Intent = &brief.Intent{Recorded: true, Title: a.Intent.Title, Body: a.Intent.Body, Digest: a.Intent.Digest}
		}
		seenSources := map[string]bool{}
		appendStance := func(sourceID string, row queue.AttributedStance) {
			if seenSources[sourceID] {
				return
			}
			seenSources[sourceID] = true
			stance := row.Event.Stance
			evidence := brief.StanceEvidence{UserID: stance.UserID, Kind: string(stance.Kind), Channel: string(stance.Source.Channel), SourceID: stance.Source.ID, Head: stance.Head, Revision: uint64(row.Revision), Time: stance.Source.Time, TimeKnown: stance.Source.TimeKnown, UpperBound: stance.Source.UpperTimeBound, Precision: string(stance.Source.Precision)}
			if stance.Receipt != nil {
				evidence.IntentDigest = stance.Receipt.IntentDigest
				evidence.BriefDigest = stance.Receipt.BriefDigest
			}
			f.Presentation.Stances = append(f.Presentation.Stances, evidence)
		}
		users := make([]string, 0, len(a.Latest))
		for user := range a.Latest {
			users = append(users, user)
		}
		slices.Sort(users)
		for _, user := range users {
			sourceID := a.Latest[user]
			row, ok := a.Sources[sourceID]
			if !ok || row.Revision != rev.Revision && row.Revision != 0 {
				continue
			}
			appendStance(sourceID, row)
		}
		// Revision zero is an explicit historical/ambiguous attribution. It is
		// useful evidence, but never becomes authorization, so retain it even
		// when it is no longer the latest source for a reviewer.
		ambiguousSources := make([]string, 0)
		for sourceID, row := range a.Sources {
			if row.Revision == 0 && !seenSources[sourceID] {
				ambiguousSources = append(ambiguousSources, sourceID)
			}
		}
		slices.Sort(ambiguousSources)
		for _, sourceID := range ambiguousSources {
			appendStance(sourceID, a.Sources[sourceID])
		}
	}
	basis := queue.ApprovalBasis{ChangeID: queue.ChangeID(id), Revision: rev.Revision, Head: rev.HeadSHA}
	policyCode := ""
	if opt.policy != nil {
		policyCode = applyBriefPolicy(&f, s, basis, opt)
	}
	q, entry, _ := Project(j, selected, 0, sanitizer)
	if entry != nil {
		f.Presentation.Progress = &brief.ProgressView{Position: entry.Position, Status: entry.Status, Reason: entry.Reason}
	}
	var runs []Run
	if entry != nil {
		runs = entry.RunLedger
		f.Presentation.Tests = []brief.TestEvidence{}
		f.Presentation.SupersededTests = map[int]bool{}
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
		f.Presentation.SupersededTests[len(f.Presentation.Tests)] = run.WastedReason == "rerun"
		f.Presentation.Tests = append(f.Presentation.Tests, t)
	}
	v, err := brief.Project(f, audience)
	v.ChangeID = id
	if policyCode != "" {
		appendBriefWarning(&v, audience, policyCode, "The recorded approval policy could not be reconstructed for this brief.", "Make the pinned local policy objects and recorded identity inputs available; a full effective config loader is required to verify the config digest.", brief.Step{Action: "restore_recorded_policy"})
	}
	appendBriefChangeWarnings(&v, audience, j.Key, s, id, rev, entry, runs, f)
	recovery := queue.BlockRecovery(s.Blocks(queue.Config{}))
	for _, block := range q.Blocks {
		source := queue.BlockSource(block.Source)
		action := "wait_for_recovery"
		if source.OperatorClearable() {
			action = "refresh"
		}
		appendBriefWarning(&v, audience, "queue_blocked", block.Reason+": "+block.Detail, recovery, brief.Step{Action: action, Source: block.Source})
	}
	if sanitizeBriefView(&v, sanitizer) {
		appendBriefWarning(&v, audience, "display_redacted", "Display text was redacted; the digest covers the original recorded facts.", "Use authorized access to the recorded evidence to inspect the original content.", brief.Step{Action: "read_recorded_evidence"})
	}
	return v, true, err
}

func appendBriefWarning(v *brief.View, audience, code, detail, clears string, step brief.Step) {
	for _, warning := range v.Warnings {
		if warning.Code == code && warning.NextStep.Source == step.Source {
			return
		}
	}
	warning := brief.Warning{Code: code, NextStep: step}
	if audience != "agent" {
		warning.Detail = detail
		warning.Clears = clears
	}
	v.Warnings = append(v.Warnings, warning)
}

func appendBriefChangeWarnings(v *brief.View, audience string, key store.QueueKey, s queue.State, id string, rev queue.RevisionRecord, entry *Entry, runs []Run, f brief.Fact) {
	if entry != nil {
		switch entry.Reason {
		case string(queue.Untrusted):
			appendBriefWarning(v, audience, "untrusted_head", "The current head has not passed the exact-head trust gate.", "Record a fresh observation that trusts this exact head before queueing it.", brief.Step{Action: "trust_exact_head", Source: "exact-head", Commit: entry.HeadSHA})
		case string(queue.NotEligible):
			appendBriefWarning(v, audience, "not_eligible", "The current recorded eligibility conditions do not admit this change.", "Record corrected eligibility evidence before requesting queue admission.", brief.Step{Action: "fix_eligibility"})
		case string(queue.HeadChanged):
			appendBriefWarning(v, audience, "head_changed", "The admission is pinned to an older head.", "Let reconciliation retire the stale admission and observe the new exact head before re-admission.", brief.Step{Action: "wait_for_recovery", Source: "head-observation", Commit: rev.HeadSHA})
		case string(queue.Conflict), string(queue.FastForwardRequired), string(queue.MergeCommitsNotAllowed):
			target := s.BaseSHA
			if target == "" && f.Classification != nil && f.Classification.Inputs.Base != "" {
				target = f.Classification.Inputs.Base
			}
			if target == "" {
				target = rev.Basis.BaseSHA
			}
			appendBriefWarning(v, audience, "rebase_required", "The recorded head no longer matches the queue admission or base-history requirement.", "Rebase the change onto the recorded queue base, then record the new exact head.", brief.Step{Action: "rebase_onto", Path: key.BaseBranch, Commit: target})
		case string(queue.ChecksFailed), string(queue.ChecksTimeout):
			step := brief.Step{Action: "wait_for_recovery"}
			if check := failedBriefRun(runs); check != nil && check.Identity != nil {
				step = brief.Step{Action: "fix_check", Name: check.Identity.Name, URL: safeBriefURL(check.Identity.URL), Commit: check.TestedSHA}
			}
			appendBriefWarning(v, audience, "checks_blocked", "The recorded check evidence does not permit this change to proceed.", "Resolve the recorded check outcome and record a fresh result for the exact tested head.", step)
		}
	}
	for _, wait := range s.ApprovalWaits() {
		if wait.Basis.ChangeID == queue.ChangeID(id) && wait.Basis.Revision == rev.Revision && wait.Basis.Head == rev.HeadSHA {
			appendBriefWarning(v, audience, "approval_wait", "The recorded approval gate is waiting for the missing evidence listed in the journal.", "Record the required approvals or corrected approval inputs for this exact head.", brief.Step{Action: "wait_for_approval"})
			break
		}
	}
}

func failedBriefRun(runs []Run) *Run {
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].WastedReason != "rerun" && runs[i].Identity != nil && terminalBriefFailure(runs[i].Outcome) {
			return &runs[i]
		}
	}
	return nil
}

func terminalBriefFailure(outcome string) bool {
	switch strings.ToLower(outcome) {
	case "failure", "failed", "error", "cancelled", "canceled", "timeout", "timed_out", "timed-out", "action_required", "action-required":
		return true
	default:
		return false
	}
}

func safeBriefURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return raw
}

var errBriefQuery = errors.New("brief requires a canonical change ID and ?audience=reviewer|operator|agent")

// Sanitize the completed display, after hashing the untouched recorded Fact.
// Walking DTO strings also covers future display fields and nested slices.
func sanitizeBriefView(v *brief.View, sanitizer *diagnostic.Sanitizer) bool {
	changed := false
	var walk func(reflect.Value)
	walk = func(value reflect.Value) {
		switch value.Kind() {
		case reflect.Pointer:
			if !value.IsNil() {
				walk(value.Elem())
			}
		case reflect.Struct:
			for i := 0; i < value.NumField(); i++ {
				walk(value.Field(i))
			}
		case reflect.Slice:
			for i := 0; i < value.Len(); i++ {
				walk(value.Index(i))
			}
		case reflect.String:
			original := value.String()
			clean := sanitizer.Sanitize(original)
			if clean != original {
				value.SetString(clean)
				changed = true
			}
		}
	}
	walk(reflect.ValueOf(v))
	return changed
}
