package main

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

type acquiringVerifyReader struct {
	verifyReaderFunc
	acquired int
}

func (r *acquiringVerifyReader) AcquireGoverning(context.Context, string) error {
	r.acquired++
	return nil
}

func TestVerifyAcquiresOnceAfterEveryTargetValidated(t *testing.T) {
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
	v.Binding.CoverageDigest, err = authority.CoverageDigest("main", []authority.BaseSelector{{DefaultBranch: true}, {Pattern: "release/**"}})
	if err != nil {
		t.Fatal(err)
	}
	reader := &acquiringVerifyReader{}
	reader.verifyReaderFunc = func(context.Context, forge.WriteRestrictionInput) (authority.Observation, error) {
		if reader.acquired != 1 {
			t.Fatalf("acquisitions=%d", reader.acquired)
		}
		return authority.CloneObservation(v.Evidence), nil
	}
	plan := verificationPlan{}
	for _, base := range []string{"main", "release/v1"} {
		key := store.QueueKey{Installation: "173082682", Repository: v.Binding.Repository, BaseBranch: base}
		if _, err := db.EnsureQueue(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		input := forge.WriteRestrictionInput{Repo: forge.RepoRef{Path: key.Repository}, Binding: v.Binding, AppID: "5383059", BaseSelectors: []authority.BaseSelector{{DefaultBranch: true}, {Pattern: "release/**"}}, ExpectedRefs: []string{"refs/heads/main"}}
		input.Binding.QueueKey = store.AuthorityQueueKey(key)
		plan.Targets = append(plan.Targets, verificationTarget{Key: key, Input: input})
	}
	invalid := plan
	invalid.Targets = append(append([]verificationTarget{}, plan.Targets...), verificationTarget{Key: store.QueueKey{Repository: "unregistered"}})
	if err := verifyTargets(t.Context(), db, reader, invalid, "operator", io.Discard); err == nil || reader.acquired != 0 {
		t.Fatalf("validation ordering: err=%v reads=%d", err, reader.acquired)
	}
	if err := verifyTargets(t.Context(), db, reader, plan, "operator", io.Discard); err != nil {
		t.Fatal(err)
	}
	if reader.acquired != 1 {
		t.Fatalf("acquisitions=%d", reader.acquired)
	}
}
