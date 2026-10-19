package serve

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestStageADefiniteIdentityFailureBlocksAcrossBaseMove(t *testing.T) {
	t.Parallel()
	for _, moved := range []bool{false, true} {
		t.Run(map[bool]string{false: "unmoved_base", true: "moved_base"}[moved], func(t *testing.T) {
			t.Parallel()
			s, w, f := shadowFixture(t, 1, "main", "7", false)
			if _, err := f.PushBase(repo, "main", "governing user", map[string]string{"CODEOWNERS": "* @writer"}); err != nil {
				t.Fatal(err)
			}
			if err := s.configuration(t.Context(), w); err != nil {
				t.Fatal(err)
			}
			if moved {
				if _, err := f.PushBase(repo, "main", "base moves with failed identity", nil); err != nil {
					t.Fatal(err)
				}
			}
			f.FailNext("ResolveUser", forge.ErrIdentityUnavailable)
			if err := s.configuration(t.Context(), w); err != nil {
				t.Fatal(err)
			}
			state := shadowState(t, s, w)
			if !state.SourceBlocked(queue.ConfigSource) || w.configError == nil {
				t.Fatalf("definite governing identity did not durably block: state=%+v error=%v", state, w.configError)
			}

			if err := s.configuration(t.Context(), w); err != nil {
				t.Fatal(err)
			}
			if shadowState(t, s, w).SourceBlocked(queue.ConfigSource) || w.configError != nil {
				t.Fatal("recovered identity retained a config block")
			}
		})
	}
}

func TestStageATeamMembershipFailuresDifferentiateDurability(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		operation string
		failure   error
		durable   bool
	}{
		{name: "resolve-unavailable", operation: "ResolveTeam", failure: forge.ErrIdentityUnavailable, durable: true},
		{name: "resolve-transient", operation: "ResolveTeam", failure: errors.New("temporary team lookup")},
		{name: "members-unavailable", operation: "TeamMembers", failure: forge.ErrIdentityUnavailable, durable: true},
		{name: "members-incomplete", operation: "TeamMembers", failure: forge.ErrMembershipIncomplete},
		{name: "members-transient", operation: "TeamMembers", failure: errors.New("temporary team read")},
	} {
		for _, moved := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/unmoved_base", true: "/moved_base"}[moved], func(t *testing.T) {
				t.Parallel()
				s, w, f := shadowFixture(t, 1, "main", "7", false)
				team := forge.TeamIdentity{ID: 50, OrganizationID: 5, Organization: "org", Slug: "reviewers"}
				f.SetTeam(team, []forge.UserIdentity{{ID: 21, Login: "writer", Kind: forge.UserAccount}})
				if _, err := f.PushBase(repo, "main", "governing team", map[string]string{"CODEOWNERS": "* @org/reviewers"}); err != nil {
					t.Fatal(err)
				}
				if err := s.configuration(t.Context(), w); err != nil {
					t.Fatal(err)
				}
				before, err := s.store.Watermark(t.Context(), w.id)
				if err != nil {
					t.Fatal(err)
				}
				if moved {
					if _, err := f.PushBase(repo, "main", "membership failure", nil); err != nil {
						t.Fatal(err)
					}
				}
				f.FailNext(tc.operation, tc.failure)
				err = s.configuration(t.Context(), w)
				if tc.durable && err != nil || !tc.durable && err == nil {
					t.Fatalf("configuration error = %v, durable=%t", err, tc.durable)
				}
				state := shadowState(t, s, w)
				if state.SourceBlocked(queue.ConfigSource) != tc.durable {
					t.Fatalf("config block=%t durable=%t", state.SourceBlocked(queue.ConfigSource), tc.durable)
				}
				if !tc.durable {
					events, err := s.store.EventsAfter(t.Context(), w.id, before)
					if err != nil {
						t.Fatal(err)
					}
					if len(events) != 0 || w.configTip != "" || w.configError == nil {
						t.Fatalf("transient membership failure persisted fallback: events=%+v worker=%+v", events, w)
					}
				}
			})
		}
	}
}

