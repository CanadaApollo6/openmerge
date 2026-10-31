package sim

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestEfficiencyLab runs the CI efficiency benchmark. -short runs a tiny smoke
// matrix; the full matrix (6 workloads × 6 strategies × 10 seeds) runs without
// -short. Set OPENMERGE_LAB_OUT to a directory to keep efficiency-lab.json and
// efficiency-lab.md; otherwise they go to a temporary directory.
func TestEfficiencyLab(t *testing.T) {
	t.Parallel()
	workloads, strategies := LabWorkloads(), LabStrategies()
	seeds := []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if testing.Short() {
		tiny := workloads[1]
		tiny.Name = "tiny-mixture"
		config := tiny.Config
		tiny.Config = func() Config {
			cfg := config()
			cfg.PRs, cfg.ArrivalWindow = 60, 90*time.Minute
			return cfg
		}
		workloads, strategies, seeds = []LabWorkload{tiny}, []LabStrategyCase{strategies[0], strategies[1], strategies[3]}, []uint64{1, 2}
	}
	report, err := RunEfficiencyLab(workloads, strategies, seeds)
	if err != nil {
		t.Fatal(err)
	}
	data, err := report.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if testing.Short() {
		again, err := RunEfficiencyLab(workloads, strategies, seeds)
		if err != nil {
			t.Fatal(err)
		}
		if againData, _ := again.JSON(); string(againData) != string(data) {
			t.Fatal("efficiency lab is not deterministic")
		}
	}
	calibrated := map[string]bool{}
	for _, w := range workloads {
		calibrated[w.Name] = w.Calibrate
	}
	for _, c := range report.Calibration {
		if calibrated[c.Workload] && c.Model != c.Real {
			t.Errorf("%s seed %d: isolation model predicts %d bisection runs, planner ran %d", c.Workload, c.Seed, c.Model, c.Real)
		}
	}
	dir := os.Getenv("OPENMERGE_LAB_OUT")
	if dir == "" {
		dir = t.TempDir()
	}
	markdown := report.Markdown()
	if err := os.WriteFile(filepath.Join(dir, "efficiency-lab.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "efficiency-lab.md"), []byte(markdown), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s\n%s", dir, markdown)
}
