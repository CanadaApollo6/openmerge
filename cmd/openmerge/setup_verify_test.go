package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/github"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestVerificationReceiptRejectedBeforeDatabaseOrSeeingRead(t *testing.T) {
	// A command must reject an unusable first-emission attestation before it
	// opens the operator DB, reads the plan, or acquires forge credentials.
	previous := authority.BuildRevision
	authority.BuildRevision = "receipt-command-test"
	t.Cleanup(func() { authority.BuildRevision = previous })
	revision := authority.RunningBinaryRevision()
	if revision == "" {
		t.Fatal("test binary cannot attest its revision")
	}
	dir := t.TempDir()
	backup := filepath.Join(dir, "backup")
	if err := os.WriteFile(backup, []byte("isolated restore-tested backup"), 0600); err != nil {
		t.Fatal(err)
	}
	databaseURL := "postgres://unused:unused@127.0.0.1:1/unused?sslmode=disable"
	identity := "openmerge-db-v2:sha256:offline-test"
	now := time.Now().UTC()
	receipt := authority.EmissionReceipt{DatabaseIdentity: identity, BinaryRevision: revision, DrainedWorkers: []string{"isolated/worker"}, DrainedAt: now.Add(-time.Minute), BackupPath: backup, BackupSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("isolated restore-tested backup"))), RestoreTestedAt: now, CompatibleReadersOnly: true}
	completeEmissionOperations(&receipt)
	for _, tc := range []struct {
		name, diagnostic string
		maxAge           time.Duration
		mutate           func(*authority.EmissionReceipt)
	}{
		{"zero age", "maximum age must be positive", 0, func(*authority.EmissionReceipt) {}},
		{"negative age", "maximum age must be positive", -time.Second, func(*authority.EmissionReceipt) {}},
		{"stale drain", "stale emission receipt", time.Hour, func(r *authority.EmissionReceipt) { r.DrainedAt = now.Add(-2 * time.Hour) }},
		{"stale restore", "stale emission receipt", time.Hour, func(r *authority.EmissionReceipt) {
			r.DrainedAt = now.Add(-3 * time.Hour)
			r.RestoreTestedAt = now.Add(-2 * time.Hour)
		}},
		{"wrong binary", "does not match running binary", time.Hour, func(r *authority.EmissionReceipt) { r.BinaryRevision = "another-binary" }},
		{"missing inventory", "complete all-host stopped worker inventory required", time.Hour, func(r *authority.EmissionReceipt) { r.Inventory = nil }},
		{"missing reconciliation", "reconciliation receipt required", time.Hour, func(r *authority.EmissionReceipt) { r.Reconciliation = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := receipt
			tc.mutate(&r)
			path := filepath.Join(t.TempDir(), "receipt.json")
			data, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err = runRulesVerification(t.Context(), verifyOptions{Verify: true, ReceiptMaxAge: tc.maxAge, DatabaseURL: databaseURL, Plan: filepath.Join(dir, "must-not-read"), Receipt: path, Operator: "operator"}, github.RulesOptions{Authority: "openmerge"}, &out)
			if tc.name == "wrong binary" && !errors.Is(err, authority.ErrBinaryRevisionMismatch) {
				t.Fatal(err)
			}
			if err == nil || (tc.name != "wrong binary" && !strings.Contains(err.Error(), tc.diagnostic)) || out.Len() != 0 {
				t.Fatalf("first-emission boundary: err=%v output=%q", err, out.String())
			}
		})
	}
}

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
		case "/repos/acme/project/branches":
			value = []any{map[string]any{"name": "main", "commit": map[string]string{"sha": strings.Repeat("a", 40)}}}
		case "/repos/acme/project/git/ref/heads/main":
			value = map[string]any{"object": map[string]string{"sha": strings.Repeat("a", 40), "type": "commit"}}
		case "/repos/acme/project/git/trees/" + strings.Repeat("a", 40):
			value = map[string]any{"truncated": false, "tree": []any{map[string]string{"path": ".openmerge.yml", "type": "blob", "mode": "100644", "sha": strings.Repeat("b", 40)}, map[string]string{"path": "CODEOWNERS", "type": "blob", "mode": "100644", "sha": strings.Repeat("c", 40)}}}
		case "/repos/acme/project/git/blobs/" + strings.Repeat("b", 40), "/repos/acme/project/git/blobs/" + strings.Repeat("c", 40):
			content := "version: 1\nqueues: [{name: main, merge_when: ['check-success = ci']}]\n"
			if strings.HasSuffix(req.URL.Path, strings.Repeat("c", 40)) {
				content = "* @writer\n"
			}
			value = map[string]any{"size": len(content), "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(content))}
		case "/users/writer":
			value = map[string]any{"id": 21, "login": "writer", "type": "User"}
		case "/repos/acme/project/collaborators/writer/permission":
			value = map[string]any{"permission": "write", "user": map[string]any{"id": 21, "login": "writer", "type": "User"}}
		case "/repos/acme/project/branches/main/protection":
			return &http.Response{StatusCode: 404, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"message":"Branch not protected"}`)), Request: req}, nil
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
		{"identity requires database", []string{"--print-database-identity"}, 1, "requires --database-url", false},
		{"identity rejects verification", []string{"--print-database-identity", "--verify", "--database-url", "unused"}, 1, "cannot be combined", false},
		{"identity rejects mutations", []string{"--print-database-identity", "--apply", "--database-url", "unused"}, 1, "cannot be combined", false},
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

func (f verifyReaderFunc) AcquireGoverningRecheck(context.Context, string) (func(context.Context) error, error) {
	return func(context.Context) error { return nil }, nil
}

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

func completeEmissionOperations(r *authority.EmissionReceipt) {
	r.Inventory = &authority.WorkerInventory{Complete: true}
	for _, id := range r.DrainedWorkers {
		r.Inventory.Workers = append(r.Inventory.Workers, authority.StoppedWorker{ID: id, BinaryRevision: "prior-test-revision", StoppedAt: r.DrainedAt})
	}
	r.Reconciliation = &authority.RestoreReconciliation{SourceDatabaseIdentity: r.DatabaseIdentity, RestoredDatabaseIdentity: "isolated-restore", BackupSHA256: r.BackupSHA256, BackupCapturedAt: r.DrainedAt, BackupWatermark: 1, RestoredWatermark: 1, ReconciledAt: r.RestoreTestedAt, EvidenceSHA256: r.BackupSHA256, AllRefsAndEvidenceReconciled: true}
}
