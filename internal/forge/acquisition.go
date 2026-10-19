package forge

import "context"

// CommitAcquirer obtains full pinned commit objects and retains their reachable
// graphs until release. Callers must release after all cache-only reads finish.
// Acquisition must not substitute a mutable ref for a recorded commit ID.
type CommitAcquirer interface {
	AcquireCommits(context.Context, RepoRef, []string) (release func(), err error)
}
