package dependency

import "maps"

// DiagnosticRecorded preserves a typed observation explanation without changing
// the deployed Observed payload. It is historical presentation evidence only;
// the observation remains the sole input to dependency gates and clocks.
type DiagnosticRecorded struct {
	Namespace          string       `json:"journal_namespace"`
	ObservationEventID uint64       `json:"observation_event_id"`
	Source             SourceAlias  `json:"source"`
	Evidence           ReadEvidence `json:"evidence"`
}

func (DiagnosticRecorded) dependencyEvent() {}

// Validate restricts diagnostics to known observer outcomes and their exact fold
// result. Failed permission reads may record unavailable evidence, never success.
func (e DiagnosticRecorded) Validate() error {
	o := e.Evidence.Observation
	if !validDigestID(e.Namespace, "omj1_") || e.Namespace != o.Namespace || e.ObservationEventID == 0 || !e.Source.valid() || o.Validate() != nil || e.Evidence.Declaration != o.Declaration {
		return ErrMalformed
	}
	if e.Evidence.Stage != BindingRead && e.Evidence.Stage != WorkRead && e.Evidence.Stage != LandingStartRead {
		return ErrMalformed
	}
	result := "unavailable"
	switch e.Evidence.Code {
	case "dependency-waiting":
		result = "waiting"
	case "dependency-stale":
		result = "stale"
	case "dependency-landed":
		result = "landed"
	case "dependency-unavailable", "dependency-out-of-scope", "dependency-unresolved", "dependency-source-retargeted", "dependency-not-on-base", "dependency-unverified", "dependency-closed":
	default:
		return ErrMalformed
	}
	if o.Result != result || e.Source.ForgeInstance != o.Alias.ForgeInstance || e.Source.Installation != o.Alias.Installation {
		return ErrMalformed
	}
	g := e.Evidence.Grant
	if g.RepositoryID != e.Source.RepositoryID || !cleanText(g.SubjectID) || !cleanText(g.ReturnedID) || !cleanText(g.ReadID) {
		return ErrMalformed
	}
	if e.Evidence.Code != "dependency-unavailable" && !g.valid(e.Source.RepositoryID) {
		return ErrMalformed
	}
	return nil
}

// Scoped returns a fail-closed wrapper. Callers must validate the diagnostic's
// journal association with Apply before using it; malformed values remain generic.
// No diagnostic is retained in planner snapshots: status replays full journals.
func (e DiagnosticRecorded) Scoped() ScopedObservation {
	if e.Validate() != nil {
		return ScopedObservation{}
	}
	return ScopedObservation{evidence: e.Evidence, repositoryID: e.Source.RepositoryID}
}

func (s State) validateDiagnostic(id uint64, e DiagnosticRecorded) error {
	o := e.Evidence.Observation
	current, eventID, ok := s.Observation(o)
	if !ok || eventID != e.ObservationEventID || current != o || e.ObservationEventID >= id || e.Evidence.Declaration != s.Prior(o.Alias) {
		return ErrStale
	}
	return e.Source.validate(s.initialization.scope())
}

// DiagnosticProjection is rebuilt from a complete journal for presentation. It
// deliberately has no snapshot codec; absent companion evidence fails closed.
// Values are immutable through Apply and Observation.
type DiagnosticProjection struct {
	records map[string]DiagnosticRecorded
}

// Apply accepts only a validated companion for the state's exact current
// observation. It cannot change dependency eligibility or queue authority.
func (p DiagnosticProjection) Apply(s State, id uint64, e DiagnosticRecorded) (DiagnosticProjection, error) {
	if e.Validate() != nil {
		return p, ErrMalformed
	}
	if s.initialization == nil || e.Namespace != s.initialization.Namespace {
		return p, ErrOutOfScope
	}
	if err := s.validateDiagnostic(id, e); err != nil {
		return p, err
	}
	next := DiagnosticProjection{records: maps.Clone(p.records)}
	if next.records == nil {
		next.records = map[string]DiagnosticRecorded{}
	}
	next.records[e.Evidence.Observation.key()] = e
	if len(next.records) > maxProjectionEntries*MaxDirectEdges {
		return p, ErrLimit
	}
	return next, nil
}

// Observation returns only the companion matching the current recorded side fact.
// A replaced observation or missing companion must not reuse an old explanation.
func (p DiagnosticProjection) Observation(s State, read Observed) (ScopedObservation, bool) {
	e, ok := p.records[read.key()]
	current, eventID, found := s.Observation(read)
	if !ok || !found || eventID != e.ObservationEventID || current != read || e.Evidence.Observation != current || e.Evidence.Declaration != s.Prior(read.Alias) {
		return ScopedObservation{}, false
	}
	return e.Scoped(), true
}
