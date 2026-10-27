package provenance

// CommitMapping comes from the trusted composer, never replay trailer text.
// More than one original can map to the same squash commit.
type CommitMapping struct{ Original, Build string }

// BuildObservation identifies one composition. Tested and landed observations
// are separate records, even when the same change revision supplies originals.
// Marker is an optional synthetic fast-forward test marker, never a contributor.
// Replays is the composer's explicit mapping for newly replayed originals.
type BuildObservation struct {
	ID, Stage, Method, Base, Result, Marker string
	Replays                                 []CommitMapping
}

// BuildMapping binds one change's source observation to one local composition.
// It retains the original inventory generation and parser policy identity.
type BuildMapping struct {
	Request    InventoryRequest
	PolicyID   string
	Build      BuildObservation
	Commits    []CommitMapping
	Complete   bool
	Diagnostic Diagnostic
}
