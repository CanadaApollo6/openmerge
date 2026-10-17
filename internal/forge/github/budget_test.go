package github

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestBudgetReservationsAtomicAndProtectShares(t *testing.T) {
	tr := &transport{now: func() time.Time { return epoch }, budgetKnown: true, limit: 1000, remaining: 1000, reset: epoch.Add(time.Hour)}
	reserve := func(ctx context.Context) error { _, err := tr.reserve(ctx); return err }
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for range 1200 {
		wg.Go(func() {
			if reserve(t.Context()) == nil {
				admitted.Add(1)
			}
		})
	}
	wg.Wait()
	if got := admitted.Load(); got != 780 {
		t.Fatalf("idle reservations=%d, want 780", got)
	}
	active := forge.WithRequestClass(t.Context(), forge.ActiveRequest)
	for range 100 {
		must(t, reserve(active))
	}
	if reserve(active) == nil {
		t.Fatal("active work spent protected capacity")
	}
	for _, class := range []forge.RequestClass{forge.DiscoveryRequest, forge.RulesRequest} {
		ctx := forge.WithRequestClass(t.Context(), class)
		for range 10 {
			must(t, reserve(ctx))
		}
		if reserve(ctx) == nil {
			t.Fatal("minimum share exceeded reserve")
		}
	}
	if tr.remaining != 100 {
		t.Fatalf("settlement reserve=%d", tr.remaining)
	}
	ctx := forge.WithRequestClass(t.Context(), forge.SettlementRequest)
	for range 100 {
		must(t, reserve(ctx))
	}
	if reserve(ctx) == nil {
		t.Fatal("overspent rate budget")
	}
}

func TestInstallationBudgetsAreIndependent(t *testing.T) {
	f := newAPI(t)
	other, err := f.app.Installation(t.Context(), 8)
	must(t, err)
	tr := f.driver.client.Client().Transport.(*transport)
	tr.mu.Lock()
	tr.budgetKnown = true
	tr.limit = 1000
	tr.remaining = 100
	tr.reset = epoch.Add(time.Hour)
	tr.mu.Unlock()
	if _, err := tr.reserve(t.Context()); err == nil {
		t.Fatal("exhausted installation admitted polling")
	}
	_, err = other.ListOpenPullRequests(t.Context(), testRepo, "main")
	must(t, err)
	if other.Budget().Remaining <= 100 {
		t.Fatal("another installation inherited the exhausted budget")
	}
}

func TestConditionalRevalidationReturnsReservation(t *testing.T) {
	f := newAPI(t)
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/v3/repos/acme/project" {
			return false
		}
		w.Header().Set("ETag", `"repo"`)
		if r.Header.Get("If-None-Match") == `"repo"` {
			w.WriteHeader(http.StatusNotModified)
		} else {
			_, _ = fmt.Fprint(w, `{"default_branch":"main"}`)
		}
		return true
	}
	_, err := f.driver.DefaultBranch(t.Context(), testRepo)
	must(t, err)
	before := f.driver.Budget()
	for range 10 {
		_, err := f.driver.DefaultBranch(t.Context(), testRepo)
		must(t, err)
	}
	after := f.driver.Budget()
	if !before.Known || after.Remaining != before.Remaining {
		t.Fatalf("304 consumed reserved budget: before=%+v after=%+v", before, after)
	}
}
