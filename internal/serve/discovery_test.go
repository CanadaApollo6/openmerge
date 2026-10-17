package serve

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

type discoveryFixture struct {
	f               *fake.Forge
	repos           []Repository
	installations   []Installation
	appErr, listErr error
}

func (d *discoveryFixture) source() Discovery {
	return Discovery{Installations: func(context.Context) ([]Installation, error) { return d.installations, d.appErr }, Target: func(_ context.Context, key store.QueueKey) (Target, error) {
		target := managedRepository(d.f, key.Repository).Target(key.BaseBranch)
		target.Key = key
		return target, nil
	}}
}

func newDiscoveryFixture(t *testing.T) (*Server, *discoveryFixture) {
	t.Helper()
	f := fake.New()
	f.Init(repo, "main", "openmerge/fence/main")
	_, err := f.PushBase(repo, "main", "configured", map[string]string{".openmerge.yml": repositoryConfig})
	must(t, err)
	d := &discoveryFixture{f: f, repos: []Repository{managedRepository(f, repo.Path)}}
	d.installations = []Installation{{ID: "7", Repositories: func(context.Context) ([]Repository, error) { return d.repos, d.listErr }}}
	s, err := NewDiscovery(t.Context(), database(t), d.source(), Options{})
	must(t, err)
	must(t, s.Refresh(t.Context()))
	return s, d
}

func TestDiscoveryRemovalSuspensionAndReinstatement(t *testing.T) {
	for _, change := range []string{"repository", "installation", "suspension", "allowlist", "config deletion"} {
		t.Run(change, func(t *testing.T) {
			s, d := newDiscoveryFixture(t)
			w := s.workers()[0]
			id, key := w.id, w.Key
			recordLanding(t, s, w, d.f, true)
			must(t, s.block(t.Context(), w, queue.Operator, "operator hold", ""))
			saved := d.installations
			switch change {
			case "repository":
				d.repos = nil
			case "installation":
				d.installations = nil
			case "suspension":
				d.installations[0].Suspended = true
			case "allowlist":
				s.discovery.RepositoryAllowlist = []string{"someone/*"}
			case "config deletion":
				original := d.repos[0].Target
				d.repos[0].Target = func(base string) Target {
					target := original(base)
					target.Driver = missingConfigDriver{target.Driver}
					return target
				}

			}
			must(t, s.Refresh(t.Context()))
			state := lifecycleState(t, s, w)
			if !state.SourceBlocked(queue.RepositoryAccess) || state.Active == nil || state.Active.Phase != queue.Landing {
				t.Fatal("removal lost landing or did not block")
			}
			d.f.FailNext("ReadFence", errors.New("credentials gone"))
			if err := s.cycle(t.Context(), w); err == nil {
				t.Fatal("credential failure swallowed")
			}
			if state = lifecycleState(t, s, w); state.Active == nil || state.Active.Phase != queue.Landing {
				t.Fatal("credentials invented outcome")
			}
			must(t, s.cycle(t.Context(), w))
			state = lifecycleState(t, s, w)
			if state.Active != nil || !state.SourceBlocked(queue.RepositoryAccess) {
				t.Fatal("removed landing failed to settle")
			}
			d.installations = saved
			d.installations[0].Suspended = false
			d.repos = []Repository{managedRepository(d.f, repo.Path)}
			s.discovery.RepositoryAllowlist = nil
			_, err := d.f.PushBase(repo, "main", "restore configuration", map[string]string{".openmerge.yml": repositoryConfig})
			must(t, err)
			must(t, s.Refresh(t.Context()))
			state = lifecycleState(t, s, w)
			if len(s.Keys()) != 1 || w.id != id || w.Key != key || !w.ready || state.SourceBlocked(queue.RepositoryAccess) || !state.SourceBlocked(queue.Operator) {
				t.Fatalf("reinstatement changed identity/sources: %+v", state.Blocks(queue.Config{}))
			}
			events, err := s.store.EventsAfter(t.Context(), w.id, 0)
			must(t, err)
			for _, event := range events {
				if _, bad := event.Event.(queue.LandingAbandoned); bad {
					t.Fatal("abandoned a landed attempt")
				}
			}
		})
	}
}

