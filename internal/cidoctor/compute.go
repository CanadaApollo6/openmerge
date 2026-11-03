package cidoctor

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/queuemetrics"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

type observation struct {
	detail   string
	id       uint64
	baseline []uint64
	hit      bool
	excess   float64
	values   map[string]float64
}

type series struct {
	code, subject string
	observations  []observation
}

type analyzer struct {
	report Report
	series map[string]*series
}

// Compute uses the journal's push recording time to select [Since, Until).
// Default-branch and overlapping PR metadata are comparison evidence only.
func Compute(j store.Journal, actions Actions, w Window) (Report, error) {
	if w.Since.IsZero() || !w.Until.After(w.Since) {
		return Report{}, fmt.Errorf("invalid CI doctor window")
	}
	if actions.Version != CIDoctorVersion || actions.Repository != j.Key.Repository || actions.DefaultBranch == "" {
		return Report{}, fmt.Errorf("Actions version, repository, or default branch does not match the report contract")
	}
	a := analyzer{report: Report{Version: CIDoctorVersion, Installation: j.Key.Installation, Repository: j.Key.Repository, BaseBranch: j.Key.BaseBranch, Lane: j.Key.Lane, Since: w.Since, Until: w.Until,
		Thresholds:  map[string]float64{"MinimumRuns": MinimumRuns, "ColdSetupRatio": ColdSetupRatio, "LongPoleFraction": LongPoleFraction, "SiblingIdleFraction": SiblingIdleFraction, "SerialStepFraction": SerialStepFraction, "LongJobSeconds": LongJobSeconds, "CheckoutFraction": CheckoutFraction, "CheckoutSeconds": CheckoutSeconds, "RetryFraction": RetryFraction},
		Assessments: []Assessment{}, Findings: []Finding{}, Jobs: []JobTiming{}, Gaps: []Gap{}}, series: map[string]*series{}}
	pushes := journalPushes(j, w)
	counts := map[string]int{}
	for _, r := range actions.Runs {
		counts[attemptKey(r.ID, r.Attempt, "")]++
	}
	baseline := map[string][]struct {
		id    uint64
		setup float64
	}{}
	for _, r := range actions.Runs {
		if r.HeadBranch != actions.DefaultBranch || r.Event != "push" || r.Attempt != 1 || r.CreatedAt.Before(w.Since) || !r.CreatedAt.Before(w.Until) {
			continue
		}
		if counts[attemptKey(r.ID, r.Attempt, "")] != 1 || validateRun(r, w.Until) != "" {
			a.gap(r.ID, "baseline-data", "incomplete or ambiguous default-branch run")
			continue
		}
		baselineTimings := []JobTiming{}
		for _, job := range r.Jobs {
			if job.Conclusion != "success" {
				a.gap(r.ID, "baseline-data", job.Name+": baseline job did not succeed")
				break
			}
			t, err := timing(r, job, nil, w.Until)
			if err != nil {
				a.gap(r.ID, "baseline-data", job.Name+": "+err.Error())
				break
			}
			baselineTimings = append(baselineTimings, t)
		}
		if len(baselineTimings) != len(r.Jobs) {
			continue
		}
		for i, job := range r.Jobs {
			t := baselineTimings[i]
			k := subject(r, job)
			baseline[k] = append(baseline[k], struct {
				id    uint64
				setup float64
			}{r.ID, t.SetupSeconds})
		}
	}
	matched := map[*push]bool{}
	runIDs := map[uint64]bool{}
	for _, r := range actions.Runs {
		var candidates []*push
		for _, p := range pushes {
			if p.sha == r.HeadSHA && p.ref == r.HeadBranch {
				candidates = append(candidates, p)
			}
		}
		if len(candidates) == 0 {
			continue
		}
		for _, p := range candidates {
			matched[p] = true
		}
		if len(candidates) != 1 || counts[attemptKey(r.ID, r.Attempt, "")] != 1 {
			a.gap(r.ID, "ambiguous-run", "multiple journal pushes or recorded attempts share this identity")
			continue
		}
		p := candidates[0]
		if len(p.checks) > 0 && !p.checks[r.ID] {
			a.gap(r.ID, "run-identity", "workflow run is not among the journal's identified checks")
			continue
		}
		if r.Event != "push" {
			a.gap(r.ID, "run-identity", "queue run is not a push event")
			continue
		}
		if detail := validateRun(r, w.Until); detail != "" {
			a.gap(r.ID, "run-data", detail)
			continue
		}
		runIDs[r.ID] = true
		timings := make([]JobTiming, 0, len(r.Jobs))
		complete := true
		for _, job := range r.Jobs {
			t, err := timing(r, job, p, w.Until)
			if err != nil {
				a.gap(r.ID, "job-timing", job.Name+": "+err.Error())
				complete = false
				continue
			}
			timings = append(timings, t)
			a.report.Jobs = append(a.report.Jobs, t)
		}
		a.maskedSkip(r)
		if !complete {
			continue
		}
		for i, job := range r.Jobs {
			t := timings[i]
			k := subject(r, job)
			if t.DispatchSeconds == nil {
				a.gap(r.ID, "dispatch-time", job.Name+": push timestamp unavailable, after job creation, or a rerun")
			} else {
				a.add("time-split", k, observation{id: r.ID, hit: true, values: map[string]float64{"dispatch_seconds": *t.DispatchSeconds, "wait_seconds": t.WaitSeconds, "setup_seconds": t.SetupSeconds, "run_seconds": t.RunSeconds}})
			}
			// Baseline attempts and runner labels must match. Renames become gaps.
			if r.Attempt == 1 {
				var values []float64
				var ids []uint64
				for _, b := range baseline[k] {
					values = append(values, b.setup)
					ids = append(ids, b.id)
				}
				if len(values) < MinimumRuns || median(values) <= 0 {
					a.gap(r.ID, "warm-baseline", job.Name+": fewer than 10 matching default-branch runs or zero median")
				}
				if len(values) > 0 && median(values) > 0 {
					warm := median(values)
					a.add("cold-setup", k, observation{id: r.ID, baseline: ids, hit: t.SetupSeconds > warm*ColdSetupRatio, excess: max(0, t.SetupSeconds-warm) / 60, values: map[string]float64{"queue_setup_seconds": t.SetupSeconds, "baseline_setup_seconds": warm, "setup_ratio": t.SetupSeconds / warm}})
				}
			}
			duration := t.SetupSeconds + t.RunSeconds
			for _, step := range t.Steps {
				hit := duration > LongJobSeconds && step.Seconds > SerialStepFraction*duration
				a.add("serial-step", k+" :: "+step.Name, observation{id: r.ID, hit: hit, excess: choose(hit, max(0, step.Seconds-SerialStepFraction*duration)/60, 0), values: map[string]float64{"job_seconds": duration, "step_seconds": step.Seconds, "step_fraction": step.Seconds / max(duration, 1)}})
			}
			requestedAt, confirmed := p.retries[attemptKey(r.ID, r.Attempt, job.Name)]
			retry := confirmed && !job.StartedAt.Before(requestedAt)
			if r.Attempt > 1 && !retry {
				a.gap(r.ID, "retry-evidence", job.Name+": no confirmed journal retry preceding execution")
			}
			a.add("retry-habit", k, observation{id: r.ID, hit: retry, excess: choose(retry, duration/60, 0), values: map[string]float64{"job_seconds": duration}})
		}
		// Run-wide comparisons require the complete job set and the first attempt.
		if r.Attempt == 1 {
			a.runComparisons(r, timings, actions, counts)
		}
	}
	for _, p := range pushes {
		if !matched[p] {
			a.gap(0, "missing-actions", p.ref+" @ "+p.sha)
		}
	}
	a.report.QueueRuns = len(runIDs)
	a.finish()
	return a.report, nil
}

