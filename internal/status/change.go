package status

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// ChangeReport is additive reporting metadata, independent of the queue wire.
type ChangeReport struct {
	Evidence       *brief.View        `json:"evidence"`
	ID             string             `json:"id"`
	Revision       uint64             `json:"revision"`
	Accountability AccountabilityView `json:"accountability"`
	WaitingReasons []string           `json:"waiting_reasons"`
	BriefURL       string             `json:"brief_url"`
	DecisionsURL   string             `json:"decisions_url"`
}

// IdentityView describes a recorded stable account identity.
type IdentityView struct {
	ID    string `json:"id"`
	Login string `json:"login"`
	Kind  string `json:"kind"`
}

// AccountabilityView separates authentication, authorship and accountable humans.
type AccountabilityView struct {
	Truncated   bool           `json:"truncated,omitempty"`
	Status      string         `json:"status"`
	Actor       *IdentityView  `json:"actor"`
	Author      *IdentityView  `json:"author"`
	SubmitterID string         `json:"submitter_id"`
	Humans      []IdentityView `json:"humans"`
}

// LandingView names a confirmed journal outcome; an intended SHA is never
// substituted for an absent landing. Null means not recorded.
type LandingView struct {
	Outcome   string `json:"outcome"`
	SHA       string `json:"sha"`
	EventID   int64  `json:"event_id"`
	BatchID   string `json:"batch_id"`
	AttemptID string `json:"attempt_id"`
}

// ChangeResponse combines the normal PR lookup with the full reviewer evidence.
type ChangeResponse struct {
	Version int        `json:"version"`
	PR      *Entry     `json:"pr"`
	Brief   brief.View `json:"brief"`
}

func changePath(key store.QueueKey, id string) string {
	parts := strings.SplitN(key.Repository, "/", 2)
	if len(parts) != 2 {
		return ""
	}
	return "/v1/queues/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/" + url.PathEscape(key.BaseBranch) + "/changes/" + url.PathEscape(id)
}

func reportChange(key store.QueueKey, s queue.State, pr int, sanitizer *diagnostic.Sanitizer) *ChangeReport {
	rev, ok := s.PRRevision(pr)
	if !ok {
		return nil
	}
	id, err := key.ChangeID(pr)
	if err != nil {
		return nil
	}
	c := &ChangeReport{ID: string(id), Revision: uint64(rev.Revision), Accountability: AccountabilityView{Status: brief.NotRecorded, SubmitterID: brief.NotRecorded}, WaitingReasons: []string{}}
	path := changePath(key, string(id))
	c.BriefURL, c.DecisionsURL = path+"/brief?audience=reviewer", path+"/decisions"
	if facts, found := s.ApprovalFacts(id); found && facts.Accountability != nil {
		a := facts.Accountability
		if a.Basis.Revision == rev.Revision && a.Basis.Head == rev.HeadSHA {
			actor := IdentityView{a.Actor.ID, sanitizer.Sanitize(a.Actor.Login), string(a.Actor.Kind)}
			author := IdentityView{a.Author.ID, sanitizer.Sanitize(a.Author.Login), string(a.Author.Kind)}
			c.Accountability.Actor, c.Accountability.Author = &actor, &author
			c.Accountability.SubmitterID = a.SubmitterID
			c.Accountability.Humans = []IdentityView{}
			for i, h := range a.Humans {
				if i == 32 {
					c.Accountability.Truncated = true
					break
				}
				c.Accountability.Humans = append(c.Accountability.Humans, IdentityView{h.ID, sanitizer.Sanitize(h.Login), string(h.Kind)})
			}
			if a.Recorded {
				c.Accountability.Status = "recorded"
			}
		}
	}
	for _, w := range s.ApprovalWaits() {
		if w.Basis.ChangeID == id && w.Basis.Revision == rev.Revision && w.Basis.Head == rev.HeadSHA {
			c.WaitingReasons = append(c.WaitingReasons, w.Reasons...)
		}
	}
	return c
}

type changeRequest struct {
	keys     []store.QueueKey
	id, kind string
	revision uint64
	after    int64
	limit    int
}

