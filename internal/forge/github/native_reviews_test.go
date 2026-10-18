package github

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/forgetest"
)

func nativeUserJSON(user forge.UserIdentity) map[string]any {
	return map[string]any{"id": user.ID, "login": user.Login, "type": map[forge.AccountKind]string{forge.UserAccount: "User", forge.BotAccount: "Bot", forge.AppAccount: "App"}[user.Kind]}
}

// The localhost server serializes independent REST field names, not SDK DTOs.
// Two items per page forces review and timeline pagination. Comment pagination
// is covered separately with a complete set from each endpoint.
func TestNativeReviewContract(t *testing.T) {
	forgetest.RunNativeReviewReader(t, func(t *testing.T) forgetest.NativeReviewEnv {
		f := newAPI(t)
		var value forge.NativeReviews
		var grant forge.UserIdentity
		var failure error
		f.override = func(w http.ResponseWriter, r *http.Request) bool {
			path := strings.TrimPrefix(r.URL.Path, "/api/v3/")
			if strings.Contains(path, "/collaborators/") {
				f.json(w, map[string]any{"user": nativeUserJSON(grant), "permission": "write"})
				return true
			}
			var objects []map[string]any
			switch path {
			case "repos/acme/project/pulls/1/reviews":
				if failure != nil {
					if errors.Is(failure, forge.ErrNativeReviewsIncomplete) {
						w.Header().Set("Link", fmt.Sprintf(`<%s%s?page=101>; rel="next"`, f.server.URL, r.URL.Path))
						f.json(w, []any{})
					} else {
						f.apiError(w, 404, "not readable")
					}
					return true
				}
				for _, review := range value.Reviews {
					states := map[forge.NativeReviewState]string{forge.ReviewApproved: "APPROVED", forge.ReviewChangesRequested: "CHANGES_REQUESTED", forge.ReviewCommented: "COMMENTED", forge.ReviewDismissed: "DISMISSED", forge.ReviewPending: "PENDING"}
					object := map[string]any{"id": review.ID, "user": nativeUserJSON(review.Author), "commit_id": review.CommitID, "state": states[review.State], "submitted_at": review.Submitted.Time}
					if review.Dismissal != nil && review.Dismissal.Time.Source == forge.ReviewDismissalTime {
						object["dismissed_at"] = review.Dismissal.Time.Time
						object["dismissed_by"] = nativeUserJSON(review.Dismissal.Actor)
					}
					objects = append(objects, object)
				}
			case "repos/acme/project/issues/1/timeline":
				// Unrelated events require traversing later pages to find the dismissal.
				objects = []map[string]any{{"id": 1, "event": "labeled"}, {"id": 2, "event": "unlabeled"}}
				for _, review := range value.Reviews {
					if review.Dismissal != nil && review.Dismissal.EventID != 0 {
						d := review.Dismissal
						objects = append(objects, map[string]any{"id": d.EventID, "event": "review_dismissed", "created_at": d.Time.Time, "actor": nativeUserJSON(d.Actor), "dismissed_review": map[string]any{"review_id": review.ID}})
					}
				}
			case "repos/acme/project/issues/1/comments", "repos/acme/project/pulls/1/comments":
				for _, comment := range value.Comments {
					if (comment.Kind == forge.IssueComment) != strings.Contains(path, "/issues/") {
						continue
					}
					object := map[string]any{"id": comment.ID, "user": nativeUserJSON(comment.Author), "body": comment.Body, "created_at": comment.Created.Time, "pull_request_review_id": comment.ReviewID, "commit_id": comment.CommitID}
					objects = append(objects, object)
				}
			default:
				return false
			}
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if page < 1 || r.URL.Query().Get("per_page") != "100" {
				t.Errorf("invalid pagination %s", r.URL.RawQuery)
				page = 1
			}
			start := min((page-1)*2, len(objects))
			end := min(start+2, len(objects))
			if end < len(objects) {
				w.Header().Set("Link", fmt.Sprintf(`<%s%s?page=%d>; rel="next"`, f.server.URL, r.URL.Path, page+1))
			}
			w.Header().Set("Date", epoch.Format(http.TimeFormat))
			f.json(w, append([]map[string]any{}, objects[start:end]...))
			return true
		}
		return forgetest.NativeReviewEnv{Reader: f.driver, Identities: f.driver, Repo: testRepo,
			Set: func(v forge.NativeReviews) {
				f.mu.Lock()
				defer f.mu.Unlock()
				value = v
				value.Reviews = slices.Clone(v.Reviews)
				value.Comments = slices.Clone(v.Comments)
			},
			SetGrant: func(user forge.UserIdentity) { f.mu.Lock(); defer f.mu.Unlock(); grant = user },
			Fail:     func(err error) { f.mu.Lock(); defer f.mu.Unlock(); failure = err },
		}
	})
}