func choose(ok bool, a, b float64) float64 {
	if ok {
		return a
	}
	return b
}
func median(v []float64) float64 { return queuemetrics.Percentile(v, 0.5) }
func subject(r Run, j Job) string {
	labels := slices.Clone(j.Labels)
	slices.Sort(labels)
	return fmt.Sprintf("%d %s :: %s [%s]", r.WorkflowID, r.Path, j.Name, strings.Join(labels, ","))
}
func (a *analyzer) gap(id uint64, code, detail string) {
	a.report.Gaps = append(a.report.Gaps, Gap{RunID: id, Code: code, Detail: detail})
}
func (a *analyzer) add(code, subject string, o observation) {
	k := code + "\x00" + subject
	s := a.series[k]
	if s == nil {
		s = &series{code: code, subject: subject}
		a.series[k] = s
	}
	s.observations = append(s.observations, o)
}
func unique(ids []uint64) []uint64 { slices.Sort(ids); return slices.Compact(ids) }

func (a *analyzer) finish() {
	for _, s := range a.series {
		ids, hits, base := []uint64{}, []uint64{}, []uint64{}
		values := map[string][]float64{}
		excess := 0.0
		details := []string{}
		for _, o := range s.observations {
			if o.detail != "" {
				details = append(details, o.detail)
			}
			ids = append(ids, o.id)
			base = append(base, o.baseline...)
			if o.hit {
				hits = append(hits, o.id)
			}
			excess += o.excess
			for k, v := range o.values {
				values[k] = append(values[k], v)
			}
		}
		assessment := Assessment{Code: s.code, Subject: s.subject, RunIDs: unique(ids), BaselineIDs: unique(base), Metrics: map[string]float64{}, Details: []string{}}
		slices.Sort(details)
		assessment.Details = slices.Compact(details)
		assessment.QualifyingRuns = len(assessment.RunIDs)
		assessment.TriggeredIDs = unique(hits)
		assessment.TriggeredRuns = len(assessment.TriggeredIDs)
		for k, v := range values {
			assessment.Metrics["median_"+k] = median(v)
		}
		fraction := float64(assessment.TriggeredRuns) / float64(assessment.QualifyingRuns)
		assessment.Metrics["trigger_fraction"] = fraction
		a.report.Assessments = append(a.report.Assessments, assessment)
		trigger := assessment.TriggeredRuns > 0
		switch s.code {
		case "time-split":
			trigger = true
		case "cold-setup":
			trigger = len(assessment.BaselineIDs) >= MinimumRuns && assessment.Metrics["median_queue_setup_seconds"] > ColdSetupRatio*assessment.Metrics["median_baseline_setup_seconds"]
		case "long-pole":
			trigger = fraction >= LongPoleFraction && assessment.Metrics["median_sibling_idle_seconds"] > SiblingIdleFraction*assessment.Metrics["median_job_seconds"]
		case "retry-habit":
			trigger = fraction > RetryFraction
		}
		if assessment.QualifyingRuns < MinimumRuns || !trigger {
			continue
		}
		fix, comparison := advice(s.code)
		a.report.Findings = append(a.report.Findings, Finding{Code: s.code, Subject: s.subject, Evidence: assessment, MinutesAtStake: excess, Comparison: comparison, SuggestedFix: fix})
	}
	slices.SortFunc(a.report.Assessments, func(x, y Assessment) int { return strings.Compare(x.Code+"\x00"+x.Subject, y.Code+"\x00"+y.Subject) })
	slices.SortFunc(a.report.Findings, func(x, y Finding) int { return strings.Compare(x.Code+"\x00"+x.Subject, y.Code+"\x00"+y.Subject) })
	slices.SortFunc(a.report.Jobs, func(x, y JobTiming) int {
		return strings.Compare(fmt.Sprintf("%020d/%020d/%s", x.RunID, x.Attempt, x.Job), fmt.Sprintf("%020d/%020d/%s", y.RunID, y.Attempt, y.Job))
	})
	sort.Slice(a.report.Gaps, func(i, j int) bool {
		x, y := a.report.Gaps[i], a.report.Gaps[j]
		if x.RunID != y.RunID {
			return x.RunID < y.RunID
		}
		return x.Code+x.Detail < y.Code+y.Detail
	})
	a.report.Gaps = slices.Compact(a.report.Gaps)
	a.report.GapCount = len(a.report.Gaps)
}

