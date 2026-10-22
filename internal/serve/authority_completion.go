package serve

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// authorityCompletionStore keeps the repository-wide source explicit. Production
// installs the actual Store; tests may wrap it to count calls without replacing
// its all-partition snapshot with per-queue receipts.
type authorityCompletionStore interface {
	RepositoryAuthorityCompletion(context.Context, store.AuthorityCompletionRequest, store.AuthorityBranchInventory) (store.AuthorityCompletion, error)
}

// The status API only serves completion through this interface; signature drift
// would otherwise silently turn the production endpoint into a 503.
var _ status.AuthorityCompletionSource = (*Server)(nil)

// AuthorityCompletion performs fresh discovery on both sides of the durable
// repository snapshot. It never treats this process's worker list as exhaustive,
// installs authority hooks, or caches a successful/failed completion.
func (s *Server) AuthorityCompletion(ctx context.Context, request store.AuthorityCompletionRequest) (store.AuthorityCompletion, error) {
	if err := request.Validate(); err != nil {
		return store.AuthorityCompletion{}, err
	}
	ctx = forge.WithRequestClass(ctx, forge.DiscoveryRequest)
	repo, err := s.authorityCompletionRepository(ctx, request)
	if err != nil {
		return store.AuthorityCompletion{}, err
	}
	target := repo.Target("")
	if repo.Branches == nil || target.Driver == nil || target.Git == nil {
		return store.AuthorityCompletion{}, errors.New("authority completion discovery unavailable")
	}
	reader := target.AuthorityCompletionGit
	if reader == nil {
		reader = target.Git
	}
	tip := func() (string, string, error) {
		branch, err := target.Driver.DefaultBranch(ctx, forge.RepoRef{Path: repo.Name})
		if err != nil {
			return "", "", err
		}
		sha, err := reader.ReadRef(ctx, forge.RepoRef{Path: repo.Name}, branch)
		return branch, sha, err
	}
	branch, sha, err := tip()
	if err != nil || sha == "" {
		return store.AuthorityCompletion{}, errors.New("authority completion governing ref unavailable")
	}
	before, err := repo.Branches(ctx)
	if err != nil {
		return store.AuthorityCompletion{}, errors.New("authority completion branch inventory unavailable")
	}
	before = slices.Clone(before)
	source := s.authorityCompletionStore
	if source == nil {
		source = s.store
	}
	result, err := source.RepositoryAuthorityCompletion(ctx, request, store.AuthorityBranchInventory{DefaultBranch: branch, DefaultTip: sha, Branches: before})
	if err != nil {
		return store.AuthorityCompletion{}, errors.New("authority completion durable inventory unavailable")
	}
	after, err := repo.Branches(ctx)
	if err != nil {
		return store.AuthorityCompletion{}, errors.New("authority completion branch inventory unavailable")
	}
	nextBranch, nextSHA, err := tip()
	if err != nil || nextBranch != branch || nextSHA != sha {
		return store.AuthorityCompletion{}, errors.New("authority completion governing ref changed; retry")
	}
	after = slices.Clone(after)
	slices.Sort(before)
	slices.Sort(after)
	if !slices.Equal(before, after) {
		return store.AuthorityCompletion{}, errors.New("authority completion branch inventory changed; retry")
	}
	return result, nil
}

func (s *Server) authorityCompletionRepository(ctx context.Context, request store.AuthorityCompletionRequest) (Repository, error) {
	repos := s.repositories
	if s.discovery != nil {
		installations, err := s.discovery.Installations(ctx)
		if err != nil {
			return Repository{}, errors.New("authority completion installation inventory unavailable")
		}
		repos = nil
		for _, installation := range installations {
			if installation.Suspended || installation.Repositories == nil {
				continue
			}
			listed, err := installation.Repositories(ctx)
			if err != nil {
				return Repository{}, errors.New("authority completion repository inventory unavailable")
			}
			for _, repo := range listed {
				repo.Installation = installation.ID
				if strings.EqualFold(repo.Name, request.Repository) {
					if repo.Installation != request.Installation {
						return Repository{}, errors.New("authority completion requires complete discovery across installations")
					}
					if s.allowedRepository(repo) {
						repos = append(repos, repo)
					}
				}
			}
		}
	}
	var matched []Repository
	for _, repo := range repos {
		if strings.EqualFold(repo.Name, request.Repository) {
			if repo.Installation != request.Installation {
				return Repository{}, errors.New("authority completion requires complete discovery across installations")
			}
			matched = append(matched, repo)
		}
	}
	if len(matched) != 1 || matched[0].Target == nil {
		return Repository{}, errors.New("authority completion repository is not uniquely served")
	}
	return matched[0], nil
}
