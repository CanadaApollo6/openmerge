package serve

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// A declaration hold is independent of the ordinary held-request lifecycle.
// A pending request may auto-admit when entry conditions become true, and a
// dequeue followed by a fresh request may admit the same head, but neither
// route can clear the alias-scoped declaration hold.
func TestDependencyDeclarationHoldSurvivesHeldRequestLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dequeue bool
	}{
		{name: "auto-admission"},
		{name: "dequeue-and-re-request", dequeue: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			db, _ := databaseConnection(t)
			s, w, f, _ := fixtureWithStore(t, repositoryConfig+"    enter_when: [check-success = sign-off]\n", db)
			pr, err := f.PullRequest(ctx, repo, 1)
			must(t, err)
			for _, user := range []forge.UserIdentity{
				{ID: 12, Login: "contributor", Kind: forge.UserAccount},
				{ID: 21, Login: "editor", Kind: forge.UserAccount},
				{ID: 22, Login: "independent", Kind: forge.UserAccount},
			} {
				f.SetIdentity(user)
			}
			f.SetWriteGrant(repo, 21, "write")
			f.SetWriteGrant(repo, 22, "write")
			f.SetCommitAuthor(repo, forge.CommitAuthor{SHA: pr.HeadSHA, ID: 21, Login: "editor"})

			var token [32]byte
			token[0] = 6
			binding, err := dependency.NewJournalIdentity("openmerge-db-v2:sha256:"+strings.Repeat("d", 64), token, "github:https://github.com", "7")
			must(t, err)
			consumer := dependency.QueueKey{Repository: "https://github.com/owner/repo", Base: "main"}
			source := dependency.QueueKey{Repository: "https://github.com/owner/api", Base: "main"}
			alias := dependency.SourceAlias{ForgeInstance: binding.ForgeInstance, Installation: binding.Installation, RepositoryID: "id-owner-repo", Number: 1}
			change, err := queue.PRChangeID("7", "owner/repo", "main", "", 1)
			must(t, err)
			actors := dependencyHoldActors(t, ctx, f, alias)
			ref, err := dependency.Reference{Scope: dependency.Scope{ForgeInstance: binding.ForgeInstance, Installation: binding.Installation, JournalNamespace: binding.Namespace}, ChangeID: "chg1_" + strings.Repeat("a", 64)}.Canonical()
			must(t, err)
			edge := dependency.DeclaredEdge{Reference: ref, Queue: source, Baseline: dependency.Baseline{Revision: 1, IntentDigest: strings.Repeat("9", 64), Head: strings.Repeat("a", 40)}}
			initial := dependency.DependenciesDeclared{
				Hold:           dependency.DeclarationHold{Editors: []string{}},
				Namespace:      binding.Namespace,
				Alias:          alias,
				ChangeID:       string(change),
				Revision:       1,
				Queue:          consumer,
				Poll:           1,
				Digest:         dependency.DeclarationDigest([]dependency.DeclaredEdge{edge}),
				Edges:          []dependency.DeclaredEdge{edge},
				Bindings:       []dependency.AliasBinding{},
				Attribution:    actors.attribution,
				ReadGrant:      actors.declarationGrant,
				Accountability: actors.accountability,
			}
			depSide(t, s.store, w.id, binding, "init", binding)
			policy := depPolicy(binding, alias, consumer, []string{source.Repository}, []string{})
			policyID := depSide(t, s.store, w.id, binding, "policy", policy)
			initialID := depSide(t, s.store, w.id, binding, "initial", initial)
			removal := initial
			removal.Hold = dependency.DeclarationHold{Held: true, Editors: actors.attribution.Editors}
			removal.Prior = dependency.Basis{EventID: initialID, Digest: initial.Digest}
			removal.Poll++
			removal.Edges = []dependency.DeclaredEdge{}
			removal.Digest = dependency.DeclarationDigest(removal.Edges)
			removalID := depSide(t, s.store, w.id, binding, "removal", removal)
			w.dependencies = &dependencyPass{
				binding: binding,
				recover: s.store.RecoverProvenance,
				landingStart: func(context.Context, queue.Land, queue.LandingStarted) (store.DependencyStart, error) {
					return store.DependencyStart{}, errors.New("unexpected dependency landing start")
				},
			}

			f.SetCheckRuns(repo, pr.HeadSHA, []forge.CheckRun{{Name: "sign-off", State: forge.Pending}})
			f.AddComment(repo, 1, "writer", "@openmerge queue")
			must(t, s.cycle(ctx, w))
			state, _, err := s.store.Load(ctx, w.id)
			must(t, err)
			if _, held := state.HeldRequest(1); !held || len(state.Entries) != 0 || state.Active != nil {
				t.Fatalf("pending queue request was not held: held=%v entries=%+v active=%+v", held, state.Entries, state.Active)
			}

			f.SetCheckRuns(repo, pr.HeadSHA, []forge.CheckRun{{Name: "sign-off", State: forge.Success}})
			must(t, s.cycle(ctx, w))
			state, _, err = s.store.Load(ctx, w.id)
			must(t, err)
			if _, held := state.HeldRequest(1); held || len(state.Entries) != 1 || state.Entries[0].HeadSHA != pr.HeadSHA || state.Active != nil {
				t.Fatalf("request did not admit while declaration hold still blocked activation: held=%v entries=%+v active=%+v", held, state.Entries, state.Active)
			}
			if tc.dequeue {
				f.AddComment(repo, 1, "writer", "@openmerge dequeue")
				must(t, s.Notify(ctx, "7", repo.Path, "dependency-hold-dequeue"))
				must(t, s.cycle(ctx, w))
				state, _, err = s.store.Load(ctx, w.id)
				must(t, err)
				if _, held := state.HeldRequest(1); held || len(state.Entries) != 0 || state.Active != nil {
					t.Fatalf("dequeue retained admitted request or started dependency-held change: held=%v entries=%+v active=%+v", held, state.Entries, state.Active)
				}
				f.AddComment(repo, 1, "writer", "@openmerge queue")
				must(t, s.Notify(ctx, "7", repo.Path, "dependency-hold-re-request"))
				must(t, s.cycle(ctx, w))
				state, _, err = s.store.Load(ctx, w.id)
				must(t, err)
				if _, held := state.HeldRequest(1); held || len(state.Entries) != 1 || state.Active != nil {
					t.Fatalf("re-request cleared dependency hold or failed to admit: held=%v entries=%+v active=%+v", held, state.Entries, state.Active)
				}
			}

			ack := dependency.Acknowledged{
				Namespace:       binding.Namespace,
				Alias:           alias,
				Declaration:     dependency.Basis{EventID: removalID, Digest: removal.Digest},
				Writer:          actors.independentGrant,
				Exclusions:      actors.exclusions,
				PolicyEventID:   policyID,
				GoverningCommit: policy.Commit,
				PolicyDigest:    policy.Digest,
			}
			depSide(t, s.store, w.id, binding, "acknowledgement", ack)
			must(t, s.cycle(ctx, w))
			state, _, err = s.store.Load(ctx, w.id)
			must(t, err)
			if state.Active == nil || state.Active.Phase != queue.Testing {
				t.Fatalf("acknowledgement did not release dependency-held request: active=%+v entries=%+v", state.Active, state.Entries)
			}
		})
	}
}

