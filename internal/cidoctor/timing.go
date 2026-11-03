package cidoctor

import (
	"fmt"
	"strings"
	"time"
)

// Names are the jobs API's only step description. Unknown commands are shown
// as unknown, and end the setup prefix. This never claims to inspect commands.
func classify(name string) string {
	n := strings.ToLower(name)
	if strings.HasPrefix(n, "post ") || n == "complete job" || n == "stop containers" {
		return "cleanup"
	}
	if strings.Contains(n, "actions/checkout@") || n == "checkout" || n == "checkout code" {
		return "checkout"
	}
	if n == "set up job" || n == "initialize containers" || strings.Contains(n, "actions/setup-") || strings.Contains(n, "actions/cache/restore@") || strings.Contains(n, "actions/cache@") || n == "restore cache" || n == "install dependencies" || n == "npm ci" || n == "run npm ci" || n == "run go mod download" {
		return "setup"
	}
	return "unknown"
}

func terminal(c string) bool {
	switch c {
	case "success", "failure", "cancelled", "skipped", "timed_out", "neutral", "action_required", "startup_failure":
		return true
	}
	return false
}

func validateRun(r Run, until time.Time) string {
	if r.ID == 0 || r.WorkflowID == 0 || r.Path == "" || r.Attempt == 0 || r.HeadSHA == "" || r.CreatedAt.IsZero() || !r.CreatedAt.Before(until) || !r.JobsComplete || len(r.Jobs) == 0 {
		return "missing identity, incomplete jobs pagination, or run outside closed window"
	}
	names, ids := map[string]bool{}, map[uint64]bool{}
	for _, j := range r.Jobs {
		if j.ID == 0 || j.Name == "" || names[j.Name] || ids[j.ID] || j.Attempt != r.Attempt || !terminal(j.Conclusion) {
			return "missing/ambiguous job identity, attempt, or terminal conclusion"
		}
		names[j.Name], ids[j.ID] = true, true
	}
	return ""
}

func timing(r Run, j Job, p *push, until time.Time) (JobTiming, error) {
	t := JobTiming{RunID: r.ID, Attempt: r.Attempt, Job: j.Name, Steps: []Classification{}, CreatedAt: j.CreatedAt, StartedAt: j.StartedAt, CompletedAt: j.CompletedAt, Conclusion: j.Conclusion}
	if j.CreatedAt.IsZero() || j.CreatedAt.Before(r.CreatedAt) || j.StartedAt.Before(j.CreatedAt) || j.CompletedAt.Before(j.StartedAt) || j.CompletedAt.IsZero() || !j.CompletedAt.Before(until) || len(j.Labels) == 0 || len(j.Steps) == 0 || j.Conclusion == "skipped" || j.Conclusion == "cancelled" {
		return t, fmt.Errorf("missing, unfinished, skipped, or inconsistent job timing")
	}
	t.WaitSeconds = j.StartedAt.Sub(j.CreatedAt).Seconds()
	if p != nil && r.Attempt == 1 && !p.at.IsZero() && !j.CreatedAt.Before(p.at) {
		d := j.CreatedAt.Sub(p.at).Seconds()
		t.DispatchSeconds = &d
	}
	boundary, previous, number := j.CompletedAt, j.StartedAt, 0
	running := false
	for _, s := range j.Steps {
		if s.Name == "" || s.Number <= number || !terminal(s.Conclusion) {
			return t, fmt.Errorf("missing or ambiguous step identity/conclusion")
		}
		number = s.Number
		class := classify(s.Name)
		phase := "setup"
		seconds := 0.0
		if s.Conclusion != "skipped" {
			if s.StartedAt.IsZero() || s.StartedAt.Before(previous) || s.CompletedAt.Before(s.StartedAt) || s.CompletedAt.After(j.CompletedAt) {
				return t, fmt.Errorf("missing or inconsistent step timestamps")
			}
			seconds = s.CompletedAt.Sub(s.StartedAt).Seconds()
			previous = s.CompletedAt
			if !running && class != "setup" && class != "checkout" {
				boundary = s.StartedAt
				running = true
			}
		}
		if running {
			phase = "run"
		}
		t.Steps = append(t.Steps, Classification{Name: s.Name, Class: class, Phase: phase, Seconds: seconds})
	}
	t.SetupSeconds = boundary.Sub(j.StartedAt).Seconds()
	t.RunSeconds = j.CompletedAt.Sub(boundary).Seconds()
	return t, nil
}
