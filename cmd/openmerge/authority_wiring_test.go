package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge/forgetest"
	"github.com/CanadaApollo6/openmerge/internal/serve"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestProductionAuthorityRuntimeWiring(t *testing.T) {
	input := forgetest.WriteRestrictionInput()
	key := store.QueueKey{Installation: "7", Repository: input.Binding.Repository, BaseBranch: "main"}
	input.Binding.QueueKey = store.AuthorityQueueKey(key)
	plan := filepath.Join(t.TempDir(), "plan.json")
	raw, err := json.Marshal(struct {
		Targets []serve.AuthorityTarget `json:"targets"`
	}{[]serve.AuthorityTarget{{Key: key, Input: input}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan, raw, 0600); err != nil {
		t.Fatal(err)
	}
	o := serveOptions{app: 5383059, authorityPlan: plan, emissionReceipt: "receipt.json", emissionReceiptMaxAge: time.Hour}
	runtime, err := productionAuthorityRuntime(t.Context(), o, authorityIdentityStub{})
	if err == nil || runtime != nil || !strings.Contains(err.Error(), "App binding mismatch") {
		t.Fatalf("mismatched App accepted: %v", err)
	}
	// The valid plan comes from the same App identity used by production discovery.
	o.app = 42
	runtime, err = productionAuthorityRuntime(t.Context(), o, authorityIdentityStub{})
	if err != nil || runtime == nil {
		t.Fatal("valid runtime missing", err)
	}
	if runtime.DatabaseIdentity != "startup-database" {
		t.Fatal("startup failed to pin database identity", runtime.DatabaseIdentity)
	}
	if _, err := productionAuthorityRuntime(t.Context(), o, authorityIdentityStub{fail: true}); err == nil || err.Error() != "authority database identity unavailable" {
		t.Fatalf("identity failure accepted: %v", err)
	}
	o.emissionReceipt = ""
	runtime, err = productionAuthorityRuntime(t.Context(), o, authorityIdentityStub{})
	if err != nil {
		t.Fatalf("plan-only runtime must permit operation after first emission: %v", err)
	}
	if _, err := runtime.Receipt(); err == nil || !strings.Contains(err.Error(), "receipt unavailable or invalid") {
		t.Fatalf("missing first-emission receipt accepted: %v", err)
	}
	opts := productionServeOptions(o, runtime, nil, nil)
	if opts.Authority != runtime || opts.Authority == nil {
		t.Fatal("production discovery omitted authority runtime")
	}
}

type authorityIdentityStub struct{ fail bool }

func (s authorityIdentityStub) DatabaseIdentity(context.Context) (string, error) {
	if s.fail {
		return "", errors.New("SECRET")
	}
	return "startup-database", nil
}
