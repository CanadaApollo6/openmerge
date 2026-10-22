package serve

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/gitops"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// The golden export was built on main d9ec975 with this test overlaid.
// They contain actual legacy generated OIDs and complete, unnormalized event
// payloads for deterministic builds, permission failure/repair and commands.
func TestAuthorityAcceptanceMixedGithubBytes(t *testing.T) {
	t.Setenv("GIT_AUTHOR_DATE", "2026-10-15T12:00:00Z")
	t.Setenv("GIT_COMMITTER_DATE", "2026-10-15T12:00:00Z")
	var alone, mixed map[string]string
	testSynctest(t, func(t *testing.T) { alone = acceptanceGithubBytes(t, false) })
	testSynctest(t, func(t *testing.T) { mixed = acceptanceGithubBytes(t, true) })
	a, err := json.Marshal(alone)
	must(t, err)
	b, err := json.Marshal(mixed)
	must(t, err)
	if !bytes.Equal(a, b) {
		t.Fatalf("same-repository authority worker changed GitHub bytes\nalone: %s\nmixed: %s", a, b)
	}
	// Pin main-built compatibility in CI, independently of alone/mixed equality.
	const mainSHA256 = "cc95a654054e61e816120f2e57b9e19ffea3ff4855c32f4f5c055c4e99b913ee"
	if got := fmt.Sprintf("%x", sha256.Sum256(b)); got != mainSHA256 {
		t.Fatalf("GitHub export SHA-256 = %s, want main d9ec975 %s", got, mainSHA256)
	}
	if path := os.Getenv("ADR_ACCEPTANCE_GITHUB_BYTES"); path != "" {
		must(t, os.WriteFile(path, b, 0600))
	}
}

