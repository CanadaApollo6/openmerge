package status

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// QueueEvent embeds the PR projection so polling shares its redaction rules.
type QueueEvent struct {
	Event
	PR int `json:"pr,omitempty"`
}

// QueueEventsResponse retains an exhausted cursor so polling resumes after appends.
type QueueEventsResponse struct {
	Version    int          `json:"version"`
	Events     []QueueEvent `json:"events"`
	NextCursor int64        `json:"next_cursor"`
	HasMore    bool         `json:"has_more"`
}

func serveQueueEvents(w http.ResponseWriter, r *http.Request, source Source, keys []store.QueueKey, sanitizer *diagnostic.Sanitizer) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		fail(w, 400, "invalid-request", "invalid events query")
		return
	}
	after, limit := int64(0), 20
	for name, values := range query {
		if len(values) != 1 || name != "after" && name != "limit" {
			fail(w, 400, "invalid-request", "events accepts after and limit once each")
			return
		}
		if name == "after" {
			after, err = strconv.ParseInt(values[0], 10, 64)
		} else {
			limit, err = strconv.Atoi(values[0])
		}
		if err != nil || after < 0 || limit < 1 || limit > 100 {
			fail(w, 400, "invalid-request", "after must be nonnegative and limit must be between 1 and 100")
			return
		}
	}
	selector := r.Clone(r.Context())
	u := *r.URL
	selector.URL = &u
	u.RawPath = strings.TrimSuffix(r.URL.EscapedPath(), "/events")
	u.Path, _ = url.PathUnescape(u.RawPath)
	u.RawQuery = ""
	selected, _, _, err := route(selector, keys)
	if err != nil {
		fail(w, 400, "invalid-request", sanitizer.Sanitize(err.Error()))
		return
	}
	if len(selected) == 0 {
		fail(w, 404, "not-found", "queue not found")
		return
	}
	if len(selected) > 1 {
		fail(w, 409, "ambiguous-queue", "multiple installations or lanes match this queue")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	journals, err := source.Journals(ctx, selected)
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		fail(w, 503, "unavailable", "queue events exceeded the 15 second request deadline")
		return
	}
	if err != nil {
		fail(w, 503, "unavailable", "queue history could not be read")
		return
	}
	if len(journals) != 1 {
		fail(w, 404, "not-found", "queue not found")
		return
	}
	j := journals[0]
	// Private dependency reads withhold journal positions and timing on PR detail too.
	if j.DependencyRedacted {
		fail(w, 409, "event-history-withheld", "event history is withheld; retrying will not help; ask an operator to restore access for retained history; use queue or PR detail meanwhile")
		return
	}
	response := QueueEventsResponse{Version: Version, Events: []QueueEvent{}, NextCursor: after}
	s := j.Seed
	aliases := map[queue.ChangeID]int{}
	for pr, revision := range s.ChangeRevisions() {
		aliases[revision.ChangeID] = pr
	}
	for _, record := range j.Events {
		if ctx.Err() != nil {
			fail(w, 503, "unavailable", "queue events exceeded the 15 second request deadline")
			return
		}
		if record.ID > after && len(response.Events) == limit {
			response.HasMore = true
			break
		}
		before := s
		s = queue.Apply(s, record.Event)
		pr := directPR(record.Event)
		if metadata, ok := record.Event.(queue.PRMetadataObserved); ok {
			pr = metadata.PR
		}
		if pr > 0 {
			id, _ := j.Key.ChangeID(pr)
			aliases[id] = pr
		} else {
			pr = aliases[eventChangeID(record.Event)]
		}
		if record.ID <= after {
			continue
		}
		evidence := projectEvent(record, before, s, sanitizer)
		if record.Event == nil {
			// Side evidence has no public formatter. Keep its envelope, never its payload.
			var header struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(record.Payload, &header) != nil || header.Type == "" {
				fail(w, 503, "unavailable", "queue event envelope could not be read")
				return
			}
			evidence.Type = sanitizer.Sanitize(header.Type)
		}
		if evidence.Type == "" {
			fail(w, 503, "unavailable", "queue event could not be projected")
			return
		}
		response.Events = append(response.Events, QueueEvent{Event: evidence, PR: pr})
		response.NextCursor = record.ID
	}
	if ctx.Err() != nil {
		fail(w, 503, "unavailable", "queue events exceeded the 15 second request deadline")
		return
	}
	writeJSON(w, response)
}

func eventChangeID(event queue.Event) queue.ChangeID {
	switch e := event.(type) {
	case queue.ChangeClassified:
		return queue.ChangeID(e.Evidence.Classification.Inputs.ChangeID)
	case queue.ChangeAccountabilityObserved:
		return e.Basis.ChangeID
	case queue.ChangeIntentObserved:
		return e.Basis.ChangeID
	case queue.ReviewStanceObserved:
		return e.Basis.ChangeID
	case queue.ApprovalReceiptRecorded:
		return e.Basis.ChangeID
	case queue.ApprovalEligibilityObserved:
		return e.Basis.ChangeID
	case queue.ApprovalInputsChanged:
		return e.Basis.ChangeID
	case queue.LandingDecisionRecorded:
		if e.Approval != nil {
			return e.Approval.Basis.ChangeID
		}
		if e.Remedy != nil {
			return e.Remedy.ChangeID
		}
	}
	return ""
}