func TestNativeDismissalTimeEvidence(t *testing.T) {
	for _, mode := range []string{"reviews-api", "timeline", "missing-event", "missing-event-time", "missing-date", "invalid-date", "duplicate-event", "conflicting-events"} {
		t.Run(mode, func(t *testing.T) {
			f := newAPI(t)
			submitted := epoch.Add(-24 * time.Hour)
			dismissed := epoch.Add(-time.Hour)
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				switch {
				case strings.HasSuffix(r.URL.Path, "/reviews"):
					review := map[string]any{"id": 7, "user": nativeUserJSON(forge.UserIdentity{ID: 11, Login: "alice", Kind: forge.UserAccount}), "commit_id": "old-head", "state": "DISMISSED", "submitted_at": submitted}
					if mode == "reviews-api" {
						review["dismissed_at"] = dismissed
						review["dismissed_by"] = nativeUserJSON(forge.UserIdentity{ID: 22, Login: "actor", Kind: forge.BotAccount})
					}
					switch mode {
					case "missing-date":
						w.Header()["Date"] = nil
					case "invalid-date":
						w.Header().Set("Date", "invalid")
					default:
						w.Header().Set("Date", epoch.Format(http.TimeFormat))
					}
					f.json(w, []any{review})
				case strings.HasSuffix(r.URL.Path, "/timeline"):
					w.Header().Set("Date", epoch.Format(http.TimeFormat))
					if mode == "missing-event" || mode == "missing-date" || mode == "invalid-date" {
						f.json(w, []any{})
						return true
					}
					event := map[string]any{"id": 91, "event": "review_dismissed", "actor": nativeUserJSON(forge.UserIdentity{ID: 22, Login: "actor", Kind: forge.BotAccount}), "dismissed_review": map[string]any{"review_id": 7}}
					if mode != "missing-event-time" {
						event["created_at"] = dismissed
					}
					events := []any{event}
					if mode == "duplicate-event" {
						events = append(events, event)
					}
					if mode == "conflicting-events" {
						events = append(events, map[string]any{"id": 92, "event": "review_dismissed", "dismissed_review": map[string]any{"review_id": 7}})
					}
					f.json(w, events)
				case strings.HasSuffix(r.URL.Path, "/comments"):
					f.json(w, []any{})
				default:
					return false
				}
				return true
			}
			got, err := f.driver.NativeReviews(t.Context(), testRepo, 1)
			if mode == "conflicting-events" {
				if !errors.Is(err, forge.ErrNativeReviewsIncomplete) || got.Reviews != nil {
					t.Fatalf("ambiguous dismissal: %+v, %v", got, err)
				}
				return
			}
			must(t, err)
			review := got.Reviews[0]
			d := review.Dismissal
			if review.Submitted.Time != submitted || d == nil || d.ReviewID != 7 {
				t.Fatalf("lost review/dismissal: %+v", review)
			}
			switch mode {
			case "reviews-api", "timeline", "duplicate-event":
				source := forge.TimelineDismissalTime
				if mode == "reviews-api" {
					source = forge.ReviewDismissalTime
				}
				if d.Time.Source != source || d.Time.Time != dismissed || d.Time.Precision != time.Second || d.Time.UpperBound != dismissed.Add(time.Second) || d.EventID != 91 || d.Actor.ID != 22 || d.Actor.Kind != forge.BotAccount {
					t.Fatalf("authoritative evidence: %+v", d)
				}
			default:
				if d.Time.Source != forge.ObservationTimeFallback || d.Time.Time.Equal(submitted) || d.Time.Precision != time.Second || d.Time.Uncertainty != nativeClockAllowance {
					t.Fatalf("fallback used submission/local time: %+v", d)
				}
				if mode == "missing-date" || mode == "invalid-date" {
					if !d.Time.UpperBound.IsZero() || !d.Time.Time.IsZero() {
						t.Fatalf("invented forge clock: %+v", d)
					}
				} else if d.Time.Time != epoch || d.Time.UpperBound != epoch.Add(3*time.Second) {
					t.Fatalf("wrong fallback bound: %+v", d)
				}
			}
			if review.Observation.ObservedAt != epoch || d.Observation.ObservedAt != epoch {
				t.Fatal("lost local observation time")
			}
		})
	}
}