func acceptanceGithubBytes(t *testing.T, mixed bool) map[string]string {
	t.Helper()
	s, main, f, _ := authorityWorkerFixture(t)
	if mixed {
		must(t, s.cycle(t.Context(), main))
		if pin := lifecycleState(t, s, main).AuthorityPin(); pin == nil || pin.Authority != authority.OpenMerge {
			t.Fatal("mixed server lacks an authority worker")
		}
	} else {
		main.AuthorityRequired = false
		main.resolveAuthorityLifecycle = nil
	}
	fixture := newCompositionFixture(t, []int{2})
	fixture.git("", "--git-dir="+fixture.remote, "update-ref", "refs/heads/release", fixture.base)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	g, err := gitops.New(t.Context(), gitops.Config{WorkDir: filepath.Join(t.TempDir(), "cache"), Now: func() time.Time { return now }, Remote: func(context.Context, forge.RepoRef) (string, gitops.Auth, error) {
		return fixture.remote, gitops.Auth{}, nil
	}, HeadRef: func(n int) string { return fmt.Sprintf("refs/pull/%d/head", n) }})
	must(t, err)
	driver := &countingDriver{Driver: f}
	w, err := s.register(t.Context(), Target{Key: store.QueueKey{Installation: main.Key.Installation, Repository: main.Key.Repository, BaseBranch: "release"}, Driver: driver, Git: g, BotLogin: main.BotLogin})
	must(t, err)
	_, before, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	missing := true
	permissionReads := 0
	w.CheckPermissions = func(context.Context) ([]string, []string, error) {
		permissionReads++
		if missing {
			return []string{"installed App missing permission contents: write"}, nil, nil
		}
		return nil, nil, nil
	}
	must(t, s.repositoryRules(t.Context(), w))
	if len(lifecycleState(t, s, w).RulesSourceProblems(queue.Permissions)) != 1 {
		t.Fatal("GitHub installed-permission refusal bypassed")
	}
	missing = false
	w.rulesNextCheck = time.Time{}
	must(t, s.repositoryRules(t.Context(), w))
	if len(lifecycleState(t, s, w).RulesSourceProblems(queue.Permissions)) != 0 {
		t.Fatal("GitHub installed-permission repair lost")
	}
	// The shared legacy command handler must still read current permissions,
	// refuse a reader and apply the same writer event in a mixed server.
	for i, user := range []string{"reader", "writer"} {
		_, err = s.handleLegacyCommand(t.Context(), w, 1, forge.Comment{ID: int64(800 + i), Author: user, Body: "@openmerge dequeue", CreatedAt: now})
		must(t, err)
		current, loadErr := s.store.EventsAfter(t.Context(), w.id, before)
		must(t, loadErr)
		deques := 0
		for _, row := range current {
			if _, ok := row.Event.(queue.DequeueRequested); ok {
				deques++
			}
		}
		if deques != i {
			t.Fatalf("%s command recorded %d dequeue events, want %d", user, deques, i)
		}
	}
	if driver.permissions != 2 || permissionReads != 2 {
		t.Fatalf("permission reads: commands=%d installation=%d", driver.permissions, permissionReads)
	}
	rows, err := s.store.EventsAfter(t.Context(), w.id, before)
	must(t, err)
	events := []json.RawMessage{}
	appendEvent := func(e queue.Event) {
		_, raw, err := queue.EncodeEvent(e)
		must(t, err)
		events = append(events, json.RawMessage(raw))
	}
	for _, row := range rows {
		appendEvent(row.Event)
	}
	output := map[string]string{"permission-reads": fmt.Sprintf("installation=%d commands=%d", permissionReads, driver.permissions)}
	// Match the existing offline legacy Git probe: only this serial build
	// segment injects nonce bytes, after database/schema allocation is finished.
	originalRandom := rand.Reader
	defer func() { rand.Reader = originalRandom }()
	rand.Reader = bytes.NewReader(bytes.Repeat([]byte{0x5a}, 65536))
	for _, method := range []queue.MergeMethod{queue.Merge, queue.Squash, queue.Rebase, queue.FastForward} {
		batch := "legacy-acceptance-" + string(method)
		ref := "openmerge/queue/release/" + batch
		x := executor.Executor{AllowNoRequiredChecks: true, Git: g, Repo: w.repo(), Base: "release", Now: func() time.Time { return now }, PreparePush: func(ctx context.Context, op forge.PushOperation, refs []forge.PushRefIntent) (forge.PushOptions, error) {
			opts := s.authorityPushOptions(ctx, w, op, refs)
			if !reflect.DeepEqual(opts, forge.PushOptions{}) {
				t.Fatalf("GitHub operation inherited authority options: %+v", opts)
			}
			return opts, nil
		}}
		err = x.Execute(t.Context(), queue.BuildTestRef{BatchID: batch, Ref: ref, BaseSHA: fixture.base, PRs: fixture.pins, Method: method}, func(e queue.Event) error {
			appendEvent(e)
			return nil
		})
		must(t, err)
		oid := fixture.git("", "--git-dir="+fixture.remote, "rev-parse", "refs/heads/"+ref)
		output["tested-"+string(method)] = oid
		if method == queue.FastForward {
			fenceOpts := s.authorityPushOptions(t.Context(), w, forge.PushEnsureFence, []forge.PushRefIntent{{Ref: "refs/heads/openmerge/fence/release"}})
			fence, fenceErr := g.EnsureFence(t.Context(), w.repo(), "openmerge/fence/release", fenceOpts)
			must(t, fenceErr)
			landOpts := s.authorityPushOptions(t.Context(), w, forge.PushLand, []forge.PushRefIntent{{Ref: "refs/heads/release", OldSHA: fixture.base, NewSHA: fixture.pins[0].HeadSHA}, {Ref: "refs/heads/openmerge/fence/release", OldSHA: fence}})
			_, landErr := g.Land(t.Context(), w.repo(), forge.LandRequest{Base: "release", ExpectedBaseSHA: fixture.base, TestedSHA: oid, Fence: "openmerge/fence/release", ExpectedFence: fence, NextFenceID: "land/legacy", PRs: fixture.prs, FastForwardHeadSHA: fixture.pins[0].HeadSHA}, landOpts)
			must(t, landErr)
			oid = fixture.git("", "--git-dir="+fixture.remote, "rev-parse", "refs/heads/release")
			if oid != fixture.pins[0].HeadSHA {
				t.Fatal("GitHub fast-forward landed a generated marker instead of the legacy head")
			}
		}
		output["commit-"+string(method)] = oid
		output["object-"+string(method)] = fixture.git("", "--git-dir="+fixture.remote, "cat-file", "commit", oid)
	}
	raw, err := json.Marshal(events)
	must(t, err)
	output["events"] = string(raw)
	comments := []string{}
	for key, comment := range f.Comments(repo, 1) {
		if key == "comment/800/unauthorized" {
			comments = append(comments, comment)
		}
	}
	if len(comments) != 1 {
		t.Fatalf("unauthorized command replies=%v", comments)
	}
	raw, err = json.Marshal(comments)
	must(t, err)
	output["permission-replies"] = string(raw)
	if lifecycleState(t, s, w).AuthorityPin() != nil {
		t.Fatal("ordinary same-repository queue acquired authority pin")
	}
	return output
}