type dependencyHoldActorFacts struct {
	attribution      dependency.Attribution
	accountability   dependency.Accountability
	declarationGrant dependency.Grant
	independentGrant dependency.Grant
	exclusions       []string
}

type dependencyHoldForgeReader interface {
	PullRequest(context.Context, forge.RepoRef, int) (forge.PullRequest, error)
	CommitAuthors(context.Context, forge.RepoRef, int) ([]forge.CommitAuthor, error)
	forge.IdentityReader
}

// dependencyHoldActors gets every actor and grant used by the declaration from
// the fake forge's PR, commit-author, identity and permission readers. The
// lifecycle fixture deliberately does not synthesize an editor list or parse a
// body: production acquisition remains unwired until slice 6.
func dependencyHoldActors(t *testing.T, ctx context.Context, f dependencyHoldForgeReader, alias dependency.SourceAlias) dependencyHoldActorFacts {
	t.Helper()
	pr, err := f.PullRequest(ctx, repo, int(alias.Number))
	must(t, err)
	author, err := f.ResolveUser(ctx, pr.Author)
	must(t, err)
	if author.ID != pr.AuthorID {
		t.Fatal("fake forge returned a different PR author", author, pr)
	}
	commits, err := f.CommitAuthors(ctx, repo, pr.Number)
	must(t, err)
	if len(commits) == 0 {
		t.Fatal("fake forge returned no declaration editor")
	}
	editors := make([]string, 0, len(commits))
	var declarationGrant dependency.Grant
	for _, commit := range commits {
		editor, err := f.ResolveUser(ctx, commit.Login)
		must(t, err)
		if editor.ID != commit.ID {
			t.Fatal("fake forge commit editor changed identity", editor, commit)
		}
		grant, err := f.WriteGrant(ctx, repo, editor)
		must(t, err)
		if !grant.CanWrite(editor.ID) {
			t.Fatal("fake forge editor is not a writer", editor, grant)
		}
		id := strconv.FormatInt(editor.ID, 10)
		editors = append(editors, id)
		if declarationGrant.SubjectID == "" {
			declarationGrant = dependency.Grant{SubjectID: id, ReturnedID: strconv.FormatInt(grant.User.ID, 10), RepositoryID: alias.RepositoryID, ReadID: "fake-declaration-editor", Fresh: true, Allowed: grant.CanRead(editor.ID)}
		}
	}
	slices.Sort(editors)
	editors = slices.Compact(editors)
	independent, err := f.ResolveUser(ctx, "independent")
	must(t, err)
	grant, err := f.WriteGrant(ctx, repo, independent)
	must(t, err)
	if !grant.CanWrite(independent.ID) {
		t.Fatal("fake forge independent writer unavailable", independent, grant)
	}
	exclusions := append([]string{strconv.FormatInt(author.ID, 10)}, editors...)
	slices.Sort(exclusions)
	exclusions = slices.Compact(exclusions)
	return dependencyHoldActorFacts{
		attribution:      dependency.Attribution{Editors: editors},
		accountability:   dependency.Accountability{Author: strconv.FormatInt(author.ID, 10), Humans: []string{}},
		declarationGrant: declarationGrant,
		independentGrant: dependency.Grant{SubjectID: strconv.FormatInt(independent.ID, 10), ReturnedID: strconv.FormatInt(grant.User.ID, 10), RepositoryID: alias.RepositoryID, ReadID: "fake-independent-writer", Fresh: true, Allowed: grant.CanWrite(independent.ID)},
		exclusions:       exclusions,
	}
}