func TestNativeReviewFailuresDiscardEntireObservation(t *testing.T) {
	for _, endpoint := range []string{"reviews", "timeline", "issue-comments", "review-comments"} {
		for _, mode := range []string{"403", "404", "later-page", "later-partial", "later-null", "cycle", "limit", "malformed-next", "missing-next", "invalid-json", "missing-author", "unknown-state", "null", "partial"} {
			t.Run(endpoint+"/"+mode, func(t *testing.T) {
				f := newAPI(t)
				f.override = func(w http.ResponseWriter, r *http.Request) bool {
					path := r.URL.Path
					current := ""
					switch {
					case strings.HasSuffix(path, "/reviews"):
						current = "reviews"
					case strings.HasSuffix(path, "/timeline"):
						current = "timeline"
					case strings.Contains(path, "/issues/1/comments"):
						current = "issue-comments"
					case strings.Contains(path, "/pulls/1/comments"):
						current = "review-comments"
					default:
						return false
					}
					var objects []any
					switch current {
					case "reviews":
						objects = []any{map[string]any{"id": 7, "user": nativeUserJSON(forge.UserIdentity{ID: 11, Login: "alice"}), "commit_id": "head", "state": "DISMISSED", "submitted_at": epoch}}
					case "timeline":
						objects = []any{map[string]any{"id": 91, "event": "review_dismissed", "created_at": epoch, "dismissed_review": map[string]any{"review_id": 7}}}
					default:
						objects = []any{map[string]any{"id": 31, "user": nativeUserJSON(forge.UserIdentity{ID: 11, Login: "alice"}), "created_at": epoch, "pull_request_review_id": 7, "commit_id": "head"}}
					}
					if current == endpoint {
						switch mode {
						case "403", "404":
							code, _ := strconv.Atoi(mode)
							f.apiError(w, code, "unreadable")
							return true
						case "later-page", "later-partial", "later-null":
							if r.URL.Query().Get("page") == "2" {
								switch mode {
								case "later-partial":
									w.WriteHeader(http.StatusPartialContent)
									f.json(w, objects)
								case "later-null":
									_, _ = fmt.Fprint(w, `null`)
								default:
									f.apiError(w, 404, "truncated")
								}
								return true
							}
							w.Header().Set("Link", fmt.Sprintf(`<%s%s?page=2>; rel="next"`, f.server.URL, path))
						case "cycle":
							w.Header().Set("Link", fmt.Sprintf(`<%s%s?page=1>; rel="next"`, f.server.URL, path))
						case "limit":
							w.Header().Set("Link", fmt.Sprintf(`<%s%s?page=101>; rel="next"`, f.server.URL, path))
						case "malformed-next":
							w.Header().Set("Link", fmt.Sprintf(`<%s%s?page=bad>; rel="next"`, f.server.URL, path))
						case "missing-next":
							w.Header().Set("Link", fmt.Sprintf(`<%s%s?page=2>; rel="last"`, f.server.URL, path))
						case "null":
							_, _ = fmt.Fprint(w, `null`)
							return true
						case "partial":
							w.WriteHeader(http.StatusPartialContent)
						case "invalid-json":
							_, _ = fmt.Fprint(w, `[{`)
							return true
						case "missing-author":
							object := objects[0].(map[string]any)
							delete(object, "user")
							if current == "timeline" {
								delete(object, "id")
							}
						case "unknown-state":
							object := objects[0].(map[string]any)
							switch current {
							case "reviews":
								object["state"] = "FUTURE"
							case "timeline":
								object["dismissed_review"] = map[string]any{}
							default:
								object["id"] = 0
							}
						}
					}
					f.json(w, objects)
					return true
				}
				got, err := f.driver.NativeReviews(t.Context(), testRepo, 1)
				if err == nil || got.Reviews != nil || got.Comments != nil {
					t.Fatalf("failed %s returned usable prefix: %+v, %v", endpoint, got, err)
				}
				switch mode {
				case "later-page", "later-partial", "later-null":
					if !errors.Is(err, forge.ErrNativeReviewsIncomplete) || !errors.Is(err, forge.ErrNativeReviewsUnavailable) {
						t.Fatalf("later-page failure must wrap both sentinels: %v", err)
					}
				case "cycle", "limit", "malformed-next", "missing-next":
					if !errors.Is(err, forge.ErrNativeReviewsIncomplete) {
						t.Fatalf("not typed incomplete: %v", err)
					}
				}
			})
		}
	}
}

