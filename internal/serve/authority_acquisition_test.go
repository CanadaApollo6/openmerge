package serve

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

type incompleteGoverningGit struct {
	forge.Git
	forge.GoverningFilesReader
	wrongCommit bool
}

func (g incompleteGoverningGit) GoverningFiles(ctx context.Context, repo forge.RepoRef, req forge.GoverningFilesRequest) (forge.GoverningFiles, error) {
	files, err := g.GoverningFilesReader.GoverningFiles(ctx, repo, req)
	if g.wrongCommit {
		files.CommitSHA = strings.Repeat("f", 40)
	} else {
		files.Coverage.Completeness = forge.DiffIncomplete
	}
	return files, err
}

func TestAuthorityGoverningAcquisitionBoundaries(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"discovery", "configuration"} {
		for _, failure := range []string{"none", "missing-owners", "uncovered", "unresolved", "incomplete-files", "wrong-commit", "incomplete-protection", "missing-protection", "read-secret"} {
			t.Run(boundary+"/"+failure, func(t *testing.T) {
				testSynctest(t, func(t *testing.T) {
					s, w, f, _ := authorityWorkerFixture(t)
					before := lifecycleState(t, s, w)
					switch failure {
					case "missing-owners":
						_, err := f.PushBase(repo, "main", "remove owners", map[string]string{"CODEOWNERS": ""})
						must(t, err)
					case "uncovered":
						_, err := f.PushBase(repo, "main", "uncovered", map[string]string{"CODEOWNERS": "/README.md @writer\n"})
						must(t, err)
					case "unresolved":
						_, err := f.PushBase(repo, "main", "unresolved", map[string]string{"CODEOWNERS": "* @missing\n"})
						must(t, err)
					case "incomplete-files", "wrong-commit":
						w.Git = incompleteGoverningGit{w.Git, f, failure == "wrong-commit"}
					case "incomplete-protection", "missing-protection", "read-secret":
						resolver := w.resolveAuthorityLifecycle
						w.resolveAuthorityLifecycle = func(ctx context.Context, c config.AuthorityCandidate, branch, tip string) (authorityLifecycleResolution, error) {
							r, err := resolver(ctx, c, branch, tip)
							r.Activation.Read = func(context.Context) (authority.Observation, error) {
								if failure == "read-secret" {
									return authority.Observation{}, errors.New("https://token:ghs_SECRET@example.test/")
								}
								return forge.EmptyWriteRestrictionObservation(), nil
							}
							if failure == "missing-protection" {
								r.Activation.Read = nil
							}
							return r, err
						}
					}
					var err error
					if boundary == "discovery" {
						target := w.Target
						s.repositories = []Repository{{Installation: w.Key.Installation, Name: w.Key.Repository, Branches: func(ctx context.Context) ([]string, error) { return f.ListBranches(ctx, repo) }, Target: func(base string) Target { target.Key.BaseBranch = base; return target }}}
						err = s.RefreshBranches(t.Context())
					} else {
						err = s.configuration(t.Context(), w)
					}
					if failure == "none" {
						must(t, err)
						if lifecycleState(t, s, w).AuthorityPin() == nil {
							t.Fatal("complete acquisition did not reach lifecycle")
						}
						return
					}
					if err == nil && (boundary != "configuration" || !lifecycleState(t, s, w).SourceBlocked(queue.ConfigSource)) {
						t.Fatal("incomplete evidence accepted")
					}
					if err != nil && strings.Contains(err.Error(), "ghs_SECRET") {
						t.Fatal("transport secret exposed")
					}
					after := lifecycleState(t, s, w)
					if after.ConfigDigest != before.ConfigDigest || after.ConfigCommit != before.ConfigCommit || !reflect.DeepEqual(after.AuthorityPin(), before.AuthorityPin()) {
						t.Fatal("partial activation")
					}
					if boundary == "configuration" && w.configError == nil {
						t.Fatal("failed acquisition left work enabled")
					}
				})
			})
		}
	}
}

func TestAuthorityInvalidLandedConfigurationBlocks(t *testing.T) {
	t.Parallel()
	testSynctest(t, func(t *testing.T) {
		s, w, f, _ := authorityWorkerFixture(t)
		must(t, s.configuration(t.Context(), w))
		before := lifecycleState(t, s, w)
		_, watermark, err := s.store.Load(t.Context(), w.id)
		must(t, err)
		_, err = f.PushBase(repo, "main", "invalid landed config", map[string]string{".openmerge.yml": "version: broken"})
		must(t, err)
		must(t, s.cycle(t.Context(), w))
		after := lifecycleState(t, s, w)
		if w.configError == nil || !after.SourceBlocked(queue.ConfigSource) || !strings.Contains(after.BlockSourceDetail(queue.ConfigSource), "invalid landed authority configuration") {
			t.Fatal("invalid authority config did not block", w.configError)
		}
		if after.ConfigDigest != before.ConfigDigest || !reflect.DeepEqual(after.AuthorityPin(), before.AuthorityPin()) {
			t.Fatal("invalid landed config replaced policy")
		}
		rows, err := s.store.EventsAfter(t.Context(), w.id, watermark)
		must(t, err)
		for _, row := range rows {
			switch row.Event.(type) {
			case queue.ConfigApplied, queue.PolicyAuthorityApplied, queue.LandingStarted:
				t.Fatalf("invalid config emitted %T", row.Event)
			}
		}
		_, err = f.PushBase(repo, "main", "repair", map[string]string{".openmerge.yml": lifecycleConfig(repositoryConfig)})
		must(t, err)
		must(t, s.configuration(t.Context(), w))
		if w.configError != nil || lifecycleState(t, s, w).SourceBlocked(queue.ConfigSource) {
			t.Fatal("repaired config did not recover")
		}
	})
}
