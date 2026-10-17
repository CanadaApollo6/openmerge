package fake

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

type rerunRecord struct {
	fingerprint string
	result      forge.RerunChecksResult
}

// RerunFailedChecks models a forge that can create a new pending run for every
// identified check. It intentionally performs no CI work; callers observe the
// new run through CheckRuns, just as they must with a real forge.
func (f *Forge) RerunFailedChecks(ctx context.Context, repo forge.RepoRef, req forge.RerunChecksRequest) (forge.RerunChecksResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "RerunFailedChecks"); err != nil {
		return forge.RerunChecksResult{}, err
	}
	if req.TestedSHA == "" || req.TestRef == "" || req.Key == "" || len(req.Runs) == 0 {
		return forge.RerunChecksResult{}, errors.New("tested SHA, test ref, key, and failed runs are required")
	}
	fingerprint, err := rerunFingerprint(req)
	if err != nil {
		return forge.RerunChecksResult{}, err
	}
	r := f.repo(repo)
	cacheKey := req.TestRef + "\x00" + req.Key
	if prior, ok := r.reruns[cacheKey]; ok {
		if prior.fingerprint != fingerprint {
			return forge.RerunChecksResult{}, fmt.Errorf("rerun key %q was already used for another request", req.Key)
		}
		return prior.result, nil
	}

	// Validate the whole request before creating any new run. A status, an
	// unrecognised terminal state, or a zero native ID cannot be targeted safely
	// and is a normal provider limitation rather than a durable request error.
	for _, requested := range req.Runs {
		if !rerunnableState(requested.State) {
			return f.recordRerun(r, cacheKey, fingerprint, forge.RerunChecksResult{
				Unsupported: true,
				Detail:      fmt.Sprintf("check %q is not rerunnable in state %q", requested.Name, requested.State),
			})
		}
		if strings.HasPrefix(requested.Source, "status:") {
			return f.recordRerun(r, cacheKey, fingerprint, forge.RerunChecksResult{Unsupported: true, Detail: "commit statuses cannot be rerun"})
		}
		if requested.Name == "" || requested.Source == "" {
			return f.recordRerun(r, cacheKey, fingerprint, forge.RerunChecksResult{Unsupported: true, Detail: "check provenance is unavailable"})
		}
		if requested.ID == 0 {
			return f.recordRerun(r, cacheKey, fingerprint, forge.RerunChecksResult{Unsupported: true, Detail: "check run has no native ID"})
		}
	}

	type plan struct {
		requested forge.CheckRun
		current   forge.CheckRun
		dispatch  bool
	}
	plans := make([]plan, 0, len(req.Runs))
	unsupported := false
	for _, requested := range req.Runs {
		requestedAttempt := effectiveAttempt(requested.Attempt)
		var exact forge.CheckRun
		var newest forge.CheckRun
		foundExact, foundNewest := false, false
		for _, current := range r.checks[req.TestedSHA] {
			if current.Name != requested.Name || current.Source != requested.Source || current.HeadBranch != req.TestRef {
				continue
			}
			current.Attempt = effectiveAttempt(current.Attempt)
			if !foundNewest || newerCheck(current, newest) {
				newest, foundNewest = current, true
			}
			if current.ID == requested.ID && current.Attempt == requestedAttempt {
				exact, foundExact = current, true
			}
		}
		if !foundExact {
			if foundNewest && newerCheck(newest, forge.CheckRun{ID: requested.ID, Attempt: requestedAttempt}) {
				// A newer identity is enough to recover a lost process cache.
				plans = append(plans, plan{requested: requested, current: newest})
			} else {
				// The exact old observation is gone. It is unsafe to broaden the
				// request to a different run, so report a normal limitation.
				unsupported = true
			}
			continue
		}
		if foundNewest && (newest.ID != exact.ID || newest.Attempt != exact.Attempt) {
			// This is the crash-replay path: the original may still be failed,
			// but a newer pending/completed run proves the request already took
			// effect. Never match the first old failure and dispatch again.
			plans = append(plans, plan{requested: requested, current: newest})
			continue
		}
		plans = append(plans, plan{requested: requested, current: exact, dispatch: rerunnableState(exact.State)})
	}
	if unsupported {
		return f.recordRerun(r, cacheKey, fingerprint, forge.RerunChecksResult{Unsupported: true, Detail: "exact failed check is no longer observable"})
	}

	maxID := maxCheckID(r.checks[req.TestedSHA])
	created := 0
	for i := range plans {
		if !plans[i].dispatch {
			continue
		}
		maxID++
		attempt := effectiveAttempt(plans[i].requested.Attempt) + 1
		r.checks[req.TestedSHA] = append(r.checks[req.TestedSHA], forge.CheckRun{
			ID: maxID, Attempt: attempt,
			Name: plans[i].requested.Name, Source: plans[i].requested.Source,
			State: forge.Pending, HeadBranch: req.TestRef,
		})
		created++
	}
	result := forge.RerunChecksResult{Detail: "rerun already observed"}
	if created > 0 {
		result.Detail = "rerun requested"
	}
	return f.recordRerun(r, cacheKey, fingerprint, result)
}

func (f *Forge) recordRerun(r *repository, key, fingerprint string, result forge.RerunChecksResult) (forge.RerunChecksResult, error) {
	r.reruns[key] = rerunRecord{fingerprint: fingerprint, result: result}
	return result, nil
}

func rerunnableState(state forge.CheckState) bool {
	switch state {
	case forge.Failure, forge.Cancelled, forge.Skipped, forge.Neutral:
		return true
	default:
		return false
	}
}

func effectiveAttempt(attempt uint64) uint64 {
	if attempt == 0 {
		return 1
	}
	return attempt
}

func newerCheck(candidate, current forge.CheckRun) bool {
	if candidate.ID != current.ID {
		return candidate.ID > current.ID
	}
	return effectiveAttempt(candidate.Attempt) > effectiveAttempt(current.Attempt)
}

func maxCheckID(checks []forge.CheckRun) uint64 {
	var maxID uint64
	for _, check := range checks {
		if check.ID > maxID {
			maxID = check.ID
		}
	}
	return maxID
}

func rerunFingerprint(req forge.RerunChecksRequest) (string, error) {
	runs := slices.Clone(req.Runs)
	for i := range runs {
		if runs[i].Name == "" || runs[i].Source == "" {
			return "", errors.New("each failed run needs a name and source")
		}
		runs[i].Attempt = effectiveAttempt(runs[i].Attempt)
	}
	slices.SortFunc(runs, func(a, b forge.CheckRun) int {
		if a.Name != b.Name {
			return strings.Compare(a.Name, b.Name)
		}
		if a.Source != b.Source {
			return strings.Compare(a.Source, b.Source)
		}
		if a.ID != b.ID {
			if a.ID < b.ID {
				return -1
			}
			return 1
		}
		if a.Attempt < b.Attempt {
			return -1
		}
		if a.Attempt > b.Attempt {
			return 1
		}
		return strings.Compare(a.HeadBranch, b.HeadBranch)
	})
	return fmt.Sprintf("%s\x00%s\x00%#v", req.TestedSHA, req.TestRef, runs), nil
}
