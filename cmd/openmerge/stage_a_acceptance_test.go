package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/serve"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"github.com/jackc/pgx/v5"
)

// The fake's immutable object map stands in for the production local Git cache.
// These reporting reads never resolve a mutable ref or call a remote forge.
type acceptanceGit struct{ *fake.Forge }

// A synthetic CI producer succeeds on each newly built queue ref.
func (g acceptanceGit) BuildTestRef(ctx context.Context, r forge.RepoRef, req forge.BuildRequest, opts forge.PushOptions) (string, error) {
	sha, err := g.Forge.BuildTestRef(ctx, r, req, opts)
	if err == nil {
		g.SetCheckRuns(r, sha, []forge.CheckRun{{ID: 1, Name: "ci", State: forge.Success, HeadBranch: req.Ref}})
	}
	return sha, err
}

func (g acceptanceGit) LocalConfig(ctx context.Context, r forge.RepoRef, sha string) ([]byte, error) {
	return g.ReadFile(ctx, r, sha, ".openmerge.yml")
}
func (g acceptanceGit) LocalGoverningFiles(ctx context.Context, r forge.RepoRef, sha string) (forge.GoverningFiles, error) {
	return g.GoverningFiles(ctx, r, forge.GoverningFilesRequest{CommitSHA: sha, AllAttributes: true})
}

func acceptanceDB(t *testing.T) *store.Store {
	t.Helper()
	raw := os.Getenv("OPENMERGE_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set OPENMERGE_TEST_DATABASE_URL to run fake-forge acceptance")
	}
	require := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	conn, err := pgx.Connect(t.Context(), raw)
	require(err)
	schema := testSchemaName(t, "acceptance_12b_")
	_, err = conn.Exec(t.Context(), "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize())
	require(err)
	t.Cleanup(func() {
		_, err := conn.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		require(err)
		require(conn.Close(context.Background()))
	})
	u, err := url.Parse(raw)
	require(err)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := store.Open(t.Context(), u.String(), store.WithTestLockScope(schema))
	require(err)
	t.Cleanup(db.Close)
	return db
}

func TestAcceptanceDBParallelSynctest(t *testing.T) {
	if os.Getenv("OPENMERGE_TEST_DATABASE_URL") == "" {
		t.Skip("set OPENMERGE_TEST_DATABASE_URL to run fake-forge acceptance")
	}
	if _, err := net.LookupHost("localhost"); err != nil {
		t.Fatal(err)
	}
	// Channels outside the bubbles keep both schemas alive without advancing
	// either virtual clock. Start both bubbles regardless of -parallel or -run.
	created := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	var bubbles sync.WaitGroup
	for i := range 2 {
		bubbles.Go(func() {
			synctest.Test(t, func(t *testing.T) {
				defer func() {
					close(created[i])
					<-created[1-i]
				}()
				if got := time.Now(); !got.Equal(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)) {
					t.Fatalf("fixture must start at virtual t=0, got %s", got)
				}
				acceptanceDB(t)
			})
		})
	}
	bubbles.Wait()
}

