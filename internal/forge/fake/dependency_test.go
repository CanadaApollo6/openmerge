package fake

import (
	"errors"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestDependencyObservationContract(t *testing.T) {
	dependencytest.Run(t, func(_ *testing.T) dependencytest.Env {
		f := New()
		repo := forge.RepoRef{Path: "acme/project"}
		return dependencytest.Env{
			Reader: f,
			SetGrant: func(user forge.UserIdentity, permission string) {
				f.SetIdentity(user)
				f.SetWriteGrant(repo, user.ID, permission)
			},
			SetSource:   func(source forge.DependencySource) { f.SetDependencySource(repo, source) },
			SourceReads: func() int { f.mu.Lock(); defer f.mu.Unlock(); return f.identityCalls["DependencySource"] },
			FailSource:  func() { f.FailNext("DependencySource", errors.New("source unavailable")) },
			FailGrant:   func() { f.FailNext("WriteGrant", errors.New("permission unavailable")) },
		}
	})
}
