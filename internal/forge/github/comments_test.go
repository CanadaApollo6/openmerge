package github

import (
	"context"
	"reflect"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
)

func TestCommentsIncludeSameSecondBoundaryAndExcludeOldEdits(t *testing.T) {
	f := newAPI(t)
	comment := func(id int64, created, updated time.Time) *gh.IssueComment {
		return &gh.IssueComment{ID: &id, Body: new("@openmerge enqueue"), User: &gh.User{Login: new("writer")}, CreatedAt: &gh.Timestamp{Time: created}, UpdatedAt: &gh.Timestamp{Time: updated}}
	}
	f.comments[1] = []*gh.IssueComment{comment(1, epoch, epoch)}
	first, err := f.driver.ListComments(context.Background(), testRepo, 1, time.Time{})
	must(t, err)
	f.comments[1] = append(f.comments[1], comment(2, epoch, epoch), comment(3, epoch, epoch), comment(4, epoch.Add(-time.Hour), epoch.Add(time.Minute)))
	for range 2 {
		comments, err := f.driver.ListComments(context.Background(), testRepo, 1, first[0].CreatedAt)
		must(t, err)
		var ids []int64
		for _, c := range comments {
			ids = append(ids, c.ID)
		}
		if !reflect.DeepEqual(ids, []int64{1, 2, 3}) {
			t.Fatalf("boundary or pagination lost comments, or old edit resurfaced: %+v", comments)
		}
	}
	comments, err := f.driver.ListComments(context.Background(), testRepo, 1, epoch.Add(time.Nanosecond))
	must(t, err)
	if len(comments) != 0 {
		t.Fatalf("creation-time filter ignored subsecond cursor: %+v", comments)
	}
}