func TestNativeReviewReadsPreserveSettlementBudget(t *testing.T) {
	f := newAPI(t)
	_, err := f.driver.CanWrite(t.Context(), testRepo, "writer")
	must(t, err)
	tr := f.driver.client.Client().Transport.(*transport)
	tr.mu.Lock()
	tr.budgetKnown, tr.limit, tr.remaining, tr.reset = true, 1000, 100, epoch.Add(time.Hour)
	tr.mu.Unlock()
	ctx := forge.WithRequestClass(t.Context(), forge.SettlementRequest)
	got, err := f.driver.NativeReviews(ctx, testRepo, 1)
	var rate *RateLimitError
	if !errors.As(err, &rate) || got.Reviews != nil || f.driver.Budget().Remaining != 100 {
		t.Fatalf("native read consumed settlement reserve: %+v, %v", got, err)
	}
	allowed, err := f.driver.CanWrite(ctx, testRepo, "reader")
	must(t, err)
	if allowed || f.driver.Budget().Remaining != 99 {
		t.Fatal("real settlement lost its reserved capacity")
	}
}

func TestNativeCommentPaginationAndRevalidation(t *testing.T) {
	f := newAPI(t)
	date := epoch
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Date", date.Format(http.TimeFormat))
		if strings.HasSuffix(r.URL.Path, "/reviews") {
			f.json(w, []any{})
			return true
		}
		if !strings.HasSuffix(r.URL.Path, "/comments") {
			return false
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page == 1 {
			w.Header().Set("Link", fmt.Sprintf(`<%s%s?page=2>; rel="next"`, f.server.URL, r.URL.Path))
		}
		f.json(w, []any{map[string]any{"id": page, "user": nativeUserJSON(forge.UserIdentity{ID: 11, Login: "alice", Kind: forge.AppAccount}), "created_at": epoch, "pull_request_review_id": 7, "commit_id": "head"}})
		return true
	}
	for i := range 2 {
		got, err := f.driver.NativeReviews(t.Context(), testRepo, 1)
		must(t, err)
		if len(got.Comments) != 4 {
			t.Fatalf("truncated comments: %+v", got.Comments)
		}
		for _, c := range got.Comments {
			if c.Author.Kind != forge.AppAccount || c.Observation.ResponseDate != date {
				t.Fatalf("lost account kind/revalidated Date: %+v", c)
			}
		}
		if i == 1 && f.notModified != 5 {
			t.Fatalf("each page needs conditional round trip: %d", f.notModified)
		}
		f.mu.Lock()
		date = epoch.Add(time.Minute)
		f.mu.Unlock()
	}
}

func TestNativeFallbackReobservationIsInputForPinnedBarrier(t *testing.T) {
	f := newAPI(t)
	date := epoch
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Date", date.Format(http.TimeFormat))
		switch {
		case strings.HasSuffix(r.URL.Path, "/reviews"):
			f.json(w, []any{map[string]any{"id": 7, "user": nativeUserJSON(forge.UserIdentity{ID: 11, Login: "alice"}), "commit_id": "head", "state": "DISMISSED", "submitted_at": epoch.Add(-24 * time.Hour)}})
		case strings.HasSuffix(r.URL.Path, "/timeline"), strings.HasSuffix(r.URL.Path, "/comments"):
			f.json(w, []any{})
		default:
			return false
		}
		return true
	}
	first, err := f.driver.NativeReviews(t.Context(), testRepo, 1)
	must(t, err)
	f.mu.Lock()
	date = epoch.Add(time.Minute)
	f.clock = date
	f.mu.Unlock()
	later, err := f.driver.NativeReviews(t.Context(), testRepo, 1)
	must(t, err)
	a, b := first.Reviews[0].Dismissal, later.Reviews[0].Dismissal
	if a.ReviewID != b.ReviewID || a.Time.UpperBound != epoch.Add(3*time.Second) || b.Time.UpperBound != date.Add(3*time.Second) || f.notModified != 4 {
		t.Fatalf("missing stable key or revalidated clock: %+v, %+v", a, b)
	}
	// Acquisition reports both observations faithfully. PR 6 must persist a's
	// bound, not replace it with b's bound; persistence/restart are PR 6 tests.
}

