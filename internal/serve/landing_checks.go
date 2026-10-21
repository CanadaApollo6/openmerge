package serve

import (
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// landingCheckEvidence copies the exact evaluator read. It deliberately retains
// all evaluator runs after the executor's provenance and retry filtering,
// including unreferenced producer collisions. Under checks.retry_failed this is
// only the newest-attempt set; superseded attempts remain in the retry ledger.
// PR 10a must join by batch/tested SHA and provider run/attempt identity. No later forge read enriches it.
func landingCheckEvidence(basis queue.State, policyDigest string, land queue.Land, checks []forge.CheckRun, at time.Time) (*queue.LandingChecksBuilder, error) {
	e := queue.LandingChecksRecorded{BatchID: land.BatchID, ConfigCommit: basis.ConfigCommit, ConfigDigest: basis.ConfigDigest, PolicyDigest: policyDigest, TestedSHA: land.TestedSHA, LandedSHA: land.TestedSHA, Method: land.Method, TestRef: land.Ref, RequiredCheckSource: basis.LandingCheckSources(), ObservedAt: at.UTC()}
	if land.Method == queue.FastForward && len(land.PRs) > 0 {
		e.LandedSHA = land.PRs[len(land.PRs)-1].HeadSHA
	}
	builder, err := queue.NewLandingChecksBuilder(e)
	if err != nil {
		return nil, err
	}
	for _, run := range checks {
		kind, identity, ok := strings.Cut(run.Source, ":")
		var producer *string
		if run.Source == "" {
			kind = "unavailable"
		} else if !ok || kind != "app" && kind != "status" {
			kind = "other"
			identity = run.Source
			producer = &identity
		} else if identity != "" && identity != "0" {
			producer = &identity
		}
		if producer == nil {
			kind = "unavailable"
		}
		c := queue.LandingCheck{Name: run.Name, Source: run.Source, ProducerKind: kind, ProducerIdentity: producer, Result: run.State, RunID: knownCheckNumber(run.ID), Attempt: knownCheckNumber(run.Attempt), WorkflowRunID: knownCheckNumber(run.WorkflowRunID)}
		if run.AttemptInferred {
			c.Attempt = nil
		}
		if run.HeadBranch != "" {
			branch := run.HeadBranch
			c.HeadBranch = &branch
		}
		if err := builder.AddCheck(c); err != nil {
			return nil, err
		}
	}
	return builder, nil
}

func knownCheckNumber(n uint64) *uint64 {
	if n == 0 {
		return nil
	}
	return &n
}