func changeRoute(r *http.Request, keys []store.QueueKey) (changeRequest, bool, error) {
	parts := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
	c := changeRequest{limit: 20}
	if (len(parts) != 7 && len(parts) != 8) || parts[0] != "v1" || parts[1] != "queues" || parts[5] != "changes" || len(parts) == 8 && parts[7] != "decisions" {
		return c, false, nil
	}
	c.kind = "show"
	if len(parts) == 8 {
		c.kind = "decisions"
	}
	id, err := url.PathUnescape(parts[6])
	if err != nil || queue.ChangeID(id).Validate() != nil {
		return c, true, errors.New("canonical change ID required")
	}
	c.id = id
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return c, true, errors.New("invalid query")
	}
	for name, v := range values {
		if len(v) != 1 {
			return c, true, errors.New("query parameters must occur once")
		}
		switch name {
		case "revision":
			c.revision, err = strconv.ParseUint(v[0], 10, 64)
			if c.revision == 0 {
				err = errors.New("invalid revision")
			}
		case "after":
			if c.kind != "decisions" {
				err = errors.New("after requires decisions")
			} else {
				c.after, err = strconv.ParseInt(v[0], 10, 64)
				if c.after < 0 {
					err = errors.New("invalid cursor")
				}
			}
		case "limit":
			if c.kind != "decisions" {
				err = errors.New("limit requires decisions")
			} else {
				c.limit, err = strconv.Atoi(v[0])
				if c.limit < 1 || c.limit > 100 {
					err = errors.New("limit must be between 1 and 100")
				}
			}
		default:
			err = errors.New("unknown change query parameter")
		}
		if err != nil {
			return c, true, errors.New("invalid change query: " + err.Error())
		}
	}
	selector := r.Clone(r.Context())
	u := *r.URL
	selector.URL = &u
	u.RawPath = "/" + strings.Join(parts[:5], "/")
	u.Path, _ = url.PathUnescape(u.RawPath)
	u.RawQuery = ""
	c.keys, _, _, err = route(selector, keys)
	return c, true, err
}

// selectChangeRevision stops at the last committed event of the requested
// revision. It never attributes later accountability or stances to old heads.
func selectChangeRevision(j store.Journal, id string, revision uint64) (store.Journal, int, bool) {
	s := j.Seed
	target, end := 0, -1
	seen := map[int]bool{}
	consider := func(pr int) {
		if target != 0 || seen[pr] {
			return
		}
		seen[pr] = true
		canonical, err := j.Key.ChangeID(pr)
		if err == nil && string(canonical) == id {
			target = pr
		}
	}
	inspect := func(index int) {
		if target == 0 {
			return
		}
		rev, ok := s.PRRevision(target)
		if ok && (revision == 0 || uint64(rev.Revision) == revision) {
			end = index
		}
	}
	for _, e := range s.Entries {
		consider(e.PR)
	}
	inspect(0)
	for i, r := range j.Events {
		switch e := r.Event.(type) {
		case queue.PRObserved:
			consider(e.PR)
		case queue.ChangeRevisionObserved:
			consider(e.PR)
		}
		s = queue.Apply(s, r.Event)
		inspect(i + 1)
	}
	if end < 0 {
		return j, 0, false
	}
	if revision != 0 {
		j.Events = j.Events[:end]
	}
	return j, target, true
}

func enrichChanges(ctx context.Context, source Source, j store.Journal, q *Queue, entry *Entry, sanitizer *diagnostic.Sanitizer) {
	enrich := func(e *Entry) {
		if e.Change == nil {
			return
		}
		view, found, err := projectBrief(j, e.Change.ID, "reviewer", briefOptions{sanitizer: sanitizer, policy: briefPolicyProjection(ctx, source, j)})
		if err == nil && found {
			// Queue status carries bounded evidence pointers and progress. The
			// dedicated brief route retains its full bounded inventory and hunks.
			if view.Classification != nil && len(view.Classification.Files) > 0 {
				view.Classification.Files = nil
				view.Truncated = true
			}
			e.Change.Evidence = &view
		}
	}
	for i := range q.Entries {
		enrich(&q.Entries[i])
	}
	if entry != nil {
		enrich(entry)
	}
}
