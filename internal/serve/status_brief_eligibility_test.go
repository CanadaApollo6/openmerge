package serve

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/classification"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/gitops"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

type journalBriefSource struct {
	*Server
	journal store.Journal
}

func (s *journalBriefSource) Journals(context.Context, []store.QueueKey) ([]store.Journal, error) {
	return []store.Journal{s.journal}, nil
}

func appendBriefEvents(j *store.Journal, events ...queue.Event) {
	for _, e := range events {
		j.Events = append(j.Events, store.JournalEvent{ID: int64(len(j.Events) + 1), Event: e})
	}
}

// All policy, governing and diff objects are created directly in an isolated
// bare cache. Brief reads use Server.BriefPolicy with no Driver or transport.
func briefEligibilityFixture(t *testing.T, receipt bool) (*journalBriefSource, cp.Policy, queue.ApprovalEligibilityObserved, queue.ChangeClassified) {
	t.Helper()
	key := store.QueueKey{Installation: "7", Repository: "local/brief", BaseBranch: "main"}
	id, err := key.ChangeID(5)
	must(t, err)
	work := t.TempDir()
	cache := filepath.Join(work, fmt.Sprintf("%x.git", sha256.Sum256([]byte(key.Repository))))
	git := func(input string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test")
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("", "init", "--bare", "--template=", cache)
	object := func(input string, args ...string) string {
		return git(input, append([]string{"--git-dir=" + cache}, args...)...)
	}
	extra := "{code_owners: true}"
	if receipt {
		extra = "{any_writer: true, receipt: true}"
	}
	data := "version: 1\nqueues:\n- name: default\n  merge_when: [check-success = ci]\n  approvals:\n    default: any_writer\n    rules:\n    - when: {touches: [owned.txt]}\n      require: {human: true, count: 1}\n    - when: {touches: [owned.txt]}\n      require: " + extra + "\n"
	cfg, _, err := config.Load([]byte(data))
	must(t, err)
	configBlob := object(data, "hash-object", "-w", "--stdin")
	ownersBlob := object("owned.txt @owner\n", "hash-object", "-w", "--stdin")
	tree := func(content string) string {
		blob := object(content, "hash-object", "-w", "--stdin")
		return object(fmt.Sprintf("100644 blob %s\t.openmerge.yml\n100644 blob %s\tCODEOWNERS\n100644 blob %s\towned.txt\n", configBlob, ownersBlob, blob), "mktree")
	}
	base := object("base\n", "commit-tree", tree("before\n"))
	head := object("head\n", "commit-tree", tree("after\n"), "-p", base)
	object("", "update-ref", "refs/heads/main", base)
	object("", "update-ref", "refs/heads/fixture-head", head)
	reading := false
	g, err := gitops.New(t.Context(), gitops.Config{WorkDir: work, GCInterval: -1, GCThresholdBytes: -1, Remote: func(context.Context, forge.RepoRef) (string, gitops.Auth, error) {
		if reading {
			t.Fatal("brief read resolved remote or credentials")
		}
		return cache, gitops.Auth{}, nil
	}, HeadRef: func(int) string { return "refs/heads/fixture-head" }})
	must(t, err)
	repo := forge.RepoRef{Path: key.Repository}
	governing, err := g.LocalGoverningFiles(t.Context(), repo, base)
	must(t, err)
	owners := cp.GoverningContent{Path: "CODEOWNERS", Known: true, Present: true, Content: "owned.txt @owner\n"}
	attrs := []cp.GoverningContent{{Path: ".gitattributes", Known: true}}
	resolved := []cp.ResolvedIdentity{{Name: "owner", ID: "github.com:user:3", Kind: cp.HumanAccount}}
	component, err := cp.PolicyDigest(cp.DigestInputs{Repository: cfg.Policy, Queue: cfg.Policy.Queues[0], Resolved: resolved, Owners: owners, Attributes: attrs, ClassifierVersion: classification.Version})
	must(t, err)
	effective, err := cp.WithPolicyDigest(cfg.Queues[0].Digest(true), component)
	must(t, err)
	diff, err := g.RevisionDiff(t.Context(), repo, forge.RevisionDiffRequest{BaseSHA: base, HeadSHA: head})
	must(t, err)
	// Classification retains an owner snapshot; only gate-time observations
	// establish current reviewer eligibility.
	evidence, err := classification.Classify(cp.ClassificationInputs{ChangeID: string(id), Revision: 1, Generation: 1, Base: base, Head: head, MergeBase: base, GoverningCommit: base, PolicyDigest: component}, diff, governing, classification.Settings{}, classification.TreeDeriver{Resolve: func(classification.Owner) cp.IdentitySet {
		return cp.IdentitySet{IDs: []string{"github.com:user:3"}, Resolved: true, Fresh: true, Reason: "classification snapshot"}
	}})
	must(t, err)
	classified := queue.ChangeClassified{Evidence: evidence}
	must(t, classified.Validate())
	b := queue.ApprovalBasis{ChangeID: id, Revision: 1, Head: head, ReadID: "gate-read", At: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	author := cp.Identity{ID: "github.com:user:1", Login: "author", Kind: cp.HumanAccount}
	reviewer := cp.Identity{ID: "github.com:user:2", Login: "non-owner", Kind: cp.HumanAccount}
	owner := cp.Identity{ID: "github.com:user:3", Login: "owner", Kind: cp.HumanAccount}
	fresh := queue.ApprovalEligibilityObserved{Basis: b, Actors: map[string]cp.Actor{}, Groups: map[string]cp.IdentitySet{}, OwnerPools: map[string]cp.IdentitySet{"owned:user:owner": {IDs: []string{owner.ID}, Resolved: true, Fresh: true}}, Known: true, Reasons: []string{}}
	for _, who := range []cp.Identity{reviewer, owner} {
		fresh.Actors[who.ID] = cp.Actor{Identity: who, Grant: cp.WriteGrant{SubjectID: who.ID, ReturnedID: who.ID, Fresh: true, Write: cp.True}}
	}
	must(t, fresh.Validate())
	review, ok := queue.NativeStance(b, forge.NativeReview{ID: 5577526295, CommitID: head, Author: forge.UserIdentity{ID: 2, Login: reviewer.Login, Kind: forge.UserAccount}, State: forge.ReviewApproved, Submitted: forge.NativeTime{Time: b.At.Add(-time.Minute), Source: forge.ReviewSubmissionTime, Precision: time.Second}})
	if !ok {
		t.Fatal("invalid review fixture")
	}
	src := &journalBriefSource{Server: &Server{queues: []*worker{{Target: Target{Key: key, Git: g}, configBranch: "main"}}}, journal: store.Journal{Key: key}}
	appendBriefEvents(&src.journal, queue.ConfigApplied{Commit: base, Digest: effective, Policy: &cp.PolicyPin{DefaultBranch: "main", Resolved: resolved, ClassifierVersion: classification.Version}}, queue.BaseMoved{SHA: base}, queue.ChangeRevisionObserved{PR: 5, ChangeID: id, Revision: 1, HeadSHA: head, Basis: queue.RevisionObservationBasis{BaseSHA: base, At: b.At.Add(-time.Hour)}}, queue.PRObserved{PR: 5, HeadSHA: head, Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 5, At: b.At}, classified, queue.Accountability(b, author, author, nil, component), queue.Intent(b, "claimed intent", "body"), review)
	reading = true
	return src, cp.Policy{Digest: component, Approvals: cfg.Queues[0].Approvals}, fresh, classified
}

func readEligibilityBrief(t *testing.T, src *journalBriefSource, audience string) brief.View {
	t.Helper()
	id, err := src.journal.Key.ChangeID(5)
	must(t, err)
	path := "/v1/queues/local/brief/main/changes/" + string(id) + "/brief?audience=" + audience
	w := httptest.NewRecorder()
	status.Handler(src, []store.QueueKey{src.journal.Key}, "", nil).ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	var view brief.View
	must(t, json.Unmarshal(w.Body.Bytes(), &view))
	if w.Code != 200 || view.Digest == brief.NotRecorded || len(view.Obligations) != 2 {
		t.Fatalf("brief: %d %s", w.Code, w.Body)
	}
	return view
}

func TestBriefEligibilitySandboxCasesRealGit(t *testing.T) {
	for _, receipt := range []bool{true, false} {
		t.Run(fmt.Sprint("receipt=", receipt), func(t *testing.T) {
			src, policy, fresh, _ := briefEligibilityFixture(t, receipt)
			before := readEligibilityBrief(t, src, "reviewer")
			if before.Obligations[0].Code != "eligibility_pending" {
				t.Fatalf("missing gate read: %+v", before.Obligations)
			}
			appendBriefEvents(&src.journal, fresh)
			// Simulate the approval-wait journal state after a tested batch is rejected.
			b := fresh.Basis
			prs := []queue.BatchPR{{PR: 5, HeadSHA: b.Head}}
			appendBriefEvents(&src.journal, queue.BatchStarted{BatchID: "b1", BaseSHA: src.journal.Events[1].Event.(queue.BaseMoved).SHA, PRs: prs, At: b.At}, queue.TestRefBuilt{BatchID: "b1", TestedSHA: strings.Repeat("a", 40), Method: queue.Merge, At: b.At})
			state := src.journal.Seed
			for _, row := range src.journal.Events {
				state = queue.Apply(state, row.Event)
			}
			appendBriefEvents(&src.journal, queue.ApprovalGateRejected{BatchID: "b1", TestedSHA: strings.Repeat("a", 40), PRs: prs, Waiting: []queue.ApprovalWait{{Basis: b, Fingerprint: state.ApprovalFingerprint(b.ChangeID), Reasons: []string{"approval-required"}}}})
			for _, audience := range []string{"reviewer", "agent", "operator"} {
				view := readEligibilityBrief(t, src, audience)
				count := view.Obligations[0]
				if !count.Met || count.Code != "met" || !reflect.DeepEqual(count.ApprovedIDs, []string{"github.com:user:2"}) {
					t.Fatalf("count approval missing: %+v", count)
				}
				second := view.Obligations[1]
				if receipt {
					if second.Code != "receipt_required" || second.NextStep.Action != "post_receipt" || second.NextStep.Command != "@openmerge approve "+view.Digest {
						t.Fatalf("receipt next step: %+v", second)
					}
				} else if second.Met || second.Code != "approval_required" || len(second.ApprovedIDs) != 0 || !reflect.DeepEqual(second.EligibleIDs, []string{"github.com:user:3"}) {
					t.Fatalf("owner requirement: %+v", second)
				}
				waiting := false
				for _, warning := range view.Warnings {
					waiting = waiting || warning.Code == "approval_wait"
				}
				if !waiting {
					t.Fatalf("not an approval wait: %+v", view.Warnings)
				}
				if view.Digest != before.Digest {
					t.Fatal("approval progress changed receipt digest")
				}
			}
			summary, err := status.ReviewerCheckSummary(src.journal, 5, policy)
			must(t, err)
			if !strings.Contains(summary, "human count=1 receipt=false met=true") || receipt && !strings.Contains(summary, "@openmerge approve "+before.Digest) {
				t.Fatalf("check progress: %s", summary)
			}
			// The digest issued before the eligibility read still accepts a receipt.
			for _, row := range src.journal.Events {
				state = queue.Apply(state, row.Event)
			}
			recorded, err := state.PrepareBareReceipt(policy, b, fresh.Actors["github.com:user:2"].Identity, "88", b.At, before.Digest)
			must(t, err)
			if !recorded.Accepted {
				t.Fatalf("receipt invalidated: %+v", recorded)
			}
		})
	}
}
