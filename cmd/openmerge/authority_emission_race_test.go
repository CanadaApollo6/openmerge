package main

import (
	"context"
	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type reviewVerifyReader struct {
	verifyReaderFunc
	recheck func(context.Context) error
}

func (r reviewVerifyReader) AcquireGoverningRecheck(context.Context, string) (func(context.Context) error, error) {
	return r.recheck, nil
}
func TestReviewFirstEmissionAppendRace(t *testing.T) {
	_, dnsErr := net.LookupHost("localhost")
	if dnsErr != nil {
		t.Fatal(dnsErr)
	}
	synctest.Test(t, func(t *testing.T) {
		db := acceptanceDB(t)
		raw, err := os.ReadFile("../../internal/queue/testdata/wire/v1/event_write_restriction_verified.json")
		if err != nil {
			t.Fatal(err)
		}
		e, err := queue.DecodeEvent(raw)
		if err != nil {
			t.Fatal(err)
		}
		v := e.(queue.WriteRestrictionVerified)
		key := store.QueueKey{Installation: "173082682", Repository: v.Binding.Repository, BaseBranch: "main"}
		q, err := db.EnsureQueue(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		other := key
		other.BaseBranch = "release/other"
		otherID, err := db.EnsureQueue(t.Context(), other)
		if err != nil {
			t.Fatal(err)
		}
		watermark, err := db.Append(t.Context(), q, []queue.Event{queue.BaseMoved{SHA: "backup"}}, "")
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
		reader := reviewVerifyReader{verifyReaderFunc: func(context.Context, forge.WriteRestrictionInput) (authority.Observation, error) {
			return authority.CloneObservation(v.Evidence), nil
		}, recheck: func(ctx context.Context) error {
			_, err := db.Append(ctx, otherID, []queue.Event{queue.BaseMoved{SHA: "unbacked"}}, "")
			return err
		}}
		err = verifyTargets(t.Context(), db, reader, plan, "operator", io.Discard, verificationReceiptGate{Watermark: &watermark, Prepare: func() error { return db.CheckAuthorityEmissionBackup(t.Context(), watermark) }, Recheck: func() error { return nil }})
		if err == nil || !strings.Contains(err.Error(), "watermark") {
			t.Fatal("first authority event committed after an unbacked append during final governing recheck")
		}
		boundary, err := db.AuthorityBoundaryExists(t.Context())
		if err != nil || boundary {
			t.Fatalf("refusal persisted boundary: %t %v", boundary, err)
		}
	})
}

func TestVerificationReceiptOnlyBeforeBoundary(t *testing.T) {
	_, err := net.LookupHost("localhost")
	if err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		db := acceptanceDB(t)
		opts := verifyOptions{Receipt: "/missing-emission-receipt", ReceiptMaxAge: time.Hour}
		if _, err := verificationEmissionReceipt(t.Context(), db, opts); err == nil || err.Error() != "emission receipt stale/invalid" {
			t.Fatalf("first emission diagnostic: %v", err)
		}
		raw, err := os.ReadFile("../../internal/queue/testdata/wire/v1/event_write_restriction_verified.json")
		if err != nil {
			t.Fatal(err)
		}
		event, err := queue.DecodeEvent(raw)
		if err != nil {
			t.Fatal(err)
		}
		v := event.(queue.WriteRestrictionVerified)
		q, err := db.EnsureQueue(t.Context(), store.QueueKey{Installation: "173082682", Repository: v.Binding.Repository, BaseBranch: "main"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.CommitWriteRestrictionVerification(t.Context(), []store.VerificationRecord{{QueueID: q, Event: v}}); err != nil {
			t.Fatal(err)
		}
		if receipt, err := verificationEmissionReceipt(t.Context(), db, opts); err != nil || receipt != nil {
			t.Fatalf("post-boundary setup requires receipt: %+v %v", receipt, err)
		}
	})
}
