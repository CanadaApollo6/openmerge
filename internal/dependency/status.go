package dependency

import (
	"fmt"
	"slices"
)

// StatusView contains only audience-safe presentation, never planner authority.
// Redacted is adapter metadata and must not be serialized as source evidence.
type StatusDiagnostic struct {
	Code   string
	Source string
}

type StatusView struct {
	Diagnostics []StatusDiagnostic
	Text        string
	Details     []string
	URL         string
	Redacted    bool
	Blocking    bool
}

// Status projects one exact current declaration using recorded audience grants keyed
// by source reference. An incomplete audience or missing diagnostic hides the
// entire dependency view, including landed edges and the number of blockers.
// No service credential, consumer writer grant or display audience implies access.
func (s State) Status(changeID string, revision uint64, p DiagnosticProjection, audience map[string][]Grant) (StatusView, bool) {
	d, ok := s.declarationFor(changeID)
	if !ok || d.Event.Revision != revision {
		return StatusView{}, false
	}
	if len(d.Event.Edges) == 0 && !d.Held && !d.Unknown {
		return StatusView{}, false
	}
	generic := StatusView{Text: "Dependency cannot be verified", Redacted: true, Blocking: true}
	view := StatusView{Text: "Waiting for dependencies"}
	authorized := true
	for _, edge := range d.Event.Edges {
		read, _, ok := s.Observation(Observed{Alias: d.Event.Alias, ChangeID: d.Event.ChangeID, Revision: d.Event.Revision, Source: edge.Reference})
		scoped, found := p.Observation(s, read)
		v := scoped.View(audience[edge.Reference])
		if !ok || !found || v.Code == "" {
			authorized = false
			continue
		}
		// Landed facts remain visible in authorized detail, but never supply a
		// success signal in the generic view above.
		detail := v.Code + ": " + edge.Reference
		ref, err := ParseReference(edge.Reference, s.initialization.scope())
		if err == nil && ref.Revision > 0 {
			detail += fmt.Sprintf(" at revision %d", ref.Revision)
		}
		view.Details = append(view.Details, detail)
		view.Diagnostics = append(view.Diagnostics, StatusDiagnostic{Code: v.Code, Source: edge.Reference})
	}
	if !authorized {
		return generic, true
	}
	admission := s.Admission(d.Event.ChangeID, d.Event.Revision)
	view.Blocking = admission.Code != ""
	if admission.Code == "" {
		view.Text = "Dependencies verified"
	} else {
		view.Details = append(view.Details, admission.Code)
	}
	if d.Held || d.Unknown {
		view.Text = "Dependency declaration requires acknowledgement"
	}
	view.Details = slices.Clone(view.Details)
	return view, true
}
