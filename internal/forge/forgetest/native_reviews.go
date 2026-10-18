package forgetest

import (
	"errors"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// NativeReviewEnv supplies independent native evidence and subsequent permission
// responses. HTTP implementations must exercise real paginated endpoints.
type NativeReviewEnv struct {
	Reader     forge.NativeReviewReader
	Identities forge.IdentityReader
	Repo       forge.RepoRef
	Set        func(forge.NativeReviews)
	SetGrant   func(forge.UserIdentity)
	Fail       func(error)
}

// RunNativeReviewReader pins the source DTO contract for fake and HTTP drivers.
// Acquisition-specific timeline and pagination failures are tested by the driver.
func RunNativeReviewReader(t *testing.T, newEnv func(*testing.T) NativeReviewEnv) {
	at := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	author := forge.UserIdentity{ID: 11, Login: "alice", Kind: forge.UserAccount}
	stamp := forge.NativeTime{Time: at, Source: forge.ReviewSubmissionTime, Precision: time.Second, UpperBound: at.Add(time.Second)}
	obs := forge.NativeObservation{ObservedAt: at.Add(time.Minute), ResponseDate: at.Add(time.Minute)}
	sample := func() forge.NativeReviews {
		reviews := []forge.NativeReview{}
		for i, state := range []forge.NativeReviewState{forge.ReviewChangesRequested, forge.ReviewApproved, forge.ReviewCommented, forge.ReviewDismissed, forge.ReviewPending} {
			review := forge.NativeReview{ID: int64(10 - i), CommitID: "old-head", Author: author, State: state, Submitted: stamp, Observation: obs}
			if state == forge.ReviewDismissed {
				review.Dismissal = &forge.NativeDismissal{ReviewID: review.ID, EventID: 91, Actor: forge.UserIdentity{ID: 22, Login: "maintainer", Kind: forge.BotAccount}, Time: forge.NativeTime{Time: at.Add(time.Minute), Source: forge.TimelineDismissalTime, Precision: time.Second, UpperBound: at.Add(time.Minute + time.Second)}, Observation: obs}
			}
			reviews = append(reviews, review)
		}
		reviews = append(reviews, reviews[1])
		return forge.NativeReviews{Reviews: reviews, Comments: []forge.NativeComment{
			{ID: 31, Author: author, Body: "@openmerge approve digest", Kind: forge.IssueComment, Created: forge.NativeTime{Time: at, Source: forge.CommentCreationTime, Precision: time.Second, UpperBound: at.Add(time.Second)}, Observation: obs},
			{ID: 32, ReviewID: 9, CommitID: "old-head", Author: author, Body: "inline text", Kind: forge.ReviewComment, Created: forge.NativeTime{Time: at, Source: forge.CommentCreationTime, Precision: time.Second, UpperBound: at.Add(time.Second)}, Observation: obs},
		}}
	}
	t.Run("complete-source-evidence-duplicates-out-of-order-and-equal-times", func(t *testing.T) {
		e := newEnv(t)
		want := sample()
		e.Set(want)
		got, err := e.Reader.NativeReviews(t.Context(), e.Repo, 1)
		if err != nil || len(got.Reviews) != len(want.Reviews) || len(got.Comments) != 2 {
			t.Fatalf("incomplete observation: %+v, %v", got, err)
		}
		for i, r := range got.Reviews {
			w := want.Reviews[i]
			if r.ID != w.ID || r.CommitID != w.CommitID || r.Author != w.Author || r.State != w.State || r.Submitted != w.Submitted {
				t.Fatalf("lost review evidence: %+v; want %+v", r, w)
			}
			if w.Dismissal != nil && (r.Dismissal == nil || r.Dismissal.ReviewID != w.ID || r.Dismissal.EventID != 91 || r.Dismissal.Actor != w.Dismissal.Actor || r.Dismissal.Time != w.Dismissal.Time) {
				t.Fatalf("lost dismissal evidence: %+v", r.Dismissal)
			}
		}
		for i, c := range got.Comments {
			w := want.Comments[i]
			if c.ID != w.ID || c.Author != w.Author || c.Created != w.Created || c.Kind != w.Kind || c.Body != w.Body || c.CommitID != w.CommitID || c.ReviewID != w.ReviewID {
				t.Fatalf("lost comment evidence: %+v", c)
			}
		}
		got.Reviews[3].Dismissal.Actor.ID = 999
		got.Comments[0].Body = "mutated"
		again, err := e.Reader.NativeReviews(t.Context(), e.Repo, 1)
		if err != nil || again.Reviews[3].Dismissal.Actor.ID != 22 || again.Comments[0].Body == "mutated" {
			t.Fatal("caller mutated future reads")
		}
	})
	t.Run("explicit-dismissal-and-fallback-evidence", func(t *testing.T) {
		for _, source := range []forge.NativeTimeSource{forge.ReviewDismissalTime, forge.ObservationTimeFallback} {
			e := newEnv(t)
			value := sample()
			d := value.Reviews[3].Dismissal
			d.Time.Source = source
			d.EventID = 0
			if source == forge.ObservationTimeFallback {
				d.Time.Time = time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
				d.Time.Uncertainty = 2 * time.Second
				d.Time.UpperBound = d.Time.Time.Add(3 * time.Second)
				d.Actor = forge.UserIdentity{Kind: forge.UnknownAccount}
			}
			e.Set(value)
			got, err := e.Reader.NativeReviews(t.Context(), e.Repo, 1)
			if err != nil {
				t.Fatal(err)
			}
			actual := got.Reviews[3].Dismissal
			if actual == nil || actual.Time != d.Time || actual.ReviewID != d.ReviewID || actual.EventID != 0 || actual.Actor != d.Actor {
				t.Fatalf("lost explicit/fallback evidence: %+v; want %+v", actual, d)
			}
		}
	})

	t.Run("rename-or-reassignment-between-reads", func(t *testing.T) {
		for _, id := range []int64{22, 0} {
			e := newEnv(t)
			e.Set(sample())
			got, err := e.Reader.NativeReviews(t.Context(), e.Repo, 1)
			if err != nil {
				t.Fatal(err)
			}
			e.SetGrant(forge.UserIdentity{ID: id, Login: author.Login, Kind: forge.UserAccount})
			for _, a := range []forge.UserIdentity{got.Reviews[0].Author, got.Comments[0].Author} {
				grant, err := e.Identities.WriteGrant(t.Context(), e.Repo, a)
				if !errors.Is(err, forge.ErrIdentityMismatch) || grant.CanWrite(a.ID) {
					t.Fatalf("reassigned/missing ID lent authority: %+v, %v", grant, err)
				}
			}
			renamed := author
			renamed.Login = "alice-renamed"
			value := sample()
			value.Reviews[0].Author = renamed
			value.Comments[0].Author = renamed
			e.Set(value)
			e.SetGrant(renamed)
			got, err = e.Reader.NativeReviews(t.Context(), e.Repo, 1)
			if err != nil {
				t.Fatal(err)
			}
			if got.Reviews[0].Author != renamed || got.Comments[0].Author != renamed {
				t.Fatal("rename changed stable ID")
			}
			grant, err := e.Identities.WriteGrant(t.Context(), e.Repo, got.Reviews[0].Author)
			if err != nil || !grant.CanWrite(author.ID) {
				t.Fatalf("renamed author's own grant: %+v, %v", grant, err)
			}
		}
	})
	t.Run("unavailable-and-incomplete-are-not-empty", func(t *testing.T) {
		for _, failure := range []error{forge.ErrNativeReviewsUnavailable, forge.ErrNativeReviewsIncomplete} {
			e := newEnv(t)
			e.Set(sample())
			e.Fail(failure)
			got, err := e.Reader.NativeReviews(t.Context(), e.Repo, 1)
			if !errors.Is(err, failure) || got.Reviews != nil || got.Comments != nil {
				t.Fatalf("usable prefix on error: %+v, %v", got, err)
			}
		}
	})
	t.Run("known-empty", func(t *testing.T) {
		e := newEnv(t)
		e.Set(forge.NativeReviews{Reviews: []forge.NativeReview{}, Comments: []forge.NativeComment{}})
		got, err := e.Reader.NativeReviews(t.Context(), e.Repo, 1)
		if err != nil || got.Reviews == nil || got.Comments == nil || len(got.Reviews)+len(got.Comments) != 0 {
			t.Fatalf("empty is unknown: %+v, %v", got, err)
		}
	})
}
