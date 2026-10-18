package brief

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
)

// Presentation contains recorded evidence excluded from Canonical. It is never
// used to authorize a receipt. Nil preserves historical Fact JSON encodings.
type Presentation struct {
	Stances             []StanceEvidence
	Tests               []TestEvidence
	SupersededTests     map[int]bool
	Saturated           bool
	RequirementsMissing bool
	Progress            *ProgressView
}

// StanceEvidence retains recorded source times and native or receipt bindings.
type StanceEvidence struct {
	UserID, Kind, Channel, SourceID, Head string
	Revision                              uint64
	Time, UpperBound                      time.Time
	TimeKnown                             bool
	Precision, IntentDigest, BriefDigest  string
}

// TestEvidence identifies recorded CI evidence, without looking up a run.
type TestEvidence struct{ Name, URL, Status, SHA string }

// View is the public v1 DTO. No queue or persisted wire struct is embedded.
// Null sections mean not recorded; [] means recorded and empty.
type View struct {
	Progress          *ProgressView       `json:"progress"`
	Version           int                 `json:"version"`
	Audience          string              `json:"audience"`
	ChangeID          string              `json:"change_id"`
	Digest            string              `json:"digest"`
	Intent            *IntentView         `json:"claimed_intent"`
	Classification    *ClassificationView `json:"classification"`
	Obligations       []ObligationView    `json:"obligations"`
	ApprovalsRequired *bool               `json:"approvals_required"`
	Stances           []StanceView        `json:"stances"`
	Tests             []TestView          `json:"tests"`
	Warnings          []Warning           `json:"warnings"`
	Truncated         bool                `json:"truncated"`
}

