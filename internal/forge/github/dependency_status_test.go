package github

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest/statusfixture"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/status"
)

// Only the loopback API fixture handles check writes. This compares the actual
// GitHub title, summary and text, including the driver's final sanitization.
func TestDependencyMultiCheckOutputPrivacyGoldens(t *testing.T) {
	for _, depth := range []int{1, 4} {
		for _, cobatch := range []bool{false, true} {
			t.Run(fmt.Sprintf("depth-%d/cobatch-%t", depth, cobatch), func(t *testing.T) {
				f := newAPI(t)
				baseline := map[int][]byte{}
				for _, code := range []string{"missing", "dependency-unresolved", "dependency-waiting", "dependency-closed", "dependency-stale", "dependency-landed", "dependency-unavailable", "dependency-out-of-scope"} {
					for _, phase := range []string{"queued", "building", "testing", "invalidating", "discarded"} {
						j, b, _, _, prs := statusfixture.MultiJournal(t, code, phase, depth, cobatch)
						pj, err := status.ProjectDependencies(j, b, nil, statusfixture.StatusSources())
						must(t, err)
						for _, pr := range prs {
							q, e, _ := status.Project(pj, pr, 100, nil)
							check := status.DependencyCheckStatus(&q, e, forge.Status{State: forge.Testing, Reason: phase, Summary: code})
							check.Summary += "\n\nCurrent state: " + status.WaitingCheckText(&q, *e, nil) + status.LedgerSummary(e) + status.QueueInstructions(&q)
							must(t, f.driver.ReportStatus(t.Context(), testRepo, pr, e.HeadSHA, check))
							runs := f.runs[e.HeadSHA]
							if len(runs) == 0 {
								t.Fatal("no check emitted", pr)
							}
							out := runs[len(runs)-1].GetOutput()
							raw, err := json.Marshal(struct{ Title, Summary, Text string }{out.GetTitle(), out.GetSummary(), out.GetText()})
							must(t, err)
							if want, ok := baseline[pr]; ok {
								if !bytes.Equal(want, raw) {
									t.Fatalf("PR %d private %s/%s check output leaked:\n%s\n%s", pr, code, phase, want, raw)
								}
							} else {
								baseline[pr] = bytes.Clone(raw)
							}
						}
					}
				}
				digests := map[int]string{}
				for pr, raw := range baseline {
					digests[pr] = fmt.Sprintf("%x", sha256.Sum256(raw))
				}
				raw, err := json.Marshal(digests)
				must(t, err)
				path := filepath.Join("testdata", fmt.Sprintf("dependency-private-check-multi-depth-%d-cobatch-%t.json", depth, cobatch))
				if os.Getenv("OPENMERGE_UPDATE_REPORTING_GOLDENS") == "1" {
					must(t, os.WriteFile(path, raw, 0644))
				}
				want, err := os.ReadFile(path)
				must(t, err)
				if !bytes.Equal(want, raw) {
					t.Fatal("check output golden differs")
				}
			})
		}
	}
}
