package sim

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// committedLabSummary is the reviewed summary of the full suite.
const committedLabSummary = "../../docs/research/ci-efficiency-lab.json"

// TestEfficiencyLab runs the CI efficiency benchmark. -short runs a tiny smoke
// matrix twice to check determinism. Without -short it runs LabSuite over ten
// seeds and checks that the summary matches the committed
// docs/research/ci-efficiency-lab.json. Set OPENMERGE_LAB_OUT to a directory
// to keep efficiency-lab.json (the summary), efficiency-lab-runs.json (every
// per-seed run) and efficiency-lab.md; otherwise they go to a temporary
// directory. Set OPENMERGE_LAB_UPDATE=1 to rewrite the committed summary.
func TestEfficiencyLab(t *testing.T) {
	t.Parallel()
	matrices, seeds := LabSuite(), []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if testing.Short() {
		tiny := LabWorkloads()[1]
		tiny.Name = "tiny-mixture"
		config := tiny.Config
		tiny.Config = func() Config {
			cfg := config()
			cfg.PRs, cfg.ArrivalWindow = 60, 90*time.Minute
			return cfg
		}
		strategies := LabStrategies()
		matrices = []LabMatrix{{Name: "smoke", Workloads: []LabWorkload{tiny}, Strategies: []LabStrategyCase{strategies[0], strategies[2], strategies[4]}, Detail: true}}
		seeds = []uint64{1, 2}
	}
	report, err := RunLabSuite(matrices, seeds)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := report.JSON()
	if err != nil {
		t.Fatal(err)
	}
	summary, err := report.SummaryJSON()
	if err != nil {
		t.Fatal(err)
	}
	summary = append(summary, '\n')
	if testing.Short() {
		again, err := RunLabSuite(matrices, seeds)
		if err != nil {
			t.Fatal(err)
		}
		if againRaw, _ := again.JSON(); !bytes.Equal(againRaw, raw) {
			t.Fatal("efficiency lab is not deterministic")
		}
	}
	for _, r := range report.Reports {
		calibrated := map[string]bool{}
		for _, w := range r.Workloads {
			calibrated[w["name"].(string)], _ = w["calibrated"].(bool)
		}
		for _, c := range r.Calibration {
			if calibrated[c.Workload] && c.Model != c.Real {
				t.Errorf("%s/%s seed %d: isolation model predicts %d bisection runs, planner ran %d", r.Name, c.Workload, c.Seed, c.Model, c.Real)
			}
		}
	}
	dir := os.Getenv("OPENMERGE_LAB_OUT")
	if dir == "" {
		dir = t.TempDir()
	}
	markdown := report.Markdown()
	for name, data := range map[string][]byte{"efficiency-lab.json": summary, "efficiency-lab-runs.json": append(raw, '\n'), "efficiency-lab.md": []byte(markdown)} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("wrote %s\n%s", dir, markdown)
	if testing.Short() {
		return
	}
	if os.Getenv("OPENMERGE_LAB_UPDATE") != "" {
		if err := os.WriteFile(committedLabSummary, summary, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	committed, err := os.ReadFile(committedLabSummary)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(committed, summary) {
		return
	}
	// Tolerate last-digit differences from platform floating point; any
	// structural change or real numeric drift fails.
	var want, got any
	if err := json.Unmarshal(committed, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(summary, &got); err != nil {
		t.Fatal(err)
	}
	if path, ok := jsonClose(want, got, "$"); !ok {
		t.Fatalf("regenerated lab summary differs from %s at %s; rerun with OPENMERGE_LAB_UPDATE=1 and review the diff", committedLabSummary, path)
	}
	t.Log("lab summary matches the committed summary within rounding tolerance, not byte for byte")
}

func jsonClose(want, got any, path string) (string, bool) {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok || len(g) != len(w) {
			return path, false
		}
		for k, v := range w {
			if p, ok := jsonClose(v, g[k], path+"."+k); !ok {
				return p, false
			}
		}
		return "", true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return path, false
		}
		for i := range w {
			if p, ok := jsonClose(w[i], g[i], fmt.Sprintf("%s[%d]", path, i)); !ok {
				return p, false
			}
		}
		return "", true
	case float64:
		g, ok := got.(float64)
		return path, ok && math.Abs(g-w) <= 2e-3*math.Max(1, math.Abs(w))
	default:
		return path, want == got
	}
}