// ProgressView describes queue position at the journal snapshot.
type ProgressView struct {
	Position int    `json:"position"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
}

// OwnerView retains a governing owner row and its recorded pool.
type OwnerView struct {
	Path     string   `json:"path"`
	Side     string   `json:"side"`
	Status   string   `json:"status"`
	Entry    string   `json:"entry"`
	IDs      []string `json:"eligible_ids"`
	Resolved bool     `json:"resolved"`
}

// IntentView presents the submitter’s claimed title and body.
type IntentView struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// ClassificationView exposes pinned classification and display coverage.
type ClassificationView struct {
	Generation        uint64      `json:"generation"`
	ClassifierVersion string      `json:"classifier_version"`
	Owners            []OwnerView `json:"owners"`
	Revision          uint64      `json:"revision"`
	Head              string      `json:"head"`
	Base              string      `json:"base"`
	MergeBase         string      `json:"merge_base"`
	PolicyDigest      string      `json:"policy_digest"`
	InventoryComplete bool        `json:"inventory_complete"`
	HunksComplete     bool        `json:"hunks_complete"`
	Labels            []LabelView `json:"labels"`
	Files             []FileView  `json:"files"`
}

// LabelView pairs a three-valued label with recorded reasons.
type LabelView struct {
	Name    string   `json:"name"`
	Truth   string   `json:"truth"`
	Reasons []string `json:"reasons"`
}

// HunkView retains a diff hunk and its immutable source ranges.
type HunkView struct {
	OldStart int    `json:"old_start"`
	OldLines int    `json:"old_lines"`
	NewStart int    `json:"new_start"`
	NewLines int    `json:"new_lines"`
	Text     string `json:"text"`
}

// OmissionView identifies a recorded omitted range without expanding it.
type OmissionView struct {
	OldStart int    `json:"old_start"`
	OldLines int    `json:"old_lines"`
	NewStart int    `json:"new_start"`
	NewLines int    `json:"new_lines"`
	Reason   string `json:"reason"`
	Count    int    `json:"count"`
}

// FileView presents a bounded file and hunk inventory.
type FileView struct {
	Omissions []OmissionView `json:"omitted_ranges"`
	OldPath   string         `json:"old_path"`
	NewPath   string         `json:"new_path"`
	Hunks     []HunkView     `json:"hunks"`
	Complete  bool           `json:"complete"`
}

// ObligationView describes one requirement, progress and recovery.
type ObligationView struct {
	Human       bool     `json:"human"`
	EligibleIDs []string `json:"eligible_ids"`
	Code        string   `json:"code"`
	Path        string   `json:"path"`
	Side        string   `json:"side"`
	Selector    string   `json:"selector"`
	Group       string   `json:"group"`
	Count       int      `json:"count"`
	Receipt     bool     `json:"receipt"`
	Met         bool     `json:"met"`
	Explanation string   `json:"explanation,omitempty"`
	Clears      string   `json:"what_clears_it,omitempty"`
	ApprovedIDs []string `json:"approved_ids"`
	NextStep    Step     `json:"next_step"`
}

// Step is a stable action with structured recovery parameters.
type Step struct {
	Source string `json:"source,omitempty"`
	Action string `json:"action"`
	Path   string `json:"path,omitempty"`
	UserID string `json:"user_id,omitempty"`
	After  string `json:"source_time_after,omitempty"`
	Name   string `json:"name,omitempty"`
	URL    string `json:"url,omitempty"`
	Commit string `json:"commit,omitempty"`
}

// Warning preserves a safety-relevant condition independently of row limits.
type Warning struct {
	Detail   string `json:"detail,omitempty"`
	Code     string `json:"code"`
	Clears   string `json:"what_clears_it,omitempty"`
	NextStep Step   `json:"next_step"`
}

// StanceView distinguishes native evidence from receipt bindings.
type StanceView struct {
	UserID       string `json:"user_id"`
	Kind         string `json:"kind"`
	Channel      string `json:"channel"`
	SourceID     string `json:"source_id"`
	Head         string `json:"head"`
	Revision     uint64 `json:"revision"`
	SourceTime   string `json:"source_time"`
	Precision    string `json:"precision"`
	UpperBound   string `json:"upper_bound"`
	IntentDigest string `json:"intent_digest"`
	BriefDigest  string `json:"brief_digest"`
	NextStep     Step   `json:"next_step"`
	Attribution  string `json:"attribution,omitempty"`
}

// TestView exposes a recorded check name, safe URL, status and SHA.
type TestView struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Status string `json:"status"`
	SHA    string `json:"sha"`
}

// NotRecorded is the explicit spelling for absent historical evidence.
const NotRecorded = "not recorded"
const maxRows = 32
const maxText = 2048

const (
	displayBudget           = 512 << 10
	displayMetadataBudget   = 64 << 10
	displayObligationBudget = 96 << 10
	displayProvenanceBudget = 96 << 10
	displayTestBudget       = 64 << 10
	displayHunkBudget       = displayBudget - displayMetadataBudget - displayObligationBudget - displayProvenanceBudget - displayTestBudget
)

// MaxViewBytes caps public HTTP and CLI brief responses.
const MaxViewBytes = 2 << 20

// ValidAudience recognizes the three public brief audiences.
func ValidAudience(a string) bool { return a == "reviewer" || a == "operator" || a == "agent" }

type textBudget struct {
	remaining int
	sanitizer *diagnostic.Sanitizer
	redacted  *bool
}

func (b *textBudget) clean(s string, truncated *bool) string {
	clean, redacted := b.sanitizer.SanitizeTextRedacted(s)
	if redacted {
		*b.redacted = true
	}
	text := bounded(clean, truncated)
	escaped, _ := json.Marshal(text)
	if len(escaped) > b.remaining {
		*truncated = true
		return "[truncated]"
	}
	b.remaining -= len(escaped)
	return text
}

// Project bounds only the public display. The source Fact and its digest remain
// untouched. Safety warnings are collected before any display row is omitted.
func Project(f Fact, audience string, sanitizers ...*diagnostic.Sanitizer) (View, error) {
	if !ValidAudience(audience) {
		return View{}, fmt.Errorf("invalid audience")
	}
	v := View{Version: 1, Audience: audience, ChangeID: NotRecorded, Digest: NotRecorded, Warnings: []Warning{}}
	var sanitizer *diagnostic.Sanitizer
	if len(sanitizers) > 0 {
		sanitizer = sanitizers[0]
	}
	redacted := false
	metadata := textBudget{displayMetadataBudget, sanitizer, &redacted}
	obligations := textBudget{displayObligationBudget, sanitizer, &redacted}
	tests := textBudget{displayTestBudget, sanitizer, &redacted}
	hunks := textBudget{displayHunkBudget, sanitizer, &redacted}
	warn := func(code, clears, action string) {
		for _, w := range v.Warnings {
			if w.Code == code {
				return
			}
		}
		v.Warnings = append(v.Warnings, Warning{Code: code, Clears: clears, NextStep: Step{Action: action}})
	}
	warnStep := func(code, clears string, step Step) {
		count := 0
		for _, w := range v.Warnings {
			if w.Code != code {
				continue
			}
			count++
			if w.NextStep.Name == step.Name {
				return
			}
		}
		if count >= maxRows {
			v.Truncated = true
			return
		}
		v.Warnings = append(v.Warnings, Warning{Code: code, Clears: clears, NextStep: step})
	}
	if f.Intent != nil && f.Intent.Recorded {
		v.Intent = &IntentView{metadata.clean(f.Intent.Title, &v.Truncated), metadata.clean(f.Intent.Body, &v.Truncated)}
	}
	if c := f.Classification; c != nil {
		i := c.Inputs
		v.ChangeID = bounded(i.ChangeID, &v.Truncated)
		cv := &ClassificationView{Revision: i.Revision, Head: bounded(i.Head, &v.Truncated), Base: bounded(i.Base, &v.Truncated), MergeBase: bounded(i.MergeBase, &v.Truncated), PolicyDigest: bounded(i.PolicyDigest, &v.Truncated), InventoryComplete: c.InventoryComplete, HunksComplete: c.HunksComplete, Labels: []LabelView{}, Files: []FileView{}}
		v.Classification = cv
		cv.Generation = i.Generation
		cv.ClassifierVersion = bounded(i.ClassifierVersion, &v.Truncated)
		cv.Owners = []OwnerView{}
		for index, o := range c.CodeOwners {
			if index >= maxRows {
				v.Truncated = true
				continue
			}
			ids := append([]string{}, o.Eligible.IDs...)
			slices.Sort(ids)
			if len(ids) > maxRows {
				ids = ids[:maxRows]
				v.Truncated = true
			}
			for j := range ids {
				ids[j] = bounded(ids[j], &v.Truncated)
			}
			cv.Owners = append(cv.Owners, OwnerView{metadata.clean(o.Path, &v.Truncated), bounded(string(o.Side), &v.Truncated), bounded(string(o.Status), &v.Truncated), metadata.clean(o.LastMatchingEntry, &v.Truncated), ids, o.Eligible.Resolved})
		}

		if !c.InventoryComplete {
			warn("unknown_inventory", "Split the change until a complete inventory can be recorded.", "split_change")
		}
		if !c.HunksComplete {
			warn("incomplete_hunks", "Review the complete diff; retained hunks are incomplete.", "review_complete_diff")
		}
		if !c.CodeOwnersKnown || !c.PolicyOwnersKnown {
			warn("unknown_owners", "Resolve governing CODEOWNERS and record complete owner evidence.", "resolve_owners")
		}
		names := make([]string, 0, len(c.Facts))
		for n := range c.Facts {
			names = append(names, n)
		}
		slices.Sort(names)
		for _, n := range names[:min(len(names), maxRows)] {
			l := LabelView{Name: bounded(n, &v.Truncated), Truth: truthText(uint8(c.Facts[n])), Reasons: []string{}}
			for _, r := range c.Reasons {
				if r.Label == n {
					for _, reason := range r.Reasons[:min(len(r.Reasons), maxRows)] {
						l.Reasons = append(l.Reasons, metadata.clean(reason, &v.Truncated))
					}
					if len(r.Reasons) > maxRows {
						v.Truncated = true
					}
				}
			}
			cv.Labels = append(cv.Labels, l)
		}
		if len(names) > maxRows {
			v.Truncated = true
		}
		for _, file := range c.Files[:min(len(c.Files), maxRows)] {
			row := FileView{OldPath: metadata.clean(file.OldPath, &v.Truncated), NewPath: metadata.clean(file.NewPath, &v.Truncated), Hunks: []HunkView{}, Complete: file.Hunks.Complete, Omissions: []OmissionView{}}
			for _, h := range file.Hunks.Hunks[:min(len(file.Hunks.Hunks), 8)] {
				row.Hunks = append(row.Hunks, HunkView{h.OldStart, h.OldLines, h.NewStart, h.NewLines, hunks.clean(h.Text, &v.Truncated)})
			}
			if len(file.Hunks.Hunks) > 8 {
				v.Truncated = true
			}
			for _, o := range file.Hunks.Omitted[:min(len(file.Hunks.Omitted), maxRows)] {
				row.Omissions = append(row.Omissions, OmissionView{o.OldStart, o.OldLines, o.NewStart, o.NewLines, metadata.clean(o.Reason, &v.Truncated), o.Count})
			}
			if len(file.Hunks.Omitted) > maxRows {
				v.Truncated = true
			}
			cv.Files = append(cv.Files, row)
		}
		if len(c.Files) > maxRows {
			v.Truncated = true
		}
	}
	if f.Obligations != nil {
		v.Obligations = []ObligationView{}
	}
	for index, o := range f.Obligations {
		code := o.Code
		switch code {
		case "met", "approval_required", "unknown_inventory", "unknown_owners", "unknown_eligibility", "excluded_owner":
		default:
			code = "unknown_eligibility"
		}
		clears, action := clearing(code)
		if !o.Met {
			warn(code, clears, action)
		}
		if index >= maxRows {
			v.Truncated = true
			continue
		}
		ids := append([]string{}, o.ApprovedIDs...)
		slices.Sort(ids)
		if len(ids) > maxRows {
			ids = ids[:maxRows]
			v.Truncated = true
		}
		for j := range ids {
			ids[j] = bounded(ids[j], &v.Truncated)
		}
		eligible := append([]string{}, o.EligibleIDs...)
		slices.Sort(eligible)
		if len(eligible) > maxRows {
			eligible = eligible[:maxRows]
			v.Truncated = true
		}
		for j := range eligible {
			eligible[j] = bounded(eligible[j], &v.Truncated)
		}
		path := metadata.clean(o.Path, &v.Truncated)
		v.Obligations = append(v.Obligations, ObligationView{o.Requirement.Human, eligible, bounded(code, &v.Truncated), path, bounded(string(o.Side), &v.Truncated), bounded(string(o.Requirement.Selector), &v.Truncated), bounded(o.Requirement.Group, &v.Truncated), o.Requirement.Count, o.Requirement.Receipt, o.Met, obligations.clean(o.Explanation, &v.Truncated), clears, ids, Step{Action: action, Path: path}})
	}
	if p := f.Presentation; p != nil {
		if p.Stances != nil {
			v.Stances = []StanceView{}
		}
		if p.Tests != nil {
			v.Tests = []TestView{}
		}
		if p.Progress != nil {
			v.Progress = &ProgressView{p.Progress.Position, bounded(p.Progress.Status, &v.Truncated), bounded(p.Progress.Reason, &v.Truncated)}
		}
		if p.Saturated {
			warn("approval_saturated", "Record a new revision and fresh reviews or receipts to recover source capacity; retained barriers and command pins remain pinned, so the change may remain saturated because capacity cannot recover.", "new_revision")
		}
		if p.RequirementsMissing {
			v.Obligations = nil
		}
		knownStances, ambiguousStances := 0, 0
		for _, s := range p.Stances {
			step := Step{Action: "none"}
			userID := bounded(s.UserID, &v.Truncated)
			if s.Revision != 0 && (s.Kind == "withdraw" || s.Kind == "request_changes") {
				step = Step{Action: "new_revision", UserID: userID}
				if bound, ok := sourceBound(s); ok {
					step.Action = "reobserve_then_fresh_approval"
					step.After = bound.UTC().Format(time.RFC3339Nano)
				}
				warn("stance_barrier", "Re-observe the blocker; accept a fresh review or receipt strictly after its source-time bound with no newer blocker. Without a comparable bound, use a new revision and fresh approval.", "resolve_stance_barrier")
			}
			if s.Revision == 0 {
				warn("ambiguous_attribution", "Record unambiguous source attribution before relying on this evidence for approval.", "new_revision")
				ambiguousStances++
				if ambiguousStances > maxRows {
					v.Truncated = true
					continue
				}
			} else {
				knownStances++
				if knownStances > maxRows {
					v.Truncated = true
					continue
				}
			}
			v.Stances = append(v.Stances, StanceView{userID, bounded(s.Kind, &v.Truncated), bounded(s.Channel, &v.Truncated), bounded(s.SourceID, &v.Truncated), bounded(s.Head, &v.Truncated), s.Revision, recordedTime(s.Time, s.TimeKnown), missing(bounded(s.Precision, &v.Truncated)), recordedTime(s.UpperBound, !s.UpperBound.IsZero()), missing(bounded(s.IntentDigest, &v.Truncated)), missing(bounded(s.BriefDigest, &v.Truncated)), step, ""})
			if s.Revision == 0 {
				v.Stances[len(v.Stances)-1].Attribution = "not counted: ambiguous attribution"
			}
		}
		for index, t := range p.Tests {
			link := t.URL
			u, err := url.Parse(link)
			if err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "" || u.User != nil {
				link = ""
			}
			if !p.SupersededTests[index] && failedCheckStatus(t.Status) && p.Progress != nil && (p.Progress.Reason == "checks-failed" || p.Progress.Reason == "checks-timeout") {
				warnStep("check_failed", "Resolve the recorded check before relying on this brief for approval.", Step{Action: "fix_check", Name: missing(tests.clean(t.Name, &v.Truncated)), URL: tests.clean(link, &v.Truncated), Commit: missing(bounded(t.SHA, &v.Truncated))})
			}
			if index >= maxRows {
				v.Truncated = true
				continue
			}
			name := tests.clean(t.Name, &v.Truncated)
			status := bounded(t.Status, &v.Truncated)
			sha := bounded(t.SHA, &v.Truncated)
			cleanLink := tests.clean(link, &v.Truncated)
			v.Tests = append(v.Tests, TestView{missing(name), missing(cleanLink), missing(status), missing(sha)})
		}
	}
	if v.Obligations != nil {
		approvalsRequired := f.ApprovalsRequired
		v.ApprovalsRequired = &approvalsRequired
	}
	if v.Obligations != nil && (f.Presentation == nil || !f.Presentation.RequirementsMissing) {
		if digest, err := Digest(f); err == nil {
			v.Digest = digest
		}
	}
	if v.Intent == nil || v.Classification == nil {
		warn("not_recorded", "Record missing evidence before relying on this brief for approval.", "record_evidence")
	}
	if redacted {
		warn("display_redacted", "Use authorized access to the recorded evidence to inspect the original content.", "read_recorded_evidence")
	}
	if v.Truncated {
		warn("display_truncated", "Read the retained journal evidence for omitted display content; do not treat this view as a complete diff.", "read_recorded_evidence")
	}
	if audience == "agent" {
		for i := range v.Obligations {
			v.Obligations[i].Explanation = ""
			v.Obligations[i].Clears = ""
		}
		for i := range v.Warnings {
			v.Warnings[i].Clears = ""
			v.Warnings[i].Detail = ""
		}
	}
	return v, nil
}
func missing(s string) string {
	if s == "" {
		return NotRecorded
	}
	return s
}

func truthText(truth uint8) string {
	switch truth {
	case 1:
		return "false"
	case 2:
		return "true"
	default:
		return "unknown"
	}
}

func recordedTime(t time.Time, known bool) string {
	if !known || t.IsZero() {
		return NotRecorded
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func sourceBound(s StanceEvidence) (time.Time, bool) {
	if !s.UpperBound.IsZero() {
		return s.UpperBound, true
	}
	if s.TimeKnown && !s.Time.IsZero() {
		return s.Time, true
	}
	return time.Time{}, false
}

func failedCheckStatus(status string) bool {
	switch strings.ToLower(status) {
	case "failure", "failed", "error", "cancelled", "canceled", "timeout", "timed_out", "timed-out", "action_required", "action-required":
		return true
	default:
		return false
	}
}

func clearing(code string) (string, string) {
	switch code {
	case "met":
		return "Satisfied by recorded approval progress.", "none"
	case "unknown_inventory":
		return "Split the change and record a complete inventory.", "split_change"
	case "unknown_owners":
		return "Resolve governing owners and record owner evidence.", "resolve_owners"
	case "excluded_owner":
		return "Obtain an eligible independent owner's approval.", "independent_owner_approval"
	case "unknown_eligibility":
		return "Record resolved identity, membership and writer eligibility.", "record_eligibility"
	case "approval_required":
		return "Obtain the required eligible approvals; receipt requirements need the exact current brief digest.", "wait_for_approval"
	default:
		return "Obtain the required eligible approvals; receipt requirements need the exact current brief digest.", "wait_for_approval"
	}
}

// BoundDisplayText applies the per-field display bound to supplemental text.
// Callers must redact the complete input before bounding it.
func BoundDisplayText(s string, truncated *bool) string {
	return bounded(s, truncated)
}

func bounded(s string, truncated *bool) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' || unicode.In(r, unicode.Cf) {
			return '�'
		}
		return r
	}, s)
	// Bound the JSON-escaped size as well as raw UTF-8. HTML characters can
	// expand sixfold; a raw-byte bound alone would exceed the response budget.
	budget := maxText - len(" [truncated]")
	used := 0
	for index, r := range s {
		cost := utf8.RuneLen(r)
		switch r {
		case '<', '>', '&', '\u2028', '\u2029':
			cost = 6
		case '\n', '\r', '\t', '"', '\\':
			cost = 2
		}
		if used+cost > budget {
			*truncated = true
			return s[:index] + " [truncated]"
		}
		used += cost
	}
	return s
}

// Render consumes the same DTO as JSON; all untrusted strings use JSON string
// escaping, including HTML characters and control bytes. Operator ordering puts
// queue progress and recovery before the review evidence.
func Render(v View) ([]byte, error) {
	if v.Audience == "agent" {
		return json.MarshalIndent(v, "", "  ")
	}
	if !ValidAudience(v.Audience) {
		return nil, fmt.Errorf("invalid audience")
	}
	var b strings.Builder
	quote := func(s string) string { raw, _ := json.Marshal(s); return string(raw) }
	fmt.Fprintf(&b, "Change %s (%s)\nDigest: %s\n", quote(v.ChangeID), v.Audience, quote(v.Digest))
	warnings := func() {
		fmt.Fprintln(&b, "\nWarnings and what clears them:")
		if len(v.Warnings) == 0 {
			fmt.Fprintln(&b, "  none recorded")
		}
		for _, w := range v.Warnings {
			if w.Detail == "" {
				fmt.Fprintf(&b, "  %s\n", quote(w.Code))
			} else {
				fmt.Fprintf(&b, "  %s: %s\n", quote(w.Code), quote(w.Detail))
			}
			if w.Clears != "" {
				fmt.Fprintf(&b, "    What clears it: %s\n", quote(w.Clears))
			}
		}
	}
	if v.Audience == "reviewer" {
		warnings()
	}
	fmt.Fprintln(&b, "\nQueue progress:")
	if v.Progress == nil {
		fmt.Fprintln(&b, NotRecorded)
	} else {
		fmt.Fprintf(&b, "  position %d; status %s; reason %s\n", v.Progress.Position, quote(v.Progress.Status), quote(v.Progress.Reason))
	}
	if v.Audience == "operator" {
		warnings()
	}
	fmt.Fprintln(&b, "\nClaimed intent (submitter's claim):")
	if v.Intent == nil {
		fmt.Fprintln(&b, NotRecorded)
	} else {
		fmt.Fprintf(&b, "  Title: %s\n  Body: %s\n", quote(v.Intent.Title), quote(v.Intent.Body))
	}
	fmt.Fprintln(&b, "\nClassification and retained diff:")
	if c := v.Classification; c == nil {
		fmt.Fprintln(&b, NotRecorded)
	} else {
		fmt.Fprintf(&b, "  Revision %d; generation %d; classifier %s\n  Head %s; base %s; merge base %s\n  Policy %s\n  Inventory complete: %t; hunks complete: %t\n", c.Revision, c.Generation, quote(c.ClassifierVersion), quote(c.Head), quote(c.Base), quote(c.MergeBase), quote(c.PolicyDigest), c.InventoryComplete, c.HunksComplete)
		for _, l := range c.Labels {
			fmt.Fprintf(&b, "  Label %s: truth=%s\n", quote(l.Name), quote(l.Truth))
			for _, r := range l.Reasons {
				fmt.Fprintf(&b, "    Reason: %s\n", quote(r))
			}
		}
		for _, f := range c.Files {
			fmt.Fprintf(&b, "  File %s -> %s; hunks complete: %t\n", quote(f.OldPath), quote(f.NewPath), f.Complete)
			for _, h := range f.Hunks {
				fmt.Fprintf(&b, "    Hunk %d+%d -> %d+%d:\n", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
				for _, line := range strings.Split(strings.TrimSuffix(h.Text, "\n"), "\n") {
					fmt.Fprintf(&b, "      %s\n", quote(line))
				}
			}
		}
		for _, f := range c.Files {
			for _, o := range f.Omissions {
				fmt.Fprintf(&b, "    Omitted range: old %d+%d; new %d+%d; reason %s; count %d\n", o.OldStart, o.OldLines, o.NewStart, o.NewLines, quote(o.Reason), o.Count)
			}
		}
		for _, o := range c.Owners {
			fmt.Fprintf(&b, "  Owner %s (%s): %s; resolved: %t; entry %s\n", quote(o.Path), quote(o.Side), quote(o.Status), o.Resolved, quote(o.Entry))
			for _, id := range o.IDs {
				fmt.Fprintf(&b, "    Eligible ID: %s\n", quote(id))
			}
		}
	}
	fmt.Fprintln(&b, "\nApproval obligations:")
	if v.Obligations == nil {
		fmt.Fprintln(&b, NotRecorded)
	}
	for _, o := range v.Obligations {
		fmt.Fprintf(&b, "  %s: %s (%s); selector %s; group %s; count %d; receipt: %t; met: %t\n    %s\n    What clears it: %s\n", quote(o.Code), quote(o.Path), quote(o.Side), quote(o.Selector), quote(o.Group), o.Count, o.Receipt, o.Met, quote(o.Explanation), quote(o.Clears))
		fmt.Fprintf(&b, "    Human required: %t\n", o.Human)
		for _, id := range o.EligibleIDs {
			fmt.Fprintf(&b, "    Eligible ID: %s\n", quote(id))
		}
		for _, id := range o.ApprovedIDs {
			fmt.Fprintf(&b, "    Approved ID: %s\n", quote(id))
		}
	}
	fmt.Fprintln(&b, "\nStance and provenance:")
	if v.Stances == nil {
		fmt.Fprintln(&b, NotRecorded)
	} else if len(v.Stances) == 0 {
		fmt.Fprintln(&b, "  none recorded")
	}
	for _, s := range v.Stances {
		if s.Attribution != "" {
			fmt.Fprintf(&b, "  %s\n", quote(s.Attribution))
		}
		fmt.Fprintf(&b, "  %s: %s via %s source %s; revision %d; head %s\n    Source time %s; precision %s; upper bound %s\n    Intent binding: %s; brief binding: %s\n    Next step: %s; source time strictly after: %s\n", quote(s.UserID), quote(s.Kind), quote(s.Channel), quote(s.SourceID), s.Revision, quote(s.Head), quote(s.SourceTime), quote(s.Precision), quote(s.UpperBound), quote(s.IntentDigest), quote(s.BriefDigest), quote(s.NextStep.Action), quote(missing(s.NextStep.After)))
	}
	fmt.Fprintln(&b, "\nRecorded tests:")
	if v.Tests == nil {
		fmt.Fprintln(&b, NotRecorded)
	}
	for _, t := range v.Tests {
		fmt.Fprintf(&b, "  %s: %s; tested SHA %s; %s\n", quote(t.Name), quote(t.Status), quote(t.SHA), quote(t.URL))
	}
	return []byte(b.String()), nil
}