func TestFailedDiscoveryReadsPreserveDurableState(t *testing.T) {
	for _, failure := range []string{"app", "repository listing", "metadata", "file", "branches"} {
		t.Run(failure, func(t *testing.T) {
			s, d := newDiscoveryFixture(t)
			w := s.workers()[0]
			before := s.DiscoveryStatus()
			_, watermark, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			switch failure {
			case "app":
				d.appErr = errors.New("page two failed")
			case "repository listing":
				d.listErr = errors.New("page two failed")
			case "metadata":
				d.f.FailNext("DefaultBranch", errors.New("metadata unavailable"))
			case "file":
				d.f.FailNext("ReadFile", errors.New("file unavailable"))
			case "branches":
				d.f.FailNext("ListBranches", errors.New("branches unavailable"))
			}
			if err := s.Refresh(t.Context()); err == nil {
				t.Fatal("failed read accepted")
			}
			events, err := s.store.EventsAfter(t.Context(), w.id, watermark)
			must(t, err)
			after := s.DiscoveryStatus()
			if len(events) != 0 || after.Repositories[0].State != "served" || !after.LastSuccessfulSweep.Equal(*before.LastSuccessfulSweep) {
				t.Fatalf("failure changed state: events=%+v status=%+v", events, after)
			}
		})
	}
}

func TestDiscoveryInventoryIncludesUnconfiguredInvalidAndDormant(t *testing.T) {
	s, d := newDiscoveryFixture(t)
	for _, name := range []string{"owner/unconfigured", "owner/invalid", "owner/dormant"} {
		ref := forge.RepoRef{Path: name}
		d.f.Init(ref, "main", "openmerge/fence/main")
		contents := ""
		if strings.HasSuffix(name, "invalid") {
			contents = "version: invalid"
		}
		if strings.HasSuffix(name, "dormant") {
			contents = "version: 1\nqueues:\n - name: release\n   base: release/*\n   merge_when: [check-success = ci]\n"
		}
		if contents != "" {
			_, err := d.f.PushBase(ref, "main", "policy", map[string]string{".openmerge.yml": contents})
			must(t, err)
		}
		d.repos = append(d.repos, managedRepository(d.f, name))
	}
	must(t, s.Refresh(t.Context()))
	states := map[string]string{}
	for _, r := range s.DiscoveryStatus().Repositories {
		states[r.Name] = r.State
	}
	if states["owner/unconfigured"] != "not configured" || states["owner/invalid"] != "invalid config" || states["owner/dormant"] != "dormant base" || len(s.Keys()) != 1 {
		t.Fatalf("inventory=%v queues=%v", states, s.Keys())
	}
}

func TestDiscoveryCanonicalRegistrationRaces(t *testing.T) {
	s, d := newDiscoveryFixture(t)
	w := s.workers()[0]
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			target := managedRepository(d.f, repo.Path).Target("main")
			if i%2 == 0 {
				target.Key.Repository = strings.ToUpper(repo.Path)
			}
			got, err := s.register(t.Context(), target)
			if err != nil || got != w {
				t.Errorf("registration=%v %v", got, err)
			}
		})
	}
	wg.Wait()
	if len(s.Keys()) != 1 {
		t.Fatal("duplicate workers")
	}
}

func TestDiscoveryReusesExactLiveDeploymentKey(t *testing.T) {
	db := database(t)
	key := store.QueueKey{Installation: "172166049", Repository: "CanadaApollo6/openmerge", BaseBranch: "main"}
	id, err := db.EnsureQueue(t.Context(), key)
	must(t, err)
	_, err = db.Append(t.Context(), id, []queue.Event{queue.FenceInitialized{Token: "stored-fence"}}, "")
	must(t, err)
	f := fake.New()
	ref := forge.RepoRef{Path: key.Repository}
	f.Init(ref, "main", "openmerge/fence/main")
	_, err = f.PushBase(ref, "main", "config", map[string]string{".openmerge.yml": repositoryConfig})
	must(t, err)
	r := managedRepository(f, key.Repository)
	r.Installation = key.Installation
	target := r.Target
	r.Target = func(base string) Target { t := target(base); t.Key.Installation = key.Installation; return t }
	s, err := NewDiscovery(t.Context(), db, Discovery{Installations: func(context.Context) ([]Installation, error) {
		return []Installation{{ID: key.Installation, Repositories: func(context.Context) ([]Repository, error) { return []Repository{r}, nil }}}, nil
	}, Target: func(_ context.Context, k store.QueueKey) (Target, error) {
		t := r.Target(k.BaseBranch)
		t.Key = k
		return t, nil
	}}, Options{})
	must(t, err)
	must(t, s.Refresh(t.Context()))
	w := s.workers()[0]
	state := lifecycleState(t, s, w)
	if len(s.Keys()) != 1 || w.id != id || w.Key != key || state.Fence != "stored-fence" {
		t.Fatal("live partition recreated")
	}
}

