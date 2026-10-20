package github

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
	gh "github.com/google/go-github/v93/github"
)

// Real local Git objects establish distinct PR heads. All GitHub transport is
// handled by the loopback fixture; no real GitHub call or credential is used.
func TestQueueInstructionsRealHeadsGitHubIdempotence(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.git")
	git := func(input string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test")
		cmd.Stdin = strings.NewReader(input)
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, raw)
		}
		return strings.TrimSpace(string(raw))
	}
	git("", "init", "--bare", "--template=", remote)
	tree := git("", "--git-dir="+remote, "mktree")
	old := git("old\n", "--git-dir="+remote, "commit-tree", tree)
	head := git("head\n", "--git-dir="+remote, "commit-tree", tree, "-p", old)
	git("", "--git-dir="+remote, "update-ref", "refs/pull/1/head", head)
	t.Logf("local bare remote refs/pull/1/head=%s; previous=%s", head, old)
	raw, err := os.ReadFile("../../queue/testdata/wire/v1/event_policy_authority_applied.json")
	must(t, err)
	event, err := queue.DecodeEvent(raw)
	must(t, err)
	pin := event.(queue.PolicyAuthorityApplied)
	seed := queue.Apply(queue.State{}, queue.ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest})
	seed = queue.Apply(seed, pin)
	q, _, _ := status.Project(store.Journal{Seed: seed}, 0, 0, nil)
	instruction := status.QueueInstructions(&q)
	if instruction == "" {
		t.Fatal("authority instruction missing")
	}
	f := newAPI(t)
	f.prs[1].Head.SHA = new(head)
	// A foreign App's check with our name/external ID must remain untouched.
	foreign := &gh.CheckRun{ID: new(int64(500)), Name: new("OpenMerge"), ExternalID: new("openmerge/pr/1"), App: &gh.App{ID: new(int64(999))}, Output: &gh.CheckRunOutput{Summary: new("foreign")}}
	f.runs[head] = []*gh.CheckRun{foreign}
	for _, state := range []forge.StatusState{forge.Queued, forge.Testing, forge.Blocked, forge.Merged} {
		summary := "Queue state: " + string(state) + instruction
		for range 2 {
			must(t, f.driver.ReportStatus(t.Context(), testRepo, 1, head, forge.Status{State: state, Summary: summary}))
		}
		if len(f.runs[head]) != 2 || len(f.runs[old]) != 0 {
			t.Fatal("wrong head or duplicate check")
		}
		own := f.runs[head][1]
		if own.GetHeadSHA() != head || own.GetOutput().GetSummary() != summary || foreign.GetOutput().GetSummary() != "foreign" {
			t.Fatalf("check identity/output: %+v", own)
		}
	}
	// Long hostile names are bounded by the existing driver, while the static
	// instruction on its own line stays intact and occurs once.
	summary := strings.Repeat("<hostile>`", 2000) + instruction
	must(t, f.driver.ReportStatus(t.Context(), testRepo, 1, head, forge.Status{State: forge.Blocked, Summary: summary}))
	if strings.Count(f.runs[head][1].GetOutput().GetSummary(), "`@openmerge queue`") != 1 || len(f.runs[head][1].GetOutput().GetSummary()) > 2000 {
		t.Fatal("instruction lost to body bounds")
	}
	pin.Authority = authority.GitHub
	pin.OpenMerge = nil
	seed = queue.Apply(seed, pin)
	q, _, _ = status.Project(store.Journal{Seed: seed}, 0, 0, nil)
	must(t, f.driver.ReportStatus(t.Context(), testRepo, 1, head, forge.Status{State: forge.Queued, Summary: "Waiting" + status.QueueInstructions(&q)}))
	if len(f.runs[head]) != 2 || f.runs[head][1].GetOutput().GetSummary() != "Waiting" || len(f.comments[1]) != 0 {
		t.Fatal("rollback or quietness failed")
	}
}
