package main

import (
	"context"
	"errors"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/github"
	"github.com/CanadaApollo6/openmerge/internal/gitops"
	"github.com/CanadaApollo6/openmerge/internal/serve"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"log/slog"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

type installationBundle struct {
	driver       *github.Driver
	git          *gitops.Repo
	authorityGit *gitops.Repo
}

type bundleFailure struct {
	err   error
	next  time.Time
	count int
}

type installationTargets struct {
	mu       sync.Mutex
	locks    sync.Map
	app      *github.App
	options  serveOptions
	logger   *slog.Logger
	bundles  map[string]*installationBundle
	failures map[string]bundleFailure
}

func (f *installationTargets) bundle(ctx context.Context, id string) (result *installationBundle, resultErr error) {
	guard, _ := f.locks.LoadOrStore(id, &sync.Mutex{})
	lock := guard.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	f.mu.Lock()
	cached := f.bundles[id]
	failure := f.failures[id]
	f.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	if time.Now().Before(failure.next) {
		return nil, failure.err
	}
	defer func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if resultErr == nil {
			delete(f.failures, id)
			return
		}
		if ctx.Err() != nil {
			return
		}
		failure.count++
		failure.err = resultErr
		failure.next = time.Now().Add(min(5*time.Second*time.Duration(1<<min(failure.count-1, 6)), 5*time.Minute))
		var rate *github.RateLimitError
		if errors.As(resultErr, &rate) {
			failure.next = rate.RetryAt
		}
		if f.failures == nil {
			f.failures = make(map[string]bundleFailure)
		}
		f.failures[id] = failure
	}()
	number, err := strconv.ParseInt(id, 10, 64)
	if err != nil || number <= 0 {
		return nil, errors.New("invalid stored installation ID")
	}
	driver, err := f.app.Installation(ctx, number)
	if err != nil {
		return nil, err
	}
	committer := serveCommitter(ctx, f.options, f.logger, driver)
	git, err := gitops.New(ctx, gitops.Config{WorkDir: filepath.Join(f.options.cache, id), Remote: driver.Remote, HeadRef: github.HeadRef, PullRequest: driver.PullRequestMetadata, CommitAuthors: driver.CommitAuthors, Committer: committer})
	if err != nil {
		return nil, err
	}
	bundle := &installationBundle{driver: driver, git: git}
	if f.options.authorityPlan != "" {
		bundle.authorityGit, err = gitops.New(ctx, gitops.Config{WorkDir: filepath.Join(f.options.cache, "authority-completion", id), Remote: driver.Remote, HeadRef: github.HeadRef})
		if err != nil {
			return nil, err
		}
	}
	f.mu.Lock()
	f.bundles[id] = bundle
	f.mu.Unlock()
	return bundle, nil
}

func (f *installationTargets) target(ctx context.Context, key store.QueueKey) (serve.Target, error) {
	bundle, err := f.bundle(ctx, key.Installation)
	if err != nil {
		return serve.Target{}, err
	}
	return bundle.target(key), nil
}

func (f *installationTargets) outsidePushHistory(ctx context.Context, key store.QueueKey) (forge.OutsidePushHistory, error) {
	number, err := strconv.ParseInt(key.Installation, 10, 64)
	if err != nil || number <= 0 {
		return nil, errors.New("invalid stored installation ID")
	}
	// This provider exposes only Git history. No committer discovery, PR
	// metadata, or other installation-authenticated API callbacks are wired.
	return gitops.New(ctx, gitops.Config{
		WorkDir: filepath.Join(f.options.cache, key.Installation),
		Remote:  f.app.HistoryRemote(number), HeadRef: github.HeadRef,
	})
}

func (b *installationBundle) target(key store.QueueKey) serve.Target {
	ref := forge.RepoRef{Path: key.Repository}
	return serve.Target{Key: key, Driver: b.driver, Git: b.git, AuthorityCompletionGit: b.authorityGit, BotLogin: b.driver.BotLogin(), CheckRules: func(ctx context.Context) ([]string, []string, error) {
		return b.driver.ValidateRepositoryRules(ctx, ref, key.BaseBranch)
	}, CheckPermissions: b.driver.ValidateInstallationPermissions, CheckProtection: func(ctx context.Context, method forge.MergeMethod) ([]string, []string, error) {
		return b.driver.ValidateClassicProtection(ctx, ref, key.BaseBranch, method)
	}}
}

func (f *installationTargets) budget(id string) status.Budget {
	f.mu.Lock()
	bundle := f.bundles[id]
	f.mu.Unlock()
	if bundle == nil {
		return status.Budget{}
	}
	budget := bundle.driver.Budget()
	return status.Budget{Known: budget.Known, Limit: budget.Limit, Remaining: budget.Remaining, SettlementReserve: budget.SettlementReserve, ResetAt: budget.ResetAt}
}
