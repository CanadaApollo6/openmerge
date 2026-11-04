package status

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/queuemetrics"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// SavingsResponse preserves the report contract inside the status version envelope.
type SavingsResponse struct {
	Version int                  `json:"version"`
	Savings queuemetrics.Savings `json:"savings"`
}

func serveSavings(w http.ResponseWriter, r *http.Request, source Source, keys []store.QueueKey, sanitizer *diagnostic.Sanitizer) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		fail(w, 400, "invalid-request", "invalid savings query")
		return
	}
	for name, values := range query {
		if len(values) != 1 || name != "since" && name != "until" && name != "full-suite-minutes" {
			fail(w, 400, "invalid-request", "savings accepts since, until, and full-suite-minutes once each")
			return
		}
	}
	now := time.Now().UTC()
	window := queuemetrics.SavingsWindow{Until: now}
	window.Since, err = time.Parse(time.RFC3339Nano, query.Get("since"))
	if err == nil && query.Has("until") {
		window.Until, err = time.Parse(time.RFC3339Nano, query.Get("until"))
	}
	if err != nil || window.Since.IsZero() || !window.Until.After(window.Since) || window.Until.After(now) {
		fail(w, 400, "invalid-request", "since and until must be RFC3339 with since before until; until must not be in the future")
		return
	}
	if query.Has("full-suite-minutes") {
		window.FullSuiteMinutes, err = strconv.ParseFloat(query.Get("full-suite-minutes"), 64)
		if err != nil {
			fail(w, 400, "invalid-request", "full-suite-minutes must be finite and nonnegative")
			return
		}
	}
	if math.IsNaN(window.FullSuiteMinutes) || math.IsInf(window.FullSuiteMinutes, 0) || window.FullSuiteMinutes < 0 {
		fail(w, 400, "invalid-request", "full-suite-minutes must be finite and nonnegative")
		return
	}
	selector := r.Clone(r.Context())
	u := *r.URL
	selector.URL = &u
	u.RawPath = strings.TrimSuffix(r.URL.EscapedPath(), "/reports/savings")
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
	var report queuemetrics.Savings
	if err == nil && len(journals) == 1 {
		report, err = queuemetrics.ComputeSavingsContext(ctx, journals[0], window)
	}
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		fail(w, 503, "unavailable", "savings report exceeded the 15 second request deadline; use the database report for large histories")
		return
	}
	if err != nil {
		fail(w, 503, "unavailable", "savings history could not be read or computed")
		return
	}
	if len(journals) != 1 {
		fail(w, 404, "not-found", "queue not found")
		return
	}
	raw, err := json.Marshal(SavingsResponse{Version: Version, Savings: report})
	if err != nil {
		fail(w, 503, "unavailable", "savings report could not be encoded")
		return
	}
	if ctx.Err() != nil {
		fail(w, 503, "unavailable", "savings report exceeded the 15 second request deadline; use the database report for large histories")
		return
	}
	_, _ = w.Write(append(raw, '\n'))
}