func TestStageAAcceptanceCLI(t *testing.T) {
	t.Setenv("OPENMERGE_API_TOKEN", "acceptance-read-token")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	depths := []int{1}
	if !testing.Short() {
		depths = []int{1, 4}
	}
	for _, depth := range depths {
		for _, tc := range []struct{ name, policy, path string }{
			{"low-risk-none", "{default: {human: true, count: 1}, rules: [{when: {labels: [docs]}, require: none}]}", "docs/readme.md"},
			{"exact-head-human", "{default: {human: true, count: 1}}", "src/main.go"},
			{"receipt-intent", "{default: {human: true, count: 1, receipt: true}}", "src/main.go"},
			{"owned-non-policy", "\n      default: {human: true, count: 1}\n      rules:\n      - when: {paths: ['**']}\n        require: {human: true, count: 1}\n      - when: {touches: ['**']}\n        require: {code_owners: true}", "owned/service.go"},
			{"uncovered-non-policy", "\n      default: {human: true, count: 1}\n      rules:\n      - when: {paths: ['**']}\n        require: {human: true, count: 1}\n      - when: {touches: ['**']}\n        require: {code_owners: true}", "uncovered/service.go"},
		} {
			t.Run(fmt.Sprintf("depth-%d/%s", depth, tc.name), func(t *testing.T) {
				must := func(err error) {
					t.Helper()
					if err != nil {
						t.Fatal(err)
					}
				}
				f := fake.New()
				r := forge.RepoRef{Path: "owner/acceptance"}
				f.Init(r, "main", "openmerge/fence/main")
				source := fmt.Sprintf("version: 1\nclassification: {docs: ['docs/**']}\nqueues:\n  - name: opted\n    base: main\n    speculation: {min_depth: %d, max_depth: %d}\n    merge_when: [check-success = ci]\n    approvals: %s\n  - name: legacy\n    base: release\n    merge_when: [check-success = ci]\n", depth, depth, tc.policy)
				cfg, _, err := config.Load([]byte(source))
				must(err)
				selected, diags := cfg.ResolveBranch("release", "main")
				if len(diags) > 0 {
					t.Fatal(diags)
				}
				tip, err := f.PushBase(r, "main", "sandbox policy", map[string]string{".openmerge.yml": source, ".github/CODEOWNERS": "/.openmerge.yml @owner\n/.github/CODEOWNERS @owner\n/owned/ @owner\n"})
				must(err)
				must(f.PushRef(r, "release", tip))
				f.Init(r, "release", "openmerge/fence/release")
				agent := forge.UserIdentity{ID: 12, Login: "contributor", Kind: forge.BotAccount}
				reviewer := forge.UserIdentity{ID: 21, Login: "reviewer", Kind: forge.UserAccount}
				owner := forge.UserIdentity{ID: 31, Login: "owner", Kind: forge.UserAccount}
				for _, user := range []forge.UserIdentity{agent, reviewer, owner} {
					f.SetIdentity(user)
					f.SetWriteGrant(r, user.ID, "write")
					f.SetCanWrite(r, user.Login, true)
				}
				pr, err := f.OpenPRWithFiles(r, 1, tip, true, map[string]string{tc.path: "change by agent"})
				must(err)
				must(f.SetPRMetadata(r, 1, "main", false, agent.Login))
				_, err = f.OpenPR(r, 2, tip, true)
				must(err)
				must(f.SetPRMetadata(r, 2, "release", false, agent.Login))
				native := forge.NativeReviews{Reviews: []forge.NativeReview{{ID: 200, CommitID: tip, Author: reviewer, State: forge.ReviewApproved, Submitted: forge.NativeTime{Time: time.Now().UTC(), Source: forge.ReviewSubmissionTime}}}}
				comment := func(user forge.UserIdentity, body string) {
					native.Comments = append(native.Comments, forge.NativeComment{ID: int64(100 + len(native.Comments)), Kind: forge.IssueComment, Author: user, Body: body, Created: forge.NativeTime{Time: time.Now().UTC(), Source: forge.CommentCreationTime, Precision: time.Second}})
					f.SetNativeReviews(r, 1, native)
				}
				comment(agent, "@openmerge queue")
				f.AddComment(r, 2, agent.Login, "@openmerge queue")
				keys := []store.QueueKey{{Installation: "7", Repository: r.Path, BaseBranch: "main"}, {Installation: "7", Repository: r.Path, BaseBranch: "release"}}
				db := acceptanceDB(t)
				targets := []serve.Target{}
				for _, key := range keys {
					targets = append(targets, serve.Target{Key: key, Driver: f, Git: acceptanceGit{f}, BotLogin: fake.Identity})
				}
				s, err := serve.New(t.Context(), db, targets, serve.Options{PollInterval: 10 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
				must(err)
				api := httptest.NewServer(status.Handler(s, keys, "acceptance-read-token", nil))
				defer api.Close()
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan error, 1)
				go func() { done <- s.Run(ctx) }()
				stopped := false
				stop := func() {
					if !stopped {
						cancel()
						must(<-done)
						stopped = true
					}
				}
				defer stop()
				id, err := keys[0].ChangeID(1)
				must(err)
				// All journey observations go through the same read-only CLI users invoke.
				read := func(command string, target any) bool {
					var out, stderr bytes.Buffer
					code := runChange([]string{command, string(id), "--repo", r.Path, "--api", api.URL, "--json"}, &out, &stderr)
					if code != 0 {
						return false
					} // startup/worker-busy responses are retried below
					// Omitted JSON fields must not retain values from an earlier poll.
					reflect.ValueOf(target).Elem().SetZero()
					must(json.Unmarshal(out.Bytes(), target))
					return true
				}
				wait := func(label string, predicate func() bool) {
					t.Helper()
					ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
					defer cancel()
					tick := time.NewTicker(20 * time.Millisecond)
					defer tick.Stop()
					for {
						if predicate() {
							return
						}
						select {
						case <-tick.C:
						case <-ctx.Done():
							var show status.ChangeResponse
							_ = read("show", &show)
							t.Fatalf("timed out waiting for %s: entry=%+v digest=%s receipts=%+v obligations=%+v", label, show.PR, show.Brief.Digest, show.Brief.Receipts, show.Brief.Obligations)
						}
					}
				}
				var show status.ChangeResponse
				wait("agent revision", func() bool {
					return read("show", &show) && show.PR != nil && show.PR.Change != nil && show.PR.Change.Accountability.Author != nil
				})
				if show.PR.Change.Accountability.Author.Kind != "agent" {
					t.Fatalf("author not agent: %+v", show.PR.Change.Accountability)
				}
				var view brief.View
				postedDigest := ""
				// Approval cases remain blocked until this test supplies the missing
				// evidence. The none case can already be settled on its first read.
				if tc.name != "low-risk-none" {
					wait("recorded brief", func() bool { return read("brief", &view) && len(view.Digest) == 64 })
				}
				if strings.Contains(tc.name, "non-policy") {
					nonPolicy := false
					for _, label := range view.Classification.Labels {
						if label.Name == "policy" && label.Truth == "false" {
							nonPolicy = true
						}
					}
					if !nonPolicy {
						t.Fatal("fixture must be classified as a non-policy file")
					}
				}
				if tc.name != "low-risk-none" {
					wait("approval wait", func() bool { return read("show", &show) && len(show.PR.Change.WaitingReasons) > 0 })
					if show.PR.Landing != nil {
						t.Fatal("landed without required approval")
					}
					// The initial wrong-head native review was present before admission;
					// the observed approval wait proves it could not satisfy this head.
					native.Reviews[0].CommitID = pr.HeadSHA
					native.Reviews[0].ID = 201
					native.Reviews[0].Submitted.Time = time.Now().UTC().Add(time.Second)
					f.SetNativeReviews(r, 1, native)
					must(s.Notify(t.Context(), "7", r.Path, "review-201"))
					if tc.name == "receipt-intent" {
						wait("native receipt wait", func() bool {
							if !read("brief", &view) {
								return false
							}
							for _, stance := range view.Stances {
								if stance.SourceID == "201" && len(view.Digest) == 64 {
									return true
								}
							}
							return false
						})
						if !read("show", &show) || show.PR.Landing != nil {
							t.Fatal("native review bypassed receipt")
						}
						digest := view.Digest
						postedDigest = digest
						comment(reviewer, "@openmerge approve "+digest)
						must(s.Notify(t.Context(), "7", r.Path, "receipt-101"))
					}
					if tc.name == "owned-non-policy" {
						wait("owner still required", func() bool {
							if !read("brief", &view) {
								return false
							}
							countReviewRecorded, ownerPending := false, false
							for _, obligation := range view.Obligations {
								if obligation.Selector == "code_owners" && !obligation.Met {
									ownerPending = true
								}
							}
							for _, stance := range view.Stances {
								if stance.SourceID == "201" && stance.Head == pr.HeadSHA {
									countReviewRecorded = true
								}
							}
							return countReviewRecorded && ownerPending
						})
						if !read("show", &show) || show.PR.Landing != nil {
							t.Fatal("non-owner approval bypassed ownership")
						}
						native.Reviews = append(native.Reviews, forge.NativeReview{ID: 202, CommitID: pr.HeadSHA, Author: owner, State: forge.ReviewApproved, Submitted: forge.NativeTime{Time: time.Now().UTC().Add(2 * time.Second), Source: forge.ReviewSubmissionTime}})
						f.SetNativeReviews(r, 1, native)
						must(s.Notify(t.Context(), "7", r.Path, "owner-202"))
					}
				}
				wait("confirmed landing", func() bool {
					return read("show", &show) && show.PR.Landing != nil && show.PR.Landing.Outcome == "landed"
				})
				wait("legacy sibling landing", func() bool {
					sha, e := f.ReadRef(t.Context(), r, "release")
					return e == nil && f.ContainsPR(r, sha, 2)
				})
				// Quiesce workers before historical reads: deliberately observe every
				// successful approval only after settlement, without policy-lock contention.
				stop()
				if !read("brief", &view) {
					t.Fatal("settled brief unavailable")
				}
				if len(view.Digest) != 64 {
					t.Fatalf("settled brief lost its reconstructible digest: %+v", view)
				}
				var decisions status.DecisionsResponse
				if !read("decisions", &decisions) || len(decisions.Decisions) != 1 || decisions.Decisions[0].Outcome != "landed" {
					t.Fatalf("settled decision unavailable: %+v", decisions)
				}
				d := decisions.Decisions[0]
				retained, err := json.Marshal(d)
				must(err)
				var again status.DecisionsResponse
				if !read("decisions", &again) || len(again.Decisions) != 1 {
					t.Fatalf("repeated settled decision unavailable: %+v", again)
				}
				repeated, err := json.Marshal(again.Decisions[0])
				must(err)
				if !bytes.Equal(retained, repeated) {
					t.Fatal("settled decision changed between CLI reads")
				}
				if tc.name == "low-risk-none" && len(d.Sources) != 0 {
					t.Fatal("none relied on approval evidence", d)
				}
				if d.EventID <= 0 || d.EligibilityEventID <= 0 || d.SettlementEventID <= 0 || d.AttemptID == "" || d.Revision != show.PR.Change.Revision || d.LandedSHA != show.PR.Landing.SHA || d.TestedSHA == "" {
					t.Fatalf("incomplete decision: %+v", d)
				}
				if tc.name == "receipt-intent" {
					var receipt *brief.ReceiptView
					for i := range view.Receipts {
						if view.Receipts[i].SourceID == "101" {
							receipt = &view.Receipts[i]
						}
					}
					if receipt == nil || !receipt.Accepted || receipt.BriefDigest != postedDigest || receipt.Revision != d.Revision || receipt.UserID != "github.com:user:21" || receipt.Binding != "current" || view.Digest != postedDigest {
						t.Fatalf("accepted receipt not retained: %+v", receipt)
					}
					if d.BriefDigest != postedDigest {
						t.Fatal("decision lost posted digest", d)
					}
					found := false
					for _, source := range d.Sources {
						if source.SourceID == "101" && source.BriefDigest == postedDigest && source.ReceiptID == receipt.SourceID && source.UserID == receipt.UserID {
							found = true
						}
					}
					if !found {
						t.Fatal("receipt absent from immutable decision", d)
					}
				}
				for _, source := range d.Sources {
					if source.SourceID == "200" {
						t.Fatal("wrong-head review entered landing decision")
					}
				}
				if tc.name == "owned-non-policy" {
					countSource, ownerSource := false, false
					for _, requirement := range d.Requirements {
						for _, source := range requirement.ReliedOn {
							if requirement.Selector == "human" && source.SourceID == "201" {
								countSource = true
							}
							if requirement.Selector == "code_owners" && source.SourceID == "202" {
								ownerSource = true
							}
						}
					}
					if !countSource || !ownerSource {
						t.Fatal("owned landing did not retain independent count and owner evidence", d)
					}
				}
				if tc.name == "uncovered-non-policy" {
					count := false
					for _, requirement := range d.Requirements {
						if requirement.Selector == "code_owners" {
							t.Fatal("uncovered file acquired owner requirement")
						}
						if (requirement.Human || requirement.Selector == "human") && requirement.Count == 1 && len(requirement.ReliedOn) > 0 {
							count = true
						}
					}
					if !count {
						t.Fatal("uncovered file lost count rule", d)
					}
				}
				// Fixture audit only: the agent journey above never reads private queue state.
				journals, err := s.Journals(t.Context(), keys)
				must(err)
				for _, j := range journals {
					if j.Key.BaseBranch != "release" {
						continue
					}
					state := j.Seed
					for _, row := range j.Events {
						state = queue.Apply(state, row.Event)
						switch e := row.Event.(type) {
						case queue.ConfigApplied:
							if e.Policy != nil {
								t.Fatal("legacy acquired policy pin")
							}
						case queue.ChangeRevisionObserved, queue.ChangeClassified, queue.ChangeAccountabilityObserved, queue.ChangeIntentObserved, queue.ApprovalEligibilityObserved, queue.ReviewStanceObserved, queue.ApprovalReceiptRecorded, queue.LandingDecisionRecorded, queue.ApprovalGateRejected, queue.ApprovalInputsChanged:
							t.Fatalf("legacy acquired Stage A evidence: %T", row.Event)
						}
					}
					if state.ConfigDigest != selected.Digest(true) {
						t.Fatal("legacy digest changed")
					}
				}
				sha, err := f.ReadRef(t.Context(), r, "release")
				must(err)
				commit, err := f.Commit(r, sha)
				must(err)
				if strings.Contains(commit.Message, "OpenMerge-Change:") || strings.Contains(commit.Message, "OpenMerge-Decision:") {
					t.Fatal("legacy acquired trailers", commit.Message)
				}
			})
		}
	}
}

var _ forge.Git = (*acceptanceGit)(nil)
