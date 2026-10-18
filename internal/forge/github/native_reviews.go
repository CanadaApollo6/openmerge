package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	gh "github.com/google/go-github/v93/github"
)

var _ forge.NativeReviewReader = (*Driver)(nil)

// GitHub timestamps and HTTP Date have second resolution. The additional
// allowance covers clock uncertainty; it is explicit evidence, not a local
// clock substitution. PR 6 pins this bound rather than renewing it on polls.
const nativeClockAllowance = 2 * time.Second

// nativeReviewJSON also accepts an explicit dismissed_at extension from a
// provider that exposes one. Public GitHub normally needs timeline evidence.
type nativeReviewJSON struct {
	ID          int64     `json:"id"`
	User        *gh.User  `json:"user"`
	CommitID    string    `json:"commit_id"`
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submitted_at"`
	DismissedAt time.Time `json:"dismissed_at"`
	DismissedBy *gh.User  `json:"dismissed_by"`
}

type nativeCommentJSON struct {
	ID        int64     `json:"id"`
	ReviewID  int64     `json:"pull_request_review_id"`
	CommitID  string    `json:"commit_id"`
	User      *gh.User  `json:"user"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

type nativeEventJSON struct {
	ID              int64     `json:"id"`
	Event           string    `json:"event"`
	Actor           *gh.User  `json:"actor"`
	CreatedAt       time.Time `json:"created_at"`
	DismissedReview struct {
		ReviewID int64 `json:"review_id"`
	} `json:"dismissed_review"`
}

func nativeTime(t time.Time, source forge.NativeTimeSource) forge.NativeTime {
	result := forge.NativeTime{Time: t, Source: source, Precision: time.Second}
	if !t.IsZero() {
		result.UpperBound = t.Add(time.Second)
	}
	return result
}

func nativeFallback(obs forge.NativeObservation) forge.NativeTime {
	result := forge.NativeTime{Time: obs.ResponseDate, Source: forge.ObservationTimeFallback, Precision: time.Second, Uncertainty: nativeClockAllowance}
	if !obs.ResponseDate.IsZero() {
		result.UpperBound = obs.ResponseDate.Add(time.Second + nativeClockAllowance)
	}
	return result
}

// nativePages uses the existing authenticated, conditional installation transport
// and budget. Invalid/cyclic/over-limit pagination cannot yield a usable prefix.
func nativePages[T any](ctx context.Context, d *Driver, path string, visit func(T, forge.NativeObservation) error) error {
	for page := 1; page != 0; {
		req, err := d.client.NewRequest(ctx, http.MethodGet, fmt.Sprintf("%s?per_page=100&page=%d", path, page), nil)
		if err != nil {
			return forge.ErrNativeReviewsUnavailable
		}
		var items []T
		res, err := d.client.Do(req, &items)
		if err != nil {
			if page > 1 {
				return fmt.Errorf("%w: %w: %w", forge.ErrNativeReviewsIncomplete, forge.ErrNativeReviewsUnavailable, safeError(err))
			}
			return fmt.Errorf("%w: %w", forge.ErrNativeReviewsUnavailable, safeError(err))
		}
		if res.StatusCode != http.StatusOK || items == nil {
			return forge.ErrNativeReviewsUnavailable
		}
		date, _ := http.ParseTime(res.Header.Get("Date"))
		obs := forge.NativeObservation{ObservedAt: d.app.now(), ResponseDate: date}
		next, err := nativeNextPage(res.Header, page)
		if err != nil || len(items) > 100 {
			return forge.ErrNativeReviewsIncomplete
		}
		for _, item := range items {
			if err := visit(item, obs); err != nil {
				return err
			}
		}
		page = next
	}
	return nil
}

// Accept GitHub's numbered Link format only. Unsupported/malformed pagination
// is incomplete even when the SDK would silently drop an unrecognized link.
func nativeNextPage(header http.Header, page int) (int, error) {
	next, last := 0, 0
	for _, line := range header.Values("Link") {
		for _, link := range strings.Split(line, ",") {
			target, rel, ok := strings.Cut(strings.TrimSpace(link), ";")
			if !ok || !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
				return 0, forge.ErrNativeReviewsIncomplete
			}
			u, err := url.Parse(strings.TrimSuffix(strings.TrimPrefix(target, "<"), ">"))
			if err != nil {
				return 0, forge.ErrNativeReviewsIncomplete
			}
			n, err := strconv.Atoi(u.Query().Get("page"))
			if err != nil || n < 1 || n > 100 {
				return 0, forge.ErrNativeReviewsIncomplete
			}
			switch strings.TrimSpace(rel) {
			case `rel="next"`:
				if next != 0 || n != page+1 {
					return 0, forge.ErrNativeReviewsIncomplete
				}
				next = n
			case `rel="last"`:
				last = n
			case `rel="prev"`, `rel="first"`:
			default:
				return 0, forge.ErrNativeReviewsIncomplete
			}
		}
	}
	if next == 0 && last > page {
		return 0, forge.ErrNativeReviewsIncomplete
	}
	return next, nil
}

func nativeAuthor(user *gh.User) (forge.UserIdentity, error) {
	author := userIdentity(user)
	if author.ID <= 0 || !validIdentityLogin(author.Login) {
		return forge.UserIdentity{}, forge.ErrNativeReviewsUnavailable
	}
	return author, nil
}

// NativeReviews acquires raw native evidence without consulting write grants,
// reducing decisive reviews, attributing revisions, or changing legacy approvals.
func (d *Driver) NativeReviews(ctx context.Context, repo forge.RepoRef, number int) (forge.NativeReviews, error) {
	owner, name, err := splitRepo(repo)
	if err != nil || number <= 0 {
		return forge.NativeReviews{}, forge.ErrNativeReviewsUnavailable
	}
	ctx = identityContext(ctx)
	root := fmt.Sprintf("repos/%s/%s", owner, name)
	pull := fmt.Sprintf("%s/pulls/%d", root, number)
	issue := fmt.Sprintf("%s/issues/%d", root, number)
	result := forge.NativeReviews{Reviews: []forge.NativeReview{}, Comments: []forge.NativeComment{}}
	err = nativePages(ctx, d, pull+"/reviews", func(raw nativeReviewJSON, obs forge.NativeObservation) error {
		author, err := nativeAuthor(raw.User)
		if err != nil || raw.ID <= 0 {
			return forge.ErrNativeReviewsUnavailable
		}
		states := map[string]forge.NativeReviewState{"APPROVED": forge.ReviewApproved, "CHANGES_REQUESTED": forge.ReviewChangesRequested, "COMMENTED": forge.ReviewCommented, "DISMISSED": forge.ReviewDismissed, "PENDING": forge.ReviewPending}
		state, ok := states[raw.State]
		if !ok || state != forge.ReviewPending && (raw.SubmittedAt.IsZero() || raw.CommitID == "") {
			return forge.ErrNativeReviewsUnavailable
		}
		review := forge.NativeReview{ID: raw.ID, CommitID: raw.CommitID, Author: author, Submitted: nativeTime(raw.SubmittedAt, forge.ReviewSubmissionTime), State: state, Observation: obs}
		if state == forge.ReviewDismissed {
			evidence := &forge.NativeDismissal{ReviewID: raw.ID, Actor: userIdentity(raw.DismissedBy), Observation: obs, Time: nativeFallback(obs)}
			if !raw.DismissedAt.IsZero() {
				evidence.Time = nativeTime(raw.DismissedAt, forge.ReviewDismissalTime)
			}
			review.Dismissal = evidence
		}
		result.Reviews = append(result.Reviews, review)
		return nil
	})
	if err != nil {
		return forge.NativeReviews{}, err
	}
	// Even when explicit times are present, timeline supplies event/actor identity.
	needsTimeline := false
	for _, review := range result.Reviews {
		needsTimeline = needsTimeline || review.Dismissal != nil
	}
	if needsTimeline {
		err = nativePages(ctx, d, issue+"/timeline", func(event nativeEventJSON, obs forge.NativeObservation) error {
			if event.Event != "review_dismissed" {
				return nil
			}
			if event.ID <= 0 || event.DismissedReview.ReviewID <= 0 {
				return forge.ErrNativeReviewsUnavailable
			}
			for i := range result.Reviews {
				evidence := result.Reviews[i].Dismissal
				if evidence == nil || evidence.ReviewID != event.DismissedReview.ReviewID {
					continue
				}
				if evidence.EventID != 0 && evidence.EventID != event.ID {
					return forge.ErrNativeReviewsIncomplete
				}
				evidence.EventID = event.ID
				if event.Actor != nil {
					evidence.Actor = userIdentity(event.Actor)
				}
				evidence.Observation = obs
				if evidence.Time.Source != forge.ReviewDismissalTime {
					evidence.Time = nativeFallback(obs)
					if !event.CreatedAt.IsZero() {
						evidence.Time = nativeTime(event.CreatedAt, forge.TimelineDismissalTime)
					}
				}
			}
			return nil
		})
		if err != nil {
			return forge.NativeReviews{}, err
		}
	}
	for _, source := range []struct {
		path string
		kind forge.NativeCommentKind
	}{{issue + "/comments", forge.IssueComment}, {pull + "/comments", forge.ReviewComment}} {
		err = nativePages(ctx, d, source.path, func(raw nativeCommentJSON, obs forge.NativeObservation) error {
			author, err := nativeAuthor(raw.User)
			if err != nil || raw.ID <= 0 || raw.CreatedAt.IsZero() || source.kind == forge.ReviewComment && (raw.ReviewID <= 0 || raw.CommitID == "") {
				return forge.ErrNativeReviewsUnavailable
			}
			result.Comments = append(result.Comments, forge.NativeComment{ID: raw.ID, ReviewID: raw.ReviewID, CommitID: raw.CommitID, Author: author, Body: raw.Body, Kind: source.kind, Created: nativeTime(raw.CreatedAt, forge.CommentCreationTime), Observation: obs})
			return nil
		})
		if err != nil {
			return forge.NativeReviews{}, err
		}
	}
	return result, nil
}
