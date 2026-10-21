package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestRulesetsFlagParsingAndDispatch(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "mock-admin")
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	calls := 0
	http.DefaultTransport = importTestTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodGet || req.URL.Host != "api.github.com" {
			t.Fatalf("unexpected request %s %s", req.Method, req.URL)
		}
		var value any
		switch req.URL.Path {
		case "/repos/acme/project":
			value = map[string]any{"default_branch": "main", "permissions": map[string]bool{"admin": true}}
		case "/orgs/acme/teams/owners":
			value = map[string]any{"id": 98, "slug": "owners", "organization": map[string]any{"id": 55, "login": "acme"}}
		case "/repos/acme/project/rulesets":
			value = []any{}
		default:
			t.Fatalf("unexpected route %s", req.URL.Path)
		}
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data)), Request: req}, nil
	})
	plan := filepath.Join(t.TempDir(), "x")
	if err := os.WriteFile(plan, []byte("version: 1\npolicy_authority: openmerge\nbreak_glass: {team: acme/owners}\nqueues: [{name: main, merge_when: ['check-success = ci']}]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		args    []string
		code    int
		message string
		reads   bool
	}{
		{"help registers flags once", []string{"--help"}, 0, "--verify", false},
		{"install preview", []string{"--authority", "openmerge", "--config", plan}, 0, "Preview only.", true},
		{"verification gate", []string{"--authority", "openmerge", "--verify"}, 1, "--emission-receipt", false},
		{"verification requires authority", []string{"--verify"}, 1, "--authority openmerge --verify requires", false},
		{"verification rejects github", []string{"--verify", "--authority", "github"}, 1, "--authority openmerge --verify requires", false},
		{"restore setup", []string{"--authority", "openmerge", "--restore", plan, "--config", plan}, 1, "--restore cannot be combined", false},
		{"rollback credential wiring", []string{"--authority", "github", "--rollback-preflight", "--config", plan, "--installation-id", "7", "--private-key", plan + ".missing"}, 1, "x.missing", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls = 0
			var out, diagnostics bytes.Buffer
			args := append([]string{"rulesets", "--repo", "acme/project", "--app-id", "42"}, tt.args...)
			if exit := runSetup(args, &out, &diagnostics); exit != tt.code || !strings.Contains(out.String()+diagnostics.String(), tt.message) || (calls > 0) != tt.reads {
				t.Fatalf("dispatch: exit=%d out=%s diagnostics=%s calls=%d", exit, &out, &diagnostics, calls)
			}
		})
	}
}

func TestVerifyRejectsSetupMutationsBeforeReads(t *testing.T) {
	for _, args := range [][]string{
		{"--apply"}, {"--restore", "capture.json"}, {"--rollback-preflight"}, {"--rotate-break-glass"},
	} {
		t.Run(args[0], func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			base := []string{"rulesets", "--repo", "acme/project", "--app-id", "42", "--authority", "openmerge", "--verify"}
			exit := runSetup(append(base, args...), &out, &diagnostics)
			if exit != 1 || !strings.Contains(diagnostics.String(), "--verify cannot be combined") || out.Len() != 0 {
				t.Fatalf("mutation accepted: %d %s %s", exit, &out, &diagnostics)
			}
		})
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

func TestVerifyTargetsStaleBasisRetainsEvidenceAndRequiresRetry(t *testing.T) {
	db := acceptanceDB(t)
	read := func(name string) queue.Event {
		t.Helper()
		raw, err := os.ReadFile("../../internal/queue/testdata/wire/v1/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		e, err := queue.DecodeEvent(raw)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	v := read("event_write_restriction_verified").(queue.WriteRestrictionVerified)
	pin := read("variants/policy_authority_applied_activation").(queue.PolicyAuthorityApplied)
	pin.OpenMerge.Activation = nil
	selectors := []authority.BaseSelector{{DefaultBranch: true}, {Pattern: "release/**"}}
	coverageDigest, err := authority.CoverageDigest("main", selectors)
	v.Binding.CoverageDigest = coverageDigest
	if err != nil {
		t.Fatal(err)
	}
	pin.OpenMerge.Binding = v.Binding
	pin.CoverageDigest = v.Binding.CoverageDigest
	key := store.QueueKey{Installation: "173082682", Repository: v.Binding.Repository, BaseBranch: "main"}
	q, err := db.EnsureQueue(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Append(t.Context(), q, []queue.Event{queue.ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest}, pin}, ""); err != nil {
		t.Fatal(err)
	}
	state, before, err := db.Load(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	oldTime := state.WriteRestrictionStatus().LastVerifiedAt
	plan := verificationPlan{Targets: []verificationTarget{{Key: key, Input: forge.WriteRestrictionInput{Repo: forge.RepoRef{Path: key.Repository}, Binding: v.Binding, AppID: "5383059", BaseSelectors: selectors, ExpectedRefs: []string{"refs/heads/main"}}}}}
	reader := verifyReaderFunc(func(context.Context, forge.WriteRestrictionInput) (authority.Observation, error) {
		state, _, err := db.Load(t.Context(), q)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Append(t.Context(), q, []queue.Event{queue.WriteRestrictionObserved{Binding: v.Binding, Basis: state.RulesSourceRevision(queue.WriteRestriction), ObservedAt: time.Now().UTC(), Evidence: authority.CloneObservation(v.Evidence)}}, "")
		if err != nil {
			t.Fatal(err)
		}
		return authority.CloneObservation(v.Evidence), nil
	})
	var out bytes.Buffer
	err = verifyTargets(t.Context(), db, reader, plan, "operator", &out)
	if err == nil || !strings.Contains(err.Error(), "retry --verify") {
		t.Fatalf("stale read claimed success: %v %s", err, &out)
	}
	state, after, err := db.Load(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	if after <= before || !state.WriteRestrictionStatus().LastVerifiedAt.Equal(oldTime) {
		t.Fatal("stale read advanced verification")
	}
	records, err := db.EventsAfter(t.Context(), q, before)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("lost evidence: %+v", records)
	}
	evidence, ok := records[1].Event.(queue.WriteRestrictionVerified)
	if !ok || evidence.Scope != "applied" || !strings.Contains(out.String(), "write_restriction_verified event") {
		t.Fatal("missing appended evidence", out.String())
	}
	good := verifyReaderFunc(func(context.Context, forge.WriteRestrictionInput) (authority.Observation, error) {
		return authority.CloneObservation(v.Evidence), nil
	})
	if err := verifyTargets(t.Context(), db, good, plan, "operator", &out); err != nil {
		t.Fatal("fresh retry failed", err)
	}
	state, _, err = db.Load(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	if !state.WriteRestrictionStatus().LastVerifiedAt.After(oldTime) {
		t.Fatal("fresh retry did not advance verification time")
	}
}