type missingConfigDriver struct{ forge.Driver }

func (missingConfigDriver) ReadFile(context.Context, forge.RepoRef, string, string) ([]byte, error) {
	return nil, forge.ErrFileNotFound
}

func TestDiscoveryRestoresLandingBeforeCredentialsAndFailedListing(t *testing.T) {
	s, d := newDiscoveryFixture(t)
	w := s.workers()[0]
	recordLanding(t, s, w, d.f, true)
	_, before, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	available := false
	source := d.source()
	target := source.Target
	source.Target = func(ctx context.Context, key store.QueueKey) (Target, error) {
		if !available {
			return Target{}, errors.New("credentials unavailable")
		}
		return target(ctx, key)
	}
	source.Installations = func(context.Context) ([]Installation, error) { return nil, errors.New("App listing unavailable") }
	restarted, err := NewDiscovery(t.Context(), s.store, source, Options{})
	must(t, err)
	if len(restarted.workers()) != 1 {
		t.Fatal("historical attempt not restored")
	}
	recovered := restarted.workers()[0]
	if err := restarted.cycle(t.Context(), recovered); err == nil {
		t.Fatal("credential failure ignored")
	}
	events, err := s.store.EventsAfter(t.Context(), w.id, before)
	must(t, err)
	if len(events) != 0 || lifecycleState(t, restarted, recovered).Active == nil {
		t.Fatal("credential loss changed durable attempt")
	}
	available = true
	must(t, restarted.cycle(t.Context(), recovered))
	if lifecycleState(t, restarted, recovered).Active != nil {
		t.Fatal("returning credentials did not settle")
	}
}

func TestDiscoverySharedDatabaseKeepsAppOwnership(t *testing.T) {
	s, d := newDiscoveryFixture(t)
	// The existing installation was authenticated as App 42, while another App
	// owns a different stored installation in the same isolated test database.
	must(t, s.store.BindInstallation(t.Context(), "7", "42"))
	foreign := store.QueueKey{Installation: "8", Repository: "foreign/repo", BaseBranch: "main"}
	foreignID, err := s.store.EnsureQueue(t.Context(), foreign)
	must(t, err)
	must(t, s.store.BindInstallation(t.Context(), "8", "99"))
	source := d.source()
	source.App = "42"
	restarted, err := NewDiscovery(t.Context(), s.store, source, Options{})
	must(t, err)
	must(t, restarted.Refresh(t.Context()))
	if len(restarted.Keys()) != 1 || restarted.Keys()[0].Installation != "7" {
		t.Fatalf("served another App: %+v", restarted.Keys())
	}
	events, err := s.store.EventsAfter(t.Context(), foreignID, 0)
	must(t, err)
	if len(events) != 0 {
		t.Fatalf("discovery wrote foreign queue: %+v", events)
	}
	if err := s.store.BindInstallation(t.Context(), "8", "42"); err == nil {
		t.Fatal("installation ownership stolen")
	}
	d.installations = nil
	must(t, restarted.Refresh(t.Context()))
	if state := lifecycleState(t, restarted, restarted.workers()[0]); !state.SourceBlocked(queue.RepositoryAccess) {
		t.Fatal("successful App omission did not block own queue")
	}
}

func TestRemovalBlocksStartsBeforeBusyWorkerReturns(t *testing.T) {
	s, _ := newDiscoveryFixture(t)
	w := s.workers()[0]
	w.mu.Lock()
	done := make(chan error, 1)
	go func() {
		done <- s.removeRepository(t.Context(), s.repositoryStatus(w.Key.Installation, w.Key.Repository), "removed", "allowlist dropped repository")
	}()
	// Model a cycle that still owns the process lock. The durable start gate
	// must become visible while it finishes its already-started operation.
	deadline := time.Now().Add(3 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		state, _, err := s.store.Load(t.Context(), w.id)
		must(t, err)
		if state.SourceBlocked(queue.RepositoryAccess) {
			blocked = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	w.mu.Unlock()
	must(t, <-done)
	if !blocked {
		t.Fatal("busy worker delayed the durable admission/start block")
	}
}
