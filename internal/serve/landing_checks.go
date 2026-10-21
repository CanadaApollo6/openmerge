package serve

import (
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// landingCheckEvidence copies the exact evaluator read. It deliberately retains
// all evaluator runs after the executor's provenance and retry filtering,
// including unreferenced producer collisions. No later forge read enriches it.
func landingCheckEvidence(basis queue.State, policyDigest string, land queue.Land, checks []forge.CheckRun, members []queue.LandingCheckMember, at time.Time) queue.LandingChecksRecorded {
	e := queue.LandingChecksRecorded{BatchID: land.BatchID, ConfigCommit: basis.ConfigCommit, ConfigDigest: basis.ConfigDigest, PolicyDigest: policyDigest, TestedSHA: land.TestedSHA, LandedSHA: land.TestedSHA, Method: land.Method, TestRef: land.Ref, RequiredCheckSource: basis.LandingCheckSources(), Members: members, Checks: []queue.LandingCheck{}, ObservedAt: at.UTC()}
	if land.Method == queue.FastForward && len(land.PRs) > 0 {
		e.LandedSHA = land.PRs[len(land.PRs)-1].HeadSHA
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
		e.Checks = append(e.Checks, c)
	}
	return e
}

func knownCheckNumber(n uint64) *uint64 {
	if n == 0 {
		return nil
	}
	return &n
}
