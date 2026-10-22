package forgetest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func runPushOptions(t *testing.T, newEnv func(*testing.T) Env) {
	runPushDispatchBeforeLease(t, newEnv)
	t.Run("authority prepared push", func(t *testing.T) {
		e := newEnv(t)
		ctx := t.Context()
		ref := "refs/heads/authority-contract-test"
		evidence := authority.Observation{Inventory: authority.Visible, Protections: []authority.Protection{{ID: "one", Source: "repository_ruleset", Target: "push", Enforcement: "active", TargetsVisibility: authority.Visible, RulesVisibility: authority.Visible, BypassVisibility: authority.Visible, Includes: []string{}, Excludes: []string{}, Conditions: []authority.Parameter{}, Rules: []authority.Rule{{Kind: "required_signatures", Parameters: []authority.Parameter{}}}, BypassActors: []authority.BypassActor{{Kind: "app", ID: "42", Mode: "always"}}}}, Coverage: []authority.Coverage{{Ref: "refs/heads/" + e.Base, Visibility: authority.Visible, ProtectionIDs: []string{"repository_ruleset/one"}}}, Findings: []authority.Finding{}}
		fingerprint, err := evidence.Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		c, err := forge.NewPushValidationContext(forge.PushBinding{Repository: e.Repo, QueueKey: "contract/main", GoverningCommit: e.BaseSHA, EffectiveDigest: strings.Repeat("a", 64), ProtectionFingerprint: fingerprint}, forge.PushBuild, []forge.PushRefIntent{{Ref: ref, BuildBaseSHA: e.BaseSHA}}, forge.PushRuleContext{DefaultBranch: e.Base, BaseRef: "refs/heads/" + e.Base, AppID: "42", Observation: evidence})
		if err != nil {
			t.Fatal(err)
		}
		opts := forge.PushOptions{AuthorityRequired: true, Context: c, Guard: func(context.Context, forge.PushValidationContext) error { return nil }}
		for _, method := range []forge.MergeMethod{forge.Merge, forge.Squash, forge.Rebase, forge.FastForward} {
			_, err := e.Git.BuildTestRef(ctx, e.Repo, forge.BuildRequest{Ref: ref, BaseSHA: e.BaseSHA, PRs: e.PRs[:1], Method: method, BatchID: "authority-contract"}, opts)
			if err == nil {
				t.Fatalf("%s accepted unsigned generated objects", method)
			}
			if sha, err := e.Git.ReadRef(ctx, e.Repo, ref); err == nil || sha != "" {
				t.Fatalf("rejected %s changed ref %s", method, sha)
			}
		}
		missing := forge.PushOptions{AuthorityRequired: true}
		if _, err := e.Git.EnsureFence(ctx, e.Repo, "authority-new-fence", missing); err == nil {
			t.Fatal("created fence without context")
		}
		initial, err := e.Git.EnsureFence(ctx, e.Repo, "authority-existing-fence", forge.PushOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got, err := e.Git.EnsureFence(ctx, e.Repo, "authority-existing-fence", missing); err != nil || got != initial {
			t.Fatalf("existing fence no-op: %s %v", got, err)
		}
		if err := e.Git.DeleteBranch(ctx, e.Repo, "authority-absent", e.BaseSHA, missing); err != nil {
			t.Fatal("missing cleanup no-op rejected", err)
		}
	})
}

func runPushDispatchBeforeLease(t *testing.T, newEnv func(*testing.T) Env) {
	t.Run("authority dispatch precedes stale fence lease", func(t *testing.T) {
		e := newEnv(t)
		sentinel := errors.New("dispatch policy unavailable")
		opts := forge.PushOptions{AuthorityRequired: true, Guard: func(context.Context, forge.PushValidationContext) error { return sentinel }}
		_, err := e.Git.AdvanceFence(t.Context(), e.Repo, "authority-absent-fence", e.BaseSHA, "abandon/order", opts)
		if !errors.Is(err, sentinel) {
			t.Fatalf("expected dispatch rejection before lease, got %v", err)
		}
	})
}
