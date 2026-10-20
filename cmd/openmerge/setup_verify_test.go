package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"os"
	"strings"
	"testing"
)

func TestVerifyRequiresGateBeforeDBOrGitHub(t *testing.T) {
	for _, args := range [][]string{
		{"rulesets", "--repo", "local/sandbox", "--app-id", "42", "--authority", "openmerge", "--verify"},
		{"rulesets", "--repo", "local/sandbox", "--app-id", "42", "--authority", "openmerge", "--verify", "--apply"},
	} {
		var out, diagnostics bytes.Buffer
		exit := runSetup(args, &out, &diagnostics)
		if exit == 0 || !strings.Contains(diagnostics.String(), "--emission-receipt") || out.Len() != 0 {
			t.Fatalf("gate bypassed: %d %s %s", exit, &out, &diagnostics)
		}
	}
}

type verifyReaderFunc func(context.Context, forge.WriteRestrictionInput) (authority.Observation, error)

func (f verifyReaderFunc) ObserveWriteRestriction(ctx context.Context, in forge.WriteRestrictionInput) (authority.Observation, error) {
	return f(ctx, in)
}

func TestVerifyTargetsProposedViolationAndFailedRead(t *testing.T) {
	db := acceptanceDB(t)
	raw, err := os.ReadFile("../../internal/queue/testdata/wire/v1/event_write_restriction_verified.json")
	if err != nil {
		t.Fatal(err)
	}
	event, err := queue.DecodeEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	v := event.(queue.WriteRestrictionVerified)
	key := store.QueueKey{Installation: "173082682", Repository: v.Binding.Repository, BaseBranch: "main"}
	q, err := db.EnsureQueue(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	selectors := []authority.BaseSelector{{DefaultBranch: true}, {Pattern: "release/**"}}
	v.Binding.CoverageDigest, err = authority.CoverageDigest("main", selectors)
	if err != nil {
		t.Fatal(err)
	}
	input := forge.WriteRestrictionInput{Repo: forge.RepoRef{Path: key.Repository}, Binding: v.Binding, AppID: "5383059", BaseSelectors: selectors, ExpectedRefs: []string{"refs/heads/main"}}
	plan := verificationPlan{Targets: []verificationTarget{{Key: key, Input: input}}}
	good := verifyReaderFunc(func(context.Context, forge.WriteRestrictionInput) (authority.Observation, error) {
		return authority.CloneObservation(v.Evidence), nil
	})
	var out bytes.Buffer
	for range 2 {
		if err := verifyTargets(t.Context(), db, good, plan, "operator", &out); err != nil {
			t.Fatal(err)
		}
	}
	journals, err := db.Journals(t.Context(), []store.QueueKey{key})
	if err != nil {
		t.Fatal(err)
	}
	records := journals[0].Events
	a := records[0].Event.(queue.WriteRestrictionVerified)
	b := records[1].Event.(queue.WriteRestrictionVerified)
	if len(records) != 2 || a.Nonce == b.Nonce || !a.VerifiedAt.Before(b.VerifiedAt) || a.ProtectionFingerprint != b.ProtectionFingerprint {
		t.Fatal("identical seeing reads reused an invocation")
	}
	if !strings.Contains(out.String(), "write_restriction_verified event") || !strings.Contains(out.String(), "scope=proposed") || !strings.Contains(out.String(), "last_verified_at=0001") {
		t.Fatal("verification output conflated proposed/active ages", out.String())
	}
	violated := verifyReaderFunc(func(context.Context, forge.WriteRestrictionInput) (authority.Observation, error) {
		o := authority.CloneObservation(v.Evidence)
		o.Protections[0].Enforcement = "disabled"
		return o, nil
	})
	if err := verifyTargets(t.Context(), db, violated, plan, "operator", &out); err == nil {
		t.Fatal("visible violation exited successfully")
	}
	state, before, err := db.Load(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	if state.AuthorityPin() != nil || len(state.RulesProblems()) > 0 {
		t.Fatal("proposed violation changed active authority")
	}
	failed := verifyReaderFunc(func(context.Context, forge.WriteRestrictionInput) (authority.Observation, error) {
		return forge.EmptyWriteRestrictionObservation(), errors.New("failed seeing read")
	})
	if err := verifyTargets(t.Context(), db, failed, plan, "operator", &out); err == nil {
		t.Fatal("failed read claimed verification")
	}
	_, after, err := db.Load(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("failed read appended verification")
	}
}
