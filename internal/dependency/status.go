package dependency

import (
	"fmt"
	"slices"
)

// MaxStatusDetails bounds an authorized summary. Complete evidence remains in
// the paginated scoped export; the summary states how many lines it omits.
const MaxStatusDetails = 8

// MultiWriterWarning is the nonblocking notice on an opted-in hold or completed
// self-acknowledgement when the repository records more than one writer.
const MultiWriterWarning = "Single-maintainer self-acknowledgement is enabled although this repository has multiple writers; an independent writer acknowledgement is preferred (nonblocking)"

// StatusDiagnostic is a typed source explanation for an authorized reader.
type StatusDiagnostic struct {
	Code   string
	Source string
}

// SelfAcknowledgementView is a completed single-maintainer acknowledgement. All
// fields are consumer-repository facts. EventID is a journal reference: callers
// must omit it whenever the queue's journal positions are withheld.
type SelfAcknowledgementView struct {
	Kind            string
	EventID         uint64
	Actor           string
	Overridden      []string
	GoverningCommit string
	PolicyDigest    string
	Revoked         bool
}

// StatusView contains only audience-safe presentation, never planner authority.
// Redacted is adapter metadata and must not be serialized as source evidence.
type StatusView struct {
	Diagnostics         []StatusDiagnostic
	Text                string
	Details             []string
	URL                 string
	Redacted            bool
	Blocking            bool
	SelfAcknowledgement *SelfAcknowledgementView
	Warnings            []string
}

// Status projects one exact current declaration using recorded audience grants keyed
// by source reference. An incomplete audience or missing diagnostic hides the
// entire dependency view, including landed edges and the number of blockers.
// No service credential, consumer writer grant or display audience implies access.
// A completed self-acknowledgement stays visible after its hold clears, distinctly
// from independent acknowledgement, but never inside the generic view.
func (s State) Status(changeID string, revision uint64, p DiagnosticProjection, audience map[string][]Grant) (StatusView, bool) {
	d, ok := s.declarationFor(changeID)
	if !ok || d.Event.Revision != revision {
		return StatusView{}, false
	}
	self := d.Ack != nil && d.Ack.Kind == selfAcknowledgementKind
	if len(d.Event.Edges) == 0 && !d.Held && !d.Unknown && !self {
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
	policy := s.policy(d.Event).Event
	if self {
		a := d.Ack
		view.SelfAcknowledgement = &SelfAcknowledgementView{Kind: a.Kind, EventID: a.EventID, Actor: a.Event.Writer.SubjectID, Overridden: slices.Clone(a.Overridden), GoverningCommit: a.Event.GoverningCommit, PolicyDigest: a.Event.PolicyDigest, Revoked: a.Revoked}
		state := "self-acknowledged by " + a.Event.Writer.SubjectID + " under the single-maintainer opt-in"
		if a.Revoked {
			state += " (revoked: the opt-in no longer governs this repository)"
		}
		view.Details = append(view.Details, state)
		if len(d.Event.Edges) == 0 && !view.Blocking {
			view.Text = "Dependency declaration self-acknowledged"
		}
	}
	if d.Held || d.Unknown {
		view.Text = "Dependency declaration requires acknowledgement"
		if policy.selfAckEnabled() && !d.Unknown {
			view.Details = append(view.Details, "an authenticated writer may self-acknowledge under the single-maintainer opt-in")
		}
	}
	if (d.Held || d.Unknown || self) && s.Hold(d.Event.Alias).MultiWriterWarning {
		view.Warnings = []string{MultiWriterWarning}
	}
	view.Details = BoundDetails(view.Details)
	return view, true
}

// BoundDetails returns at most MaxStatusDetails lines, replacing the excess
// with a count. It never fabricates a line for a short summary.
func BoundDetails(details []string) []string {
	if n := len(details); n > MaxStatusDetails {
		return append(slices.Clone(details[:MaxStatusDetails-1]), fmt.Sprintf("%d more details in the paginated authorized dependency export", n-MaxStatusDetails+1))
	}
	return slices.Clone(details)
}