func TestNativeDismissalActors(t *testing.T) {
	valid := map[string]any{"id": 22, "login": "actor", "type": "User"}
	// GitHub's deleted-user login alone supplies no stable identity. When the
	// source supplies an ID for ghost, retain it without inferring its former owner.
	ghost := map[string]any{"login": "ghost"}
	for _, tc := range []struct {
		name                       string
		reviewActor, timelineActor any
		wantID                     int64
		wantConflict               bool
	}{
		{"valid-review-idless-timeline", valid, ghost, 22, false},
		{"idless-review-valid-timeline", ghost, valid, 22, false},
		{"conflicting-actors", valid, map[string]any{"id": 23, "login": "other"}, 0, true},
		{"conflicting-duplicate-event-actors", nil, valid, 0, true},
		{"missing-actors", nil, nil, 0, false},
		{"ghost-actors-without-id", ghost, ghost, 0, false},
		{"ghost-review-with-id", map[string]any{"id": 10137, "login": "ghost"}, nil, 10137, false},
		{"ghost-timeline-with-id", nil, map[string]any{"id": 10137, "login": "ghost"}, 10137, false},
		{"invalid-review-login", map[string]any{"id": 23, "login": "bad login"}, valid, 22, false},
		{"invalid-timeline-login", valid, map[string]any{"id": 23, "login": "bad login"}, 22, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAPI(t)
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				switch {
				case strings.HasSuffix(r.URL.Path, "/reviews"):
					f.json(w, []any{map[string]any{"id": 7, "user": valid, "commit_id": "head", "state": "DISMISSED", "submitted_at": epoch, "dismissed_by": tc.reviewActor}})
				case strings.HasSuffix(r.URL.Path, "/timeline"):
					events := []any{map[string]any{"id": 91, "event": "review_dismissed", "actor": tc.timelineActor, "created_at": epoch, "dismissed_review": map[string]any{"review_id": 7}}}
					if tc.name == "conflicting-duplicate-event-actors" {
						events = append(events, map[string]any{"id": 91, "event": "review_dismissed", "actor": map[string]any{"id": 23, "login": "other"}, "created_at": epoch, "dismissed_review": map[string]any{"review_id": 7}})
					}
					f.json(w, events)
				case strings.HasSuffix(r.URL.Path, "/comments"):
					f.json(w, []any{})
				default:
					return false
				}
				return true
			}
			got, err := f.driver.NativeReviews(t.Context(), testRepo, 1)
			if tc.wantConflict {
				if !errors.Is(err, forge.ErrNativeReviewsIncomplete) || got.Reviews != nil || got.Comments != nil {
					t.Fatalf("conflicting actor returned usable evidence: %+v, %v", got, err)
				}
				return
			}
			must(t, err)
			actor := got.Reviews[0].Dismissal.Actor
			if actor.ID != tc.wantID || tc.wantID == 0 && actor != (forge.UserIdentity{Kind: forge.UnknownAccount}) {
				t.Fatalf("dismissal actor = %+v, want ID %d", actor, tc.wantID)
			}
		})
	}
}

func TestNativeGhostAuthorsAndCommentNamespaces(t *testing.T) {
	for _, endpoint := range []string{"reviews", "issue-comments", "review-comments"} {
		for _, withID := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/with-id=%t", endpoint, withID), func(t *testing.T) {
				f := newAPI(t)
				ghost := map[string]any{"login": "ghost", "type": "User"}
				if withID {
					ghost["id"] = 10137
				}
				f.override = func(w http.ResponseWriter, r *http.Request) bool {
					if !strings.HasSuffix(r.URL.Path, "/reviews") && !strings.HasSuffix(r.URL.Path, "/comments") {
						return false
					}
					current := "review-comments"
					if strings.HasSuffix(r.URL.Path, "/reviews") {
						current = "reviews"
					} else if strings.Contains(r.URL.Path, "/issues/") {
						current = "issue-comments"
					}
					if current != endpoint {
						f.json(w, []any{})
						return true
					}
					f.json(w, []any{map[string]any{"id": 7, "user": ghost, "state": "APPROVED", "submitted_at": epoch, "created_at": epoch, "pull_request_review_id": 9, "commit_id": "head"}})
					return true
				}
				got, err := f.driver.NativeReviews(t.Context(), testRepo, 1)
				if !withID {
					if !errors.Is(err, forge.ErrNativeReviewsUnavailable) || got.Reviews != nil || got.Comments != nil {
						t.Fatalf("ID-less ghost supplied usable identity: %+v, %v", got, err)
					}
					return
				}
				must(t, err)
				var author forge.UserIdentity
				if endpoint == "reviews" {
					author = got.Reviews[0].Author
				} else {
					comment := got.Comments[0]
					author = comment.Author
					if endpoint == "issue-comments" {
						if comment.Kind != forge.IssueComment || comment.ReviewID != 0 || comment.CommitID != "" {
							t.Fatalf("issue comment leaked inline references: %+v", comment)
						}
					} else if comment.Kind != forge.ReviewComment || comment.ReviewID != 9 || comment.CommitID != "head" {
						t.Fatalf("inline comment lost references: %+v", comment)
					}
				}
				if author.ID != 10137 || author.Login != "ghost" || author.Kind != forge.UserAccount {
					t.Fatalf("ghost source identity lost: %+v", author)
				}
			})
		}
	}
}
