package github

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	gh "github.com/google/go-github/v93/github"
)

// Status producers are CI provenance, not approval decision identities. Their
// historical normalization is preserved even when approvals are enabled.
func TestStatusUnicodeLegacyEquivalence(t *testing.T) {
	for _, pair := range [][2]string{{"Kam", "kam"}, {"Kam", "kam"}, {"Élodie", "élodie"}, {"ſam", "sam"}} {
		t.Run(pair[0], func(t *testing.T) {
			f := newAPI(t)
			f.statuses["tested"] = []*gh.RepoStatus{
				{ID: new(int64(1)), Context: new("ci"), State: new("success"), Creator: &gh.User{Login: new(pair[0])}},
				{ID: new(int64(2)), Context: new("ci"), State: new("success"), Creator: &gh.User{Login: new(pair[1])}},
			}
			runs, err := f.driver.CheckRuns(t.Context(), testRepo, "tested")
			must(t, err)
			for i, run := range runs {
				if run.Source != "status:"+strings.ToLower(pair[i]) {
					t.Fatalf("source=%q", run.Source)
				}
			}
			x := executor.Executor{Driver: struct{ forge.Driver }{f.driver}, Repo: testRepo, Base: "main", RequiredChecks: []string{"ci"}, Now: func() time.Time { return epoch }}
			var completed bool
			err = x.Execute(t.Context(), queue.RefreshChecks{BatchID: "b1", TestedSHA: "tested"}, func(e queue.Event) error {
				if c, ok := e.(queue.ChecksCompleted); ok {
					completed = c.Success
				}
				return nil
			})
			ambiguous := pair[0] == "ſam"
			var sourceError *executor.CheckSourceAmbiguityError
			if completed == ambiguous || errors.As(err, &sourceError) != ambiguous || !ambiguous && err != nil {
				t.Fatalf("success=%v error=%v", completed, err)
			}
			fmt.Printf("UNICODE_STATUS pair=%q sources=%q,%q success=%v error=%v\n", pair, runs[0].Source, runs[1].Source, completed, err)
		})
	}
}