func TestStageAGoverningCacheReusesPinnedPolicyContent(t *testing.T) {
	t.Parallel()
	s, w, f := shadowFixture(t, 1, "main", "7", false)
	before := f.GoverningFilesCalls()
	for range 4 {
		if err := s.configuration(t.Context(), w); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.GoverningFilesCalls(); got != before {
		t.Fatalf("unchanged immutable policy caused %d governing reads, want %d", got, before)
	}
}

func TestStageAForwardBaseThenUnchangedPollSkipsImmutableReads(t *testing.T) {
	t.Parallel()
	s, w, f := shadowFixture(t, 1, "main", "7", false)
	if _, err := f.PushBase(repo, "main", "forward base", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.configuration(t.Context(), w); err != nil {
		t.Fatal(err)
	}
	// The first observation refreshes the pinned governing provenance at the
	// same generation. Later polls are exact immutable cache hits.
	if _, err := s.observe(t.Context(), w, 1); err != nil {
		t.Fatal(err)
	}
	diffs, governing := f.RevisionDiffCalls(), f.GoverningFilesCalls()
	for range 4 {
		if _, err := s.observe(t.Context(), w, 1); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.RevisionDiffCalls(); got != diffs {
		t.Fatalf("unchanged forward-base polls made %d revision diffs, want %d", got, diffs)
	}
	if got := f.GoverningFilesCalls(); got != governing {
		t.Fatalf("unchanged forward-base polls made %d governing reads, want %d", got, governing)
	}
}

func TestStageANativeNoiseUsesOneStaticPoolRead(t *testing.T) {
	t.Parallel()
	s, w, f := shadowFixture(t, 1, "main", "7", false)
	w.stageA.repository.Groups = map[string][]string{"reviewers": {"writer"}}
	if err := s.configuration(t.Context(), w); err != nil {
		t.Fatal(err)
	}
	pr, err := f.PullRequest(t.Context(), repo, 1)
	if err != nil {
		t.Fatal(err)
	}
	native := forge.NativeReviews{}
	noise := int64(20)
	if testing.Short() {
		noise = 3
	}
	for id := int64(1); id <= noise; id++ {
		native.Reviews = append(native.Reviews, forge.NativeReview{ID: id, CommitID: pr.HeadSHA, Author: forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount}, State: forge.ReviewChangesRequested, Submitted: forge.NativeTime{Time: time.Now().UTC(), Source: forge.ReviewSubmissionTime}})
	}
	f.SetNativeReviews(repo, 1, native)
	before := f.IdentityCalls("ResolveUser")
	if _, err := s.observe(t.Context(), w, 1); err != nil {
		t.Fatal(err)
	}
	if got := f.IdentityCalls("ResolveUser") - before; got != 2 { // PR author + one static group member
		t.Fatalf("20 native stances caused %d ResolveUser calls, want 2", got)
	}
}

func TestStageANativeGrantCallsPerDistinctReviewer(t *testing.T) {
	t.Parallel()
	for _, reviewers := range []int{1, 3} {
		counts := []int{1, 30}
		if testing.Short() {
			counts = []int{1, 3}
		}
		for _, count := range counts {
			t.Run(fmt.Sprintf("reviewers=%d/approvals=%d", reviewers, count), func(t *testing.T) {
				t.Parallel()
				s, w, f := shadowFixture(t, 1, "main", "7", false)
				pr, err := f.PullRequest(t.Context(), repo, 1)
				must(t, err)
				native := forge.NativeReviews{}
				for actor := range reviewers {
					user := forge.UserIdentity{ID: int64(21 + actor), Login: fmt.Sprintf("writer%d", actor), Kind: forge.UserAccount}
					f.SetIdentity(user)
					f.SetWriteGrant(repo, user.ID, "write")
					for i := range count {
						native.Reviews = append(native.Reviews, forge.NativeReview{ID: int64(actor*100 + i + 1), Author: user, CommitID: pr.HeadSHA, State: forge.ReviewApproved, Submitted: forge.NativeTime{Time: time.Now().Add(time.Minute), Source: forge.ReviewSubmissionTime}})
					}
				}
				for i := range count {
					native.Reviews = append(native.Reviews, forge.NativeReview{ID: int64(1000 + i), Author: forge.UserIdentity{ID: 999, Login: "stale", Kind: forge.UserAccount}, CommitID: strings.Repeat("a", 40), State: forge.ReviewApproved})
				}
				f.SetNativeReviews(repo, 1, native)
				for pass := range 2 {
					before := f.IdentityCalls("WriteGrant")
					_, err := s.observe(t.Context(), w, 1)
					must(t, err)
					if got := f.IdentityCalls("WriteGrant") - before; got != reviewers {
						t.Fatalf("pass %d: %d grants for %d current reviewers (%d approvals each)", pass, got, reviewers, count)
					}
					for actor := range reviewers {
						f.SetWriteGrant(repo, int64(21+actor), "read")
					}
				}
			})
		}
	}
}
