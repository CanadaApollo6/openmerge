package forge

import (
	"context"
	"errors"
	"github.com/CanadaApollo6/openmerge/internal/authority"
	"strings"
	"testing"
)

func TestPushOptionsRequiredBindingsAndGuard(t *testing.T) {
	p := preparedFixture()
	rules := ruleContext()
	fingerprint, err := rules.Observation.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	repo := RepoRef{Path: "owner/repo"}
	binding := PushBinding{Repository: repo, QueueKey: "main", GoverningCommit: strings.Repeat("a", 40), EffectiveDigest: strings.Repeat("b", 64), ProtectionFingerprint: fingerprint}
	refs := []PushRefIntent{{Ref: p.Refs[0].Ref, OldSHA: p.Refs[0].OldSHA, BuildBaseSHA: p.Refs[0].OldSHA}}
	frozen, err := NewPushValidationContext(binding, PushBuild, refs, rules)
	if err != nil {
		t.Fatal(err)
	}
	refs[0].Ref = "refs/heads/changed"
	refs[0].BuildBaseSHA = strings.Repeat("e", 40)
	returned := frozen.RefIntents()
	returned[0].BuildBaseSHA = strings.Repeat("f", 40)
	if frozen.BuildBaseSHA() != p.Refs[0].OldSHA {
		t.Fatal("build base did not remain immutable")
	}
	if _, err := NewPushValidationContext(binding, PushBuild, []PushRefIntent{{Ref: p.Refs[0].Ref}}, rules); err == nil {
		t.Fatal("build accepted without authenticated base")
	}
	rules.Observation.Protections[0].Rules = []authority.Rule{{Kind: "future", Parameters: []authority.Parameter{}}}
	calls := 0
	opts := PushOptions{AuthorityRequired: true, Context: frozen, Guard: func(context.Context, PushValidationContext) error { calls++; return nil }}
	if err := opts.CheckDispatch(t.Context(), repo, PushBuild, frozen.RefIntents()); err != nil {
		t.Fatal(err)
	}
	if err := opts.ValidateBeforePush(t.Context(), repo, p); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("guard calls %d", calls)
	}
	for name, mutate := range map[string]func(*PushOptions){
		"missing context": func(o *PushOptions) { o.Context = PushValidationContext{} },
		"missing guard":   func(o *PushOptions) { o.Guard = nil },
		"wrong operation": func(o *PushOptions) { o.Context.operation = PushLand },
		"wrong ref":       func(o *PushOptions) { o.Context.refs = []PushRefIntent{{Ref: "refs/heads/other"}} },
		"wrong lease": func(o *PushOptions) {
			o.Context.refs = []PushRefIntent{{Ref: p.Refs[0].Ref, OldSHA: strings.Repeat("c", 40)}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := opts
			mutate(&bad)
			if err := bad.ValidateBeforePush(t.Context(), repo, p); err == nil {
				t.Fatal("bad context accepted")
			}
		})
	}
	sentinel := errors.New("reload failed")
	opts.Guard = func(context.Context, PushValidationContext) error { return sentinel }
	if err := opts.CheckDispatch(t.Context(), repo, PushBuild, frozen.RefIntents()); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if err := (PushOptions{}).ValidateBeforePush(t.Context(), repo, PreparedPush{}); err != nil {
		t.Fatal("zero options changed legacy behavior", err)
	}
}