func advice(code string) (string, string) {
	switch code {
	case "time-split":
		return "", "Context only; no excess or projected saving. Dispatch and wait are per job; parallel timings are not additive wall-clock."
	case "cold-setup":
		return "Save caches from the default branch only, so queue branches can restore them, and key the caches on the lockfiles.", "Sum of positive queue setup excess over the matching default-branch median; step names are heuristic, not proof of a cold cache."
	case "long-pole":
		return "Shard the job, give it a larger runner, or split its slowest step.", "Sum of median sibling finish-to-last-job idle seconds per observed run; last-finisher proxy, not a projected saving."
	case "serial-step":
		return "Split or parallelize the step. If it is a test suite, shard it by package or by historical duration.", "Measured step time above 70% of job duration, only in jobs longer than 10 minutes."
	case "checkout-cost":
		return "Fetch only what the job needs: fetch-depth: 1 or a blobless partial clone (filter: blob:none), and sparse checkout for jobs that read one directory.", "Larger of measured summed checkout excess over 10% of runner time and the largest checkout excess over 60 seconds; never added together."
	case "masked-skip":
		return "Make the aggregate job fail unless every needs.*.result is success.", "Zero minutes; a passing aggregate with skipped or cancelled dependencies does not establish a time saving."
	case "retry-habit":
		return "Fix or quarantine the flaky test. Use ADR 0015's targeted reruns, not whole-workflow retries.", "Measured runner time of journal-confirmed same-SHA rerun jobs, compared with no rerun."
	case "duplicate-pr-run":
		return "Cancel superseded pull request runs with a concurrency group, and never cancel queue runs.", "Measured PR job runner time overlapping the same-SHA queue run; wait is excluded and each PR job is counted once per queue comparison. Findings may overlap; do not sum them."
	}
	return "", ""
}
