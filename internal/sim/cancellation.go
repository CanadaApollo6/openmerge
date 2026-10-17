package sim

import (
	"context"
	"fmt"
	"slices"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// CancelChecks exercises the executor's optional forge capability, rather than
// inferring cancellation from a discarded planner batch. Only the exact run
// identities in the durable request can release simulated runner capacity.
func (f *faultForge) CancelChecks(ctx context.Context, repo forge.RepoRef, req forge.CancelChecksRequest) (forge.CancelChecksResult, error) {
	if err := ctx.Err(); err != nil {
		return forge.CancelChecksResult{}, err
	}
	if repo != f.sim.repo {
		return forge.CancelChecksResult{}, fmt.Errorf("cancellation repository does not match simulation")
	}
	run := f.sim.tests[req.TestedSHA]
	if run == nil || run.ref != req.TestRef {
		return forge.CancelChecksResult{}, fmt.Errorf("cancellation does not identify a tested batch commit and ref")
	}
	// This oracle is independent of queue cancellation authorization. A faulty
	// planner or stale executor must fail the simulation before the provider effect.
	if run.landed || f.sim.observed[req.TestedSHA] != nil {
		return forge.CancelChecksResult{}, fmt.Errorf("cancellation targeted landed batch %s", run.batchID)
	}
	for _, batch := range f.sim.state.Batches() {
		if batch.TestedSHA == req.TestedSHA {
			return forge.CancelChecksResult{}, fmt.Errorf("cancellation targeted live batch %s in phase %s", batch.ID, batch.Phase)
		}
	}
	// The optional fault stream must not perturb PR 3's unsupported adopter
	// workload. It draws randomness only when cancellation faults are configured.
	if f.sim.cfg.Faults.ForgeErrorRate > 0 || f.sim.cfg.Faults.Methods["CancelChecks"] > 0 || f.sim.cfg.Faults.FailFirst["CancelChecks"] > 0 {
		if err := f.before("CancelChecks"); err != nil {
			return forge.CancelChecksResult{}, err
		}
	}
	if f.sim.cfg.CI == nil || !f.sim.cfg.CI.CancellationSupported {
		for _, job := range f.sim.ciJobs {
			if job.sha == req.TestedSHA && slices.ContainsFunc(req.Runs, func(check forge.CheckRun) bool {
				return check.ID == job.id && check.Attempt == job.attempt && check.Name == "ci" && check.Source == "fake:ci"
			}) {
				job.cancellationRequested = true
				job.cancellationUnsupported = true
			}
		}
		return forge.CancelChecksResult{Unsupported: "simulated CI provider does not support cancellation"}, nil
	}
	result := forge.CancelChecksResult{}
	for _, check := range req.Runs {
		for _, job := range f.sim.ciJobs {
			if job.sha != req.TestedSHA || job.id != check.ID || job.attempt != check.Attempt || check.Name != "ci" || check.Source != "fake:ci" {
				continue
			}
			job.cancellationRequested = true
			if job.finished.IsZero() && !job.cancelled {
				job.cancelled = true
				if !job.started.IsZero() {
					job.finished = f.sim.now
					f.sim.ciRunning--
				}
			}
			if job.cancelled {
				check.State = forge.Cancelled
				if run.job == job {
					f.SetCheckRuns(repo, req.TestedSHA, []forge.CheckRun{{ID: job.id, Attempt: job.attempt, Name: "ci", State: forge.Cancelled, HeadBranch: run.ref}})
				}
			} else if job.success {
				check.State = forge.Success
			} else {
				check.State = forge.Failure
			}
			result.Confirmed = append(result.Confirmed, check)
		}
	}
	f.sim.admitCI()
	f.sim.observeWrite()
	return result, nil
}
