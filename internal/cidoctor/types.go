// Package cidoctor derives advisory CI diagnostics from a journal and recorded
// Actions metadata. Live collection performs reads only, never scheduling or forge mutations.
package cidoctor

import (
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// CIDoctorVersion identifies the report and recorded Actions JSON contracts.
const CIDoctorVersion = 1

// Thresholds are part of the versioned contract. All comparisons are strict
// except the minimum sample and critical-path fraction.
const (
	MinimumRuns         = 10
	ColdSetupRatio      = 2.0
	LongPoleFraction    = 0.80
	SiblingIdleFraction = 0.50
	SerialStepFraction  = 0.70
	LongJobSeconds      = 600.0
	CheckoutFraction    = 0.10
	CheckoutSeconds     = 60.0
	RetryFraction       = 0.05
)

// Actions contains only metadata needed by the report, never logs or artifacts.
type Actions struct {
	Version       int    `json:"version"`
	Repository    string `json:"repository"`
	DefaultBranch string `json:"default_branch"`
	Runs          []Run  `json:"runs"`
	Gaps          []Gap  `json:"gaps,omitempty"`
}

// Run records one workflow attempt from the read-only forge reader.
type Run = forge.ActionsRun

// Job retains one job and its step timings.
type Job = forge.ActionsJob

// Step retains metadata for one Actions step.
type Step = forge.ActionsStep

// Graph records dependencies and required-check policy at the run head SHA.
type Graph = forge.ActionsGraph

// Window selects journal pushes recorded in [Since, Until).
type Window struct{ Since, Until time.Time }

// Report is the versioned, advisory result for one journal queue.
type Report struct {
	Version      int                `json:"version"`
	Installation string             `json:"installation"`
	Repository   string             `json:"repository"`
	BaseBranch   string             `json:"base_branch"`
	Lane         string             `json:"lane"`
	Since        time.Time          `json:"since"`
	Until        time.Time          `json:"until"`
	Thresholds   map[string]float64 `json:"thresholds"`
	QueueRuns    int                `json:"queue_runs"`
	Assessments  []Assessment       `json:"assessments"`
	Findings     []Finding          `json:"findings"`
	Jobs         []JobTiming        `json:"jobs"`
	Gaps         []Gap              `json:"gaps"`
	GapCount     int                `json:"gap_count"`
}

// Assessment retains observations even when there are too few runs for advice.
type Assessment struct {
	Details        []string           `json:"details"`
	Code           string             `json:"code"`
	Subject        string             `json:"subject"`
	TriggeredIDs   []uint64           `json:"triggered_run_ids"`
	QualifyingRuns int                `json:"qualifying_runs"`
	TriggeredRuns  int                `json:"triggered_runs"`
	RunIDs         []uint64           `json:"workflow_run_ids"`
	BaselineIDs    []uint64           `json:"baseline_run_ids"`
	Metrics        map[string]float64 `json:"metrics"`
}

// Finding contains measured evidence and advice, never passing check evidence.
type Finding struct {
	Code           string     `json:"code"`
	Subject        string     `json:"subject"`
	Evidence       Assessment `json:"evidence"`
	MinutesAtStake float64    `json:"minutes_at_stake"`
	Comparison     string     `json:"comparison"`
	SuggestedFix   string     `json:"suggested_fix"`
}

// Gap identifies unavailable evidence for a run and diagnostic.
type Gap struct {
	RunID  uint64 `json:"workflow_run_id"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// JobTiming partitions elapsed job time and displays the heuristic classification.
type JobTiming struct {
	CreatedAt       time.Time        `json:"created_at"`
	StartedAt       time.Time        `json:"started_at"`
	CompletedAt     time.Time        `json:"completed_at"`
	Conclusion      string           `json:"conclusion"`
	RunID           uint64           `json:"workflow_run_id"`
	Attempt         uint64           `json:"run_attempt"`
	Job             string           `json:"job"`
	DispatchSeconds *float64         `json:"dispatch_seconds"`
	WaitSeconds     float64          `json:"wait_seconds"`
	SetupSeconds    float64          `json:"setup_seconds"`
	RunSeconds      float64          `json:"run_seconds"`
	Steps           []Classification `json:"steps"`
}

// Classification labels each step, including names the heuristic cannot classify.
type Classification struct {
	Name    string  `json:"name"`
	Class   string  `json:"class"`
	Phase   string  `json:"phase"`
	Seconds float64 `json:"seconds"`
}
