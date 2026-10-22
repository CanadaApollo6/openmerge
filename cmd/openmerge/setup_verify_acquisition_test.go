package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

type acquiringVerifyReader struct {
	verifyReaderFunc
	acquired  int
	moved     bool
	rechecked int
}

func (r *acquiringVerifyReader) AcquireGoverningRecheck(context.Context, string) (func(context.Context) error, error) {
	r.acquired++
	return func(context.Context) error {
		r.rechecked++
		if r.moved {
			return errors.New("ghs_SECRET")
		}
		return nil
	}, nil
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
	for i, base := range []string{"main", "release/v1"} {
		key := store.QueueKey{Installation: "173082682", Repository: v.Binding.Repository, BaseBranch: base}
		if i == 1 {
			key.Repository = strings.ToUpper(key.Repository)
		}
		if _, err := db.EnsureQueue(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		input := forge.WriteRestrictionInput{Repo: forge.RepoRef{Path: key.Repository}, Binding: v.Binding, AppID: "5383059", BaseSelectors: []authority.BaseSelector{{DefaultBranch: true}, {Pattern: "release/**"}}, ExpectedRefs: []string{"refs/heads/main"}}
		input.Binding.Repository = key.Repository
		input.Binding.QueueKey = store.AuthorityQueueKey(key)
		plan.Targets = append(plan.Targets, verificationTarget{Key: key, Input: input})
	}
	invalid := plan
	invalid.Targets = append(append([]verificationTarget{}, plan.Targets...), verificationTarget{Key: store.QueueKey{Repository: "unregistered"}})
	if err := verifyTargets(t.Context(), db, reader, invalid, "operator", io.Discard); err == nil || reader.acquired != 0 {
		t.Fatalf("validation ordering: err=%v reads=%d", err, reader.acquired)
	}
	if err := verifyTargets(t.Context(), db, observeOnlyVerifyReader{reader}, plan, "operator", io.Discard); err == nil || reader.acquired != 0 {
		t.Fatal("missing acquisition reader accepted", err)
	}
	if err := verifyTargets(t.Context(), db, reader, plan, "operator", io.Discard); err != nil {
		t.Fatal(err)
	}
	if reader.acquired != 1 || reader.rechecked != 1 {
		t.Fatalf("acquisitions=%d", reader.acquired)
	}
	reader.verifyReaderFunc = func(context.Context, forge.WriteRestrictionInput) (authority.Observation, error) {
		reader.moved = true
		return authority.CloneObservation(v.Evidence), nil
	}
	watermarks := map[store.QueueID]int64{}
	for _, q := range plan.Targets {
		id, err := db.EnsureQueue(t.Context(), q.Key)
		if err != nil {
			t.Fatal(err)
		}
		_, mark, err := db.Load(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		watermarks[id] = int64(mark)
	}
	err = verifyTargets(t.Context(), db, reader, plan, "operator", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no verification recorded") || strings.Contains(err.Error(), "SECRET") {
		t.Fatal(err)
	}
	for id, mark := range watermarks {
		_, after, err := db.Load(t.Context(), id)
		if err != nil || int64(after) != mark {
			t.Fatal("verification committed after movement", err)
		}
	}

}

type observeOnlyVerifyReader struct{ reader forge.WriteRestrictionReader }

func (r observeOnlyVerifyReader) ObserveWriteRestriction(ctx context.Context, in forge.WriteRestrictionInput) (authority.Observation, error) {
	return r.reader.ObserveWriteRestriction(ctx, in)
}
