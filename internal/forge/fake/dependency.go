package fake

import (
	"context"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

var _ forge.DependencyReader = (*Forge)(nil)

type dependencyKey struct {
	Repo   forge.RepoRef
	Number int
}

// SetDependencySource supplies a complete scoped observation independently of
// ordinary PR fixtures, so partial reads and identity swaps can be exercised.
func (f *Forge) SetDependencySource(repo forge.RepoRef, source forge.DependencySource) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dependencySources == nil {
		f.dependencySources = map[dependencyKey]forge.DependencySource{}
	}
	f.dependencySources[dependencyKey{repo, source.Number}] = source
}

// DependencySource reads the current fixture without enqueuing or changing it.
func (f *Forge) DependencySource(ctx context.Context, repo forge.RepoRef, number int) (forge.DependencySource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.identityCalls["DependencySource"]++
	if err := f.failure(ctx, "DependencySource"); err != nil {
		return forge.DependencySource{}, err
	}
	source, ok := f.dependencySources[dependencyKey{repo, number}]
	if !ok || !source.Valid() {
		return forge.DependencySource{}, forge.ErrIdentityUnavailable
	}
	return source, nil
}
