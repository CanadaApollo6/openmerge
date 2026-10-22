package serve

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

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
	tip := func() (string, string, error) {
		branch, err := target.Driver.DefaultBranch(ctx, forge.RepoRef{Path: repo.Name})
		if err != nil {
			return "", "", err
		}
		sha, err := target.Git.ReadRef(ctx, forge.RepoRef{Path: repo.Name}, branch)
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
	result, err := s.store.RepositoryAuthorityCompletion(ctx, request, before)
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
	before = slices.Clone(before)
	after = slices.Clone(after)
	slices.Sort(before)
	slices.Sort(after)
	if !slices.Equal(before, after) {
		return store.AuthorityCompletion{}, errors.New("authority completion branch inventory changed; retry")
	}
	for i := range result.Queues {
		q := &result.Queues[i]
		if q.Reason == "complete" && q.GoverningCommit != sha {
			q.Reason = "governing_commit_stale"
			result.Complete = false
		}
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
			if installation.ID != request.Installation || installation.Suspended || installation.Repositories == nil {
				continue
			}
			listed, err := installation.Repositories(ctx)
			if err != nil {
				return Repository{}, errors.New("authority completion repository inventory unavailable")
			}
			for _, repo := range listed {
				repo.Installation = installation.ID
				if s.allowedRepository(repo) {
					repos = append(repos, repo)
				}
			}
		}
	}
	var matched []Repository
	for _, repo := range repos {
		if repo.Installation == request.Installation && strings.EqualFold(repo.Name, request.Repository) {
			matched = append(matched, repo)
		}
	}
	if len(matched) != 1 || matched[0].Target == nil {
		return Repository{}, errors.New("authority completion repository is not uniquely served")
	}
	return matched[0], nil
}
